#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

if [[ ${E2E_MANAGED_ETCD_CONTROLLER_COPY:-} != 1 || $# != 6 ||
  ! ${1:-} =~ ^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$ || ! ${3:-} =~ ^[0-9a-f]{64}$ ||
  ! ${5:-} =~ ^[0-9a-f]{64}$ || ! ${6:-} =~ ^sha256:[0-9a-f]{64}$ ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_ETCD_CONTROLLER_COPY=1 bash hack/e2e-managed-etcd-controller-copy.sh <guest-uid> <private-source.json> <source-sha> <private-freeze-proof-dir> <frozen-sha> <busybox-image-digest>' >&2
  exit 2
fi

export KUBECONFIG=/var/lib/k0s/pki/admin.conf
if [[ $(uname -s) != Linux || ! -s /var/lib/bedrock/e2e-admin-password || ! -f $KUBECONFIG || -L $KUBECONFIG ]]; then
  printf '%s\n' 'Run this fixture inside an initialized Bedrock development guest.' >&2
  exit 1
fi

python3 -I - "$@" <<'PY'
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import time

namespace = "bedrock-e2e-etcd-source"
cluster = "etcd-source"
driver = "rook-ceph.rbd.csi.ceph.com"
snapshot_api = "snapshot.storage.k8s.io"
snapshot_name = "etcd-controller-frozen"
snapshot_class = "etcd-controller-retain"
claim_name = "etcd-controller-execution"
reader_name = "etcd-controller-signer-reader"
gate = [{"name": "e2e.bedrock.cloudyfolks.io/controller-copy"}]
signers = (("apiserver-etcd-client", "apiserver-etcd-client.crt", "apiserver-etcd-client.key"),
           ("ca", "ca.crt", "ca.key"), ("etcd", "etcd-ca.crt", "etcd-ca.key"),
           ("proxy", "front-proxy-ca.crt", "front-proxy-ca.key"), ("sa", "sa.pub", "sa.key"))


def require(ok):
    if not ok:
        raise ValueError()


def valid_uid(value):
    return re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", value) and value != "00000000-0000-0000-0000-000000000000"


def encoded(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


def digest(value):
    return hashlib.sha256(value).hexdigest()


def unique(pairs):
    value = dict(pairs)
    require(len(value) == len(pairs))
    return value


def parse(data):
    require(0 < len(data) <= 64 * 1024 * 1024)
    return json.loads(data, object_pairs_hook=unique)


def file_data(path, mode, limit):
    require(path.is_absolute() and path.resolve(strict=True) == path)
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as file:
        info = os.fstat(file.fileno())
        require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == mode and
                info.st_uid == os.geteuid() and info.st_nlink == 1 and 0 < info.st_size <= limit)
        value = file.read(limit + 1)
    require(len(value) == info.st_size)
    return value


def approved(path, expected):
    data = file_data(path, 0o600, 64 * 1024 * 1024)
    require(digest(data) == expected)
    return parse(data)


def runner(words, body=None, timeout=45):
    result = subprocess.run(words, input=body, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    if result.returncode:
        raise subprocess.CalledProcessError(result.returncode, words, stderr=result.stderr)
    return result.stdout


def kubectl(*words, body=None, timeout=45):
    return runner(["kubectl", f"--request-timeout={timeout - 5}s", *words], body, timeout)


def get(resource, name):
    return parse(kubectl("-n", namespace, "get", resource, name, "-o", "json"))


def objects(path, kind):
    record = parse(kubectl("get", "--raw", path))
    require(record["kind"] == kind and record["metadata"]["resourceVersion"] and not record["metadata"].get("continue") and
            not record["metadata"].get("remainingItemCount") and isinstance(record["items"], list))
    return record["items"]


def named(items, name):
    value, = [item for item in items if item["metadata"].get("namespace") == namespace and item["metadata"]["name"] == name]
    return value


def live_metadata(record, uid):
    info = record["metadata"]
    require(valid_uid(uid) and info["uid"] == uid and info.get("deletionTimestamp") is None)
    return info


def created(record):
    uid = record["metadata"]["uid"]
    live_metadata(record, uid)
    return uid


def source_evidence(proofs, guest, source_path, source_sha, frozen_sha):
    source = approved(source_path, source_sha)
    freeze = approved(proofs / "frozen.json", frozen_sha)
    inventory_path = proofs / "frozen-inventory.json"
    inventory_sha = freeze["inventory_sha256"]
    inventory = approved(inventory_path, inventory_sha)
    require(source["version"] == freeze["version"] == 1 and source["guest_uid"] == freeze["guest_uid"] == guest and
            source["namespace"] == namespace and source["cluster"] == cluster and source["k0s_version"] == "v1.36.3+k0s.1" and
            source["namespace_uid"] == freeze["namespace_uid"] and source["cluster_uid"] == freeze["source_cluster_uid"] and
            freeze["source_receipt_sha256"] == source_sha)
    binary = Path(os.environ["BEDROCK_MIGRATE"])
    binary_sha = os.environ["BEDROCK_MIGRATE_SHA256"]
    require(binary.is_absolute() and binary.resolve(strict=True) == binary and binary.is_file() and os.access(binary, os.X_OK))
    with binary.open("rb") as file:
        binary_digest = hashlib.sha256()
        for data in iter(lambda: file.read(1024 * 1024), b""):
            binary_digest.update(data)
    require(binary_digest.hexdigest() == binary_sha)
    verified = parse(runner([str(binary), "verify-frozen-legacy-etcd", str(inventory_path), inventory_sha,
                             str(source_path), source_sha, guest]))
    expected = digest(f"frozen-legacy-etcd-v1\n{inventory_sha}\n{source_sha}\n{guest}\n".encode())
    require(verified["version"] == 1 and verified["verified_disks"] == 2 and verified["inventory_sha256"] == inventory_sha and
            verified["source_sha256"] == source_sha and verified["approval_sha256"] == expected)
    return source, freeze, inventory, expected


def bound_disk(claim, size, snapshot):
    pvc = get("pvc", claim)
    claim_uid = created(pvc)
    spec = pvc["spec"]
    volume = spec["volumeName"]
    pv = get("pv", volume)
    volume_uid = created(pv)
    for obj in (pvc, pv):
        require(obj["status"]["phase"] == "Bound" and obj["spec"]["storageClassName"] == "block-retain" and
                obj["spec"].get("volumeMode", "Filesystem") == "Filesystem" and obj["spec"]["accessModes"] == ["ReadWriteOnce"])
    source = {"apiGroup": snapshot_api, "kind": "VolumeSnapshot", "name": snapshot} if snapshot else None
    require(spec.get("dataSource") == source and spec.get("dataSourceRef") in (None, source) and
            (not snapshot or not pvc["metadata"].get("ownerReferences")))
    settings = pv["spec"]
    binding, csi = settings["claimRef"], settings["csi"]
    require(spec["resources"]["requests"]["storage"] == pvc["status"]["capacity"]["storage"] == settings["capacity"]["storage"] == size and
            settings["persistentVolumeReclaimPolicy"] == "Retain" and binding["namespace"] == namespace and binding["name"] == claim and
            binding["uid"] == claim_uid and csi["driver"] == driver and not csi.get("readOnly") and csi["volumeHandle"])
    handle = csi["volumeHandle"]
    return {"claim": claim, "claim_uid": claim_uid, "volume": volume, "volume_uid": volume_uid, "handle_sha256": digest(handle.encode())}, handle


def claim_users(pods, claim):
    return [pod["metadata"]["uid"] for pod in pods if any(volume.get("persistentVolumeClaim", {}).get("claimName") == claim
            for volume in pod["spec"].get("volumes", []))]


def disk_clear(disk, handle, pods, attachments):
    return not claim_users(pods, disk["claim"]) and not any(
        attachment["spec"]["source"].get("persistentVolumeName") == disk["volume"] or
        attachment["spec"]["source"].get("inlineVolumeSpec", {}).get("csi", {}).get("volumeHandle") == handle for attachment in attachments)


def pod_inventory():
    return objects(f"/api/v1/namespaces/{namespace}/pods", "PodList")


def attachment_inventory():
    return objects("/apis/storage.k8s.io/v1/volumeattachments", "VolumeAttachmentList")


def unique_bindings(disks):
    volumes = objects("/api/v1/persistentvolumes", "PersistentVolumeList")
    for disk, handle in disks:
        owners = [item["metadata"]["uid"] for item in volumes if item["spec"].get("csi", {}).get("volumeHandle") == handle or
                  item["spec"].get("claimRef", {}).get("uid") == disk["claim_uid"] or
                  (item["spec"].get("claimRef", {}).get("namespace") == namespace and
                   item["spec"].get("claimRef", {}).get("name") == disk["claim"])]
        require(owners == [disk["volume_uid"]])


def stopped_source(source, inventory):
    for name, uid in (("kube-system", source["guest_uid"]), (namespace, source["namespace_uid"])):
        record = get("namespace", name)
        info = live_metadata(record, uid)
        require(record["status"]["phase"] == "Active" and not any(key in info.get("labels", {}) for key in
                    ("bedrock.cloudyfolks.io/tenant", "bedrock.cloudyfolks.io/project")))
    require(not objects(f"/apis/rbac.authorization.k8s.io/v1/namespaces/{namespace}/rolebindings", "RoleBindingList"))
    live = get("cluster.k0smotron.io", cluster)
    live_metadata(live, source["cluster_uid"])
    require(live["spec"] == named(inventory["clusters"]["items"], cluster)["spec"])
    pods, attachments = pod_inventory(), attachment_inventory()
    bindings = []
    for role, size in (("controller", "10Gi"), ("etcd", "1Gi")):
        record = source[role]
        live = get("statefulset", record["statefulset"])
        meta = live_metadata(live, record["statefulset_uid"])
        original = named(inventory["stateful_sets"]["items"], record["statefulset"])
        require(live["spec"] == original["spec"] and live["spec"]["replicas"] == 0 and meta["ownerReferences"] == original["metadata"]["ownerReferences"] and
                live["status"]["observedGeneration"] == meta["generation"] and all(live["status"].get(key, 0) == 0 for key in
                    ("replicas", "currentReplicas", "updatedReplicas", "readyReplicas", "availableReplicas")))
        require(all(pod["metadata"]["name"] != record["pod"] and pod["metadata"]["uid"] != record["pod_uid"] and all(
            ref["uid"] != record["statefulset_uid"] for ref in pod["metadata"].get("ownerReferences", [])) for pod in pods))
        binding = bound_disk(record["disk"]["claim"], size, "")
        require(binding[0] == record["disk"] and disk_clear(*binding, pods, attachments))
        bindings.append(binding)
    unique_bindings(bindings)
    return bindings


def denied(policy, *words, body=None):
    try:
        kubectl(*words, body=body)
    except subprocess.CalledProcessError as error:
        require(policy.encode() in error.stderr and b"The isolated etcd source is frozen." in error.stderr)
        return
    require(False)


def verify_fences(freeze, source, image):
    names = {key: "bedrock-e2e-etcd-freeze-" + suffix for key, suffix in (("pods", "pods"), ("scale", "scale"), ("retained_disks", "disks"))}
    for role, name in names.items():
        policy = get("validatingadmissionpolicy", name)
        binding = get("validatingadmissionpolicybinding", name)
        live_metadata(policy, freeze["fences"][role]["policy_uid"])
        live_metadata(binding, freeze["fences"][role]["binding_uid"])
        require(policy["spec"]["failurePolicy"] == "Fail" and binding["spec"]["policyName"] == name and binding["spec"]["validationActions"] == ["Deny"])
    for role in ("controller", "etcd"):
        record = source[role]
        claim, volume = record["disk"]["claim"], record["disk"]["volume"]
        probe = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "controller-copy-fence-" + role}, "spec": {
            "containers": [{"name": "probe", "image": image, "volumeMounts": [{"name": "source", "mountPath": "/source", "readOnly": True}]}],
            "volumes": [{"name": "source", "persistentVolumeClaim": {"claimName": claim, "readOnly": True}}]}}
        denied(names["pods"], "-n", namespace, "create", "--dry-run=server", "-f", "-", body=encoded(probe))
        for subresource in ((), ("--subresource=scale",)):
            denied(names["scale"], "-n", namespace, "patch", "statefulset", record["statefulset"], *subresource,
                   "--type=merge", "--dry-run=server", "-p", '{"spec":{"replicas":1}}')
        denied(names["retained_disks"], "-n", namespace, "delete", "pvc", claim, "--dry-run=server", "--wait=false")
        denied(names["retained_disks"], "delete", "pv", volume, "--dry-run=server", "--wait=false")


def rook_settings():
    block = get("storageclass", "block")
    storage = get("storageclass", "block-retain")
    seed = get("volumesnapshotclass." + snapshot_api, "block")
    ceph = parse(kubectl("-n", "rook-ceph", "get", "cephcluster.ceph.rook.io", "rook-ceph", "-o", "json"))
    require(all(item["metadata"].get("deletionTimestamp") is None for item in (block, storage, seed, ceph)))
    require(block["provisioner"] == storage["provisioner"] == seed["driver"] == driver and storage["reclaimPolicy"] == "Retain" and
            storage.get("volumeBindingMode", "Immediate") == "Immediate" and block["parameters"] == storage["parameters"] and
            storage["parameters"]["clusterID"] == "rook-ceph" and seed["deletionPolicy"] == "Delete")
    params = seed["parameters"]
    require(params["clusterID"] == "rook-ceph" and params["csi.storage.k8s.io/snapshotter-secret-name"] == "rook-csi-rbd-provisioner" and
            params["csi.storage.k8s.io/snapshotter-secret-namespace"] == "rook-ceph")
    fsid = ceph["status"]["ceph"]["fsid"]
    require(valid_uid(fsid))
    return {"storage_class_uid": created(storage), "ceph_fsid": fsid}, params


def snapshot_custody(source_disk, source_handle, class_uid, snapshot_uid, params):
    storage = get("volumesnapshotclass." + snapshot_api, snapshot_class)
    live_metadata(storage, class_uid)
    require(storage["driver"] == driver and storage["deletionPolicy"] == "Retain" and storage["parameters"] == params)
    snapshot = get("volumesnapshot." + snapshot_api, snapshot_name)
    live_metadata(snapshot, snapshot_uid)
    status = snapshot["status"]
    require(snapshot["spec"] == {"volumeSnapshotClassName": snapshot_class, "source": {"persistentVolumeClaimName": source_disk["claim"]}} and
            status["readyToUse"] is True and not status.get("error") and status["restoreSize"] == "10Gi")
    content_name = status["boundVolumeSnapshotContentName"]
    require(re.fullmatch(r"[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?", content_name))
    content = get("volumesnapshotcontent." + snapshot_api, content_name)
    content_uid = created(content)
    spec, status = content["spec"], content["status"]
    ref = {"apiVersion": snapshot_api + "/v1", "kind": "VolumeSnapshot", "namespace": namespace, "name": snapshot_name, "uid": snapshot_uid}
    require(spec["driver"] == driver and spec["deletionPolicy"] == "Retain" and spec["sourceVolumeMode"] == "Filesystem" and
            spec["volumeSnapshotClassName"] == snapshot_class and spec["source"] == {"volumeHandle": source_handle} and
            all(spec["volumeSnapshotRef"][key] == value for key, value in ref.items()) and status["readyToUse"] is True and
            not status.get("error") and status["restoreSize"] == 10737418240 and status["snapshotHandle"])
    return {"name": snapshot_name, "uid": snapshot_uid, "content_name": content_name, "content_uid": content_uid,
            "handle_sha256": digest(status["snapshotHandle"].encode())}


def create(definition, *args):
    return parse(kubectl("-n", namespace, "create", "-f", "-", "-o", "json", *args, body=encoded(definition)))


def restore_claim(snapshot):
    return {"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": {"name": claim_name, "namespace": namespace},
            "spec": {"accessModes": ["ReadWriteOnce"], "volumeMode": "Filesystem", "storageClassName": "block-retain",
                     "resources": {"requests": {"storage": "10Gi"}},
                     "dataSource": {"apiGroup": snapshot_api, "kind": "VolumeSnapshot", "name": snapshot["name"]}}}


def signer_projections(source, inventory):
    controller = named(inventory["stateful_sets"]["items"], source["controller"]["statefulset"])
    certs, = [volume["projected"] for volume in controller["spec"]["template"]["spec"]["volumes"] if volume["name"] == "certs"]
    require(len(certs["sources"]) == len(signers))
    projections = {item["secret"]["name"]: item["secret"] for item in certs["sources"]}
    owner = ["k0smotron.io/v1beta2", "Cluster", cluster, source["cluster_uid"], "true"]
    template = ('{.metadata.uid}{"\\t"}{.metadata.resourceVersion}{"\\t"}{.metadata.deletionTimestamp}{"\\n"}'
                '{range .metadata.ownerReferences[*]}{.apiVersion}{"\\t"}{.kind}{"\\t"}{.name}{"\\t"}{.uid}{"\\t"}{.controller}{"\\n"}{end}')
    result = []
    for role, public, private in signers:
        name = cluster + "-" + role
        projection = projections[name]
        require(not projection.get("optional") and projection["items"] == [{"key": "tls.crt", "path": public}, {"key": "tls.key", "path": private}])
        info, reference = kubectl("-n", namespace, "get", "secret", name, "-o", "jsonpath=" + template).decode().splitlines()
        uid, version, deleted = info.split("\t")
        require(valid_uid(uid) and re.fullmatch(r"[1-9][0-9]*", version) and not deleted and reference.split("\t") == owner)
        result.append({"role": role, "name": name, "uid": uid, "resource_version": version, "public_file": public, "private_file": private})
    require(len({signer["uid"] for signer in result}) == len(signers))
    return result


def reader_pod(image, projections, claim_uid):
    labels = {"migration.cloudyfolks.io/purpose": "etcd-controller-signer-reader"}
    annotations = {"migration.cloudyfolks.io/claim-uid": claim_uid}
    sources = [{"secret": {"name": item["name"], "items": [{"key": "tls.crt", "path": item["public_file"]},
               {"key": "tls.key", "path": item["private_file"]}]}} for item in projections]
    return {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": reader_name, "namespace": namespace, "labels": labels,
                                                             "annotations": annotations},
            "spec": {"schedulingGates": gate, "automountServiceAccountToken": False, "enableServiceLinks": False,
                     "restartPolicy": "Never", "activeDeadlineSeconds": 900,
                     "securityContext": {"runAsUser": 0, "seccompProfile": {"type": "RuntimeDefault"}},
                     "containers": [{"name": "reader", "image": image, "imagePullPolicy": "Never", "command": ["sleep", "900"],
                                     "securityContext": {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
                                                         "capabilities": {"drop": ["ALL"], "add": ["DAC_READ_SEARCH"]}},
                                     "volumeMounts": [{"name": "execution", "mountPath": "/checkpoint", "readOnly": True},
                                                      {"name": "signers", "mountPath": "/original-signers", "readOnly": True}]}],
                     "volumes": [{"name": "execution", "persistentVolumeClaim": {"claimName": claim_name, "readOnly": True}},
                                 {"name": "signers", "projected": {"defaultMode": 0o400, "sources": sources}}]}}


