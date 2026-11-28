#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

if [[ ${E2E_MANAGED_SQLITE_WORKER_CONFIRM:-} != 1 || $# != 5 ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_SQLITE_WORKER_CONFIRM=1 bash hack/e2e-managed-sqlite-worker-confirm.sh <isolated-guest-uid> <private-source.json> <private-assets.json> <private-worker-prejoin.json> <prejoin-sha256>' >&2
  exit 2
fi
export KUBECONFIG=/var/lib/k0s/pki/admin.conf
if [[ $(uname -sm) != 'Linux x86_64' || ! -s /var/lib/bedrock/e2e-admin-password || ! -f $KUBECONFIG || -L $KUBECONFIG ]]; then
  printf '%s\n' 'Run this fixture inside an initialized amd64 Bedrock development guest.' >&2
  exit 1
fi

python3 -I - "$@" <<'PY'
import base64
from datetime import datetime
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import time
from traceback import walk_tb

namespace = "bedrock-e2e-sqlite-source"
source_name = "sqlite-source"
service = "sqlite-endpoint"
worker = "sqlite-worker"
root_claim = worker + "-root"
bootstrap = worker + "-bootstrap"
workload = worker + "-state"
controller_set = "kmc-sqlite-source"
controller_pod = controller_set + "-0"
version = "v1.36.3+k0s.1"
image = "docker.io/library/busybox:1.37.0"
marker = b"sqlite-worker-data-before-handoff\n"
marker_dir = "/var/lib/sqlite-continuity"
failure = "The approved existing SQLite worker could not be verified."


def require(condition, message=failure):
    if not condition:
        raise ValueError(message)


def uid(value):
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}", value) and value != "00000000-0000-0000-0000-000000000000"


def sha(value):
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value)


def digest(bytes_value):
    return hashlib.sha256(bytes_value).hexdigest()


def unique(pairs):
    result = dict(pairs)
    require(len(result) == len(pairs))
    return result


def approved(path, expected):
    path = Path(path)
    require(sha(expected) and path.is_absolute() and path.resolve(strict=True) == path)
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600 and info.st_uid == os.geteuid() and
                info.st_nlink == 1 and 0 < info.st_size <= 65536)
        data = stream.read(65537)
    require(len(data) == info.st_size and digest(data) == expected)
    value = json.loads(data, object_pairs_hook=unique)
    require(type(value) is dict)
    return value


def encode(value):
    return json.dumps(value, separators=(",", ":")).encode()


def run(argv, body=None):
    result = subprocess.run(argv, input=body, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=35)
    if result.returncode:
        raise subprocess.CalledProcessError(result.returncode, argv, stderr=result.stderr)
    require(len(result.stdout) <= 16 * 1024 * 1024)
    return result.stdout


def execute(*args, body=None):
    return run(["kubectl", "--request-timeout=25s", *args], body=body)


def record(data):
    return json.loads(data, object_pairs_hook=unique) if data else None


def kc(*args, body=None):
    return record(execute(*args, body=body))


def parent(kind, name, location=namespace):
    return kc("-n", location, "get", kind, name, "-o", "json")


def child(source, *args, body=None):
    return execute("-n", namespace, "exec", "-i", controller_pod, "-c", "controller", "--", "k0s", "kubectl",
                   "--kubeconfig=/var/lib/k0s/pki/admin.conf", "--server=https://127.0.0.1:6443",
                   "--tls-server-name=" + source["api_endpoint"].removeprefix("https://").removesuffix(":6443"),
                   "--request-timeout=15s", *args, body=body)


def identity(value, expected):
    info = value["metadata"]
    require(uid(expected) and info["uid"] == expected and not info.get("deletionTimestamp"))
    return info


def owner(value, kind, name, expected):
    reference, = value["metadata"]["ownerReferences"]
    require(reference["kind"] == kind and reference["name"] == name and reference["uid"] == expected and reference.get("controller") is True)


def binding(claim_name, claim_uid, volume, volume_uid, mode):
    claim = parent("pvc", claim_name)
    identity(claim, claim_uid)
    pv = kc("get", "pv", volume, "-o", "json")
    identity(pv, volume_uid)
    ref, csi = pv["spec"]["claimRef"], pv["spec"]["csi"]
    require(claim["status"]["phase"] == pv["status"]["phase"] == "Bound" and claim["spec"]["volumeName"] == volume and
            claim["spec"]["storageClassName"] == pv["spec"]["storageClassName"] == "block" and
            claim["spec"].get("volumeMode", "Filesystem") == pv["spec"].get("volumeMode", "Filesystem") == mode and
            claim["spec"]["accessModes"] == pv["spec"]["accessModes"] == ["ReadWriteOnce"] and
            ref["uid"] == claim_uid and ref["namespace"] == namespace and ref["name"] == claim_name and
            csi["driver"] == "rook-ceph.rbd.csi.ceph.com" and csi["volumeHandle"] and not csi.get("readOnly"))
    return csi["volumeHandle"]


