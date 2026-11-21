#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

if [[ ${E2E_MANAGED_ETCD_CONTROLLER_START:-} != 1 || $# != 10 ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_ETCD_CONTROLLER_START=1 bash hack/e2e-managed-etcd-controller-start.sh <guest-uid> <source.json> <source-sha256> <frozen-proof-directory> <api-proof-directory> <api-evidence-sha256> <controller-execution.json> <controller-evidence-sha256> <reachable-member.json> <member-evidence-sha256>' >&2
  exit 2
fi
export KUBECONFIG=/var/lib/k0s/pki/admin.conf
if [[ $(uname -s) != Linux || ! -s /var/lib/bedrock/e2e-admin-password || ! -f $KUBECONFIG || -L $KUBECONFIG ]]; then
  printf '%s\n' 'Run this fixture inside an initialized Bedrock development guest.' >&2
  exit 1
fi
for tool in kubectl k0s python3; do
  command -v "$tool" >/dev/null
done
: "${BEDROCK_MIGRATE:?Set BEDROCK_MIGRATE to the verified Cluster API migration binary.}"
: "${BEDROCK_MIGRATE_SHA256:?Set BEDROCK_MIGRATE_SHA256 to the approved binary digest.}"

python3 -I - "$@" <<'PY'
import base64
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import signal
import ssl
import stat
import subprocess
import sys
import tempfile
import time

namespace = "bedrock-e2e-etcd-source"
cluster = "etcd-source"
api_name = "etcd-endpoint"
member_service = "kmc-etcd-source-etcd"
member_host = member_service + "-0." + member_service
etcd_endpoint = "https://" + member_service + ":2379"
target = "etcd-controller-recovery"
purpose_label = "migration.cloudyfolks.io/purpose"
label = {purpose_label: target}
reachable_name = "etcd-reachable-member"
reachable_label = {purpose_label: "etcd-member-recovery"}
probe_name = "etcd-api-recovery-probe"
gate = [{"name": "e2e.bedrock.cloudyfolks.io/etcd-controller-approved"}]
driver = "rook-ceph.rbd.csi.ceph.com"
snapshot_api = "snapshot.storage.k8s.io"
signers = {
    "ca": ("ca.crt", "ca.key"),
    "proxy": ("front-proxy-ca.crt", "front-proxy-ca.key"),
    "sa": ("sa.pub", "sa.key"),
    "etcd": ("etcd-ca.crt", "etcd-ca.key"),
    "apiserver-etcd-client": ("apiserver-etcd-client.crt", "apiserver-etcd-client.key"),
}
failure = "The approved isolated etcd controller recovery state could not be verified."


def require(condition, message=failure):
    if not condition:
        raise ValueError(message)


def uid(value):
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}", value) and value != "00000000-0000-0000-0000-000000000000"


def sha(value):
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value)


def digest(value):
    return hashlib.sha256(value).hexdigest()


def encode(value):
    return (json.dumps(value, separators=(",", ":")) + "\n").encode()


def unique(pairs):
    result = dict(pairs)
    require(len(result) == len(pairs))
    return result


def private_directory(path):
    info = path.lstat()
    require(path.is_absolute() and path.resolve(strict=True) == path and stat.S_ISDIR(info.st_mode) and
            stat.S_IMODE(info.st_mode) == 0o700 and info.st_uid == os.geteuid())


def approved_bytes(path, expected, maximum=1024 * 1024):
    require(sha(expected) and path.is_absolute() and path.resolve(strict=True) == path)
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600 and info.st_uid == os.geteuid() and
                info.st_nlink == 1 and 0 < info.st_size <= maximum)
        raw = stream.read(maximum + 1)
    require(len(raw) == info.st_size and digest(raw) == expected)
    return raw


def approved(path, expected, maximum=1024 * 1024):
    value = json.loads(approved_bytes(path, expected, maximum), object_pairs_hook=unique)
    require(type(value) is dict)
    return value