def check_reader(record, expected):
    info, spec = record["metadata"], record["spec"]
    wanted, constraints = expected["metadata"], expected["spec"]
    require(info["name"] == reader_name and info["namespace"] == namespace and info.get("deletionTimestamp") is None and
            not info.get("ownerReferences") and info["labels"] == wanted["labels"] and all(
                info["annotations"][key] == value for key, value in wanted["annotations"].items()))
    require(all(spec[key] == value for key, value in constraints.items() if key != "containers") and not any(spec.get(key) for key in
                ("nodeName", "runtimeClassName", "hostNetwork", "hostPID", "hostIPC", "initContainers", "ephemeralContainers")))
    container, = spec["containers"]
    expected_container, = constraints["containers"]
    require(all(container[key] == value for key, value in expected_container.items()) and not any(container.get(key) for key in
                ("env", "envFrom", "ports", "volumeDevices", "lifecycle", "startupProbe", "livenessProbe", "readinessProbe")))


def running_reader(uid, approved_spec, image, disk):
    require(claim_users(pod_inventory(), disk["claim"]) == [uid])
    pod = get("pod", reader_name)
    live_metadata(pod, uid)
    spec = pod["spec"]
    require({key: value for key, value in spec.items() if key not in ("nodeName", "schedulingGates")} == approved_spec and
            not spec.get("schedulingGates") and spec["nodeName"] and pod["status"]["phase"] == "Running")
    running, = pod["status"]["containerStatuses"]
    require(running["name"] == "reader" and running["ready"] is True and running["restartCount"] == 0 and
            running["state"].get("running") and running["imageID"].endswith(image.split("@", 1)[1]) and running["containerID"])
    return {"pod": reader_name, "pod_uid": uid, "node": spec["nodeName"], "container_id": running["containerID"],
            "image_id": running["imageID"], "claim": disk["claim"], "volume": disk["volume"], "read_only": True}