def verify_source(source, proof):
    for name, wanted in (("kube-system", proof["guest_uid"]), (namespace, proof["namespace_uid"])):
        obj = kc("get", "namespace", name, "-o", "json")
        labels = identity(obj, wanted).get("labels", {})
        require(obj["status"]["phase"] == "Active" and not any(key in labels for key in
                ("bedrock.cloudyfolks.io/project", "bedrock.cloudyfolks.io/tenant")))
    require(not kc("-n", namespace, "get", "rolebindings.rbac.authorization.k8s.io", "-o", "json")["items"])
    obj = parent("cluster.k0smotron.io", source_name)
    identity(obj, proof["source_cluster_uid"])
    config = obj["spec"]
    address = source["api_endpoint"].removeprefix("https://").removesuffix(":6443")
    require(config["version"] == version and config["replicas"] == obj["status"]["readyReplicas"] == 1 and config["externalAddress"] == address and
            config["storage"]["type"] == "kine" and config["storage"]["kine"]["dataSourceURL"] == "sqlite:///var/lib/k0s/state.db")
    require(not config.get("patches") and not config["persistence"].get("autoDeletePVCs"))
    obj = parent("service", service)
    info = identity(obj, proof["service_uid"])
    config = obj["spec"]
    require(not info.get("ownerReferences") and config["type"] == "ClusterIP" and config["clusterIP"] == address and
            not config.get("externalIPs") and config["selector"] == {"statefulset.kubernetes.io/pod-name": controller_pod} and
            sorted((entry["port"], entry["targetPort"], entry["protocol"]) for entry in config["ports"]) ==
            [(6443, 6443, "TCP"), (8132, 8132, "TCP")])
    controller = parent("statefulset", controller_set)
    owner(controller, "Cluster", source_name, proof["source_cluster_uid"])
    controller_uid = controller["metadata"]["uid"]
    identity(controller, controller_uid)
    pod = parent("pod", controller_pod)
    pod_uid = pod["metadata"]["uid"]
    identity(pod, pod_uid)
    owner(pod, "StatefulSet", controller_set, controller_uid)
    containers = [item for item in pod["status"]["containerStatuses"] if item["name"] == "controller"]
    container, = containers
    require(pod["status"]["phase"] == "Running" and container["ready"] is True and container["state"].get("running") is not None and
            any(vol.get("persistentVolumeClaim", {}).get("claimName") == source["claim"] for vol in pod["spec"]["volumes"]))
    obj = record(child(source, "-n", "default", "get", "configmap", source["marker"], "-o", "json"))
    identity(obj, source["marker_uid"])
    require(obj["data"] == {"state": "before-handoff"})
    return pod_uid, container["containerID"], info["resourceVersion"]


def verify_worker(source, proof, assets):
    vm = parent("virtualmachine.kubevirt.io", worker)
    identity(vm, proof["vm_uid"])
    require(vm["spec"]["runStrategy"] == "Always")
    vmi = parent("virtualmachineinstance.kubevirt.io", worker)
    identity(vmi, proof["vmi_uid"])
    owner(vmi, "VirtualMachine", worker, proof["vm_uid"])
    require(vmi["status"]["phase"] == "Running")
    for spec in (vm["spec"]["template"]["spec"], vmi["spec"]):
        domain = spec["domain"]
        interfaces = domain["devices"]["interfaces"]
        root = [disk for disk in domain["devices"]["disks"] if disk["name"] == "root"]
        require(spec["hostname"] == worker and domain["firmware"]["uuid"] == proof["firmware_uuid"] and len(interfaces) == 1 and
                interfaces[0]["name"] == "default" and interfaces[0]["macAddress"] == proof["mac_address"] and "masquerade" in interfaces[0] and
                len(root) == 1 and root[0]["serial"] == root_claim and root[0]["bootOrder"] == 1 and root[0]["disk"]["bus"] == "virtio" and
                len(spec["volumes"]) == 2 and
                any(vol["name"] == "root" and vol.get("persistentVolumeClaim", {}).get("claimName") == root_claim for vol in spec["volumes"]) and
                any(vol["name"] == "bootstrap" and vol.get("cloudInitNoCloud", {}).get("secretRef", {}).get("name") == bootstrap
                    for vol in spec["volumes"]))
    source_handle = binding(source["claim"], source["claim_uid"], source["volume"], source["volume_uid"], "Filesystem")
    worker_handle = binding(root_claim, proof["root_claim_uid"], proof["root_volume"], proof["root_volume_uid"], "Block")
    require(source_handle != worker_handle and source["volume_uid"] != proof["root_volume_uid"])
    dv = parent("datavolume", root_claim)
    catalog = parent("clusterimages.bedrock.cloudyfolks.io", "ubuntu-24.04")
    require(dv["status"]["phase"] == "Succeeded" and dv["spec"]["sourceRef"] ==
            {"kind": "DataSource", "namespace": "bedrock-images", "name": catalog["status"]["dataSource"]} and
            catalog["status"]["phase"] == "Ready" and catalog["metadata"]["generation"] == catalog["status"]["observedGeneration"] and
            catalog["status"]["digest"] == assets["ubuntu_digest"])