def run(args, body=None, timeout=35):
    result = subprocess.run(args, input=body, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    if result.returncode:
        raise subprocess.CalledProcessError(result.returncode, args, stderr=result.stderr)
    require(len(result.stdout) <= 64 * 1024 * 1024)
    return result.stdout


def kc(*args, body=None):
    return json.loads(run(["kubectl", "--request-timeout=25s", *args], body), object_pairs_hook=unique)


def ns(*args, body=None):
    return kc("-n", namespace, *args, body=body)


def get(kind, name):
    return ns("get", kind, name, "-o", "json")


def metadata(obj, expected):
    info = obj["metadata"]
    require(uid(expected) and info["uid"] == expected and not info.get("deletionTimestamp"))
    return info


def inventory(path, kind):
    record = kc("get", "--raw", path)
    require(record["kind"] == kind and record["metadata"]["resourceVersion"] and not record["metadata"].get("continue"))
    return record["items"]


def bound_disk(disk, source, size):
    require(set(disk) == {"claim", "claim_uid", "volume", "volume_uid", "handle_sha256"} and
            sha(disk["handle_sha256"]) and all(re.fullmatch(r"[a-z0-9][a-z0-9.-]{0,252}", disk[field]) for field in ("claim", "volume")))
    claim = get("pvc", disk["claim"])
    info = metadata(claim, disk["claim_uid"])
    backing = {"apiGroup": snapshot_api, "kind": "VolumeSnapshot", "name": source} if source else None
    spec = claim["spec"]
    require(claim["status"]["phase"] == "Bound" and claim["status"]["capacity"]["storage"] == size and
            spec["volumeName"] == disk["volume"] and spec["storageClassName"] == "block-retain" and
            spec.get("volumeMode", "Filesystem") == "Filesystem" and spec["accessModes"] == ["ReadWriteOnce"] and
            spec["resources"]["requests"]["storage"] == size and spec.get("dataSource") == backing and
            spec.get("dataSourceRef") in (None, backing) and (not source or not info.get("ownerReferences")))
    pv = kc("get", "pv", disk["volume"], "-o", "json")
    metadata(pv, disk["volume_uid"])
    spec, csi = pv["spec"], pv["spec"]["csi"]
    require(pv["status"]["phase"] == "Bound" and spec["capacity"]["storage"] == size and spec["storageClassName"] == "block-retain" and
            spec["persistentVolumeReclaimPolicy"] == "Retain" and spec.get("volumeMode", "Filesystem") == "Filesystem" and
            spec["accessModes"] == ["ReadWriteOnce"] and csi["driver"] == driver and not csi.get("readOnly") and
            all(spec["claimRef"][key] == val for key, val in (("namespace", namespace), ("name", disk["claim"]), ("uid", disk["claim_uid"]))) and
            digest(csi["volumeHandle"].encode()) == disk["handle_sha256"])
    return csi["volumeHandle"]


def checked_snapshot(proof, source_disk, original_handle):
    snapshot = proof["snapshot"]
    require(snapshot["name"] == "etcd-controller-frozen" and proof["execution_disk"]["claim"] == "etcd-controller-execution")
    store = kc("get", "storageclass", "block-retain", "-o", "json")
    metadata(store, proof["storage_class_uid"])
    require(store["reclaimPolicy"] == "Retain" and store["provisioner"] == driver and
            store.get("volumeBindingMode", "Immediate") == "Immediate" and store["parameters"]["clusterID"] == "rook-ceph")
    ceph = kc("-n", "rook-ceph", "get", "cephcluster.ceph.rook.io", "rook-ceph", "-o", "json")
    require(uid(proof["ceph_fsid"]) and ceph["status"]["ceph"]["fsid"] == proof["ceph_fsid"])
    cls = kc("get", "volumesnapshotclass.snapshot.storage.k8s.io", proof["snapshot_class"], "-o", "json")
    metadata(cls, proof["snapshot_class_uid"])
    require(cls["driver"] == driver and cls["deletionPolicy"] == "Retain" and cls["parameters"]["clusterID"] == "rook-ceph")
    current = get("volumesnapshot.snapshot.storage.k8s.io", snapshot["name"])
    metadata(current, snapshot["uid"])
    spec, status = current["spec"], current["status"]
    require(spec["source"] == {"persistentVolumeClaimName": source_disk["claim"]} and
            spec["volumeSnapshotClassName"] == proof["snapshot_class"] and status["readyToUse"] is True and
            not status.get("error") and status["restoreSize"] == "10Gi" and status["boundVolumeSnapshotContentName"] == snapshot["content_name"])
    current = kc("get", "volumesnapshotcontent.snapshot.storage.k8s.io", snapshot["content_name"], "-o", "json")
    metadata(current, snapshot["content_uid"])
    spec, status = current["spec"], current["status"]
    ref = {"apiVersion": snapshot_api + "/v1", "kind": "VolumeSnapshot", "namespace": namespace,
           "name": snapshot["name"], "uid": snapshot["uid"]}
    require(spec["driver"] == driver and spec["deletionPolicy"] == "Retain" and spec["sourceVolumeMode"] == "Filesystem" and
            spec["volumeSnapshotClassName"] == proof["snapshot_class"] and spec["source"] == {"volumeHandle": original_handle} and
            all(spec["volumeSnapshotRef"][key] == value for key, value in ref.items()) and status["readyToUse"] is True and
            not status.get("error") and status["restoreSize"] == 10737418240 and
            digest(status["snapshotHandle"].encode()) == snapshot["handle_sha256"])


def source_owner(obj, source_uid):
    owner, = obj["metadata"]["ownerReferences"]
    require(all(owner.get(key) == value for key, value in (("apiVersion", "k0smotron.io/v1beta2"), ("kind", "Cluster"),
                ("name", cluster), ("uid", source_uid), ("controller", True))))


def approved_signers(proof, source, api):
    records = proof["certificate_projections"]
    require(len(records) == len(signers) and {entry["role"] for entry in records} == signers.keys())
    projections = []
    for entry in records:
        role, name = entry["role"], entry["name"]
        public, private = signers[role]
        require(name == cluster + "-" + role and entry["public_file"] == public and entry["private_file"] == private)
        secret = get("secret", name)
        info = metadata(secret, entry["uid"])
        source_owner(secret, source["cluster_uid"])
        public_bytes = base64.b64decode(secret["data"]["tls.crt"], validate=True)
        require(info["resourceVersion"] == entry["resource_version"] and
                digest(public_bytes) == entry["public_sha256"] and
                bool(base64.b64decode(secret["data"]["tls.key"], validate=True)))
        if role == "ca":
            require(digest(ssl.PEM_cert_to_DER_cert(public_bytes.decode("ascii").strip())) == api["ca_certificate_sha256"])
        projections.append({"secret": {"name": name, "items": [
            {"key": "tls.crt", "path": public}, {"key": "tls.key", "path": private}]}})
    return projections


def pinned_image(image, prefix):
    require(re.fullmatch(re.escape(prefix) + r"@sha256:[0-9a-f]{64}", image))
    target_digest = image.rsplit("@", 1)[1]
    records = [line.split() for line in run(["k0s", "ctr", "--namespace", "k8s.io", "images", "ls"]).decode().splitlines()]
    require([row[2] for row in records if len(row) > 2 and row[0] == image] == [target_digest],
            "Preload the approved digest references on the development guest.")
    return image


def selects(selector, labels):
    if any(labels.get(key) != value for key, value in selector.get("matchLabels", {}).items()):
        return False
    for clause in selector.get("matchExpressions", []):
        key, operator, values = clause["key"], clause["operator"], clause.get("values", [])
        matches = {"In": key in labels and labels[key] in values, "NotIn": key not in labels or labels[key] not in values,
                   "Exists": key in labels, "DoesNotExist": key not in labels}
        require(operator in matches)
        if not matches[operator]:
            return False
    return True


def verify_member_policy(member, controller_uid):
    policies = inventory(f"/apis/networking.k8s.io/v1/namespaces/{namespace}/networkpolicies", "NetworkPolicyList")
    matching = [item for item in policies if selects(item["spec"]["podSelector"], reachable_label)]
    require(len(matching) == 1)
    policy = matching[0]
    info = metadata(policy, member["network_policy_uid"])
    require(info["name"] == reachable_name and info["namespace"] == namespace and not info.get("ownerReferences"))
    spec = policy["spec"]
    expected = {"podSelector": {"matchLabels": reachable_label}, "policyTypes": ["Ingress", "Egress"],
                "ingress": [{"from": [{"podSelector": {"matchLabels": label}}], "ports": [{"port": 2379, "protocol": "TCP"}]}],
                "egress": []}
    require({**spec, "egress": spec.get("egress", [])} == expected)
    pods = inventory(f"/api/v1/namespaces/{namespace}/pods", "PodList")
    require([item["metadata"]["uid"] for item in pods if selects({"matchLabels": label}, item["metadata"].get("labels", {}))] ==
            ([controller_uid] if controller_uid else []))


def verified_member(member, source, controller_uid):
    require(member["scope"] == "synthetic-etcd-reachable-member",
            "Supply the approved reachable etcd member receipt. The loopback recovery receipt cannot be used.")
    name, expected = member["pod_name"], member["pod_uid"]
    address = ipaddress.IPv4Address(member["pod_ip"])
    require(name == reachable_name and
            address.is_private and not (address.is_unspecified or address.is_loopback or address.is_link_local))
    obj = get("pod", name)
    info = metadata(obj, expected)
    require(info.get("labels") == reachable_label and not info.get("ownerReferences") and
            not any(obj["spec"].get(key, False) for key in ("hostNetwork", "hostPID", "hostIPC")) and
            not obj["spec"].get("ephemeralContainers") and obj["spec"]["automountServiceAccountToken"] is False and
            obj["status"]["phase"] == "Running" and obj["status"]["podIP"] == str(address))
    container, = obj["spec"]["containers"]
    status, = obj["status"]["containerStatuses"]
    require(container["name"] == status["name"] == "etcd" and container["image"] == member["image"] and
            status["imageID"].endswith(member["image"].rsplit("@", 1)[1]) and status["containerID"] == member["container_id"] and
            status["ready"] is True and status["restartCount"] == 0 and status["state"].get("running") is not None)
    require([item["persistentVolumeClaim"] for item in obj["spec"]["volumes"] if "persistentVolumeClaim" in item] ==
            [{"claimName": member["execution_disk"]["claim"]}])
    original, observed = source["etcd"]["status"], member["status"]
    require(all(observed[key] == original[key] for key in ("cluster_id", "member_id", "peer_url", "version")) and
            observed["endpoint"] == etcd_endpoint and
            all(int(observed[key]) >= int(original[key]) > 0 for key in ("committed_revision", "raft_term", "raft_index", "raft_applied_index")) and
            int(observed["committed_revision"]) >= int(source["marker"]["revision"]))
    verify_member_policy(member, controller_uid)
    return str(address)


def verify_frozen_source(source, frozen, stored):
    for name, expected in (("kube-system", source["guest_uid"]), (namespace, source["namespace_uid"])):
        obj = kc("get", "namespace", name, "-o", "json")
        info = metadata(obj, expected)
        require(obj["status"]["phase"] == "Active" and not any(key in info.get("labels", {}) for key in
                ("bedrock.cloudyfolks.io/project", "bedrock.cloudyfolks.io/tenant")))
    require(not ns("get", "rolebindings.rbac.authorization.k8s.io", "-o", "json")["items"])
    current = kc("get", "--raw", f"/apis/k0smotron.io/v1beta2/namespaces/{namespace}/clusters/{cluster}")
    metadata(current, source["cluster_uid"])
    original, = [item for item in stored["clusters"]["items"] if item["metadata"]["uid"] == source["cluster_uid"]]
    require(current["spec"] == original["spec"])
    pods = inventory(f"/api/v1/namespaces/{namespace}/pods", "PodList")
    for role in (source["controller"], source["etcd"]):
        original, = [item for item in stored["stateful_sets"]["items"] if item["metadata"]["uid"] == role["statefulset_uid"]]
        current = get("statefulset", role["statefulset"])
        info = metadata(current, role["statefulset_uid"])
        source_owner(current, source["cluster_uid"])
        require(current["spec"] == original["spec"] and current["spec"]["replicas"] == 0 and
                current["status"]["observedGeneration"] == info["generation"] and
                all(current["status"].get(key, 0) == 0 for key in ("replicas", "currentReplicas", "readyReplicas", "updatedReplicas", "availableReplicas")) and
                all(item["metadata"]["name"] != role["pod"] and item["metadata"]["uid"] != role["pod_uid"] and
                    all(ref["uid"] != role["statefulset_uid"] for ref in item["metadata"].get("ownerReferences", [])) for item in pods))
    backend = get("service", member_service)
    metadata(backend, source["etcd"]["service"]["uid"])
    source_owner(backend, source["cluster_uid"])
    original, = [item for item in stored["services"]["items"] if item["metadata"]["uid"] == source["etcd"]["service"]["uid"]]
    require(backend["spec"] == original["spec"] and not any(item["endpoints"] for item in endpoint_slices(member_service)))
    for role, suffix in (("pods", "pods"), ("scale", "scale"), ("retained_disks", "disks")):
        name = "bedrock-e2e-etcd-freeze-" + suffix
        policy = kc("get", "validatingadmissionpolicy", name, "-o", "json")
        metadata(policy, frozen["fences"][role]["policy_uid"])
        binding = kc("get", "validatingadmissionpolicybinding", name, "-o", "json")
        metadata(binding, frozen["fences"][role]["binding_uid"])
        require(policy["spec"]["failurePolicy"] == "Fail" and binding["spec"]["policyName"] == name and binding["spec"]["validationActions"] == ["Deny"])


def verify_source_denials(source, image):
    for role in (source["controller"], source["etcd"]):
        claim = role["disk"]["claim"]
        probe = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "etcd-controller-fence-proof"}, "spec": {
                 "containers": [{"name": "probe", "image": image, "volumeMounts": [{"name": "disk", "mountPath": "/data"}]}],
                 "volumes": [{"name": "disk", "persistentVolumeClaim": {"claimName": claim}}]}}
        commands = (
            ("pods", ("create", "--dry-run=server", "-f", "-", "-o", "json"), encode(probe)),
            ("scale", ("patch", "statefulset", role["statefulset"], "--subresource=scale", "--type=merge",
                       "--dry-run=server", "-p", '{"spec":{"replicas":1}}', "-o", "json"), None),
        )
        for policy, argv, body in commands:
            try:
                ns(*argv, body=body)
            except subprocess.CalledProcessError as error:
                require(("bedrock-e2e-etcd-freeze-" + policy).encode() in error.stderr and
                        b"The isolated etcd source is frozen." in error.stderr)
            else:
                require(False)


