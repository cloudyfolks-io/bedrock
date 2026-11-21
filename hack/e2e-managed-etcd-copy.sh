#!/usr/bin/env bash
set -euo pipefail
umask 077

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

uid_pattern='^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$'
sha_pattern='^[0-9a-f]{64}$'
if [[ ${E2E_MANAGED_ETCD_COPY:-} != 1 || $# != 5 || ! ${1:-} =~ $uid_pattern ||
  ! ${4:-} =~ $sha_pattern || ! ${5:-} =~ ^sha256:[0-9a-f]{64}$ ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_ETCD_COPY=1 bash hack/e2e-managed-etcd-copy.sh <guest-uid> <source.json> <proof-dir> <approved-frozen-sha256> <busybox-image-digest>' >&2
  exit 2
fi

guest_uid=$1
source_receipt=$2
proof_dir=$3
freeze_sha=$4
busybox_digest=$5
migrate=${BEDROCK_MIGRATE:?Set BEDROCK_MIGRATE to the verified Cluster API migration binary.}
migrate_sha=${BEDROCK_MIGRATE_SHA256:?Set BEDROCK_MIGRATE_SHA256 to the approved binary digest.}
export KUBECONFIG=/var/lib/k0s/pki/admin.conf
[[ $(uname -s) == Linux && -s /var/lib/bedrock/e2e-admin-password && -f $KUBECONFIG && ! -L $KUBECONFIG ]] ||
  fail 'Run this fixture inside an initialized Bedrock development guest.'
for tool in kubectl k0s jq python3 sha256sum awk realpath stat timeout; do
  command -v "$tool" >/dev/null
done
[[ $migrate == /* && -f $migrate && ! -L $migrate && -x $migrate && $migrate_sha =~ $sha_pattern ]] ||
  fail 'Use the approved Cluster API binary and its exact SHA-256.'
[[ $proof_dir == /* && -d $proof_dir && $(realpath -e -- "$proof_dir") == "$proof_dir" && -O $proof_dir &&
  $(stat -c '%a' -- "$proof_dir") == 700 ]] ||
  fail 'Use an absolute mode-0700 proof directory that you own.'

source "$(dirname "${BASH_SOURCE[0]}")/e2e/lib.sh"

namespace=bedrock-e2e-etcd-source
cluster=etcd-source
snapshot=etcd-source-frozen
snapshot_class=etcd-source-retain
reference_claim=etcd-source-reference
retained_claim=etcd-source-retained
driver=rook-ceph.rbd.csi.ceph.com
busybox=docker.io/library/busybox:1.37.0
inventory=$proof_dir/frozen-inventory.json
frozen=$proof_dir/frozen.json
output=$proof_dir/etcd-copy
message='The isolated etcd source is frozen.'

kc() {
  kubectl --request-timeout=30s "$@"
}

file_hash() {
  local hash
  hash=$(sha256sum -- "$1") || return 1
  printf '%s\n' "${hash%% *}"
}

private_file() {
  [[ $1 == /* && -f $1 && ! -L $1 && -O $1 && -s $1 &&
    $(realpath -e -- "$1") == "$1" && $(stat -c '%a' -- "$1") == 600 ]]
}

for file in "$source_receipt" "$frozen" "$inventory"; do
  private_file "$file" || fail 'Use only approved private evidence files.'
done
[[ ! -e $output && ! -L $output && $(file_hash "$migrate") == "$migrate_sha" &&
  $(file_hash "$frozen") == "$freeze_sha" ]] ||
  fail 'The output exists or an approved file digest changed.'
[[ $(kc get namespace kube-system -o jsonpath='{.metadata.uid}') == "$guest_uid" ]] ||
  fail 'The API does not match the approved isolated guest.'

source_sha=$(jq -er --arg pattern "$sha_pattern" '.source_receipt_sha256 | select(test($pattern))' "$frozen")
inventory_sha=$(jq -er --arg pattern "$sha_pattern" '.inventory_sha256 | select(test($pattern))' "$frozen")
frozen_approval=$("$migrate" verify-frozen-legacy-etcd "$inventory" "$inventory_sha" "$source_receipt" "$source_sha" "$guest_uid" |
  jq -er --arg inventory "$inventory_sha" --arg source "$source_sha" --arg pattern "$sha_pattern" '
    select(.version==1 and .frozen_etcd_control_planes==1 and .verified_disks==2 and
      .inventory_sha256==$inventory and .source_sha256==$source) | .approval_sha256 | select(test($pattern))')
jq -e --slurpfile source "$source_receipt" '
  .version==1 and .guest_uid==$source[0].guest_uid and .namespace_uid==$source[0].namespace_uid and
  .source_cluster_uid==$source[0].cluster_uid and (.fences | keys)==["pods","retained_disks","scale"]' "$frozen" >/dev/null
namespace_uid=$(jq -er '.namespace_uid' "$source_receipt")
cluster_uid=$(jq -er '.cluster_uid' "$source_receipt")
source_claim=$(jq -er '.etcd.disk.claim' "$source_receipt")
source_handle_sha=$(jq -er '.etcd.disk.handle_sha256' "$source_receipt")
k0s ctr --namespace k8s.io images ls | awk -v image="$busybox" -v digest="$busybox_digest" '
  $1 == image && $3 == digest { matches++ } END { exit matches != 1 }'

sealed_disk() {
  local binding=$1 handle hash
  handle=$(jq -er '.handle' <<<"$binding") || return 1
  hash=$(printf '%s' "$handle" | sha256sum) || return 1
  jq -c --arg hash "${hash%% *}" 'del(.handle) + {handle_sha256:$hash}' <<<"$binding"
}

disk_binding() {
  local claim=$1 backing=$2 size=$3 record pv
  record=$(kc -n "$namespace" get pvc "$claim" -o json | jq -ce --arg name "$claim" --arg backing "$backing" --arg size "$size" '
    select(.metadata.name==$name and .metadata.deletionTimestamp==null and .status.phase=="Bound" and
      .spec.storageClassName=="block-retain" and (.spec.volumeMode // "Filesystem")=="Filesystem" and
      .spec.accessModes==["ReadWriteOnce"] and .spec.resources.requests.storage==$size and .status.capacity.storage==$size and
      (if $backing=="" then .spec.dataSource==null and .spec.dataSourceRef==null
       else (.metadata.ownerReferences // [])==[] and .spec.dataSource=={apiGroup:"snapshot.storage.k8s.io",kind:"VolumeSnapshot",name:$backing} and
         (.spec.dataSourceRef==null or .spec.dataSourceRef==.spec.dataSource) end) and
      (.metadata.uid|length)>0 and (.spec.volumeName|length)>0) |
    {claim:.metadata.name,claim_uid:.metadata.uid,volume:.spec.volumeName}') || return 1
  pv=$(jq -er '.volume' <<<"$record") || return 1
  kc get pv "$pv" -o json | jq -ce --argjson disk "$record" --arg ns "$namespace" --arg driver "$driver" --arg size "$size" '
    select(.metadata.name==$disk.volume and .metadata.deletionTimestamp==null and .status.phase=="Bound" and
      .spec.storageClassName=="block-retain" and (.spec.volumeMode // "Filesystem")=="Filesystem" and
      .spec.accessModes==["ReadWriteOnce"] and .spec.capacity.storage==$size and .spec.persistentVolumeReclaimPolicy=="Retain" and
      .spec.csi.driver==$driver and (.spec.csi.readOnly // false)==false and
      (.spec.claimRef | .namespace==$ns and .name==$disk.claim and .uid==$disk.claim_uid) and
      (.metadata.uid|length)>0 and (.spec.csi.volumeHandle|length)>0) |
    $disk + {volume_uid:.metadata.uid,handle:.spec.csi.volumeHandle}'
}

unattached() {
  local binding=$1
  kc get --raw "/api/v1/namespaces/$namespace/pods" | jq -e --argjson disk "$binding" '
    .kind=="PodList" and (.metadata.continue // "")=="" and (.items|type)=="array" and
    all(.items[]; all(.spec.volumes[]?; (.persistentVolumeClaim.claimName // "")!=$disk.claim))' >/dev/null &&
  kc get --raw /apis/storage.k8s.io/v1/volumeattachments | jq -e --argjson disk "$binding" '
    .kind=="VolumeAttachmentList" and (.metadata.continue // "")=="" and (.items|type)=="array" and
    all(.items[]; (.spec.source.persistentVolumeName // "")!=$disk.volume and
      (.spec.source.inlineVolumeSpec.csi.volumeHandle // "")!=$disk.handle)' >/dev/null
}

unique_disks() {
  kc get --raw /api/v1/persistentvolumes | jq -e --arg ns "$namespace" --arg driver "$driver" --argjson disks "$1" '
    .kind=="PersistentVolumeList" and (.metadata.continue // "")=="" and (.items|type)=="array" and .items as $items |
    all($disks[]; . as $disk |
      [$items[] | select((.spec.csi.driver==$driver and .spec.csi.volumeHandle==$disk.handle) or
        .spec.claimRef.uid==$disk.claim_uid or (.spec.claimRef.namespace==$ns and .spec.claimRef.name==$disk.claim)) |
        .metadata.uid]==[$disk.volume_uid])' >/dev/null
}

frozen_source() {
  local role size binding
  [[ $(kc get namespace kube-system -o jsonpath='{.metadata.uid}') == "$guest_uid" ]] || return 1
  kc get namespace "$namespace" -o json | jq -e --arg uid "$namespace_uid" '
    .metadata.uid==$uid and .metadata.deletionTimestamp==null and .status.phase=="Active" and
    .metadata.labels["bedrock.cloudyfolks.io/project"]==null and .metadata.labels["bedrock.cloudyfolks.io/tenant"]==null' >/dev/null &&
  kc -n "$namespace" get rolebindings.rbac.authorization.k8s.io -o json | jq -e '.items==[]' >/dev/null &&
  kc get --raw "/apis/k0smotron.io/v1beta2/namespaces/$namespace/clusters/$cluster" |
    jq -e --slurpfile source "$source_receipt" '
      .metadata.uid==$source[0].cluster_uid and .metadata.deletionTimestamp==null and
      .spec.replicas==1 and .spec.storage.type=="etcd" and .spec.version==$source[0].k0s_version and
      .spec.externalAddress==$source[0].api_service.address' >/dev/null || return 1
  for role in controller etcd; do
    size=1Gi
    [[ $role != controller ]] || size=10Gi
    binding=$(disk_binding "$(jq -er --arg role "$role" '.[$role].disk.claim' "$source_receipt")" "" "$size") || return 1
    sealed_disk "$binding" | jq -e --slurpfile source "$source_receipt" --arg role "$role" '.==$source[0][$role].disk' >/dev/null &&
    unattached "$binding" || return 1
    kc -n "$namespace" get statefulset "$(jq -er --arg role "$role" '.[$role].statefulset' "$source_receipt")" -o json |
      jq -e --slurpfile source "$source_receipt" --arg role "$role" '
        .metadata.uid==$source[0][$role].statefulset_uid and .metadata.deletionTimestamp==null and
        .spec.replicas==0 and .status.observedGeneration==.metadata.generation and
        ([.status.replicas,.status.readyReplicas,.status.currentReplicas,.status.updatedReplicas,.status.availableReplicas] |
          all(.[]; (. // 0)==0)) and (.metadata.ownerReferences|length)==1 and
        any(.metadata.ownerReferences[]; .kind=="Cluster" and .uid==$source[0].cluster_uid and .controller==true)' >/dev/null &&
    kc get --raw "/api/v1/namespaces/$namespace/pods" | jq -e --slurpfile source "$source_receipt" --arg role "$role" '
      $source[0][$role] as $set | .kind=="PodList" and (.metadata.continue // "")=="" and (.items|type)=="array" and
      all(.items[]; .metadata.uid!=$set.pod_uid and .metadata.name!=$set.pod and
        all(.metadata.ownerReferences[]?; .uid!=$set.statefulset_uid))' >/dev/null || return 1
  done
}

denied() {
  local policy=$1 result
  shift
  if result=$("$@" 2>&1); then
    return 1
  fi
  [[ $result == *"$policy"* && $result == *"$message"* ]]
}

source_fences() {
  local type name role set claim volume
  for type in pods scale retained_disks; do
    name=bedrock-e2e-etcd-freeze-${type/retained_disks/disks}
    kc get validatingadmissionpolicy "$name" -o json |
      jq -e --slurpfile proof "$frozen" --arg type "$type" '
        .metadata.uid==$proof[0].fences[$type].policy_uid and .metadata.deletionTimestamp==null and .spec.failurePolicy=="Fail"' >/dev/null &&
    kc get validatingadmissionpolicybinding "$name" -o json |
      jq -e --slurpfile proof "$frozen" --arg type "$type" --arg name "$name" '
        .metadata.uid==$proof[0].fences[$type].binding_uid and .metadata.deletionTimestamp==null and
        .spec.policyName==$name and .spec.validationActions==["Deny"]' >/dev/null || return 1
  done
  for role in controller etcd; do
    set=$(jq -er --arg role "$role" '.[$role].statefulset' "$source_receipt") || return 1
    claim=$(jq -er --arg role "$role" '.[$role].disk.claim' "$source_receipt") || return 1
    volume=$(jq -er --arg role "$role" '.[$role].disk.volume' "$source_receipt") || return 1
    denied bedrock-e2e-etcd-freeze-pods kc -n "$namespace" create --dry-run=server -f - <<PROBE || return 1
apiVersion: v1
kind: Pod
metadata: {name: etcd-copy-source-probe-$role}
spec:
  containers:
    - name: probe
      image: $busybox
      volumeMounts: [{name: source, mountPath: /data, readOnly: true}]
  volumes: [{name: source, persistentVolumeClaim: {claimName: $claim, readOnly: true}}]
PROBE
    denied bedrock-e2e-etcd-freeze-scale kc -n "$namespace" patch statefulset "$set" --type=merge --dry-run=server -p '{"spec":{"replicas":1}}' &&
    denied bedrock-e2e-etcd-freeze-scale kc -n "$namespace" patch statefulset "$set" --subresource=scale --type=merge --dry-run=server -p '{"spec":{"replicas":1}}' &&
    denied bedrock-e2e-etcd-freeze-disks kc -n "$namespace" delete pvc "$claim" --dry-run=server --wait=false &&
    denied bedrock-e2e-etcd-freeze-disks kc delete pv "$volume" --dry-run=server --wait=false || return 1
  done
}

if ! frozen_source || ! source_fences; then
  fail 'The approved source is no longer frozen or protected.'
fi
source_disk=$(disk_binding "$source_claim" "" 1Gi)
source_handle=$(jq -er '.handle' <<<"$source_disk")
sealed_source=$(sealed_disk "$source_disk")
[[ $(jq -er '.handle_sha256' <<<"$sealed_source") == "$source_handle_sha" ]] ||
  fail 'The frozen etcd source handle changed.'
unique_disks "[$source_disk]" || fail 'The frozen etcd source has an ambiguous RBD binding.'

for resource in "volumesnapshot.snapshot.storage.k8s.io/$snapshot" "pvc/$reference_claim" "pvc/$retained_claim"; do
  kubectl_absent -n "$namespace" "$resource" || fail 'Use a fresh isolated guest without an earlier etcd copy.'
done
kubectl_absent "volumesnapshotclass.snapshot.storage.k8s.io/$snapshot_class" ||
  fail 'The isolated snapshot class already exists.'

class_params=$(kc get volumesnapshotclass.snapshot.storage.k8s.io block -o json | jq -ce --arg driver "$driver" '
  select(.metadata.deletionTimestamp==null and .driver==$driver and .deletionPolicy=="Delete" and .parameters.clusterID=="rook-ceph" and
    .parameters["csi.storage.k8s.io/snapshotter-secret-name"]=="rook-csi-rbd-provisioner" and
    .parameters["csi.storage.k8s.io/snapshotter-secret-namespace"]=="rook-ceph") | .parameters')
storage_params=$(kc get storageclass block -o json | jq -ce --arg driver "$driver" '
  select(.metadata.deletionTimestamp==null and .provisioner==$driver and .parameters.clusterID=="rook-ceph") | .parameters')
storage_class_identity() {
  kc get storageclass block-retain -o json | jq -er --arg driver "$driver" --argjson params "$storage_params" --arg uid "$uid_pattern" '
    select(.metadata.deletionTimestamp==null and .provisioner==$driver and .reclaimPolicy=="Retain" and
      (.volumeBindingMode // "Immediate")=="Immediate" and .parameters==$params) | .metadata.uid | select(test($uid))'
}
storage_class_uid=$(storage_class_identity)
ceph_identity() {
  kc -n rook-ceph get cephcluster.ceph.rook.io rook-ceph -o json | jq -er --arg uid "$uid_pattern" '
    select(.metadata.deletionTimestamp==null) | .status.ceph.fsid | select(test($uid))'
}
ceph_fsid=$(ceph_identity)
snapshot_class_uid=$(jq -n --arg name "$snapshot_class" --arg driver "$driver" --argjson params "$class_params" '
  {apiVersion:"snapshot.storage.k8s.io/v1",kind:"VolumeSnapshotClass",metadata:{name:$name},driver:$driver,
   deletionPolicy:"Retain",parameters:$params}' | kc create -f - -o json |
  jq -er --arg uid "$uid_pattern" '.metadata.uid | select(test($uid))')
snapshot_class_identity() {
  kc get volumesnapshotclass.snapshot.storage.k8s.io "$snapshot_class" -o json |
    jq -er --arg driver "$driver" --argjson params "$class_params" --arg uid "$uid_pattern" '
      select(.metadata.deletionTimestamp==null and .driver==$driver and .deletionPolicy=="Retain" and
        .parameters==$params) | .metadata.uid | select(test($uid))'
}
[[ $(snapshot_class_identity) == "$snapshot_class_uid" ]]

snapshot_uid=$(kc -n "$namespace" create -f - -o json <<SNAP | jq -er --arg uid "$uid_pattern" '.metadata.uid | select(test($uid))'
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata: {name: $snapshot}
spec:
  volumeSnapshotClassName: $snapshot_class
  source: {persistentVolumeClaimName: $source_claim}
SNAP
)
wait_jsonpath 360 true -n "$namespace" get volumesnapshot.snapshot.storage.k8s.io "$snapshot" -o jsonpath='{.status.readyToUse}'

snapshot_binding() {
  local snapshot_obj content content_obj handle hash
  snapshot_obj=$(kc -n "$namespace" get volumesnapshot.snapshot.storage.k8s.io "$snapshot" -o json |
    jq -ce --arg name "$snapshot" --arg class "$snapshot_class" --arg source "$source_claim" --arg uid "$snapshot_uid" '
      select(.metadata.name==$name and .metadata.deletionTimestamp==null and .spec.volumeSnapshotClassName==$class and
        .spec.source=={persistentVolumeClaimName:$source} and .status.readyToUse==true and .status.error==null and
        .status.restoreSize=="1Gi" and .metadata.uid==$uid and (.status.boundVolumeSnapshotContentName|length)>0) |
      {name:.metadata.name,uid:.metadata.uid,content_name:.status.boundVolumeSnapshotContentName}') || return 1
  content=$(jq -er '.content_name' <<<"$snapshot_obj") || return 1
  content_obj=$(kc get volumesnapshotcontent.snapshot.storage.k8s.io "$content" -o json |
    jq -ce --argjson snap "$snapshot_obj" --arg ns "$namespace" --arg class "$snapshot_class" --arg driver "$driver" \
      --arg handle "$source_handle" --arg uid "$uid_pattern" '
        select(.metadata.name==$snap.content_name and .metadata.deletionTimestamp==null and (.metadata.uid|test($uid)) and
          .spec.driver==$driver and .spec.deletionPolicy=="Retain" and .spec.volumeSnapshotClassName==$class and
          .spec.sourceVolumeMode=="Filesystem" and .spec.source=={volumeHandle:$handle} and
          (.spec.volumeSnapshotRef | .apiVersion=="snapshot.storage.k8s.io/v1" and .kind=="VolumeSnapshot" and
            .namespace==$ns and .name==$snap.name and .uid==$snap.uid) and
          .status.readyToUse==true and .status.error==null and .status.restoreSize==1073741824 and (.status.snapshotHandle|length)>0) |
        $snap + {content_uid:.metadata.uid,handle:.status.snapshotHandle}') || return 1
  handle=$(jq -er '.handle' <<<"$content_obj") || return 1
  hash=$(printf '%s' "$handle" | sha256sum) || return 1
  jq -c --arg hash "${hash%% *}" 'del(.handle) + {handle_sha256:$hash}' <<<"$content_obj"
}

snapshot_proof=$(snapshot_binding)
frozen_source || fail 'The approved source changed during snapshot capture.'
declare -A clone_uid
for claim in "$reference_claim" "$retained_claim"; do
  clone_uid[$claim]=$(kc -n "$namespace" create -f - -o json <<CLAIM | jq -er --arg uid "$uid_pattern" '.metadata.uid | select(test($uid))'
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: $claim}
spec:
  accessModes: [ReadWriteOnce]
  volumeMode: Filesystem
  storageClassName: block-retain
  resources:
    requests: {storage: 1Gi}
  dataSource: {apiGroup: snapshot.storage.k8s.io, kind: VolumeSnapshot, name: $snapshot}
CLAIM
  )
done
for claim in "$reference_claim" "$retained_claim"; do
  wait_jsonpath 360 Bound -n "$namespace" get pvc "$claim" -o jsonpath='{.status.phase}'
done
reference=$(disk_binding "$reference_claim" "$snapshot" 1Gi)
retained=$(disk_binding "$retained_claim" "$snapshot" 1Gi)
[[ $(jq -er '.claim_uid' <<<"$reference") == "${clone_uid[$reference_claim]}" &&
  $(jq -er '.claim_uid' <<<"$retained") == "${clone_uid[$retained_claim]}" ]] ||
  fail 'An independently created clone claim was replaced.'
jq -ne --argjson original "$source_disk" --argjson ref "$reference" --argjson copy "$retained" '
  [$original,$ref,$copy] | (map(.claim_uid)|unique|length)==3 and (map(.volume)|unique|length)==3 and
    (map(.volume_uid)|unique|length)==3 and (map(.handle)|unique|length)==3' >/dev/null
unique_disks "[$source_disk,$reference,$retained]" || fail 'The etcd copies have an ambiguous RBD binding.'

work=$(mktemp -d "$proof_dir/.etcd-copy.XXXXXX")
evidence=$work/evidence
mkdir -- "$evidence"
reader_name=
reader_uid=

remove_reader() {
  jq -nc --arg uid "$reader_uid" '{apiVersion:"v1",kind:"DeleteOptions",preconditions:{uid:$uid}}' |
    kc delete --raw "/api/v1/namespaces/$namespace/pods/$reader_name" -f - >/dev/null ||
    kubectl_absent -n "$namespace" pod "$reader_name" || return 1
  wait_gone 180 -n "$namespace" pod "$reader_name" || return 1
  reader_name=
  reader_uid=
}

cleanup() {
  local result=$?
  trap - EXIT
  if [[ -n $reader_name && -n $reader_uid ]]; then
    remove_reader || result=1
  fi
  rm -rf -- "$work"
  exit "$result"
}
trap cleanup EXIT

cat >"$work/extract.py" <<'PY'
import hashlib
from pathlib import Path
import re
import sys
import tarfile

destination = Path(sys.argv[1])
dirs = {"member", "member/snap", "member/wal"}
snap_name = re.compile(r"member/snap/(db|[0-9a-f]{16}-[0-9a-f]{16}\.snap|[0-9a-f]{16}\.snap\.db)")
wal_name = re.compile(r"member/wal/[0-9a-f]{16}-[0-9a-f]{16}\.wal")

try:
    destination.mkdir()
    seen = set()
    files = {}
    total = 0
    with tarfile.open(fileobj=sys.stdin.buffer, mode="r|") as archive:
        for item in archive:
            name = item.name
            if name in seen or len(seen) == 259:
                raise ValueError
            seen.add(name)
            path = destination / name
            if name in dirs and item.isdir():
                path.mkdir()
                continue
            if not item.isreg() or not (name == "existing" or snap_name.fullmatch(name) or wal_name.fullmatch(name)):
                raise ValueError
            if item.size < 0 or (item.size == 0) != (name == "existing") or item.size > (1 << 30) - total:
                raise ValueError
            total += item.size
            digest = hashlib.sha256()
            with archive.extractfile(item) as data, path.open("xb") as output:
                remaining = item.size
                while remaining:
                    chunk = data.read(min(1 << 20, remaining))
                    if not chunk:
                        raise ValueError
                    digest.update(chunk)
                    output.write(chunk)
                    remaining -= len(chunk)
            files[name] = item.size, digest.hexdigest()
    if not dirs <= seen or "member/snap/db" not in files or not any(wal_name.fullmatch(name) for name in files):
        raise ValueError
    digest = hashlib.sha256(b"legacy-etcd-member-tree-v1\n")
    for name, (size, checksum) in sorted(files.items()):
        digest.update(f"{name}\t{size}\t{checksum}\n".encode("ascii"))
    print(digest.hexdigest())
except (OSError, TypeError, ValueError, tarfile.TarError):
    sys.exit("Could not extract a complete private etcd member tree.")
PY

reader_evidence() {
  local claim=$1
  kc get --raw "/api/v1/namespaces/$namespace/pods" | jq -e --arg claim "$claim" --arg uid "$reader_uid" '
    .kind=="PodList" and (.metadata.continue // "")=="" and (.items|type)=="array" and
    ([.items[] | select(any(.spec.volumes[]?; .persistentVolumeClaim.claimName==$claim)) | .metadata.uid]==[$uid])' >/dev/null || return 1
  kc -n "$namespace" get pod "$reader_name" -o json |
    jq -ce --arg uid "$reader_uid" --arg claim "$claim" --arg image "$busybox" --arg digest "$busybox_digest" '
      select(.metadata.uid==$uid and .metadata.deletionTimestamp==null and (.metadata.ownerReferences // [])==[] and
        .status.phase=="Running" and (.spec.nodeName|length)>0 and .spec.automountServiceAccountToken==false and
        (.spec.hostNetwork // false)==false and (.spec.hostPID // false)==false and (.spec.hostIPC // false)==false and
        (.spec.initContainers // [])==[] and (.spec.ephemeralContainers // [])==[] and
        .spec.volumes==[{name:"data",persistentVolumeClaim:{claimName:$claim,readOnly:true}}] and
        (.spec.containers|length)==1 and .spec.containers[0].name=="reader" and .spec.containers[0].image==$image and
        .spec.containers[0].imagePullPolicy=="Never" and .spec.containers[0].command==["sleep","900"] and
        .spec.containers[0].volumeMounts==[{name:"data",mountPath:"/data",readOnly:true}] and
        .spec.containers[0].securityContext.capabilities=={drop:["ALL"],add:["DAC_READ_SEARCH"]} and
        .spec.containers[0].securityContext.allowPrivilegeEscalation==false and
        .spec.containers[0].securityContext.readOnlyRootFilesystem==true and
        (.status.containerStatuses|length)==1 and
        any(.status.containerStatuses[]; .name=="reader" and .ready==true and .restartCount==0 and
          .state.running!=null and (.containerID|length)>0 and (.imageID|endswith($digest)))) |
      {pod:.metadata.name,pod_uid:.metadata.uid,node:.spec.nodeName,container_id:.status.containerStatuses[0].containerID,
       image_id:.status.containerStatuses[0].imageID,claim:$claim,read_only:true}'
}

mount_read_only() {
  kc -n "$namespace" exec "$reader_name" -c reader -- awk '
    $5=="/data" { count++; if ($6 ~ /(^|,)ro(,|$)/) readonly++ }
    END { exit count != 1 || readonly != 1 }' /proc/self/mountinfo
}

extract_clone() {
  local binding=$1 role=$2 claim volume created mounted digest
  claim=$(jq -er '.claim' <<<"$binding")
  volume=$(jq -er '.volume' <<<"$binding")
  [[ $(disk_binding "$claim" "$snapshot" 1Gi) == "$binding" ]] ||
    fail 'The approved clone binding changed before extraction.'
  unattached "$binding"
  created=$(kc -n "$namespace" create -f - -o json <<READER
apiVersion: v1
kind: Pod
metadata: {generateName: etcd-copy-reader-}
spec:
  automountServiceAccountToken: false
  restartPolicy: Never
  activeDeadlineSeconds: 900
  securityContext:
    runAsUser: 0
    seccompProfile: {type: RuntimeDefault}
  containers:
    - name: reader
      image: $busybox
      imagePullPolicy: Never
      command: [sleep, "900"]
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities: {drop: [ALL], add: [DAC_READ_SEARCH]}
      volumeMounts: [{name: data, mountPath: /data, readOnly: true}]
  volumes: [{name: data, persistentVolumeClaim: {claimName: $claim, readOnly: true}}]
READER
  )
  reader_name=$(jq -er '.metadata.name | select(test("^etcd-copy-reader-[a-z0-9]+$"))' <<<"$created")
  reader_uid=$(jq -er --arg uid "$uid_pattern" '.metadata.uid | select(test($uid))' <<<"$created")
  kubectl -n "$namespace" wait --for=condition=Ready "pod/$reader_name" --timeout=180s >&2
  mounted=$(reader_evidence "$claim")
  mount_read_only
  digest=$(timeout 240s kubectl -n "$namespace" exec "$reader_name" -c reader -- sh -ec '
    cd /data
    for entry in * .[!.]* ..?*; do
      [ -e "$entry" ] || [ -L "$entry" ] || continue
      case "$entry" in
        member|existing) ;;
        lost+found) [ -d "$entry" ] && [ ! -L "$entry" ] || exit 1 ;;
        *) exit 1 ;;
      esac
    done
    if [ -e existing ] || [ -L existing ]; then
      exec tar -cf - member existing
    fi
    exec tar -cf - member
  ' | python3 -I "$work/extract.py" "$evidence/$role")
  [[ $digest =~ $sha_pattern ]]
  expect_equal "$mounted" "$(reader_evidence "$claim")" 'approved reader identity after extraction' >&2
  mount_read_only
  remove_reader
  retry_until 240 unattached "$binding"
  [[ $(disk_binding "$claim" "$snapshot" 1Gi) == "$binding" ]] ||
    fail 'The approved clone binding changed after extraction.'
  jq -nc --argjson reader "$mounted" --arg sha "$digest" --arg volume "$volume" '
    $reader + {member_tree_sha256:$sha,volume:$volume}' >"$work/$role.json"
}

extract_clone "$reference" reference
extract_clone "$retained" retained
tree_sha=$(jq -er '.member_tree_sha256' "$work/reference.json")
jq -e --arg sha "$tree_sha" --slurpfile original "$work/reference.json" '
  .member_tree_sha256==$sha and .pod_uid!=$original[0].pod_uid and .container_id!=$original[0].container_id' "$work/retained.json" >/dev/null

if ! frozen_source || ! source_fences; then
  fail 'The approved source changed during the read-only extractions.'
fi
[[ $(file_hash "$migrate") == "$migrate_sha" && $(file_hash "$frozen") == "$freeze_sha" &&
  $(storage_class_identity) == "$storage_class_uid" && $(snapshot_class_identity) == "$snapshot_class_uid" &&
  $(ceph_identity) == "$ceph_fsid" && $(snapshot_binding) == "$snapshot_proof" &&
  $(disk_binding "$source_claim" "" 1Gi) == "$source_disk" &&
  $(disk_binding "$reference_claim" "$snapshot" 1Gi) == "$reference" &&
  $(disk_binding "$retained_claim" "$snapshot" 1Gi) == "$retained" ]] ||
  fail 'A receipt or the approved snapshot custody changed.'
if ! unique_disks "[$source_disk,$reference,$retained]" || ! unattached "$reference" || ! unattached "$retained"; then
  fail 'An RBD claim remains in use or has an ambiguous binding.'
fi

jq -n --arg freeze "$frozen_approval" --argjson source "$sealed_source" --argjson snapshot "$snapshot_proof" \
  --argjson reference "$(sealed_disk "$reference")" --argjson retained "$(sealed_disk "$retained")" --arg tree "$tree_sha" '
    {version:1,frozen_approval_sha256:$freeze,source_disk:$source,snapshot:$snapshot,reference_disk:$reference,
     retained_disk:$retained,member_tree_sha256:$tree}' >"$evidence/data-proof.json"
proof_sha=$(file_hash "$evidence/data-proof.json")
"$migrate" verify-legacy-etcd-datastore "$inventory" "$inventory_sha" "$source_receipt" "$source_sha" "$guest_uid" \
  "$evidence/data-proof.json" "$proof_sha" "$evidence/reference" "$evidence/retained" >"$evidence/datastore.json"
datastore_sha=$(jq -er --arg frozen "$frozen_approval" --arg proof "$proof_sha" --arg tree "$tree_sha" --arg sha "$sha_pattern" '
  select(.version==1 and .frozen_approval_sha256==$frozen and .data_proof_sha256==$proof and .member_tree_sha256==$tree and
    .matched_files>=2 and .matched_bytes>0) | .approval_sha256 | select(test($sha))' "$evidence/datastore.json")
jq -n --arg freeze "$freeze_sha" --arg guest "$guest_uid" --arg ns "$namespace_uid" --arg cluster "$cluster_uid" --arg fsid "$ceph_fsid" \
  --arg storage "$storage_class_uid" --arg class "$snapshot_class" --arg class_uid "$snapshot_class_uid" \
  --arg proof "$proof_sha" --arg datastore "$datastore_sha" --slurpfile reference "$work/reference.json" --slurpfile retained "$work/retained.json" '
  {version:1,scope:"synthetic-shared-snapshot",guest_uid:$guest,namespace_uid:$ns,source_cluster_uid:$cluster,frozen_receipt_sha256:$freeze,
   ceph_fsid:$fsid,storage_class_uid:$storage,snapshot_class:$class,snapshot_class_uid:$class_uid,data_proof_sha256:$proof,
   datastore_approval_sha256:$datastore,reference_reader:$reference[0],retained_reader:$retained[0]}' >"$evidence/custody.json"
mv -T --no-clobber -- "$evidence" "$output"
[[ ! -e $evidence && -d $output && ! -L $output ]]
printf '%s\n' 'Both extracts share one synthetic RBD snapshot. The regional independent backup and native recovery gates still apply.' >&2
file_hash "$output/custody.json"
