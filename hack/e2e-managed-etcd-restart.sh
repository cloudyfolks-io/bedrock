#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

run_etcd_member_recovery() {
local entry=$1 opt_in usage
shift
case "$entry" in
  loopback)
    opt_in=${E2E_MANAGED_ETCD_RESTART:-}
    usage='E2E_MANAGED_ETCD_RESTART=1 bash hack/e2e-managed-etcd-restart.sh' ;;
  reachable)
    opt_in=${E2E_MANAGED_ETCD_REACHABLE_MEMBER:-}
    usage='E2E_MANAGED_ETCD_REACHABLE_MEMBER=1 bash hack/e2e-managed-etcd-reachable-member.sh' ;;
  *) return 2 ;;
esac
if [[ $opt_in != 1 || $# != 9 ||
  ! ${1:-} =~ ^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$ || ! ${3:-} =~ ^[0-9a-f]{64}$ ||
  ! ${5:-} =~ ^[0-9a-f]{64}$ || ! ${7:-} =~ ^[0-9a-f]{64}$ || ! ${8:-} =~ ^[0-9a-f]{64}$ ]]; then
  printf 'Usage: %s %s\n' "$usage" '<guest-uid> <source.json> <source-sha> <member-evidence.json> <member-sha> <freeze-and-copy-proof-dir> <custody-sha> <execution-sha> <output-dir>' >&2
  return 2
fi

export KUBECONFIG=/var/lib/k0s/pki/admin.conf
if [[ $(uname -s) != Linux || ! -s /var/lib/bedrock/e2e-admin-password || ! -f $KUBECONFIG || -L $KUBECONFIG ]]; then
  printf '%s\n' 'Run this fixture inside an initialized Bedrock development guest.' >&2
  return 1
fi

python3 -I - "$entry" "$@" <<'PY'
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import time
from traceback import walk_tb

namespace = "bedrock-e2e-etcd-source"
cluster = "etcd-source"
service = "kmc-etcd-source-etcd"
member_name = service + "-0"
endpoint = "https://" + service + ":2379"
advertised_host = member_name + "." + service
peer = "https://" + advertised_host + ":2380"
client = "https://" + advertised_host + ":2379"
driver = "rook-ceph.rbd.csi.ceph.com"
snapshot_api = "snapshot.storage.k8s.io"
pod_name = "etcd-native-restart"
gate_name = "e2e.bedrock.cloudyfolks.io/etcd-approved"
reachable_name = "etcd-reachable-member"
purpose_label = "migration.cloudyfolks.io/purpose"
reachable_labels = {purpose_label: "etcd-member-recovery"}
controller_labels = {purpose_label: "etcd-controller-recovery"}


def require(condition, message="The approved isolated etcd recovery state could not be verified."):
    if not condition:
        raise ValueError(message)


def unique(pairs):
    result = dict(pairs)
    require(len(result) == len(pairs))
    return result


def encoded(record):
    return (json.dumps(record, sort_keys=True, separators=(",", ":")) + "\n").encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def private_file(path, limit):
    require(path.is_absolute() and path.resolve(strict=True) == path)
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600 and info.st_uid == os.geteuid() and
                info.st_nlink == 1 and 0 < info.st_size <= limit)
        data = stream.read(limit + 1)
    require(len(data) == info.st_size)
    return data


def approved_json(path, approval):
    data = private_file(path, 1024 * 1024)
    require(digest(data) == approval)
    return json.loads(data, object_pairs_hook=unique)