def verify_disk_users(source, claim_proof, member, controller_uid):
    original = source["controller"]["disk"], source["etcd"]["disk"]
    claims = (*original, member["execution_disk"], claim_proof["execution_disk"])
    handles = [bound_disk(disk, backing, size) for disk, backing, size in
               zip(claims, ("", "", "etcd-source-frozen", claim_proof["snapshot"]["name"]), ("10Gi", "1Gi", "1Gi", "10Gi"))]
    require(len(set(handles)) == 4 and all(len({disk[field] for disk in claims}) == 4 for field in ("claim", "claim_uid", "volume", "volume_uid")))
    checked_snapshot(claim_proof, original[0], handles[0])
    pods = inventory(f"/api/v1/namespaces/{namespace}/pods", "PodList")
    attachments = inventory("/apis/storage.k8s.io/v1/volumeattachments", "VolumeAttachmentList")
    volumes = inventory("/api/v1/persistentvolumes", "PersistentVolumeList")
    for disk, handle, active in zip(claims, handles, ("", "", member["pod_uid"], controller_uid)):
        require([pv["metadata"]["uid"] for pv in volumes if pv["spec"].get("csi", {}).get("volumeHandle") == handle or
                 pv["spec"].get("claimRef", {}).get("uid") == disk["claim_uid"] or
                 (pv["spec"].get("claimRef", {}).get("namespace") == namespace and
                  pv["spec"].get("claimRef", {}).get("name") == disk["claim"])] == [disk["volume_uid"]])
        users = [item for item in pods if any(vol.get("persistentVolumeClaim", {}).get("claimName") ==
                 disk["claim"] for vol in item["spec"].get("volumes", []))]
        require([item["metadata"]["uid"] for item in users] == ([active] if active else []))
        uses = [entry for entry in attachments if entry["spec"]["source"].get("persistentVolumeName") == disk["volume"] or
                entry["spec"]["source"].get("inlineVolumeSpec", {}).get("csi", {}).get("volumeHandle") == handle]
        node = users[0]["spec"].get("nodeName") if users else None
        require(not uses or (node and len(uses) == 1 and uses[0]["spec"]["nodeName"] == node and uses[0]["spec"]["attacher"] == driver and
                uses[0]["spec"]["source"] == {"persistentVolumeName": disk["volume"]} and not uses[0]["metadata"].get("deletionTimestamp")))