def compare_signers(projections):
    script = r'''
set -eu
awk '
  $5=="/checkpoint" { disk++; if ($6 ~ /(^|,)ro(,|$)/) disk_ro++ }
  $5=="/original-signers" { signers++; if ($6 ~ /(^|,)ro(,|$)/) signers_ro++ }
  $5 ~ /^\/checkpoint\// { nested++ }
  END { exit disk != 1 || disk_ro != 1 || signers != 1 || signers_ro != 1 || nested != 0 }' /proc/self/mountinfo
test -d /checkpoint/pki
test ! -L /checkpoint/pki
while [ "$#" -gt 0 ]; do
  role=$1
  public=$2
  private=$3
  shift 3
  for file in "$public" "$private"; do
    original=/original-signers/$file
    copy=/checkpoint/pki/$file
    test -f "$original" && test -s "$original" || exit 1
    test -f "$copy" && test -s "$copy" && test ! -L "$copy" || exit 1
    test "$(stat -c %h "$copy")" -eq 1
    test "$(stat -c %s "$copy")" -le 65536
    cmp -s "$original" "$copy"
  done
  checksum=$(sha256sum "/checkpoint/pki/$public")
  printf '%s\t%s\n' "$role" "${checksum%% *}"
done
'''
    inputs = [item[key] for item in projections for key in ("role", "public_file", "private_file")]
    output = kubectl("-n", namespace, "exec", reader_name, "-c", "reader", "--", "sh", "-ec", script, "controller-copy", *inputs)
    lines = output.decode().splitlines()
    require(len(lines) == len(projections))
    result = []
    for line, item in zip(lines, projections):
        role, checksum = line.split("\t")
        require(role == item["role"] and re.fullmatch(r"[0-9a-f]{64}", checksum))
        result.append({**item, "public_sha256": checksum})
    return result