def original_token(source, proof, assets):
    secret = parent("secret", bootstrap)
    identity(secret, proof["bootstrap_secret_uid"])
    data = base64.b64decode(secret["data"]["userdata"], validate=True).decode("ascii")
    for key, path in (("k0s_sha256", "/usr/local/bin/k0s"),
                      ("airgap_sha256", "/var/lib/k0s/images/k0s-airgap.tar"),
                      ("busybox_tar_sha256", "/var/lib/k0s/images/busybox.tar")):
        require(f"'{assets[key]}' {path}" in data)
    tokens = re.findall(r"(?m)^    content: ([A-Za-z0-9+/=]+)$", data)
    require(len(tokens) == 1)
    # k0s prints one trailing LF, which k0smotron preserves in the Secret.
    token = base64.b64decode(tokens[0], validate=True).removesuffix(b"\n")
    compressed = base64.b64decode(token, validate=True)
    require(len(compressed) <= 16384)
    with gzip.GzipFile(fileobj=io.BytesIO(compressed)) as stream:
        config = stream.read(65537)
    require(len(config) <= 65536)
    config = config.decode("ascii")
    ids = re.findall(r"(?m)^\s*token: ([a-z0-9]{6})\.[a-z0-9]{16}\s*$", config)
    require(len(ids) == 1 and re.findall(r"(?m)^\s*server: (\S+)\s*$", config) == [source["api_endpoint"]])
    return ids[0]


def token_state(proof, token_id):
    request = kc("-n", namespace, "get", "jointokenrequest.k0smotron.io", worker, "--ignore-not-found", "-o", "json")
    if request:
        require(request["metadata"]["uid"] == proof["join_token_request_uid"] and request["spec"]["clusterName"] == source_name and
                request["spec"]["role"] == "worker" and request["status"]["tokenID"] == token_id)
    secret_uid = execute("-n", namespace, "get", "secret", worker, "--ignore-not-found", "-o", "jsonpath={.metadata.uid}").decode()
    require(not secret_uid or secret_uid == proof["join_token_secret_uid"])
    return request, secret_uid


def revoked(source, proof, token_id):
    request, secret_uid = token_state(proof, token_id)
    upstream = child(source, "-n", "kube-system", "get", "secret", "bootstrap-token-" + token_id, "--ignore-not-found",
                     "-o", "jsonpath={.metadata.uid}")
    return not request and not secret_uid and not upstream


def wait_for(predicate, seconds, message):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            value = predicate()
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
            value = None
        if value:
            return value
        time.sleep(3)
    require(False, message)


def node(source):
    result = record(child(source, "get", "node", worker, "--ignore-not-found", "-o", "json"))
    if not result:
        return None
    info = result["status"]["nodeInfo"]
    node_uid = result["metadata"]["uid"]
    identity(result, node_uid)
    require(info["operatingSystem"] == "linux" and info["architecture"] == "amd64" and info["osImage"].startswith("Ubuntu 24.04"))
    return (node_uid, info["systemUUID"].lower()) if any(item["type"] == "Ready" and item["status"] == "True"
                                                         for item in result["status"]["conditions"]) else None


def lease(source, node_uid):
    obj = record(child(source, "-n", "kube-node-lease", "get", "lease", worker, "--ignore-not-found", "-o", "json"))
    if not obj:
        return None
    owner_refs = obj["metadata"].get("ownerReferences", [])
    if not owner_refs:
        return None
    ref, = owner_refs
    require(ref["kind"] == "Node" and ref["name"] == worker and ref["uid"] == node_uid and obj["spec"]["holderIdentity"] == worker)
    value = obj["spec"]["renewTime"]
    return datetime.fromisoformat(value.replace("Z", "+00:00")), value