def endpoint_slices(service):
    result = ns("get", "endpointslices.discovery.k8s.io", "-l", "kubernetes.io/service-name=" + service, "-o", "json")
    require(not result["metadata"].get("continue"))
    return result["items"]


def service_state(source, selector):
    obj = get("service", api_name)
    info = metadata(obj, source["api_service"]["uid"])
    address, spec = source["api_service"]["address"], obj["spec"]
    require(not info.get("ownerReferences") and spec["type"] == "ClusterIP" and spec["clusterIP"] == address and
            spec["clusterIPs"] == [address] and not spec.get("externalIPs") and not spec.get("externalName") and
            not spec.get("publishNotReadyAddresses") and not obj.get("status", {}).get("loadBalancer", {}).get("ingress") and spec["selector"] == selector and
            sorted((port["name"], port["port"], port["targetPort"], port["protocol"], port.get("nodePort", 0)) for port in spec["ports"]) ==
            [("api", 6443, 6443, "TCP", 0), ("konnectivity", 8132, 8132, "TCP", 0)])
    return info


def route(source, old_selector, new_selector):
    info = service_state(source, old_selector)
    values = (("/metadata/uid", info["uid"]), ("/metadata/resourceVersion", info["resourceVersion"]),
              ("/spec/clusterIP", source["api_service"]["address"]), ("/spec/selector", old_selector))
    patch = [{"op": "test", "path": path, "value": value} for path, value in values]
    patch.append({"op": "replace", "path": "/spec/selector", "value": new_selector})
    result = ns("patch", "service", api_name, "--type=json", "-p", encode(patch).decode(), "-o", "json")
    updated = metadata(result, info["uid"])
    require(result["spec"]["selector"] == new_selector and result["spec"]["clusterIP"] == source["api_service"]["address"] and
            updated["resourceVersion"] != info["resourceVersion"])
    return updated["resourceVersion"]