def private_dir(path):
    require(path.is_absolute() and path.resolve(strict=True) == path)
    info = os.lstat(path)
    require(stat.S_ISDIR(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o700 and info.st_uid == os.geteuid())
    return path


def command(words, body=None, limit=40):
    result = subprocess.run(words, input=body, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=limit)
    if result.returncode:
        raise subprocess.CalledProcessError(result.returncode, words, stderr=result.stderr)
    require(0 < len(result.stdout) <= 64 * 1024 * 1024)
    return json.loads(result.stdout, object_pairs_hook=unique)


def kc(*args, body=None):
    return command(["kubectl", "--request-timeout=25s", *args], body)


def namespaced(*args, body=None):
    return kc("-n", namespace, *args, body=body)


def get(resource, name):
    return namespaced("get", resource, name, "-o", "json")


def metadata(record, uid):
    result = record["metadata"]
    require(result["uid"] == uid and result.get("deletionTimestamp") is None)
    return result


def inventory(path, kind):
    record = kc("get", "--raw", path)
    require(record["kind"] == kind and record["metadata"]["resourceVersion"] and not record["metadata"].get("continue") and
            not record["metadata"].get("remainingItemCount") and isinstance(record["items"], list))
    return record["items"]


def valid_uid(value):
    return re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", value) and value != "00000000-0000-0000-0000-000000000000"


def uint64(value):
    require(type(value) is int and 0 < value < 2**64)
    return value


def decimal(value):
    require(re.fullmatch(r"[1-9][0-9]{0,19}", value))
    return uint64(int(value))


def source_identity(source, original, guest, source_sha):
    etcd = source["etcd"]
    require(source["version"] == original["version"] == 1 and source["guest_uid"] == original["guest_uid"] == guest and
            source["namespace"] == namespace and source["cluster"] == cluster and source["k0s_version"] == "v1.36.3+k0s.1" and
            original["source_receipt_sha256"] == source_sha and original["source_cluster_uid"] == source["cluster_uid"] and
            original["namespace_uid"] == source["namespace_uid"] and original["etcd_statefulset_uid"] == etcd["statefulset_uid"] and
            original["etcd_pod_uid"] == etcd["pod_uid"] and original["etcd_service_uid"] == etcd["service"]["uid"] and
            original["etcd_image_id"] == etcd["image_id"] and etcd["pod"] == member_name and etcd["service"]["name"] == service and
            etcd["statefulset"] == service)
    approved, observed = etcd["status"], original["status"]
    for key, value in (("endpoint", endpoint), ("peer_url", peer), ("version", "3.7.1")):
        require(approved[key] == observed[key] == value)
    require(observed["member_name"] == member_name and observed["advertised_client_url"] == client and
            source["marker"]["name"] == "bedrock-etcd-continuity" and
            decimal(observed["committed_revision"]) >= decimal(source["marker"]["revision"]))
    for key in ("cluster_id", "member_id"):
        require(approved[key] == observed[key] and re.fullmatch(r"[0-9a-f]{16}", observed[key]) and int(observed[key], 16) > 0)
    for key in ("committed_revision", "raft_term", "raft_index", "raft_applied_index"):
        require(decimal(observed[key]) >= decimal(approved[key]))
    image_digest = re.search(r"sha256:[0-9a-f]{64}$", original["etcd_image_id"])
    require(image_digest)
    return "quay.io/k0sproject/etcd@" + image_digest[0]


def report_approval(path, wanted_approval, frozen_approval, proof_digest, member_tree):
    approval = digest(f"legacy-etcd-datastore-v1\n{frozen_approval}\n{proof_digest}\n{member_tree}\n".encode())
    report = json.loads(private_file(path, 1024 * 1024), object_pairs_hook=unique)
    require(report["version"] == 1 and report["frozen_approval_sha256"] == frozen_approval and
            report["data_proof_sha256"] == proof_digest and report["member_tree_sha256"] == member_tree and
            report["approval_sha256"] == approval == wanted_approval and report["matched_files"] >= 2 and report["matched_bytes"] > 0)


def check_custody(proofs, custody_sha, execution_sha, source, source_path, source_sha):
    copy = private_dir(proofs / "etcd-copy")
    custody = approved_json(copy / "custody.json", custody_sha)
    execution = approved_json(copy / "etcd-execution.json", execution_sha)
    frozen_sha = custody["frozen_receipt_sha256"]
    frozen = approved_json(proofs / "frozen.json", frozen_sha)
    inventory_path = proofs / "frozen-inventory.json"
    inventory_sha = frozen["inventory_sha256"]
    require(digest(private_file(inventory_path, 64 * 1024 * 1024)) == inventory_sha)
    for record in (custody, execution, frozen):
        require(record["version"] == 1 and record["guest_uid"] == source["guest_uid"] and
                record["namespace_uid"] == source["namespace_uid"] and record["source_cluster_uid"] == source["cluster_uid"])
    require(custody["scope"] == "synthetic-shared-snapshot" and execution["scope"] == "synthetic-etcd-execution" and
            execution["frozen_receipt_sha256"] == frozen_sha and execution["custody_sha256"] == custody_sha and
            frozen["source_receipt_sha256"] == source_sha)
    binary = Path(os.environ["BEDROCK_MIGRATE"])
    binary_sha = os.environ["BEDROCK_MIGRATE_SHA256"]
    require(binary.is_absolute() and binary.resolve(strict=True) == binary and binary.is_file() and os.access(binary, os.X_OK) and
            re.fullmatch(r"[0-9a-f]{64}", binary_sha))
    binary_digest = hashlib.sha256()
    with binary.open("rb") as stream:
        for data in iter(lambda: stream.read(1024 * 1024), b""):
            binary_digest.update(data)
    require(binary_digest.hexdigest() == binary_sha)
    freeze = command([str(binary), "verify-frozen-legacy-etcd", str(inventory_path), inventory_sha, str(source_path),
                      source_sha, source["guest_uid"]])
    expected_frozen = digest(f"frozen-legacy-etcd-v1\n{inventory_sha}\n{source_sha}\n{source['guest_uid']}\n".encode())
    require(freeze["version"] == 1 and freeze["verified_disks"] == 2 and freeze["inventory_sha256"] == inventory_sha and
            freeze["source_sha256"] == source_sha and freeze["approval_sha256"] == expected_frozen)
    data_sha, execution_data_sha = custody["data_proof_sha256"], execution["execution_data_proof_sha256"]
    proof = approved_json(copy / "data-proof.json", data_sha)
    execution_proof = approved_json(copy / "execution-data-proof.json", execution_data_sha)
    tree = execution["member_tree_sha256"]
    require(re.fullmatch(r"[0-9a-f]{64}", tree) and proof["member_tree_sha256"] == tree and execution_proof == {
        **proof, "retained_disk": execution["execution_disk"]} and proof["frozen_approval_sha256"] == expected_frozen and
        proof["source_disk"] == source["etcd"]["disk"] and execution["snapshot"] == proof["snapshot"])
    for reader, disk in ((custody["reference_reader"], proof["reference_disk"]), (custody["retained_reader"], proof["retained_disk"]),
                         (execution["execution_reader"], execution["execution_disk"])):
        require(reader["read_only"] is True and reader["claim"] == disk["claim"] and reader["volume"] == disk["volume"] and
                reader["member_tree_sha256"] == tree and valid_uid(reader["pod_uid"]))
    disks = (source["controller"]["disk"], proof["source_disk"], proof["reference_disk"], proof["retained_disk"], execution["execution_disk"])
    for field in ("claim", "claim_uid", "volume", "volume_uid", "handle_sha256"):
        require(len({disk[field] for disk in disks}) == len(disks))
    require(execution["execution_disk"]["claim"] == "etcd-source-execution" and proof["reference_disk"]["claim"] == "etcd-source-reference" and
            proof["retained_disk"]["claim"] == "etcd-source-retained" and proof["snapshot"]["name"] == "etcd-source-frozen")
    report_approval(copy / "datastore.json", custody["datastore_approval_sha256"], expected_frozen, data_sha, tree)
    report_approval(copy / "execution-datastore.json", execution["execution_datastore_approval_sha256"], expected_frozen, execution_data_sha, tree)
    return frozen, custody, proof["snapshot"], disks, tree


def disk_binding(disk, snapshot):
    claim = get("pvc", disk["claim"])
    metadata(claim, disk["claim_uid"])
    volume = kc("get", "pv", disk["volume"], "-o", "json")
    metadata(volume, disk["volume_uid"])
    spec, pv = claim["spec"], volume["spec"]
    for record, settings in ((claim, spec), (volume, pv)):
        require(record["status"]["phase"] == "Bound" and settings["storageClassName"] == "block-retain" and
                settings.get("volumeMode", "Filesystem") == "Filesystem" and settings["accessModes"] == ["ReadWriteOnce"])
    require(spec["volumeName"] == disk["volume"] and pv["persistentVolumeReclaimPolicy"] == "Retain" and
            pv["claimRef"]["namespace"] == namespace and pv["claimRef"]["name"] == disk["claim"] and pv["claimRef"]["uid"] == disk["claim_uid"] and
            pv["csi"]["driver"] == driver and not pv["csi"].get("readOnly") and digest(pv["csi"]["volumeHandle"].encode()) == disk["handle_sha256"])
    if snapshot:
        reference = {"apiGroup": snapshot_api, "kind": "VolumeSnapshot", "name": snapshot}
        require(spec["dataSource"] == reference and spec.get("dataSourceRef") in (None, reference) and
                not claim["metadata"].get("ownerReferences"))
    else:
        require(not spec.get("dataSource") and not spec.get("dataSourceRef"))
    size = "10Gi" if disk["claim"] == "kmc-etcd-source-kmc-etcd-source-0" else "1Gi"
    require(spec["resources"]["requests"]["storage"] == claim["status"]["capacity"]["storage"] == pv["capacity"]["storage"] == size)
    return pv["csi"]["volumeHandle"]


def verify_snapshot(snapshot, custody, original, original_handle):
    storage = kc("get", "storageclass", "block-retain", "-o", "json")
    metadata(storage, custody["storage_class_uid"])
    require(storage["provisioner"] == driver and storage["reclaimPolicy"] == "Retain" and storage["volumeBindingMode"] == "Immediate")
    ceph = kc("-n", "rook-ceph", "get", "cephcluster.ceph.rook.io", "rook-ceph", "-o", "json")
    require(ceph["metadata"].get("deletionTimestamp") is None and ceph["status"]["ceph"]["fsid"] == custody["ceph_fsid"])
    snap_class = kc("get", "volumesnapshotclass." + snapshot_api, custody["snapshot_class"], "-o", "json")
    metadata(snap_class, custody["snapshot_class_uid"])
    require(custody["snapshot_class"] == "etcd-source-retain" and snap_class["driver"] == driver and snap_class["deletionPolicy"] == "Retain")
    snap = get("volumesnapshot." + snapshot_api, snapshot["name"])
    metadata(snap, snapshot["uid"])
    require(snap["spec"]["volumeSnapshotClassName"] == custody["snapshot_class"] and
            snap["spec"]["source"] == {"persistentVolumeClaimName": original["claim"]} and
            snap["status"]["boundVolumeSnapshotContentName"] == snapshot["content_name"] and snap["status"]["readyToUse"] is True and
            not snap["status"].get("error") and snap["status"]["restoreSize"] == "1Gi")
    content = kc("get", "volumesnapshotcontent." + snapshot_api, snapshot["content_name"], "-o", "json")
    metadata(content, snapshot["content_uid"])
    spec, status = content["spec"], content["status"]
    ref = {"apiVersion": snapshot_api + "/v1", "kind": "VolumeSnapshot", "namespace": namespace, "name": snapshot["name"], "uid": snapshot["uid"]}
    require(spec["driver"] == driver and spec["deletionPolicy"] == "Retain" and spec["sourceVolumeMode"] == "Filesystem" and
            spec["volumeSnapshotClassName"] == custody["snapshot_class"] and spec["source"] == {"volumeHandle": original_handle} and
            all(spec["volumeSnapshotRef"][key] == value for key, value in ref.items()) and status["readyToUse"] is True and
            not status.get("error") and status["restoreSize"] == 1073741824 and digest(status["snapshotHandle"].encode()) == snapshot["handle_sha256"])


def claim_users(pods, claim):
    return [pod["metadata"]["uid"] for pod in pods if any(
        volume.get("persistentVolumeClaim", {}).get("claimName") == claim for volume in pod["spec"].get("volumes", []))]


def verify_live(source, custody, snapshot, disks, execution_uid):
    for name, uid in (("kube-system", source["guest_uid"]), (namespace, source["namespace_uid"])):
        record = kc("get", "namespace", name, "-o", "json")
        labels = metadata(record, uid).get("labels", {})
        require(record["status"]["phase"] == "Active" and not any(
            key in labels for key in ("bedrock.cloudyfolks.io/project", "bedrock.cloudyfolks.io/tenant")))
    record = get("cluster.k0smotron.io", cluster)
    live_cluster = metadata(record, source["cluster_uid"])
    require(record["apiVersion"] == "k0smotron.io/v1beta2" and live_cluster["labels"]["app.kubernetes.io/managed-by"] == "cluster-api" and
            record["spec"]["replicas"] == 1 and record["spec"]["version"] == source["k0s_version"] and
            record["spec"]["storage"]["type"] == "etcd" and record["spec"]["externalAddress"] == source["api_service"]["address"])
    require(not inventory(f"/apis/rbac.authorization.k8s.io/v1/namespaces/{namespace}/rolebindings", "RoleBindingList"))
    pods = inventory(f"/api/v1/namespaces/{namespace}/pods", "PodList")
    attachments = inventory("/apis/storage.k8s.io/v1/volumeattachments", "VolumeAttachmentList")
    volumes = inventory("/api/v1/persistentvolumes", "PersistentVolumeList")
    for role in (source["controller"], source["etcd"]):
        record = get("statefulset", role["statefulset"])
        info = metadata(record, role["statefulset_uid"])
        owner, = info["ownerReferences"]
        state = record["status"]
        require(owner["uid"] == source["cluster_uid"] and owner["controller"] is True and record["spec"]["replicas"] == 0 and
                state["observedGeneration"] == info["generation"] and all(state.get(key, 0) == 0 for key in
                    ("replicas", "currentReplicas", "readyReplicas", "updatedReplicas", "availableReplicas")))
        require(all(pod["metadata"]["name"] != role["pod"] and pod["metadata"]["uid"] != role["pod_uid"] and all(
            ref["uid"] != role["statefulset_uid"] for ref in pod["metadata"].get("ownerReferences", [])) for pod in pods))

    headless = get("service", service)
    metadata(headless, source["etcd"]["service"]["uid"])
    require(headless["spec"]["type"] == "ClusterIP" and headless["spec"]["clusterIP"] == "None" and headless["spec"]["selector"] == {
        "app": "k0smotron", "cluster": cluster, "component": "etcd", "app.kubernetes.io/managed-by": "cluster-api"})
    api = get("service", source["api_service"]["name"])
    metadata(api, source["api_service"]["uid"])
    require(api["spec"]["clusterIP"] == source["api_service"]["address"] and
            api["spec"]["selector"] == {"statefulset.kubernetes.io/pod-name": source["controller"]["pod"]})
    for name in (service, source["api_service"]["name"]):
        endpoints = namespaced("get", "endpointslices.discovery.k8s.io", "-l", "kubernetes.io/service-name=" + name, "-o", "json")
        require(all(not item["endpoints"] for item in endpoints["items"]))

    for role, disk in enumerate(disks):
        handle = disk_binding(disk, snapshot["name"] if role >= 2 else "")
        if role == 1:
            verify_snapshot(snapshot, custody, disk, handle)
        require([volume["metadata"]["uid"] for volume in volumes if
                 volume["spec"].get("csi", {}).get("volumeHandle") == handle or
                 volume["spec"].get("claimRef", {}).get("uid") == disk["claim_uid"] or
                 (volume["spec"].get("claimRef", {}).get("namespace") == namespace and
                  volume["spec"].get("claimRef", {}).get("name") == disk["claim"])] == [disk["volume_uid"]])
        users = claim_users(pods, disk["claim"])
        if role == len(disks) - 1 and execution_uid:
            require(users == [execution_uid])
        else:
            require(not users and not any(attachment["spec"]["source"].get("persistentVolumeName") == disk["volume"] or
                attachment["spec"]["source"].get("inlineVolumeSpec", {}).get("csi", {}).get("volumeHandle") == handle for attachment in attachments))


def denied(name, *args, body=None):
    try:
        kc(*args, body=body)
    except subprocess.CalledProcessError as error:
        require(name.encode() in error.stderr and b"The isolated etcd source is frozen." in error.stderr)
        return
    require(False)


def verify_fences(frozen, source, image):
    names = {role: "bedrock-e2e-etcd-freeze-" + suffix for role, suffix in
             (("pods", "pods"), ("scale", "scale"), ("retained_disks", "disks"))}
    for role, name in names.items():
        policy = kc("get", "validatingadmissionpolicy", name, "-o", "json")
        metadata(policy, frozen["fences"][role]["policy_uid"])
        require(policy["spec"]["failurePolicy"] == "Fail")
        binding = kc("get", "validatingadmissionpolicybinding", name, "-o", "json")
        metadata(binding, frozen["fences"][role]["binding_uid"])
        require(binding["spec"]["policyName"] == name and binding["spec"]["validationActions"] == ["Deny"])
    for role in ("controller", "etcd"):
        settings = source[role]
        claim, volume = settings["disk"]["claim"], settings["disk"]["volume"]
        probe = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "etcd-restart-fence-" + role}, "spec": {
            "containers": [{"name": "probe", "image": image, "volumeMounts": [{"name": "source", "mountPath": "/source"}]}],
            "volumes": [{"name": "source", "persistentVolumeClaim": {"claimName": claim}}]}}
        denied(names["pods"], "-n", namespace, "create", "--dry-run=server", "-f", "-", "-o", "json", body=encoded(probe))
        for subresource in ((), ("--subresource=scale",)):
            denied(names["scale"], "-n", namespace, "patch", "statefulset", settings["statefulset"], *subresource,
                   "--type=merge", "--dry-run=server", "-p", '{"spec":{"replicas":1}}', "-o", "json")
        denied(names["retained_disks"], "-n", namespace, "delete", "pvc", claim, "--dry-run=server", "--wait=false", "-o", "json")
        denied(names["retained_disks"], "delete", "pv", volume, "--dry-run=server", "--wait=false", "-o", "json")


