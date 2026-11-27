#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ ${E2E_MANAGED_SQLITE_COPY:-} != 1 || $# != 5 ||
  ! ${1:-} =~ ^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$ || ! ${4:-} =~ ^[0-9a-f]{64}$ || ! ${5:-} =~ ^sha256:[0-9a-f]{64}$ ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_SQLITE_COPY=1 bash hack/e2e-managed-sqlite-copy.sh <guest-uid> <sqlite-source.json> <proof-dir> <approved-frozen-sha256> <busybox-digest>' >&2
  exit 2
fi

export KUBECONFIG=/var/lib/k0s/pki/admin.conf
[[ $(uname -sm) == 'Linux x86_64' && -s /var/lib/bedrock/e2e-admin-password && -f $KUBECONFIG && ! -L $KUBECONFIG ]]
guest_uid=$1
source_receipt=$2
proof_dir=$3
freeze_sha=$4
busybox_digest=$5
migrate=${BEDROCK_MIGRATE:?Set BEDROCK_MIGRATE to the verified Cluster API migration binary.}
[[ $migrate == /* && -f $migrate && ! -L $migrate && -x $migrate ]]
[[ $proof_dir == /* && -d $proof_dir && $(realpath -e "$proof_dir") == "$proof_dir" && -O $proof_dir && $(stat -c '%a' "$proof_dir") == 700 ]]

source "$(dirname "${BASH_SOURCE[0]}")/e2e/lib.sh"

namespace=bedrock-e2e-sqlite-source
cluster=sqlite-source
statefulset=kmc-$cluster
source_pod=$statefulset-0
source_claim=$statefulset-$source_pod
target_claim=kmc-sqlite-restored-kmc-sqlite-restored-0
reference_claim=sqlite-source-reference
snapshot=sqlite-source-frozen
snapshot_class=sqlite-source-retain
driver=rook-ceph.rbd.csi.ceph.com
busybox=docker.io/library/busybox:1.37.0
inventory=$proof_dir/frozen-inventory.json
frozen=$proof_dir/frozen.json
receipt=$proof_dir/retained-copy.json

file_hash() {
  local result
  result=$(sha256sum -- "$1") || return 1
  printf '%s\n' "${result%% *}"
}

private_file() {
  [[ -f $1 && ! -L $1 && -O $1 && -s $1 && $(stat -c '%a' "$1") == 600 ]]
}

uid_of() {
  kubectl get "$@" -o jsonpath='{.metadata.uid}'
}

for file in "$source_receipt" "$inventory" "$frozen" "$proof_dir/original.kubeconfig"; do
  private_file "$file"
done
for path in "$receipt" "$proof_dir/source" "$proof_dir/retained"; do
  [[ ! -e $path && ! -L $path ]]
done
expect_equal "$guest_uid" "$(uid_of namespace kube-system)" 'isolated development guest UID' >&2
expect_equal "$freeze_sha" "$(file_hash "$frozen")" 'approved frozen receipt SHA-256' >&2
namespace_uid=$(uid_of namespace "$namespace")
cluster_uid=$(uid_of cluster.k0smotron.io "$cluster" -n "$namespace")
service_uid=$(uid_of service sqlite-endpoint -n "$namespace")
inventory_sha=$(file_hash "$inventory")
test -n "$namespace_uid" && test -n "$cluster_uid" && test -n "$service_uid"
kubectl -n "$namespace" get rolebindings.rbac.authorization.k8s.io -o json | jq -e '.items == []' >/dev/null
jq -e --arg guest "$guest_uid" --arg ns "$namespace_uid" --arg cluster "$cluster_uid" --arg service "$service_uid" \
  --arg source "$(file_hash "$source_receipt")" --arg inventory "$inventory_sha" --arg config "$(file_hash "$proof_dir/original.kubeconfig")" '
    .version==1 and .guest_uid==$guest and .namespace_uid==$ns and .source_cluster_uid==$cluster and .service_uid==$service and
    .source_receipt_sha256==$source and .inventory_sha256==$inventory and .kubeconfig_sha256==$config' "$frozen" >/dev/null
jq -e --arg ns "$namespace" --arg ns_uid "$namespace_uid" --arg cluster "$cluster" --arg uid "$cluster_uid" --arg claim "$source_claim" \
  --arg set "$statefulset" '.namespace==$ns and .namespace_uid==$ns_uid and .cluster==$cluster and .cluster_uid==$uid and
    .claim==$claim and .statefulset==$set and ([.claim_uid,.volume,.volume_uid] | all(.[]; type=="string" and length>0))' "$source_receipt" >/dev/null
source_claim_uid=$(jq -er '.claim_uid' "$source_receipt")
source_volume=$(jq -er '.volume' "$source_receipt")
source_volume_uid=$(jq -er '.volume_uid' "$source_receipt")
"$migrate" verify-frozen-legacy-storage "$inventory" "$cluster_uid" | jq -e --arg digest "$inventory_sha" '
  .version==1 and .frozen_sqlite_control_planes==1 and .inventory_sha256==$digest' >/dev/null
k0s ctr --namespace k8s.io images ls | awk -v image="$busybox" -v digest="$busybox_digest" '
  $1 == image && $3 == digest { matches++ } END { exit matches != 1 }'

unattached() {
  kubectl -n "$namespace" get pods -o json | jq -e --arg claim "$1" '
    all(.items[]; all(.spec.volumes[]?; (.persistentVolumeClaim.claimName // "")!=$claim))' >/dev/null &&
  kubectl get volumeattachments.storage.k8s.io -o json | jq -e --arg volume "$2" '
    all(.items[]; (.spec.source.persistentVolumeName // "")!=$volume)' >/dev/null
}

source_fence() {
  local denial
  if denial=$(kubectl -n "$namespace" create --dry-run=server -f - 2>&1 <<EOF
apiVersion: v1
kind: Pod
metadata: {name: sqlite-copy-fence-proof}
spec:
  containers: [{name: proof, image: '$busybox'}]
  volumes: [{name: source, persistentVolumeClaim: {claimName: $source_claim}}]
EOF
  ); then
    return 1
  fi
  [[ $denial == *'the isolated SQLite source is fenced'* && $denial == *'bedrock-e2e-sqlite-source-pods'* ]] || return 1
  if denial=$(kubectl -n "$namespace" patch statefulset "$statefulset" --type=merge --dry-run=server -p '{"spec":{"replicas":1}}' 2>&1); then
    return 1
  fi
  [[ $denial == *'the isolated SQLite source is fenced'* && $denial == *'bedrock-e2e-sqlite-source-scale'* ]] || return 1
  if denial=$(kubectl -n "$namespace" scale statefulset "$statefulset" --dry-run=server --replicas=1 2>&1); then
    return 1
  fi
  [[ $denial == *'the isolated SQLite source is fenced'* && $denial == *'bedrock-e2e-sqlite-source-scale'* ]]
}

frozen_source() {
  source_fence &&
  kubectl -n "$namespace" get cluster.k0smotron.io "$cluster" -o json | jq -e --arg uid "$cluster_uid" '
    .metadata.uid==$uid and .metadata.deletionTimestamp==null and .spec.replicas==1 and
    .spec.storage.type=="kine" and .spec.storage.kine.dataSourceURL=="sqlite:///var/lib/k0s/state.db"' >/dev/null &&
  kubectl -n "$namespace" get statefulset "$statefulset" -o json | jq -e --arg uid "$cluster_uid" '
    .metadata.deletionTimestamp==null and .spec.replicas==0 and .status.observedGeneration==.metadata.generation and
    any(.metadata.ownerReferences[]; .kind=="Cluster" and .uid==$uid and .controller==true) and
    ([.status.replicas,.status.currentReplicas,.status.updatedReplicas,.status.readyReplicas,.status.availableReplicas] |
      all(.[]; (. // 0)==0))' >/dev/null &&
  kubectl_absent -n "$namespace" pod "$source_pod" && unattached "$source_claim" "$source_volume"
}

bound_disk() {
  local claim=$1 class=$2
  kubectl -n "$namespace" get pvc "$claim" -o json | jq -ce --arg name "$claim" --arg class "$class" '
    select(.metadata.name==$name and .metadata.deletionTimestamp==null and .status.phase=="Bound" and
      .spec.storageClassName==$class and .spec.volumeMode=="Filesystem" and .spec.accessModes==["ReadWriteOnce"] and
      .spec.resources.requests.storage=="10Gi" and (.metadata.uid|length)>0 and (.spec.volumeName|length)>0) |
    {claim:.metadata.name,claim_uid:.metadata.uid,volume:.spec.volumeName}'
}

disk_binding() {
  local claim=$1 class=$2 disk
  disk=$(bound_disk "$claim" "$class") || return 1
  kubectl get pv "$(jq -er '.volume' <<<"$disk")" -o json | jq -ce --argjson disk "$disk" --arg ns "$namespace" \
    --arg class "$class" --arg driver "$driver" '
      select(.metadata.deletionTimestamp==null and .status.phase=="Bound" and .spec.storageClassName==$class and
        .spec.volumeMode=="Filesystem" and .spec.csi.driver==$driver and (.spec.csi.readOnly // false)==false and
        (.spec.claimRef | .namespace==$ns and .name==$disk.claim and .uid==$disk.claim_uid) and
        (.metadata.uid|length)>0 and (.spec.csi.volumeHandle|length)>0) |
      $disk + {volume_uid:.metadata.uid,handle:.spec.csi.volumeHandle,reclaim:.spec.persistentVolumeReclaimPolicy}'
}

frozen_source
source_binding=$(disk_binding "$source_claim" block)
jq -e --arg claim "$source_claim_uid" --arg volume "$source_volume" --arg uid "$source_volume_uid" '
  .claim_uid==$claim and .volume==$volume and .volume_uid==$uid' <<<"$source_binding" >/dev/null
source_handle=$(jq -er '.handle' <<<"$source_binding")
class_params=$(kubectl get volumesnapshotclass.snapshot.storage.k8s.io block -o json | jq -ce --arg driver "$driver" '
  select(.metadata.deletionTimestamp==null and .driver==$driver and .deletionPolicy=="Delete" and
    .parameters.clusterID=="rook-ceph" and .parameters["csi.storage.k8s.io/snapshotter-secret-name"]=="rook-csi-rbd-provisioner" and
    .parameters["csi.storage.k8s.io/snapshotter-secret-namespace"]=="rook-ceph") | .parameters')
storage_params=$(kubectl get storageclass block -o json | jq -ce --arg driver "$driver" '
  select(.metadata.deletionTimestamp==null and .provisioner==$driver and .parameters.clusterID=="rook-ceph") | .parameters')
storage_class_uid=$(kubectl get storageclass block-retain -o json |
  jq -er --arg driver "$driver" --argjson params "$storage_params" '
    select(.metadata.deletionTimestamp==null and .provisioner==$driver and .reclaimPolicy=="Retain" and .parameters==$params) |
    .metadata.uid | select(length>0)')
ceph_fsid=$(kubectl -n rook-ceph get cephcluster.ceph.rook.io rook-ceph -o jsonpath='{.status.ceph.fsid}')
[[ $ceph_fsid =~ ^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$ ]]

jq -n --arg name "$snapshot_class" --arg driver "$driver" --argjson parameters "$class_params" '
  {apiVersion:"snapshot.storage.k8s.io/v1",kind:"VolumeSnapshotClass",metadata:{name:$name},driver:$driver,
   deletionPolicy:"Retain",parameters:$parameters}' | kubectl create -f - >&2
snapshot_class_uid=$(uid_of volumesnapshotclass.snapshot.storage.k8s.io "$snapshot_class")
test -n "$snapshot_class_uid"
kubectl -n "$namespace" create -f - >&2 <<EOF
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata: {name: $snapshot}
spec:
  volumeSnapshotClassName: $snapshot_class
  source: {persistentVolumeClaimName: $source_claim}
EOF
wait_jsonpath 360 true -n "$namespace" get volumesnapshot.snapshot.storage.k8s.io "$snapshot" -o jsonpath='{.status.readyToUse}'
snapshot_obj=$(kubectl -n "$namespace" get volumesnapshot.snapshot.storage.k8s.io "$snapshot" -o json | jq -ce \
  --arg class "$snapshot_class" --arg source "$source_claim" '
    select(.metadata.deletionTimestamp==null and .spec.volumeSnapshotClassName==$class and .spec.source=={persistentVolumeClaimName:$source} and
      .status.readyToUse==true and .status.error==null and (.metadata.uid|length)>0 and (.status.boundVolumeSnapshotContentName|length)>0)')
snapshot_uid=$(jq -er '.metadata.uid' <<<"$snapshot_obj")
content=$(jq -er '.status.boundVolumeSnapshotContentName' <<<"$snapshot_obj")
content_uid=$(kubectl get volumesnapshotcontent.snapshot.storage.k8s.io "$content" -o json |
  jq -er --arg class "$snapshot_class" --arg driver "$driver" --arg handle "$source_handle" --arg snap "$snapshot" \
    --arg uid "$snapshot_uid" --arg ns "$namespace" '
      select(.metadata.deletionTimestamp==null and .spec.driver==$driver and .spec.deletionPolicy=="Retain" and
        .spec.volumeSnapshotClassName==$class and .spec.sourceVolumeMode=="Filesystem" and .spec.source=={volumeHandle:$handle} and
        (.spec.volumeSnapshotRef | .apiVersion=="snapshot.storage.k8s.io/v1" and .kind=="VolumeSnapshot" and
          .namespace==$ns and .name==$snap and .uid==$uid) and .status.readyToUse==true and .status.error==null and
        (.status.snapshotHandle|length)>0 and .status.restoreSize==10737418240) |
      .metadata.uid | select(length>0)')
frozen_source

for claim in "$reference_claim" "$target_claim"; do
  kubectl -n "$namespace" create -f - >&2 <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: $claim}
spec:
  accessModes: [ReadWriteOnce]
  volumeMode: Filesystem
  storageClassName: block-retain
  resources:
    requests: {storage: 10Gi}
  dataSource: {apiGroup: snapshot.storage.k8s.io, kind: VolumeSnapshot, name: $snapshot}
EOF
done

clone_binding() {
  local claim=$1
  kubectl -n "$namespace" get pvc "$claim" -o json | jq -e --arg snapshot "$snapshot" '
    (.metadata.ownerReferences // [])==[] and .spec.dataSource=={apiGroup:"snapshot.storage.k8s.io",kind:"VolumeSnapshot",name:$snapshot} and
    (.spec.dataSourceRef==null or .spec.dataSourceRef==.spec.dataSource)' >/dev/null || return 1
  disk_binding "$claim" block-retain | jq -ce 'select(.reclaim=="Retain")'
}

for claim in "$reference_claim" "$target_claim"; do
  wait_jsonpath 360 Bound -n "$namespace" get pvc "$claim" -o jsonpath='{.status.phase}'
done
reference=$(clone_binding "$reference_claim")
target=$(clone_binding "$target_claim")
jq -ne --argjson source "$source_binding" --argjson reference "$reference" --argjson target "$target" '
  [$source,$reference,$target] | (map(.claim_uid)|unique|length)==3 and (map(.volume)|unique|length)==3 and
    (map(.volume_uid)|unique|length)==3 and (map(.handle)|unique|length)==3' >/dev/null

temp_dir=$(mktemp -d "$proof_dir/.sqlite-copy.XXXXXX")
reader_name=
reader_uid=

remove_reader() {
  jq -nc --arg uid "$reader_uid" '{apiVersion:"v1",kind:"DeleteOptions",preconditions:{uid:$uid}}' |
    kubectl delete --raw "/api/v1/namespaces/$namespace/pods/$reader_name" -f - >/dev/null || kubectl_absent -n "$namespace" pod "$reader_name" || return 1
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
  rm -rf -- "$temp_dir"
  exit "$result"
}
trap cleanup EXIT

extract_clone() {
  local binding=$1 destination=$2 claim volume evidence archive
  claim=$(jq -er '.claim' <<<"$binding")
  volume=$(jq -er '.volume' <<<"$binding")
  expect_equal "$binding" "$(clone_binding "$claim")" 'approved clone binding before extraction' >&2
  unattached "$claim" "$volume"
  evidence=$(kubectl -n "$namespace" create -f - -o json <<EOF
apiVersion: v1
kind: Pod
metadata: {generateName: sqlite-copy-reader-}
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
EOF
  )
  reader_name=$(jq -er '.metadata.name | select(test("^sqlite-copy-reader-[a-z0-9]+$"))' <<<"$evidence")
  reader_uid=$(jq -er '.metadata.uid | select(length>0)' <<<"$evidence")
  kubectl -n "$namespace" wait --for=condition=Ready "pod/$reader_name" --timeout=180s >&2
  kubectl -n "$namespace" get pod "$reader_name" -o json |
    jq -e --arg uid "$reader_uid" --arg claim "$claim" --arg digest "$busybox_digest" '
      .metadata.uid==$uid and .metadata.deletionTimestamp==null and .spec.automountServiceAccountToken==false and
      .spec.volumes==[{name:"data",persistentVolumeClaim:{claimName:$claim,readOnly:true}}] and (.spec.containers|length)==1 and
      .spec.containers[0].volumeMounts==[{name:"data",mountPath:"/data",readOnly:true}] and
      .spec.containers[0].securityContext.capabilities=={drop:["ALL"],add:["DAC_READ_SEARCH"]} and
      any(.status.containerStatuses[]; .name=="reader" and .ready==true and (.imageID|endswith($digest)))' >/dev/null
  kubectl -n "$namespace" exec "$reader_name" -c reader -- awk '$5 == "/data" && $6 ~ /(^|,)ro(,|$)/ { count++ } END { exit count != 1 }' /proc/self/mountinfo
  kubectl -n "$namespace" exec "$reader_name" -c reader -- sh -ec 'special=$(find /data -xdev ! -type f ! -type d ! -type l -print); test -z "$special"'
  archive=$temp_dir/$destination.tar
  kubectl -n "$namespace" exec "$reader_name" -c reader -- tar -C /data -cf - . >"$archive"
  expect_equal "$reader_uid" "$(uid_of pod "$reader_name" -n "$namespace")" 'read-only extraction Pod UID' >&2
  remove_reader
  retry_until 240 unattached "$claim" "$volume"
  expect_equal "$binding" "$(clone_binding "$claim")" 'approved clone binding after extraction' >&2
  mkdir "$temp_dir/$destination"
  python3 - "$archive" "$temp_dir/$destination" <<'PY'
from pathlib import Path, PurePosixPath
import stat
import sys
import tarfile

archive, output = map(Path, sys.argv[1:])
try:
    with tarfile.open(archive, "r:") as stream:
        seen = set()
        for member in stream.getmembers():
            path = PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts or path in seen:
                raise ValueError
            seen.add(path)
            if not (member.isreg() or member.isdir() or member.issym() or member.islnk()):
                raise ValueError
        stream.extractall(output, filter="data")
    for name in (".", "pki"):
        directory = output / name
        if not stat.S_ISDIR(directory.lstat().st_mode):
            raise ValueError
        directory.chmod(0o700)
    for name in ("state.db", "state.db-wal", "state.db-shm", "pki/ca.key", "pki/front-proxy-ca.key", "pki/sa.key", "pki/etcd-ca.key"):
        path = output / name
        try:
            evidence = path.lstat()
        except FileNotFoundError:
            continue
        if not stat.S_ISREG(evidence.st_mode) or evidence.st_nlink != 1:
            raise ValueError
        path.chmod(0o600)
except (OSError, tarfile.TarError, ValueError, TypeError):
    raise SystemExit("The approved clone could not be extracted safely.")
PY
  rm -- "$archive"
}

extract_clone "$reference" source
extract_clone "$target" retained
datastore=$("$migrate" verify-legacy-sqlite-datastore "$inventory" "$inventory_sha" "$cluster_uid" "$temp_dir/source" "$temp_dir/retained")
datastore_sha=$(jq -er --arg expected "$inventory_sha" '
  select(.version==1 and .inventory_sha256==$expected) | .approval_sha256 | select(test("^[0-9a-f]{64}$"))' <<<"$datastore")
pki=$("$migrate" verify-legacy-sqlite-pki "$temp_dir/source/pki" "$temp_dir/retained/pki")
pki_sha=$(jq -er 'select(.version==1 and (.matched_signers==3 or .matched_signers==4)) | .approval_sha256 | select(test("^[0-9a-f]{64}$"))' <<<"$pki")
frozen_source
expect_equal "$source_binding" "$(disk_binding "$source_claim" block)" 'frozen source binding' >&2
expect_equal "$reference" "$(clone_binding "$reference_claim")" 'retained reference binding' >&2
expect_equal "$target" "$(clone_binding "$target_claim")" 'retained target binding' >&2
kubectl -n "$namespace" get volumesnapshot.snapshot.storage.k8s.io "$snapshot" -o json | jq -e --arg uid "$snapshot_uid" --arg content "$content" '
  .metadata.uid==$uid and .metadata.deletionTimestamp==null and .status.readyToUse==true and .status.error==null and
  .status.boundVolumeSnapshotContentName==$content' >/dev/null
kubectl get volumesnapshotcontent.snapshot.storage.k8s.io "$content" -o json | jq -e --arg uid "$content_uid" --arg snap "$snapshot_uid" '
  .metadata.uid==$uid and .metadata.deletionTimestamp==null and .spec.volumeSnapshotRef.uid==$snap and
  .spec.deletionPolicy=="Retain" and .status.readyToUse==true and .status.error==null' >/dev/null

jq -n --arg freeze "$freeze_sha" --arg snapshot "$snapshot" --arg snapshot_uid "$snapshot_uid" --arg class "$snapshot_class" \
  --arg class_uid "$snapshot_class_uid" --arg content "$content" --arg content_uid "$content_uid" --arg storage_uid "$storage_class_uid" \
  --argjson source "$source_binding" --argjson reference "$reference" --argjson target "$target" --arg fsid "$ceph_fsid" \
  --arg datastore "$datastore_sha" --arg pki "$pki_sha" '{version:1, scope:"synthetic-shared-snapshot", frozen_receipt_sha256:$freeze,
    snapshot:$snapshot,snapshot_uid:$snapshot_uid,snapshot_class:$class,snapshot_class_uid:$class_uid,content:$content,content_uid:$content_uid,
    storage_class_uid:$storage_uid, source_volume_uid:$source.volume_uid, ceph_fsid:$fsid,
    reference_claim:$reference.claim,reference_claim_uid:$reference.claim_uid,reference_volume:$reference.volume,reference_volume_uid:$reference.volume_uid,
    target_claim:$target.claim,target_claim_uid:$target.claim_uid,target_volume:$target.volume,target_volume_uid:$target.volume_uid,
    datastore_sha256:$datastore,pki_sha256:$pki}' >"$temp_dir/retained-copy.json"
mv -T -- "$temp_dir/source" "$proof_dir/source"
mv -T -- "$temp_dir/retained" "$proof_dir/retained"
mv -T -- "$temp_dir/retained-copy.json" "$receipt"
printf '%s\n' 'The two extracts share one synthetic snapshot. The independent regional source backup gate remains in force.' >&2
file_hash "$receipt"