def route_ready(source, pod_uid, address):
    slices = endpoint_slices(api_name)
    require(all(item["addressType"] == "IPv4" and not item["metadata"].get("deletionTimestamp") and
                any(ref["kind"] == "Service" and ref["name"] == api_name and ref["uid"] == source["api_service"]["uid"]
                    for ref in item["metadata"]["ownerReferences"]) and
                (not item["endpoints"] or sorted((port["name"], port["port"], port["protocol"]) for port in item["ports"]) ==
                 [("api", 6443, "TCP"), ("konnectivity", 8132, "TCP")]) for item in slices))
    endpoints = [entry for item in slices for entry in item["endpoints"]]
    if not endpoints:
        return False
    entry, = endpoints
    require(entry["addresses"] == [address] and all(entry["targetRef"][key] == value for key, value in
            (("kind", "Pod"), ("name", target), ("namespace", namespace), ("uid", pod_uid))))
    return entry["conditions"].get("ready") is True and not entry["conditions"].get("terminating")


def controller_pod(stored, source, claim, member, member_ip, image, projections, claim_sha, member_sha):
    original, = [item for item in stored["stateful_sets"]["items"] if item["metadata"]["uid"] == source["controller"]["statefulset_uid"]]
    template, = original["spec"]["template"]["spec"]["containers"]
    original_mount = next(item for item in template["volumeMounts"] if item["mountPath"] == "/var/lib/k0s")
    maps = {item["metadata"]["name"]: item for item in stored["config_maps"]["items"] if item["metadata"]["namespace"] == namespace}
    script = maps["kmc-entrypoint-etcd-source-config"]["data"]["k0smotron-entrypoint.sh"]
    config = maps["kmc-etcd-source-config"]["data"]["K0SMOTRON_K0S_YAML"]
    one_use = "set -C\n: > /var/lib/k0s/.bedrock-etcd-controller-started\nset +C\n"
    controller = {"name": "controller", "image": image, "imagePullPolicy": "Never", "command": ["/bin/sh", "-ec"],
                  "args": [one_use + script], "env": [{"name": "K0SMOTRON_K0S_YAML", "value": config}], "resources": template.get("resources", {}),
                  "volumeMounts": [original_mount],
                  "readinessProbe": {"exec": {"command": ["k0s", "status"]}, "periodSeconds": 5, "timeoutSeconds": 5,
                                     "successThreshold": 1, "failureThreshold": 3}}
    for key in ("ports", "securityContext"):
        if key in template:
            controller[key] = template[key]
    data_mount = {"name": original_mount["name"], "mountPath": "/data", "readOnly": True}
    cert_mount = {"name": "certs", "mountPath": "/signers", "readOnly": True}
    files = [filename for pair in signers.values() for filename in pair]
    signer = {"name": "verify-signers", "image": image, "imagePullPolicy": "Never", "command": ["/bin/sh", "-ec"],
              "args": ['for file; do test -f "/data/pki/$file" && test ! -L "/data/pki/$file" && cmp -s "/data/pki/$file" "/signers/$file" || exit 1; done',
                       "verify-signers", *files],
              "volumeMounts": [data_mount, cert_mount], "securityContext": {"allowPrivilegeEscalation": False}}
    probe = {"name": "verify-member-tls", "image": member["image"], "imagePullPolicy": "Never",
             "command": ["/bin/bash", "-ec"],
             "args": ['exec -c etcdctl "$@"', "etcdctl", "--endpoints=" + etcd_endpoint, "--cacert=/signers/etcd-ca.crt",
                         "--cert=/signers/apiserver-etcd-client.crt", "--key=/signers/apiserver-etcd-client.key",
                         "--insecure-transport=false", "--insecure-skip-tls-verify=false", "--dial-timeout=5s", "--command-timeout=15s",
                         "endpoint", "health"], "volumeMounts": [cert_mount], "securityContext": {"allowPrivilegeEscalation": False}}
    source_spec = original["spec"]["template"]["spec"]
    pod = {"schedulingGates": gate, "restartPolicy": "Never", "automountServiceAccountToken": False, "enableServiceLinks": False,
           "hostname": source["controller"]["pod"], "hostAliases": [{"ip": member_ip, "hostnames": [member_service, member_host]}],
           "terminationGracePeriodSeconds": source_spec.get("terminationGracePeriodSeconds", 30),
           "volumes": [{"name": original_mount["name"], "persistentVolumeClaim": {"claimName": claim}},
                       {"name": "certs", "projected": {"defaultMode": 0o400, "sources": projections}}],
           "initContainers": [signer, probe], "containers": [controller]}
    if "securityContext" in source_spec:
        pod["securityContext"] = source_spec["securityContext"]
    return {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": target, "namespace": namespace, "labels": label,
            "annotations": {"migration.cloudyfolks.io/controller-claim-sha256": claim_sha, "migration.cloudyfolks.io/reachable-member-sha256": member_sha}},
            "spec": pod}


