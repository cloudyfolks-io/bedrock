#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

if [[ ${E2E_MANAGED_ETCD_MEMBER_EVIDENCE:-} != 1 || $# != 4 ||
  ! ${1:-} =~ ^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$ || ! ${3:-} =~ ^[0-9a-f]{64}$ ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_ETCD_MEMBER_EVIDENCE=1 bash hack/e2e-managed-etcd-member-evidence.sh <isolated-guest-uid> <private-source.json> <approved-source-sha256> <private-proof-directory>' >&2
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


def require(condition):
    if not condition:
        raise ValueError()


def unique(pairs):
    result = dict(pairs)
    require(len(result) == len(pairs))
    return result


def json_record(data):
    require(0 < len(data) <= 1024 * 1024)
    return json.loads(data, object_pairs_hook=unique, parse_constant=lambda _: require(False))


def kubectl(*args):
    result = subprocess.run(["kubectl", "--request-timeout=15s", *args], check=True,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=35)
    return json_record(result.stdout)


def load_source(path, digest):
    require(path.startswith("/") and os.path.realpath(path) == path)
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as record:
        info = os.fstat(record.fileno())
        require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600 and
                info.st_uid == os.geteuid() and info.st_nlink == 1)
        data = record.read(1024 * 1024 + 1)
    require(hashlib.sha256(data).hexdigest() == digest)
    return json_record(data)


def uint64(value):
    require(type(value) is int and 0 < value < 2**64)
    return value


def decimal(value):
    require(re.fullmatch(r"[1-9][0-9]{0,19}", value))
    return uint64(int(value))


def exact_object(record, expected_uid):
    metadata = record["metadata"]
    require(metadata["uid"] == expected_uid and metadata.get("deletionTimestamp") is None)
    return metadata


def verify_runtime(source):
    namespace, cluster, etcd = source["namespace"], source["cluster"], source["etcd"]
    set_name, pod_name, service_name = etcd["statefulset"], etcd["pod"], etcd["service"]["name"]
    owner = {"apiVersion": "k0smotron.io/v1beta2", "kind": "Cluster", "name": cluster,
             "uid": source["cluster_uid"], "controller": True}
    selector = {"app": "k0smotron", "cluster": cluster, "component": "etcd",
                "app.kubernetes.io/managed-by": "cluster-api"}
    image = "quay.io/k0sproject/etcd:v3.7.1"

    for name, uid in (("kube-system", source["guest_uid"]), (namespace, source["namespace_uid"])):
        record = kubectl("get", "namespace", name, "-o", "json")
        metadata = exact_object(record, uid)
        require(record["status"]["phase"] == "Active" and not any(
            key in metadata.get("labels", {}) for key in ("bedrock.cloudyfolks.io/tenant", "bedrock.cloudyfolks.io/project")))

    record = kubectl("-n", namespace, "get", "cluster.k0smotron.io", cluster, "-o", "json")
    metadata = exact_object(record, source["cluster_uid"])
    settings = record["spec"]
    require(record["apiVersion"] == owner["apiVersion"] and metadata["labels"]["app.kubernetes.io/managed-by"] == "cluster-api" and
            settings["version"] == "v1.36.3+k0s.1" and settings["replicas"] == 1 and settings["storage"]["type"] == "etcd" and
            settings["storage"]["etcd"]["image"] == image and not settings["storage"]["etcd"].get("args"))

    record = kubectl("-n", namespace, "get", "statefulset", set_name, "-o", "json")
    metadata = exact_object(record, etcd["statefulset_uid"])
    reference, = metadata["ownerReferences"]
    require(all(reference[key] == value for key, value in owner.items()) and record["spec"]["replicas"] == 1 and
            record["spec"]["serviceName"] == service_name and record["spec"]["selector"] == {"matchLabels": selector})

    record = kubectl("-n", namespace, "get", "pod", pod_name, "-o", "json")
    metadata = exact_object(record, etcd["pod_uid"])
    reference, = metadata["ownerReferences"]
    require(all(metadata["labels"].get(key) == value for key, value in selector.items()) and
            reference["apiVersion"] == "apps/v1" and reference["kind"] == "StatefulSet" and reference["name"] == set_name and
            reference["uid"] == etcd["statefulset_uid"] and reference["controller"] is True and record["status"]["phase"] == "Running")
    pod = record["spec"]
    container, = pod["containers"]
    command, script = container["args"]
    require(container["name"] == "etcd" and container["image"] == image and container["command"] == ["/bin/bash"] and command == "-c" and
            hashlib.sha256(script.encode()).hexdigest() == "e35f330476277753f5458c7635ac9133dce68e59b71d71352a7a4b8548e34d31" and
            not any(pod.get(key) for key in ("hostNetwork", "hostPID", "hostIPC", "ephemeralContainers")))
    mounts = {item["name"]: item for item in container["volumeMounts"]}
    require(len(mounts) == len(container["volumeMounts"]) and mounts["certs"] == {"name": "certs", "mountPath": "/var/lib/k0s/pki/etcd/"} and
            mounts["etcd-data"] == {"name": "etcd-data", "mountPath": "/var/lib/k0s/etcd"})
    volumes = {item["name"]: item for item in pod["volumes"]}
    require(volumes["etcd-data"]["persistentVolumeClaim"] == {"claimName": etcd["disk"]["claim"]})
    projections = volumes["certs"]["projected"]["sources"]
    require(len(projections) == 3)
    for projection, role in zip(projections, ("ca", "server", "peer")):
        suffix = "" if role == "ca" else "-" + role
        secret = projection["secret"]
        require(set(projection) == {"secret"} and secret["name"] == cluster + "-etcd" + suffix and not secret.get("optional") and
                secret["items"] == [{"key": "tls.crt", "path": role + ".crt"}, {"key": "tls.key", "path": role + ".key"}])
    status, = record["status"]["containerStatuses"]
    require(status["name"] == "etcd" and status["ready"] is True and "running" in status["state"] and
            status["imageID"] == etcd["image_id"] and re.fullmatch(r"containerd://[0-9a-f]{64}", status["containerID"]))
    addresses = sorted(item["ip"] for item in record["status"]["podIPs"])
    require(addresses and record["status"]["podIP"] in addresses)

    record = kubectl("-n", namespace, "get", "service", service_name, "-o", "json")
    metadata = exact_object(record, etcd["service"]["uid"])
    reference, = metadata["ownerReferences"]
    service = record["spec"]
    ports = [("client", 2379, "TCP"), ("peer", 2380, "TCP")]
    require(all(reference[key] == value for key, value in owner.items()) and service["type"] == "ClusterIP" and
            service["clusterIP"] == "None" and service["publishNotReadyAddresses"] is True and service["selector"] == selector and
            not service.get("externalIPs") and not service.get("externalName") and
            sorted((port["name"], port["port"], port["protocol"]) for port in service["ports"]) == ports and
            all(port["targetPort"] == port["port"] for port in service["ports"]))

    record = kubectl("-n", namespace, "get", "endpointslices.discovery.k8s.io", "-l",
                     "kubernetes.io/service-name=" + service_name, "-o", "json")
    published = []
    for item in record["items"]:
        require(item["metadata"].get("deletionTimestamp") is None and any(
            ref["apiVersion"] == "v1" and ref["kind"] == "Service" and ref["uid"] == etcd["service"]["uid"] and
            ref["name"] == service_name and ref.get("controller") is True for ref in item["metadata"]["ownerReferences"]))
        require(sorted((port["name"], port["port"], port["protocol"]) for port in item["ports"]) == ports)
        for endpoint in item["endpoints"]:
            target = endpoint["targetRef"]
            require(endpoint["conditions"]["ready"] is True and endpoint["conditions"].get("terminating") is not True and
                    target["kind"] == "Pod" and target["namespace"] == namespace and target["name"] == pod_name and target["uid"] == etcd["pod_uid"])
            published.extend(endpoint["addresses"])
    require(sorted(published) == addresses)

    disk = etcd["disk"]
    record = kubectl("-n", namespace, "get", "pvc", disk["claim"], "-o", "json")
    exact_object(record, disk["claim_uid"])
    require(record["status"]["phase"] == "Bound" and record["spec"]["storageClassName"] == "block-retain" and
            record["spec"]["volumeName"] == disk["volume"])
    record = kubectl("get", "pv", disk["volume"], "-o", "json")
    exact_object(record, disk["volume_uid"])
    volume = record["spec"]
    binding, csi = volume["claimRef"], volume["csi"]
    require(record["status"]["phase"] == "Bound" and volume["persistentVolumeReclaimPolicy"] == "Retain" and
            binding["namespace"] == namespace and binding["name"] == disk["claim"] and binding["uid"] == disk["claim_uid"] and
            csi["driver"] == "rook-ceph.rbd.csi.ceph.com" and hashlib.sha256(csi["volumeHandle"].encode()).hexdigest() == disk["handle_sha256"])
    return status["containerID"], status["restartCount"], addresses


def observe_member(source):
    etcd = source["etcd"]
    prior = etcd["status"]
    endpoint, peer = prior["endpoint"], prior["peer_url"]
    runtime = verify_runtime(source)
    script = '''
    [[ $ETCDCTL_ENDPOINTS == "$1" && $ETCDCTL_CACERT == /var/lib/k0s/pki/etcd/ca.crt &&
      $ETCDCTL_CERT == /var/lib/k0s/pki/etcd/server.crt && $ETCDCTL_KEY == /var/lib/k0s/pki/etcd/server.key ]]
    test -s /var/lib/k0s/etcd/member/snap/db
    endpoint=$1
    shift
    exec -c etcdctl --endpoints="$endpoint" --cacert="$ETCDCTL_CACERT" --cert="$ETCDCTL_CERT" --key="$ETCDCTL_KEY" \
      --insecure-transport=false --insecure-skip-tls-verify=false --dial-timeout=5s --command-timeout=10s --write-out=json "$@"
    '''
    cmd = ("-n", source["namespace"], "exec", etcd["pod"], "-c", "etcd", "--", "/bin/bash", "-ec", script, "etcdctl", endpoint)
    membership = kubectl(*cmd, "member", "list")
    observation, = kubectl(*cmd, "endpoint", "status")
    require(verify_runtime(source) == runtime)

    member, = membership["members"]
    status = observation["Status"]
    header, member_header = status["header"], membership["header"]
    cluster_id, member_id = uint64(header["cluster_id"]), uint64(header["member_id"])
    require(uint64(member_header["cluster_id"]) == cluster_id and uint64(member_header["member_id"]) == member_id and
            uint64(status["leader"]) == member_id and uint64(member["ID"]) == member_id and
            f"{cluster_id:016x}" == prior["cluster_id"] and f"{member_id:016x}" == prior["member_id"])
    client = "https://" + etcd["pod"] + "." + etcd["statefulset"] + ":2379"
    require(member["name"] == etcd["pod"] and member["peerURLs"] == [peer] and member["clientURLs"] == [client] and
            member.get("isLearner", False) is False and status.get("isLearner", False) is False and not status.get("errors") and
            observation["Endpoint"] == endpoint and status["version"] == "3.7.1")
    revision, term = uint64(header["revision"]), uint64(status["raftTerm"])
    index, applied = uint64(status["raftIndex"]), uint64(status["raftAppliedIndex"])
    require(revision >= decimal(source["marker"]["revision"]) and revision >= decimal(prior["committed_revision"]) and
            uint64(header["raft_term"]) == term >= decimal(prior["raft_term"]) and
            index >= decimal(prior["raft_index"]) and index >= applied >= decimal(prior["raft_applied_index"]))
    return {"cluster_id": f"{cluster_id:016x}", "member_id": f"{member_id:016x}", "member_name": member["name"],
            "endpoint": endpoint, "peer_url": peer, "advertised_client_url": client, "version": status["version"],
            "committed_revision": str(revision), "raft_term": str(term), "raft_index": str(index), "raft_applied_index": str(applied)}


def main(guest, source_path, source_sha, directory):
    output = Path(directory) / "member-evidence.json"
    require(directory.startswith("/") and os.path.realpath(directory) == directory)
    info = os.stat(directory, follow_symlinks=False)
    require(stat.S_ISDIR(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o700 and info.st_uid == os.geteuid() and
            not os.path.lexists(output))
    source = load_source(source_path, source_sha)
    etcd = source["etcd"]
    ids = (source["guest_uid"], source["namespace_uid"], source["cluster_uid"], etcd["statefulset_uid"], etcd["pod_uid"],
           etcd["service"]["uid"], etcd["disk"]["claim_uid"], etcd["disk"]["volume_uid"], source["marker"]["uid"])
    require(len(set(ids)) == len(ids) and all(re.fullmatch(r"[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}", uid) and
                                             uid != "00000000-0000-0000-0000-000000000000" for uid in ids))
    require(type(source["version"]) is int and source["version"] == 1 and source["guest_uid"] == guest and
            source["namespace"] == "bedrock-e2e-etcd-source" and source["cluster"] == "etcd-source" and
            source["k0s_version"] == "v1.36.3+k0s.1" and source["marker"]["name"] == "bedrock-etcd-continuity" and
            etcd["statefulset"] == "kmc-etcd-source-etcd" and etcd["pod"] == "kmc-etcd-source-etcd-0" and
            etcd["service"]["name"] == etcd["statefulset"] and etcd["disk"]["claim"] == "etcd-data-kmc-etcd-source-etcd-0" and
            re.fullmatch(r"[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?", etcd["disk"]["volume"]) and
            re.fullmatch(r"[0-9a-f]{64}", etcd["disk"]["handle_sha256"]) and re.search(r"sha256:[0-9a-f]{64}$", etcd["image_id"]) and
            etcd["status"]["version"] == "3.7.1" and etcd["status"]["endpoint"] == "https://kmc-etcd-source-etcd:2379" and
            etcd["status"]["peer_url"] == "https://kmc-etcd-source-etcd-0.kmc-etcd-source-etcd:2380")
    status = observe_member(source)
    receipt = {"version": 1, "guest_uid": guest, "source_receipt_sha256": source_sha, "namespace_uid": source["namespace_uid"],
               "source_cluster_uid": source["cluster_uid"], "etcd_statefulset_uid": etcd["statefulset_uid"], "etcd_pod_uid": etcd["pod_uid"],
               "etcd_service_uid": etcd["service"]["uid"], "etcd_image_id": etcd["image_id"], "status": status}
    data = (json.dumps(receipt, sort_keys=True, separators=(",", ":")) + "\n").encode()
    with tempfile.NamedTemporaryFile(dir=directory, prefix=".etcd-member-") as record:
        record.write(data)
        record.flush()
        os.fsync(record.fileno())
        os.link(record.name, output)
    descriptor = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    print(hashlib.sha256(data).hexdigest())


try:
    main(*sys.argv[1:])
except (OSError, subprocess.SubprocessError, AttributeError, KeyError, TypeError, ValueError, RecursionError):
    sys.exit("The approved isolated etcd member identity could not be verified.")
PY