def recovery_pod(image, claim, tree, execution_sha):
    script = r'''
set -euo pipefail
umask 077
export LC_ALL=C
expected=$1
member=$2
client=$3
peer=$4
data=/var/lib/k0s/etcd
pki=/var/lib/k0s/pki/etcd
listeners=https://127.0.0.1:2379
if [[ ${BEDROCK_ETCD_POD_IP+x} == x ]]; then
  [[ $BEDROCK_ETCD_POD_IP =~ ^(10\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])|192\.168)\.[0-9]{1,3}\.[0-9]{1,3}$ ]] || exit 1
  listeners+=,https://$BEDROCK_ETCD_POD_IP:2379
fi
for tool in etcd etcdctl stat sha256sum sync; do
  command -v "$tool" >/dev/null || exit 1
done
for file in ca.crt server.crt server.key peer.crt peer.key; do
  [[ -s $pki/$file && -r $pki/$file ]] || exit 1
done
mounted=0
while read -r _ _ _ _ path options _; do
  case "$path" in
    "$data")
      [[ ,$options, == *,rw,* ]] || exit 1
      mounted=$((mounted + 1)) ;;
    "$data/"*) exit 1 ;;
  esac
done </proc/self/mountinfo
[[ $mounted == 1 ]] || exit 1
cd "$data"
shopt -s dotglob nullglob
for entry in *; do
  case "$entry" in
    existing|member) ;;
    lost+found)
      [[ -d $entry && ! -L $entry ]] || exit 1
      contents=(lost+found/*)
      [[ ${#contents[@]} == 0 ]] || exit 1 ;;
    *) exit 1 ;;
  esac
done
device=$(stat -c '%d' -- .)
for directory in member member/snap member/wal; do
  [[ -d $directory && ! -L $directory && $(stat -c '%d' -- "$directory") == "$device" ]] || exit 1
done
children=(member/*)
[[ ${#children[@]} == 2 && ${children[0]} == member/snap && ${children[1]} == member/wal ]] || exit 1
wal=(member/wal/*)
files=(member/snap/* "${wal[@]}")
if [[ -e existing || -L existing ]]; then
  files=(existing "${files[@]}")
fi
[[ -s member/snap/db && ${#wal[@]} -gt 0 && ${#files[@]} -le 256 ]] || exit 1
snapshot_name='^member/snap/([0-9a-f]{16}-[0-9a-f]{16}\.snap|[0-9a-f]{16}\.snap\.db)$'
wal_name='^member/wal/[0-9a-f]{16}-[0-9a-f]{16}\.wal$'
result=$(
  (
    printf 'legacy-etcd-member-tree-v1\n'
    total=0
    for file in "${files[@]}"; do
      [[ -f $file && ! -L $file ]] || exit 1
      [[ $file == existing || $file == member/snap/db || $file =~ $snapshot_name || $file =~ $wal_name ]] || exit 1
      state=$(stat -c '%d %i %h %s %y %z' -- "$file") || exit 1
      read -r file_device inode links size times <<<"$state"
      [[ $file_device == "$device" && $inode -gt 0 && $links == 1 && $size =~ ^[0-9]{1,10}$ ]] || exit 1
      if [[ $file == existing ]]; then
        [[ $size == 0 ]] || exit 1
      else
        [[ $size -gt 0 ]] || exit 1
      fi
      total=$((total + size))
      [[ $total -le 1073741824 ]] || exit 1
      hash=$(sha256sum -- "$file") || exit 1
      [[ $(stat -c '%d %i %h %s %y %z' -- "$file") == "$state" ]] || exit 1
      printf '%s\t%s\t%s\n' "$file" "$size" "${hash%% *}"
    done
  ) | sha256sum
)
[[ ${result%% *} == "$expected" ]] || exit 1
sync -f "$data"
set -C
: > .bedrock-etcd-restart-started
set +C
sync -f .bedrock-etcd-restart-started
exec -c etcd --name="$member" --data-dir="$data" --initial-cluster="$member=$peer" --initial-cluster-state=existing \
  --listen-client-urls="$listeners" --advertise-client-urls="$client" \
  --listen-peer-urls=https://127.0.0.1:2380 --initial-advertise-peer-urls="$peer" \
  --client-cert-auth=true --trusted-ca-file="$pki/ca.crt" --cert-file="$pki/server.crt" --key-file="$pki/server.key" \
  --peer-client-cert-auth=true --peer-trusted-ca-file="$pki/ca.crt" --peer-cert-file="$pki/peer.crt" --peer-key-file="$pki/peer.key" \
  --tls-min-version=TLS1.2 --enable-pprof=false >/dev/null 2>&1
'''
    projections = [{"secret": {"name": cluster + "-etcd", "items": [{"key": "tls.crt", "path": "ca.crt"}]}}] + [
        {"secret": {"name": cluster + "-etcd-" + role,
                    "items": [{"key": "tls.crt", "path": role + ".crt"}, {"key": "tls.key", "path": role + ".key"}]}}
        for role in ("server", "peer")]
    labels = {"migration.cloudyfolks.io/purpose": "etcd-native-restart"}
    annotations = {"migration.cloudyfolks.io/execution-sha256": execution_sha}
    return {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": pod_name, "namespace": namespace, "labels": labels,
                                                             "annotations": annotations},
            "spec": {"schedulingGates": [{"name": gate_name}], "restartPolicy": "Never", "terminationGracePeriodSeconds": 30,
                     "automountServiceAccountToken": False, "enableServiceLinks": False,
                     "hostAliases": [{"ip": "127.0.0.1", "hostnames": [service, advertised_host]}],
                     "securityContext": {"runAsUser": 0, "seccompProfile": {"type": "RuntimeDefault"}},
                     "volumes": [{"name": "execution", "persistentVolumeClaim": {"claimName": claim}},
                                 {"name": "certs", "projected": {"defaultMode": 0o400, "sources": projections}}],
                     "containers": [{"name": "etcd", "image": image, "imagePullPolicy": "Never", "command": ["/bin/bash", "-ec"],
                                     "args": [script, "etcd-native-restart", tree, member_name, client, peer],
                                     "securityContext": {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
                                                         "capabilities": {"drop": ["ALL"]}},
                                     "volumeMounts": [{"name": "execution", "mountPath": "/var/lib/k0s/etcd"},
                                                      {"name": "certs", "mountPath": "/var/lib/k0s/pki/etcd", "readOnly": True}]}]}}