def detach(disk, handle):
    deadline = time.monotonic() + 240
    while time.monotonic() < deadline:
        if disk_clear(disk, handle, pod_inventory(), attachment_inventory()):
            return
        time.sleep(3)
    require(False)


def read_projection(image, projections, disk, handle):
    expected = reader_pod(image, projections, disk["claim_uid"])
    dry = create(expected, "--dry-run=server")
    check_reader(dry, expected)
    uid = created(create(expected))
    try:
        live = get("pod", reader_name)
        info = live_metadata(live, uid)
        check_reader(live, expected)
        require(live["spec"] == dry["spec"] and claim_users(pod_inventory(), disk["claim"]) == [uid] and
                bound_disk(claim_name, "10Gi", snapshot_name) == (disk, handle))
        patch = [{"op": "test", "path": "/metadata/uid", "value": uid},
                 {"op": "test", "path": "/metadata/resourceVersion", "value": info["resourceVersion"]},
                 {"op": "test", "path": "/spec/schedulingGates", "value": gate}, {"op": "remove", "path": "/spec/schedulingGates"}]
        approved = {key: value for key, value in live["spec"].items() if key != "schedulingGates"}
        kubectl("-n", namespace, "patch", "pod", reader_name, "--type=json", "-p", encoded(patch).decode(), "-o", "json")
        kubectl("-n", namespace, "wait", "--for=condition=Ready", "pod/" + reader_name, "--timeout=180s", timeout=190)
        identity = running_reader(uid, approved, image, disk)
        matched = compare_signers(projections)
        require(running_reader(uid, approved, image, disk) == identity and bound_disk(claim_name, "10Gi", snapshot_name) == (disk, handle))
    finally:
        kubectl("delete", "--raw", f"/api/v1/namespaces/{namespace}/pods/{reader_name}", "-f", "-", body=encoded(
            {"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": {"uid": uid}}))
    detach(disk, handle)
    return identity, matched