def workload_pod():
    return {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": workload, "namespace": "default"},
            "spec": {"nodeName": worker, "automountServiceAccountToken": False,
                     "containers": [{"name": "marker", "image": image, "imagePullPolicy": "Never",
                                     "command": ["/bin/sh", "-ec", "cat /data/marker && exec sleep 86400"],
                                     "volumeMounts": [{"name": "state", "mountPath": "/data", "readOnly": True}]}],
                     "volumes": [{"name": "state", "hostPath": {"path": marker_dir, "type": "Directory"}}]}}


def get_workload(source):
    return record(child(source, "-n", "default", "get", "pod", workload, "--ignore-not-found", "-o", "json"))


def workload_identity(source, expected_image):
    obj = get_workload(source)
    info, config = obj["metadata"], obj["spec"]
    expected = workload_pod()["spec"]
    identity(obj, info["uid"])
    require(not info.get("ownerReferences") and all(config[key] == expected[key] for key in ("nodeName", "automountServiceAccountToken", "volumes")) and
            not any(config.get(key) for key in ("hostNetwork", "hostPID", "hostIPC", "initContainers", "ephemeralContainers")))
    actual, = config["containers"]
    wanted, = expected["containers"]
    require(all(actual[key] == value for key, value in wanted.items()) and not any(actual.get(key) for key in ("args", "env", "envFrom", "volumeDevices")))
    require(obj["status"]["phase"] not in ("Succeeded", "Failed"))
    if obj["status"]["phase"] != "Running" or not obj["status"].get("containerStatuses"):
        return None
    state, = obj["status"]["containerStatuses"]
    if not state["ready"]:
        return None
    require(state["name"] == "marker" and state["imageID"].endswith(expected_image) and state["state"].get("running") is not None and
            state["containerID"])
    return info["uid"], state["containerID"]