def reachable_pod(image, claim, tree, execution_sha):
    pod = recovery_pod(image, claim, tree, execution_sha)
    spec = pod["spec"]
    container, = spec["containers"]
    address = {"name": "BEDROCK_ETCD_POD_IP", "valueFrom": {"fieldRef": {"apiVersion": "v1", "fieldPath": "status.podIP"}}}
    return {**pod, "metadata": {**pod["metadata"], "name": reachable_name, "labels": reachable_labels},
            "spec": {**spec, "containers": [{**container, "env": [address]}]}}


def reachable_policy():
    return {"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": {"name": reachable_name, "namespace": namespace},
            "spec": {"podSelector": {"matchLabels": reachable_labels}, "policyTypes": ["Ingress", "Egress"],
                     "ingress": [{"from": [{"podSelector": {"matchLabels": controller_labels}}],
                                  "ports": [{"port": 2379, "protocol": "TCP"}]}], "egress": []}}


def selects(selector, labels):
    if any(labels.get(key) != value for key, value in selector.get("matchLabels", {}).items()):
        return False
    for clause in selector.get("matchExpressions", []):
        key, operation = clause["key"], clause["operator"]
        values = clause.get("values", [])
        matches = {"In": key in labels and labels[key] in values, "NotIn": labels.get(key) not in values,
                   "Exists": key in labels, "DoesNotExist": key not in labels}
        require(operation in matches)
        if not matches[operation]:
            return False
    return True