def assert_distinct(bindings):
    for key in ("claim", "claim_uid", "volume", "volume_uid", "handle_sha256"):
        require(len({disk[key] for disk, _ in bindings}) == len(bindings))
    unique_bindings(bindings)


def preloaded_image(image, image_digest):
    listing = runner(["k0s", "ctr", "--namespace", "k8s.io", "images", "ls"]).decode()
    aliases = [line.split()[2] for line in listing.splitlines() if len(line.split()) >= 3 and line.split()[0] == image]
    require(aliases == [image_digest])


def verify_new_names():
    candidates = ("volumesnapshotclass." + snapshot_api + "/" + snapshot_class,
                  "volumesnapshot." + snapshot_api + "/" + snapshot_name, "pvc/" + claim_name, "pod/" + reader_name)
    require(all(not kubectl("-n", namespace, "get", resource, "--ignore-not-found", "-o", "name").strip() for resource in candidates))


def publish(proofs, receipt):
    data = encoded(receipt)
    with tempfile.NamedTemporaryFile(dir=proofs, prefix=".controller-execution-") as output:
        output.write(data)
        output.flush()
        os.fsync(output.fileno())
        os.link(output.name, proofs / "etcd-controller-execution.json")
    descriptor = os.open(proofs, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    print(digest(data))


def main(guest, source_path, source_sha, proof_dir, frozen_sha, busybox_digest):
    proofs = Path(proof_dir)
    require(proofs.is_absolute() and proofs.resolve(strict=True) == proofs)
    info = os.lstat(proofs)
    require(stat.S_ISDIR(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o700 and info.st_uid == os.geteuid() and
            not os.path.lexists(proofs / "etcd-controller-execution.json"))
    image = "docker.io/library/busybox@" + busybox_digest
    source, freeze, inventory, frozen_approval = source_evidence(proofs, guest, Path(source_path), source_sha, frozen_sha)
    bindings = stopped_source(source, inventory)
    verify_fences(freeze, source, image)
    projections = signer_projections(source, inventory)
    preloaded_image(image, busybox_digest)
    verify_new_names()
    storage, params = rook_settings()
    source_disk, source_handle = bindings[0]

    class_uid = created(create({"apiVersion": snapshot_api + "/v1", "kind": "VolumeSnapshotClass", "metadata": {"name": snapshot_class},
                                "driver": driver, "deletionPolicy": "Retain", "parameters": params}))
    snapshot_uid = created(create({"apiVersion": snapshot_api + "/v1", "kind": "VolumeSnapshot",
                                   "metadata": {"name": snapshot_name, "namespace": namespace}, "spec": {
                                       "volumeSnapshotClassName": snapshot_class, "source": {"persistentVolumeClaimName": source_disk["claim"]}}}))
    kubectl("-n", namespace, "wait", "--for=jsonpath={.status.readyToUse}=true", "volumesnapshot." + snapshot_api + "/" + snapshot_name,
            "--timeout=360s", timeout=370)
    snapshot = snapshot_custody(source_disk, source_handle, class_uid, snapshot_uid, params)
    require(stopped_source(source, inventory) == bindings)
    verify_fences(freeze, source, image)

    claim_uid = created(create(restore_claim(snapshot)))
    kubectl("-n", namespace, "wait", "--for=jsonpath={.status.phase}=Bound", "pvc/" + claim_name, "--timeout=360s", timeout=370)
    execution, handle = bound_disk(claim_name, "10Gi", snapshot_name)
    require(execution["claim_uid"] == claim_uid and snapshot["handle_sha256"] not in {execution["handle_sha256"], source_disk["handle_sha256"]} and
            disk_clear(execution, handle, pod_inventory(), attachment_inventory()))
    assert_distinct([*bindings, (execution, handle)])
    require(signer_projections(source, inventory) == projections)
    reader, matched = read_projection(image, projections, execution, handle)

    require(stopped_source(source, inventory) == bindings and bound_disk(claim_name, "10Gi", snapshot_name) == (execution, handle) and
            disk_clear(execution, handle, pod_inventory(), attachment_inventory()) and rook_settings() == (storage, params) and
            snapshot_custody(source_disk, source_handle, class_uid, snapshot_uid, params) == snapshot and signer_projections(source, inventory) == projections)
    assert_distinct([*bindings, (execution, handle)])
    verify_fences(freeze, source, image)
    publish(proofs, {"version": 1, "scope": "synthetic-etcd-controller-execution", "guest_uid": guest,
                     "namespace_uid": source["namespace_uid"], "source_cluster_uid": source["cluster_uid"],
                     "source_receipt_sha256": source_sha, "frozen_receipt_sha256": frozen_sha, "frozen_approval_sha256": frozen_approval,
                     "source_controller_uid": source["controller"]["statefulset_uid"], "controller_image_id": source["controller"]["image_id"],
                     "source_disk": source_disk, "execution_disk": execution, "snapshot": snapshot, "snapshot_class": snapshot_class,
                     "snapshot_class_uid": class_uid, **storage, "certificate_projections": matched, "reader": reader})


try:
    main(*sys.argv[1:])
except (OSError, subprocess.SubprocessError, AttributeError, KeyError, TypeError, ValueError, RecursionError, KeyboardInterrupt):
    sys.exit("The approved isolated controller copy or its signer evidence could not be verified.")
PY