def validate_admitted(dry, requested):
    spec, info, wanted = dry["spec"], dry["metadata"], requested["metadata"]
    require(info["name"] == wanted["name"] and info["namespace"] == namespace and
            info["labels"] == wanted["labels"] and not info.get("ownerReferences") and
            all(info.get("annotations", {}).get(key) == value for key, value in wanted.get("annotations", {}).items()) and
            all(spec.get(key) == value for key, value in requested["spec"].items()
                if key not in ("containers", "initContainers", "volumes")) and
            not any(spec.get(key) for key in ("hostNetwork", "hostPID", "hostIPC", "ephemeralContainers", "nodeName", "runtimeClassName")) and
            spec.get("serviceAccountName", "default") == "default")
    for key in ("containers", "initContainers", "volumes"):
        items, expected_items = spec.get(key, []), requested["spec"].get(key, [])
        require(len(items) == len(expected_items))
        for actual, expected in zip(items, expected_items):
            require(all(actual.get(field) == value for field, value in expected.items()) and
                    not any(field in actual and field not in expected for field in
                            ("command", "args", "env", "envFrom", "securityContext", "volumeMounts", "volumeDevices", "lifecycle",
                             "startupProbe", "livenessProbe", "readinessProbe")))
            if key == "volumes":
                require(actual == expected)


def controller_runtime(expected, spec, image):
    obj = get("pod", target)
    metadata(obj, expected)
    actual = dict(obj["spec"])
    actual.pop("nodeName", None)
    require(actual == spec and not obj["metadata"].get("ownerReferences") and obj["metadata"]["labels"] == label)
    status = obj["status"]
    require(status.get("phase") not in ("Failed", "Succeeded"))
    if not any(entry["type"] == "Ready" and entry["status"] == "True" for entry in status.get("conditions", [])):
        return None
    container, = status["containerStatuses"]
    require(container["name"] == "controller" and container["ready"] is True and container["restartCount"] == 0 and
            container["imageID"].endswith(image.rsplit("@", 1)[1]) and container["state"].get("running") and
            not status.get("ephemeralContainerStatuses"))
    require({entry["name"] for entry in status["initContainerStatuses"]} == {"verify-signers", "verify-member-tls"} and
            all(entry["restartCount"] == 0 and entry["state"]["terminated"]["exitCode"] == 0 for entry in status["initContainerStatuses"]))
    return str(ipaddress.IPv4Address(status["podIP"])), container["containerID"]


def verified_marker(pod, container, kubeconfig, source, evidence, extra=()):
    command = ["kubectl", "--request-timeout=20s", "-n", namespace, "exec", "-i", pod, "-c", container, "--",
               "env", "HTTPS_PROXY=", "https_proxy=", "HTTP_PROXY=", "http_proxy=", "ALL_PROXY=", "all_proxy=",
               "k0s", "kubectl", "--kubeconfig=/dev/stdin", "--request-timeout=10s", *extra,
               "get", "--raw", "/api/v1/namespaces/default/configmaps/" + source["marker"]["name"]]
    obj = json.loads(run(command, kubeconfig), object_pairs_hook=unique)
    expected = source["marker"]
    info = metadata(obj, expected["uid"])
    require(obj["kind"] == "ConfigMap" and obj["apiVersion"] == "v1" and info["namespace"] == "default" and
            info["name"] == expected["name"] and info["resourceVersion"] == expected["revision"] and obj["data"] == {"state": "before-handoff"})
    result = {"name": info["name"], "uid": info["uid"], "revision": info["resourceVersion"], "state": obj["data"]["state"]}
    require(digest(encode(result)) == evidence["marker_sha256"])
    return True


def probe_runtime(expected_uid, admitted, image):
    obj = get("pod", probe_name)
    info = metadata(obj, expected_uid)
    spec = dict(obj["spec"])
    node = spec.pop("nodeName", None)
    require(spec == admitted and not info.get("ownerReferences") and
            info["labels"] == {"migration.cloudyfolks.io/purpose": probe_name})
    status = obj["status"]
    require(status.get("phase") not in ("Failed", "Succeeded"))
    if not node or not any(entry["type"] == "Ready" and entry["status"] == "True" for entry in status.get("conditions", [])):
        return None
    container, = status["containerStatuses"]
    require(status["phase"] == "Running" and container["name"] == "probe" and container["ready"] is True and
            container["restartCount"] == 0 and container["state"].get("running") is not None and
            container["imageID"].endswith(image.rsplit("@", 1)[1]) and
            re.fullmatch(r"containerd://[0-9a-f]{64}", container["containerID"]))
    return node, container["containerID"]


def remove_pod(name, expected_uid):
    deletion = {"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": {"uid": expected_uid}}
    ns("delete", "--raw", f"/api/v1/namespaces/{namespace}/pods/{name}", "-f", "-", body=encode(deletion))