def no_foreign_routes():
    pods = inventory(f"/api/v1/namespaces/{namespace}/pods", "PodList")
    require(not any(selects({"matchLabels": controller_labels}, pod["metadata"].get("labels", {})) for pod in pods))
    services = inventory(f"/api/v1/namespaces/{namespace}/services", "ServiceList")
    require(not any(record["spec"].get("selector") and selects({"matchLabels": record["spec"]["selector"]}, reachable_labels)
                    for record in services))


def matching_policies():
    policies = inventory(f"/apis/networking.k8s.io/v1/namespaces/{namespace}/networkpolicies", "NetworkPolicyList")
    return [item for item in policies if selects(item["spec"]["podSelector"], reachable_labels)]


def verify_network_policy(record, expected, uid):
    info = metadata(record, uid)
    require(info["name"] == reachable_name and info["namespace"] == namespace and not info.get("ownerReferences"))
    spec = record["spec"]
    require({**spec, "egress": spec.get("egress", [])} == expected["spec"])


def prepare_network_policy():
    no_foreign_routes()
    require(not matching_policies())
    policy = reachable_policy()
    definition = encoded(policy)
    dry = namespaced("create", "--dry-run=server", "-f", "-", "-o", "json", body=definition)
    verify_network_policy(dry, policy, dry["metadata"]["uid"])
    created = namespaced("create", "-f", "-", "-o", "json", body=definition)
    uid = created["metadata"]["uid"]
    require(valid_uid(uid))
    verify_reachable_policy(uid)
    return uid