def main(guest, source_path, assets_path, prejoin_path, prejoin_sha):
    proof = approved(prejoin_path, prejoin_sha)
    source = approved(source_path, proof["source_receipt_sha256"])
    assets = approved(assets_path, proof["assets_receipt_sha256"])
    require(proof["version"] == 1 and proof["scope"] == "synthetic-sqlite-worker-prejoin" and proof["guest_uid"] == guest and uid(guest) and
            all(uid(proof[key]) for key in ("namespace_uid", "source_cluster_uid", "service_uid", "vm_uid", "vmi_uid", "root_claim_uid",
                                            "root_volume_uid", "firmware_uuid", "join_token_request_uid", "join_token_secret_uid", "bootstrap_secret_uid")) and
            re.fullmatch(r"[a-z0-9][a-z0-9.-]{0,252}", proof["root_volume"]))
    require(source["namespace"] == namespace and source["cluster"] == source_name and source["namespace_uid"] == proof["namespace_uid"] and
            source["cluster_uid"] == proof["source_cluster_uid"] and source["service"] == service and source["statefulset"] == controller_set and
            source["marker"] == "bedrock-sqlite-continuity" and source["claim"] == controller_set + "-" + controller_pod and
            re.fullmatch(r"https://[0-9]+(?:\.[0-9]+){3}:6443", source["api_endpoint"]) and
            source["konnectivity_endpoint"] == source["api_endpoint"].removeprefix("https://").removesuffix(":6443") + ":8132")
    seed = digest(proof["source_cluster_uid"].encode())[:10]
    require(proof["mac_address"] == "02:" + ":".join(seed[i:i + 2] for i in range(0, 10, 2)))
    require(set(assets) == {"airgap_sha256", "busybox_digest", "busybox_tar_sha256", "k0s_sha256", "k0s_version", "ubuntu_digest"} and
            assets["k0s_version"] == version and all(sha(assets[key]) for key in ("airgap_sha256", "busybox_tar_sha256", "k0s_sha256")) and
            all(assets[key].startswith("sha256:") and sha(assets[key][7:]) for key in ("busybox_digest", "ubuntu_digest")))
    controller = verify_source(source, proof)
    verify_worker(source, proof, assets)
    token_id = original_token(source, proof, assets)
    token_state(proof, token_id)
    print("Waiting up to 45 minutes for the existing SQLite worker Node to become Ready.", file=sys.stderr, flush=True)
    node_uid, system_uuid = wait_for(lambda: node(source), 2700, "The existing SQLite worker Node did not become Ready.")
    require(system_uuid == proof["firmware_uuid"])
    before, _ = wait_for(lambda: lease(source, node_uid), 180, "The original SQLite worker Lease did not appear.")
    require(verify_source(source, proof) == controller)
    verify_worker(source, proof, assets)
    if get_workload(source) is None:
        try:
            child(source, "-n", "default", "create", "-f", "-", body=encode(workload_pod()))
        except subprocess.CalledProcessError as error:
            require(b"AlreadyExists" in error.stderr)
    workload_uid, container_id = wait_for(lambda: workload_identity(source, assets["busybox_digest"]), 900,
                                          "The exact SQLite worker workload did not become Ready.")
    require(container_id)
    require(child(source, "-n", "default", "logs", workload, "-c", "marker") == marker and
            child(source, "-n", "default", "exec", workload, "-c", "marker", "--", "cat", "/data/marker") == marker and
            child(source, "-n", "default", "exec", workload, "-c", "marker", "--", "sha256sum", "/data/marker").split()[0].decode() == digest(marker))
    require(workload_identity(source, assets["busybox_digest"]) == (workload_uid, container_id) and
            node(source) == (node_uid, system_uuid) and verify_source(source, proof) == controller)
    verify_worker(source, proof, assets)
    require(original_token(source, proof, assets) == token_id)
    request, _ = token_state(proof, token_id)
    if request and not request["metadata"].get("deletionTimestamp"):
        options = {"apiVersion": "v1", "kind": "DeleteOptions",
                   "preconditions": {"uid": proof["join_token_request_uid"]}}
        try:
            kc("delete", "--raw", f"/apis/k0smotron.io/v1beta2/namespaces/{namespace}/jointokenrequests/{worker}",
               "-f", "-", body=encode(options))
        except subprocess.CalledProcessError as error:
            require(b"NotFound" in error.stderr)
    wait_for(lambda: revoked(source, proof, token_id), 180, "The original SQLite worker token was not revoked.")
    revoked_lease, _ = wait_for(lambda: lease(source, node_uid), 180, "The SQLite worker Lease disappeared.")

    def renewed():
        observed = lease(source, node_uid)
        return observed if observed and observed[0] > max(before, revoked_lease) else None

    _, renewed_at = wait_for(renewed, 180, "The existing SQLite worker Lease did not renew after token revocation.")
    require(revoked(source, proof, token_id) and node(source) == (node_uid, system_uuid) and verify_source(source, proof) == controller and
            workload_identity(source, assets["busybox_digest"]) == (workload_uid, container_id) and
            child(source, "-n", "default", "exec", workload, "-c", "marker", "--", "cat", "/data/marker") == marker)
    verify_worker(source, proof, assets)
    print(json.dumps({"source_cluster_uid": proof["source_cluster_uid"], "worker": worker, "node_uid": node_uid,
                      "lease_renew_time": renewed_at, "vm_uid": proof["vm_uid"], "vmi_uid": proof["vmi_uid"],
                      "mac_address": proof["mac_address"], "firmware_uuid": proof["firmware_uuid"], "disk_serial": root_claim,
                      "root_claim": root_claim, "root_claim_uid": proof["root_claim_uid"], "root_volume": proof["root_volume"],
                      "root_volume_uid": proof["root_volume_uid"], "workload_pod_uid": workload_uid, "ubuntu_digest": assets["ubuntu_digest"],
                      "k0s_sha256": assets["k0s_sha256"], "workload_image_digest": assets["busybox_digest"], "workload_data_sha256": digest(marker)}, indent=2))


def report_checkpoint(error):
    checkpoints = [(frame.f_code.co_name, line) for frame, line in walk_tb(error.__traceback__)
                   if frame.f_code.co_filename == "<stdin>" and frame.f_code.co_name not in ("<module>", "require")]
    if checkpoints:
        name, line = checkpoints[-1]
        print(f"SQLite worker check stopped in {name} at line {line}.", file=sys.stderr)


try:
    main(*sys.argv[1:])
except ValueError as error:
    report_checkpoint(error)
    allowed = {failure, "The existing SQLite worker Node did not become Ready.", "The original SQLite worker Lease did not appear.",
               "The exact SQLite worker workload did not become Ready.", "The original SQLite worker token was not revoked.",
               "The SQLite worker Lease disappeared.", "The existing SQLite worker Lease did not renew after token revocation."}
    sys.exit(str(error) if str(error) in allowed else failure)
except (OSError, EOFError, subprocess.SubprocessError, AttributeError, IndexError, KeyError, TypeError, RecursionError) as error:
    report_checkpoint(error)
    sys.exit(failure)
PY