def service_marker(kubeconfig, source, evidence, image, controller_uid, address, original_selector, member):
    wanted = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": probe_name, "namespace": namespace,
              "labels": {"migration.cloudyfolks.io/purpose": probe_name}},
              "spec": {"automountServiceAccountToken": False, "enableServiceLinks": False, "restartPolicy": "Never",
                       "activeDeadlineSeconds": 300, "terminationGracePeriodSeconds": 1,
                       "securityContext": {"seccompProfile": {"type": "RuntimeDefault"}},
                       "containers": [{"name": "probe", "image": image, "imagePullPolicy": "Never",
                                       "command": ["/bin/sh", "-ec"], "args": ["exec sleep 300"],
                                       "securityContext": {"allowPrivilegeEscalation": False,
                                                           "capabilities": {"drop": ["ALL"]}}}]}}
    definition = encode(wanted)
    dry = ns("create", "--dry-run=server", "-f", "-", "-o", "json", body=definition)
    validate_admitted(dry, wanted)
    created = ns("create", "-f", "-", "-o", "json", body=definition)
    probe_uid = created["metadata"]["uid"]
    require(uid(probe_uid) and probe_uid != controller_uid)
    try:
        validate_admitted(created, wanted)
        require(created["spec"] == dry["spec"])
        runtime = wait_for(lambda: probe_runtime(probe_uid, dry["spec"], image))
        pods = inventory(f"/api/v1/namespaces/{namespace}/pods", "PodList")
        for selector, expected in ((original_selector, []), (label, [controller_uid])):
            require([item["metadata"]["uid"] for item in pods if all(item["metadata"].get("labels", {}).get(key) == value
                    for key, value in selector.items())] == expected)
        verify_member_policy(member, controller_uid)
        resource_version = route(source, original_selector, label)
        wait_for(lambda: route_ready(source, controller_uid, address))
        require(service_state(source, label)["resourceVersion"] == resource_version and route_ready(source, controller_uid, address))
        verified_marker(probe_name, "probe", kubeconfig, source, evidence)
        require(probe_runtime(probe_uid, dry["spec"], image) == runtime and
                service_state(source, label)["resourceVersion"] == resource_version and route_ready(source, controller_uid, address))
        verify_member_policy(member, controller_uid)
    finally:
        remove_pod(probe_name, probe_uid)
        wait_for(lambda: all(item["metadata"]["uid"] != probe_uid for item in
                             inventory(f"/api/v1/namespaces/{namespace}/pods", "PodList")))
    return probe_uid, resource_version


def wait_for(predicate):
    deadline = time.monotonic() + 240
    while time.monotonic() < deadline:
        try:
            result = predicate()
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
            result = None
        if result:
            return result
        time.sleep(3)
    require(False, "The isolated recovered controller did not become ready within the time limit.")


def publish(directory, record):
    raw = encode(record)
    with tempfile.NamedTemporaryFile(dir=directory, prefix=".controller-recovery-") as output:
        output.write(raw)
        output.flush()
        os.fsync(output.fileno())
        os.link(output.name, directory / "etcd-controller-start.json")
    return digest(raw)