def verify_reachable_policy(uid):
    no_foreign_routes()
    policy, = matching_policies()
    verify_network_policy(policy, reachable_policy(), uid)


def verify_reachable_attachment(disk, snapshot, node):
    handle = disk_binding(disk, snapshot["name"])
    attachments = inventory("/apis/storage.k8s.io/v1/volumeattachments", "VolumeAttachmentList")
    active = [record for record in attachments if record["spec"]["source"].get("persistentVolumeName") == disk["volume"] or
              record["spec"]["source"].get("inlineVolumeSpec", {}).get("csi", {}).get("volumeHandle") == handle]
    require(not active or (node and len(active) == 1 and active[0]["spec"]["nodeName"] == node and
            active[0]["spec"]["attacher"] == driver and active[0]["spec"]["source"] == {"persistentVolumeName": disk["volume"]} and
            not active[0]["metadata"].get("deletionTimestamp")))


def verify_reachable_sockets(uid, address):
    expected_ip = address.packed[::-1].hex().upper()
    result = namespaced("exec", reachable_name, "-c", "etcd", "--", "/bin/bash", "-ec", r'''
set -eu
expected=$1
client=0
peer=0
pod=0
for table in /proc/net/tcp /proc/net/tcp6; do
  [[ -r $table ]] || continue
  while read -r _ bound _ state _; do
    [[ $state == 0A ]] || continue
    case "$bound" in
      0100007F:094B) client=$((client + 1)) ;;
      0100007F:094C) peer=$((peer + 1)) ;;
      "$expected:094B") pod=$((pod + 1)) ;;
      *:094B|*:094C) exit 1 ;;
    esac
  done <"$table"
done
[[ $client == 1 && $peer == 1 && $pod == 1 ]] || exit 1
printf '%s\n' '{"verified":true}'
''', "etcd-reachable-sockets", expected_ip)
    require(result == {"verified": True})
    endpoints = inventory(f"/apis/discovery.k8s.io/v1/namespaces/{namespace}/endpointslices", "EndpointSliceList")
    require(not any(record.get("targetRef", {}).get("uid") == uid or str(address) in record["addresses"]
                    for item in endpoints for record in item["endpoints"]))


def require_contract(record, expected):
    info, spec = record["metadata"], record["spec"]
    reference, wanted = expected["metadata"], expected["spec"]
    require(info["name"] == reference["name"] and info["namespace"] == namespace and info.get("deletionTimestamp") is None and
            not info.get("ownerReferences") and info["labels"] == reference["labels"] and all(
            info["annotations"][key] == value for key, value in reference["annotations"].items()))
    require(all(spec[key] == value for key, value in wanted.items() if key != "containers") and not any(spec.get(key) for key in
                ("nodeName", "runtimeClassName", "hostNetwork", "hostPID", "hostIPC", "initContainers", "ephemeralContainers")))
    actual, = spec["containers"]
    target, = wanted["containers"]
    require(all(actual[key] == value for key, value in target.items()) and actual.get("env", []) == target.get("env", []) and
            not any(actual.get(key) for key in ("envFrom", "ports", "volumeDevices", "lifecycle", "startupProbe", "livenessProbe", "readinessProbe")))


def runtime(record, uid, spec, image):
    metadata(record, uid)
    actual = {key: value for key, value in record["spec"].items() if key not in ("nodeName", "schedulingGates")}
    require(actual == spec and not record["spec"].get("schedulingGates"))
    states = record["status"].get("containerStatuses", [])
    phase = record["status"].get("phase")
    require(phase not in ("Failed", "Succeeded"), "The isolated native etcd process terminated before verification.")
    if not states:
        return None
    state, = states
    require(state["state"].get("waiting", {}).get("reason") != "ErrImageNeverPull",
            "Preload the digest alias of the original etcd image on the development guest.")
    if not state["state"].get("running"):
        return None
    require(phase == "Running" and state["name"] == "etcd" and state["restartCount"] == 0 and
            state["imageID"].endswith(image.split("@", 1)[1]) and re.fullmatch(r"containerd://[0-9a-f]{64}", state["containerID"]))
    return record["spec"]["nodeName"], state["containerID"]


def tls_probe(name, endpoint_url, *operation):
    return namespaced("exec", name, "-c", "etcd", "--", "/bin/bash", "-ec", r'''
endpoint=$1
shift
pki=/var/lib/k0s/pki/etcd
[[ -f /var/lib/k0s/etcd/.bedrock-etcd-restart-started ]] || exit 1
exec -c etcdctl --endpoints="$endpoint" --cacert="$pki/ca.crt" --cert="$pki/server.crt" --key="$pki/server.key" \
  --insecure-transport=false --insecure-skip-tls-verify=false --dial-timeout=3s --command-timeout=8s --write-out=json "$@"
''', "etcd-restart-proof", endpoint_url, *operation)


def check_member(identity, source, membership, observed):
    member, = membership["members"]
    entry, = observed
    status, listing = entry["Status"], membership["header"]
    header = status["header"]
    cluster_id, member_id = uint64(header["cluster_id"]), uint64(header["member_id"])
    require(f"{cluster_id:016x}" == identity["cluster_id"] and f"{member_id:016x}" == identity["member_id"] and
            uint64(listing["cluster_id"]) == cluster_id and uint64(listing["member_id"]) == member_id and
            uint64(member["ID"]) == member_id == uint64(status["leader"]))
    require(entry["Endpoint"] == endpoint and status["version"] == "3.7.1" and member["name"] == member_name and
            member["peerURLs"] == [peer] and member["clientURLs"] == [client] and not status.get("errors") and
            member.get("isLearner", False) is False and status.get("isLearner", False) is False)
    revision, term, index, applied = (uint64(header["revision"]), uint64(status["raftTerm"]),
                                      uint64(status["raftIndex"]), uint64(status["raftAppliedIndex"]))
    require(revision >= max(decimal(identity["committed_revision"]), decimal(source["marker"]["revision"])) and
            uint64(header["raft_term"]) == term >= decimal(identity["raft_term"]) and
            index >= decimal(identity["raft_index"]) and index >= applied >= decimal(identity["raft_applied_index"]))
    return {**identity, "committed_revision": str(revision), "raft_term": str(term), "raft_index": str(index), "raft_applied_index": str(applied)}


def observe(name, uid, plan, image, original, source):
    deadline = time.monotonic() + 240
    while time.monotonic() < deadline:
        current = runtime(get("pod", name), uid, plan, image)
        if current:
            try:
                members = tls_probe(name, endpoint, "member", "list")
                status = tls_probe(name, endpoint, "endpoint", "status")
            except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
                time.sleep(3)
                continue
            require(runtime(get("pod", name), uid, plan, image) == current)
            return current, check_member(original["status"], source, members, status)
        time.sleep(3)
    require(False, "The isolated native etcd member did not become ready within the time limit.")


def publish(directory, filename, receipt):
    data = encoded(receipt)
    with tempfile.NamedTemporaryFile(dir=directory, prefix=".etcd-restart-") as record:
        record.write(data)
        record.flush()
        os.fsync(record.fileno())
        os.link(record.name, directory / filename)
    descriptor = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    print(digest(data))