def main(guest, source_path, source_sha, proof_dir, api_dir, api_sha, claim_path, claim_sha, member_path, member_sha):
    require(uid(guest))
    proof_dir, api_dir, source_path = Path(proof_dir), Path(api_dir), Path(source_path)
    for path in (proof_dir, api_dir):
        private_directory(path)
    require(not os.path.lexists(proof_dir / "etcd-controller-start.json"))
    source = approved(source_path, source_sha)
    claim = approved(Path(claim_path), claim_sha)
    member = approved(Path(member_path), member_sha)
    api = approved(api_dir / "api-evidence.json", api_sha)
    scope = {"guest_uid": guest, "namespace_uid": source["namespace_uid"], "source_cluster_uid": source["cluster_uid"], "source_receipt_sha256": source_sha}
    for record in (claim, member, api):
        require(record["version"] == 1 and all(record[key] == value for key, value in scope.items()))
    require(member["scope"] == "synthetic-etcd-reachable-member",
            "Supply the approved reachable etcd member receipt. The loopback recovery receipt cannot be used.")
    require(source["version"] == 1 and source["guest_uid"] == guest and source["namespace"] == namespace and source["cluster"] == cluster and
            claim["scope"] == "synthetic-etcd-controller-execution" and claim["source_disk"] == source["controller"]["disk"] and
            claim["source_controller_uid"] == source["controller"]["statefulset_uid"] and
            claim["controller_image_id"] == source["controller"]["image_id"] and
            api["service_uid"] == source["api_service"]["uid"] and api["controller_pod_uid"] == source["controller"]["pod_uid"] and
            member["frozen_receipt_sha256"] == claim["frozen_receipt_sha256"] and member["execution_disk"]["claim"] == "etcd-source-execution")
    frozen = approved(proof_dir / "frozen.json", claim["frozen_receipt_sha256"])
    require(frozen["source_receipt_sha256"] == source_sha)
    inventory_path = proof_dir / "frozen-inventory.json"
    stored = approved(inventory_path, frozen["inventory_sha256"], 64 * 1024 * 1024)
    migrate = Path(os.environ["BEDROCK_MIGRATE"])
    require(migrate.is_absolute() and not migrate.is_symlink() and migrate.is_file() and
            digest(migrate.read_bytes()) == os.environ["BEDROCK_MIGRATE_SHA256"])
    report = json.loads(run([str(migrate), "verify-frozen-legacy-etcd", str(inventory_path), frozen["inventory_sha256"],
                             str(source_path), source_sha, guest], timeout=90))
    require(report["frozen_etcd_control_planes"] == 1 and report["verified_disks"] == 2 and report["approval_sha256"] == claim["frozen_approval_sha256"])
    kubeconfig = approved_bytes(api_dir / "original.kubeconfig", api["kubeconfig_sha256"])
    current_config = get("secret", cluster + "-kubeconfig")
    source_owner(current_config, source["cluster_uid"])
    require(not current_config["metadata"].get("deletionTimestamp") and base64.b64decode(current_config["data"]["value"], validate=True) == kubeconfig)
    original_selector = {"statefulset.kubernetes.io/pod-name": source["controller"]["pod"]}
    service_state(source, original_selector)
    require(not any(item["endpoints"] for item in endpoint_slices(api_name)))
    verify_frozen_source(source, frozen, stored)
    member_ip = verified_member(member, source, "")
    member_image = pinned_image(member["image"], "quay.io/k0sproject/etcd")
    require(source["etcd"]["image_id"].endswith(member_image.rsplit("@", 1)[1]))
    image_digest = re.search(r"sha256:[0-9a-f]{64}$", source["controller"]["image_id"])
    require(image_digest)
    image = pinned_image("quay.io/k0sproject/k0s@" + image_digest[0], "quay.io/k0sproject/k0s")
    projections = approved_signers(claim, source, api)
    verify_disk_users(source, claim, member, "")
    verify_source_denials(source, image)
    wanted = controller_pod(stored, source, claim["execution_disk"]["claim"], member, member_ip, image, projections, claim_sha, member_sha)
    body = encode(wanted)
    dry = ns("create", "--dry-run=server", "-f", "-", "-o", "json", body=body)
    validate_admitted(dry, wanted)
    created = ns("create", "-f", "-", "-o", "json", body=body)
    target_uid = created["metadata"]["uid"]
    require(uid(target_uid))
    moving = False
    try:
        current = get("pod", target)
        info = metadata(current, target_uid)
        validate_admitted(current, wanted)
        require(current["spec"] == dry["spec"] and current["spec"]["schedulingGates"] == gate)
        verify_frozen_source(source, frozen, stored)
        verify_disk_users(source, claim, member, target_uid)
        require(verified_member(member, source, target_uid) == member_ip and approved_signers(claim, source, api) == projections)
        changes = [{"op": "test", "path": "/metadata/uid", "value": target_uid},
                   {"op": "test", "path": "/metadata/resourceVersion", "value": info["resourceVersion"]},
                   {"op": "test", "path": "/spec/schedulingGates", "value": gate},
                   {"op": "remove", "path": "/spec/schedulingGates"}]
        ns("patch", "pod", target, "--type=json", "-p", encode(changes).decode(), "-o", "json")
        spec = {key: value for key, value in current["spec"].items() if key != "schedulingGates"}
        runtime = wait_for(lambda: controller_runtime(target_uid, spec, image))
        ip, _ = runtime
        wait_for(lambda: verified_marker(target, "controller", kubeconfig, source, api,
                 ("--server=https://" + ip + ":6443", "--tls-server-name=" + source["api_service"]["address"])))
        require(verified_member(member, source, target_uid) == member_ip)
        moving = True
        probe_uid, route_version = service_marker(kubeconfig, source, api, image, target_uid, ip, original_selector, member)
        verify_frozen_source(source, frozen, stored)
        verify_source_denials(source, image)
        verify_disk_users(source, claim, member, target_uid)
        require(service_state(source, label)["resourceVersion"] == route_version)
        require(route_ready(source, target_uid, ip) and controller_runtime(target_uid, spec, image) == runtime and
                verified_member(member, source, target_uid) == member_ip and approved_signers(claim, source, api) == projections)
        receipt_sha = publish(proof_dir, {"version": 1, "scope": "synthetic-etcd-controller-start", **scope,
                                          "source_service_uid": source["api_service"]["uid"], "controller_pod_uid": target_uid,
                                          "api_probe_pod_uid": probe_uid, "service_resource_version": route_version,
                                          "member_pod_uid": member["pod_uid"], "controller_image": image, "marker_sha256": api["marker_sha256"],
                                          "api_evidence_sha256": api_sha, "controller_execution_sha256": claim_sha, "reachable_member_sha256": member_sha})
    except (OSError, subprocess.SubprocessError, AttributeError, KeyError, TypeError, ValueError, RecursionError, KeyboardInterrupt):
        if moving:
            try:
                if get("service", api_name)["spec"]["selector"] != original_selector:
                    route(source, label, original_selector)
                service_state(source, original_selector)
                wait_for(lambda: not any(item["endpoints"] for item in endpoint_slices(api_name)))
            except (OSError, subprocess.SubprocessError, KeyError, ValueError):
                print("Could not confirm that the isolated API Service selector was restored.", file=sys.stderr)
        try:
            remove_pod(target, target_uid)
            wait_for(lambda: all(item["metadata"]["uid"] != target_uid for item in
                                 inventory(f"/api/v1/namespaces/{namespace}/pods", "PodList")))
        except (OSError, subprocess.SubprocessError, KeyError, ValueError):
            print("Could not remove the isolated controller Pod by UID.", file=sys.stderr)
        raise
    print(receipt_sha)


def interrupted(*_):
    raise KeyboardInterrupt


try:
    signal.signal(signal.SIGTERM, interrupted)
    main(*sys.argv[1:])
except ValueError as error:
    allowed = {failure, "Supply the approved reachable etcd member receipt. The loopback recovery receipt cannot be used.",
               "Preload the approved digest references on the development guest.", "The isolated recovered controller did not become ready within the time limit."}
    sys.exit(str(error) if str(error) in allowed else failure)
except (OSError, subprocess.SubprocessError, AttributeError, KeyError, TypeError, RecursionError, KeyboardInterrupt):
    sys.exit(failure)
PY