def main(entry, guest, source_path, source_sha, original_path, original_sha, proof_dir, custody_sha, execution_sha, output_dir):
    builder, filename = {"loopback": (recovery_pod, "etcd-restart.json"),
                         "reachable": (reachable_pod, "etcd-reachable-member.json")}[entry]
    output = private_dir(Path(output_dir))
    require(not os.path.lexists(output / filename))
    source_path = Path(source_path)
    source = approved_json(source_path, source_sha)
    original = approved_json(Path(original_path), original_sha)
    image = source_identity(source, original, guest, source_sha)
    frozen, custody, snapshot, disks, tree = check_custody(private_dir(Path(proof_dir)), custody_sha, execution_sha, source, source_path, source_sha)
    disk = disks[-1]
    verify_live(source, custody, snapshot, disks, "")
    verify_fences(frozen, source, image)
    pod = builder(image, disk["claim"], tree, execution_sha)
    name = pod["metadata"]["name"]
    definition = encoded(pod)
    dry = namespaced("create", "--dry-run=server", "-f", "-", "-o", "json", body=definition)
    require_contract(dry, pod)
    policy_uid = prepare_network_policy() if entry == "reachable" else None
    created = namespaced("create", "-f", "-", "-o", "json", body=definition)
    uid = created["metadata"]["uid"]
    require(valid_uid(uid))
    try:
        proposed = get("pod", name)
        info = metadata(proposed, uid)
        require_contract(proposed, pod)
        require(proposed["spec"] == dry["spec"])
        verify_live(source, custody, snapshot, disks, uid)
        if policy_uid:
            verify_reachable_policy(policy_uid)
            verify_reachable_attachment(disk, snapshot, None)
        changes = [{"op": "test", "path": "/metadata/uid", "value": uid},
                   {"op": "test", "path": "/metadata/resourceVersion", "value": info["resourceVersion"]},
                   {"op": "test", "path": "/spec/schedulingGates", "value": [{"name": gate_name}]},
                   {"op": "remove", "path": "/spec/schedulingGates"}]
        patched = namespaced("patch", "pod", name, "--type=json", "-p", encoded(changes).decode(), "-o", "json")
        spec = {key: value for key, value in proposed["spec"].items() if key != "schedulingGates"}
        runtime(patched, uid, spec, image)
        current, status = observe(name, uid, spec, image, original, source)
        verify_live(source, custody, snapshot, disks, uid)
        verify_fences(frozen, source, image)
        running = get("pod", name)
        require(runtime(running, uid, spec, image) == current)
        receipt = {"version": 1, "scope": "synthetic-etcd-native-restart", "guest_uid": guest,
                   "namespace_uid": source["namespace_uid"], "source_cluster_uid": source["cluster_uid"],
                   "source_receipt_sha256": source_sha, "original_member_sha256": original_sha, "custody_sha256": custody_sha,
                   "execution_sha256": execution_sha, "execution_disk": disk, "member_tree_sha256": tree,
                   "pod_name": name, "pod_uid": uid, "image": image, "status": status}
        if policy_uid:
            address = ipaddress.IPv4Address(running["status"]["podIP"])
            require(address.is_private and not (address.is_loopback or address.is_link_local or address.is_unspecified))
            verify_reachable_sockets(uid, address)
            verify_reachable_policy(policy_uid)
            verify_reachable_attachment(disk, snapshot, current[0])
            checked = get("pod", name)
            require(runtime(checked, uid, spec, image) == current and checked["status"]["podIP"] == str(address) and
                    checked["metadata"]["labels"] == reachable_labels and not checked["metadata"].get("ownerReferences"))
            receipt = {**receipt, "scope": "synthetic-etcd-reachable-member", "frozen_receipt_sha256": custody["frozen_receipt_sha256"],
                       "pod_ip": str(address), "container_id": current[1], "network_policy_uid": policy_uid}
        publish(output, filename, receipt)
    except (OSError, subprocess.SubprocessError, AttributeError, KeyError, TypeError, ValueError, RecursionError, KeyboardInterrupt):
        try:
            namespaced("delete", "--raw", f"/api/v1/namespaces/{namespace}/pods/{name}", "-f", "-", body=encoded(
                {"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": {"uid": uid}}))
        except (OSError, subprocess.SubprocessError, ValueError):
            print("The isolated recovery Pod cleanup could not be verified.", file=sys.stderr)
        raise


def report_checkpoint(error):
    checkpoints = [(frame.f_code.co_name, line) for frame, line in walk_tb(error.__traceback__)
                   if frame.f_code.co_filename == "<stdin>" and frame.f_code.co_name not in ("<module>", "require")]
    if checkpoints:
        name, line = checkpoints[-1]
        print(f"etcd recovery check stopped in {name} at line {line}.", file=sys.stderr)


try:
    main(*sys.argv[1:])
except ValueError as error:
    report_checkpoint(error)
    accepted = {"The approved isolated etcd recovery state could not be verified.",
                "Preload the digest alias of the original etcd image on the development guest.",
                "The isolated native etcd process terminated before verification.",
                "The isolated native etcd member did not become ready within the time limit."}
    sys.exit(str(error) if str(error) in accepted else "The approved isolated etcd recovery state could not be verified.")
except (OSError, subprocess.SubprocessError, AttributeError, KeyError, TypeError, RecursionError, KeyboardInterrupt) as error:
    report_checkpoint(error)
    sys.exit("The approved isolated etcd recovery state could not be verified.")
PY
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  run_etcd_member_recovery loopback "$@"
fi
