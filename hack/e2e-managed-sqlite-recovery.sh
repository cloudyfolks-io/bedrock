#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ ${E2E_MANAGED_SQLITE_RECOVERY:-} != 1 || ! ${2:-} =~ ^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$ ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_SQLITE_RECOVERY=1 bash hack/e2e-managed-sqlite-recovery.sh freeze|restore <guest-uid> <sqlite-source.json> <sqlite-worker.json> <proof-dir> [retained-copy-sha256]' >&2
  exit 2
fi
case ${1:-} in
  freeze) [[ $# == 5 ]] ;;
  restore) [[ $# == 6 && ${6:-} =~ ^[0-9a-f]{64}$ ]] ;;
  *) exit 2 ;;
esac

export KUBECONFIG=/var/lib/k0s/pki/admin.conf
[[ $(uname -s) == Linux && -s /var/lib/bedrock/e2e-admin-password && -f $KUBECONFIG && ! -L $KUBECONFIG ]]
stage=$1
guest_uid=$2
source_receipt=$3
worker_receipt=$4
proof_dir=$5
copy_digest=${6:-}
migrate=${BEDROCK_MIGRATE:?Set BEDROCK_MIGRATE to the verified Cluster API migration binary.}
[[ $migrate == /* && -f $migrate && ! -L $migrate && -x $migrate ]]
[[ $proof_dir == /* && -d $proof_dir && $(realpath -e "$proof_dir") == "$proof_dir" && -O $proof_dir && $(stat -c '%a' "$proof_dir") == 700 ]]
command -v timeout >/dev/null

source "$(dirname "${BASH_SOURCE[0]}")/e2e/lib.sh"

namespace=bedrock-e2e-sqlite-source
source_cluster=sqlite-source
target_cluster=sqlite-restored
service=sqlite-endpoint
worker=sqlite-worker
source_set=kmc-$source_cluster
target_set=kmc-$target_cluster
source_pod=$source_set-0
target_pod=$target_set-0
source_claim=$source_set-$source_pod
target_claim=$target_set-$target_pod
worker_claim=$worker-root
marker=bedrock-sqlite-continuity
workload=sqlite-worker-state
driver=rook-ceph.rbd.csi.ceph.com
pod_fence=bedrock-e2e-sqlite-source-pods
scale_fence=bedrock-e2e-sqlite-source-scale
fence_message='the isolated SQLite source is fenced'
inventory=$proof_dir/frozen-inventory.json
freeze_receipt=$proof_dir/frozen.json
retained_receipt=$proof_dir/retained-copy.json
old_config=$proof_dir/original.kubeconfig

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

live_field() {
  jq -er "$1 | select(type == \"string\" and length > 0)" "$2"
}

private_file "$source_receipt"
private_file "$worker_receipt"
expect_equal "$guest_uid" "$(uid_of namespace kube-system)" 'isolated development guest UID' >&2
source_uid=$(live_field .cluster_uid "$source_receipt")
namespace_uid=$(live_field .namespace_uid "$source_receipt")
source_claim_uid=$(live_field .claim_uid "$source_receipt")
source_volume=$(live_field .volume "$source_receipt")
source_volume_uid=$(live_field .volume_uid "$source_receipt")
worker_vm_uid=$(live_field .vm_uid "$worker_receipt")
worker_claim_uid=$(live_field .root_claim_uid "$worker_receipt")
worker_volume=$(live_field .root_volume "$worker_receipt")
worker_volume_uid=$(live_field .root_volume_uid "$worker_receipt")
old_node_uid=$(live_field .node_uid "$worker_receipt")
old_vmi_uid=$(live_field .vmi_uid "$worker_receipt")
marker_uid=$(live_field .marker_uid "$source_receipt")
workload_uid=$(live_field .workload_pod_uid "$worker_receipt")
workload_sha=$(live_field .workload_data_sha256 "$worker_receipt")
mac=$(live_field .mac_address "$worker_receipt")
firmware=$(live_field .firmware_uuid "$worker_receipt")
source_sha=$(file_hash "$source_receipt")
worker_sha=$(file_hash "$worker_receipt")
address=$(kubectl -n "$namespace" get service "$service" -o jsonpath='{.spec.clusterIP}')
[[ $address =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ && $workload_sha =~ ^[0-9a-f]{64}$ ]]
endpoint=https://$address:6443
service_uid=$(uid_of service "$service" -n "$namespace")
test -n "$service_uid"

jq -e --arg ns "$namespace" --arg cluster "$source_cluster" --arg set "$source_set" --arg service "$service" \
  --arg claim "$source_claim" --arg marker "$marker" --arg api "$endpoint" --arg tunnel "$address:8132" '
  .namespace==$ns and .cluster==$cluster and .statefulset==$set and .service==$service and .claim==$claim and
  .marker==$marker and .api_endpoint==$api and .konnectivity_endpoint==$tunnel' "$source_receipt" >/dev/null
jq -e --arg uid "$source_uid" --arg worker "$worker" --arg claim "$worker_claim" '
  .source_cluster_uid==$uid and .worker==$worker and .root_claim==$claim and .disk_serial=="sqlite-worker-root"' "$worker_receipt" >/dev/null
expect_equal "$namespace_uid" "$(uid_of namespace "$namespace")" 'SQLite lab namespace UID' >&2
kubectl -n "$namespace" get rolebindings.rbac.authorization.k8s.io -o json | jq -e '.items == []' >/dev/null

control_plane() {
  kubectl -n "$namespace" get cluster.k0smotron.io "$source_cluster" -o json | jq -e --arg uid "$source_uid" --arg address "$address" '
    select(.metadata.uid==$uid and .metadata.deletionTimestamp==null and .spec.replicas==1 and .spec.externalAddress==$address and
      .spec.version=="v1.36.3+k0s.1" and .spec.storage.type=="kine" and .spec.storage.kine.dataSourceURL=="sqlite:///var/lib/k0s/state.db" and
      .spec.service.type=="ClusterIP" and .spec.service.apiPort==6443 and .spec.service.konnectivityPort==8132 and
      (.spec.persistence.autoDeletePVCs // false)==false and (.spec.patches // [])==[])'
}

bound_volume() {
  local claim=$1 claim_uid=$2 volume=$3 volume_uid=$4 mode=$5 storage=$6
  kubectl -n "$namespace" get pvc "$claim" -o json | jq -e --arg uid "$claim_uid" --arg volume "$volume" --arg mode "$mode" --arg storage "$storage" '
    .metadata.uid==$uid and .metadata.deletionTimestamp==null and .status.phase=="Bound" and .spec.volumeName==$volume and
    .spec.storageClassName==$storage and .spec.volumeMode==$mode and .spec.accessModes==["ReadWriteOnce"]' >/dev/null || return 1
  kubectl get pv "$volume" -o json | jq -e --arg uid "$volume_uid" --arg claim "$claim" --arg cuid "$claim_uid" \
    --arg ns "$namespace" --arg driver "$driver" --arg mode "$mode" --arg storage "$storage" '
    select(.metadata.uid==$uid and .metadata.deletionTimestamp==null and .status.phase=="Bound" and .spec.volumeMode==$mode and
      .spec.storageClassName==$storage and .spec.csi.driver==$driver and (.spec.csi.readOnly // false)==false and
      (.spec.claimRef | .namespace==$ns and .name==$claim and .uid==$cuid)) |
    .spec.csi.volumeHandle | select(length > 0)'
}

check_service() {
  kubectl -n "$namespace" get service "$service" -o json | jq -e --arg uid "$service_uid" --arg address "$address" --arg pod "$1" '
    .metadata.uid==$uid and .metadata.deletionTimestamp==null and .spec.clusterIP==$address and .spec.type=="ClusterIP" and
    (.spec.externalIPs // [])==[] and .spec.selector=={"statefulset.kubernetes.io/pod-name":$pod} and
    ([.spec.ports[] | [.port,.targetPort,.protocol]] | sort)==[[6443,6443,"TCP"],[8132,8132,"TCP"]]' >/dev/null
}

start_child_client() {
  local controller node image client
  probe_backend=$1
  controller=$(kubectl --request-timeout=10s -n "$namespace" get pod "$probe_backend" -o json)
  node=$(jq -er '.spec.nodeName | select(length>0)' <<<"$controller")
  image=$(jq -er '.spec.containers[] | select(.name=="controller") | .image' <<<"$controller")
  client=$(jq -n --arg node "$node" --arg image "$image" '
    {apiVersion:"v1",kind:"Pod",metadata:{generateName:"sqlite-recovery-client-"},spec:{nodeName:$node,automountServiceAccountToken:false,
      restartPolicy:"Never",activeDeadlineSeconds:3600,containers:[{name:"client",image:$image,imagePullPolicy:"Never",
        command:["/bin/sh","-c","exec sleep 3600"],securityContext:{allowPrivilegeEscalation:false,capabilities:{drop:["ALL"]}}}]}}' |
    kubectl -n "$namespace" create -f - -o json)
  probe_name=$(jq -er '.metadata.name | select(test("^sqlite-recovery-client-[a-z0-9]+$"))' <<<"$client")
  probe_uid=$(jq -er '.metadata.uid | select(length>0)' <<<"$client")
  kubectl -n "$namespace" wait --for=condition=Ready "pod/$probe_name" --timeout=90s >&2
}

remove_child_client() {
  if [[ -z $probe_name ]]; then
    return 0
  fi
  jq -nc --arg uid "$probe_uid" '{apiVersion:"v1",kind:"DeleteOptions",preconditions:{uid:$uid}}' |
    kubectl --request-timeout=10s delete --raw "/api/v1/namespaces/$namespace/pods/$probe_name" -f - >/dev/null || return 1
  probe_name=
}

child_kubectl() {
  local backend=$1
  shift
  [[ -n $probe_name && $backend == "$probe_backend" ]] || return 1
  if ! timeout 25s kubectl --request-timeout=20s -n "$namespace" exec -i "$probe_name" -c client -- \
    k0s kubectl --kubeconfig=/dev/stdin --request-timeout=15s "$@" <"$old_config" 2>/dev/null; then
    printf '%s\n' 'The SQLite client request through the private Service failed.' >&2
    return 1
  fi
}

check_child() {
  local pod=$1 hashed
  expect_equal "$marker_uid" "$(child_kubectl "$pod" -n default get configmap "$marker" -o jsonpath='{.metadata.uid}')" 'saved SQLite marker UID' >&2
  expect_equal before-handoff "$(child_kubectl "$pod" -n default get configmap "$marker" -o jsonpath='{.data.state}')" 'saved SQLite marker data' >&2
  expect_equal "$old_node_uid" "$(child_kubectl "$pod" get node "$worker" -o jsonpath='{.metadata.uid}')" 'existing worker Node UID' >&2
  expect_equal "$workload_uid" "$(child_kubectl "$pod" -n default get pod "$workload" -o jsonpath='{.metadata.uid}')" 'existing workload UID' >&2
  expect_equal sqlite-worker-data-before-handoff "$(child_kubectl "$pod" -n default logs "$workload")" 'workload logs through Konnectivity' >&2
  hashed=$(child_kubectl "$pod" -n default exec "$workload" -- sha256sum /data/marker)
  expect_equal "$workload_sha" "${hashed%% *}" 'persistent workload data through Konnectivity' >&2
}

hold_workload() {
  local patch
  patch=$(child_kubectl "$source_pod" -n default get pod "$workload" -o json | jq -ce --arg uid "$workload_uid" '
    select(.metadata.uid==$uid and .metadata.deletionTimestamp==null and .spec.nodeName=="sqlite-worker") |
    . as $pod | ["node.kubernetes.io/not-ready","node.kubernetes.io/unreachable"] as $keys |
    (reduce $keys[] as $key ((.spec.tolerations // []);
      if any(.[]; .key==$key and .effect=="NoExecute" and .operator=="Exists")
      then map(if .key==$key and .effect=="NoExecute" and .operator=="Exists" then del(.tolerationSeconds) else . end)
      else . + [{key:$key,effect:"NoExecute",operator:"Exists"}] end)) as $tolerations |
    [{op:"test",path:"/metadata/uid",value:$uid},{op:"test",path:"/metadata/resourceVersion",value:$pod.metadata.resourceVersion},
     {op:"add",path:"/spec/tolerations",value:$tolerations}]')
  child_kubectl "$source_pod" -n default patch pod "$workload" --type=json -p "$patch" >&2
  child_kubectl "$source_pod" -n default get pod "$workload" -o json | jq -e --arg uid "$workload_uid" '
    .metadata.uid==$uid and .metadata.deletionTimestamp==null and
    ([.spec.tolerations[] | select(.effect=="NoExecute" and .operator=="Exists" and .tolerationSeconds==null) | .key] as $keys |
      ($keys|index("node.kubernetes.io/not-ready"))!=null and ($keys|index("node.kubernetes.io/unreachable"))!=null)' >/dev/null
}

no_consumers() {
  kubectl -n "$namespace" get pods -o json | jq -e --arg claim "$1" '
    all(.items[]; all(.spec.volumes[]?; (.persistentVolumeClaim.claimName // "") != $claim))' >/dev/null &&
  kubectl get volumeattachments.storage.k8s.io -o json | jq -e --arg volume "$2" '
    all(.items[]; (.spec.source.persistentVolumeName // "") != $volume)' >/dev/null
}

check_worker() {
  kubectl -n "$namespace" get virtualmachine.kubevirt.io "$worker" -o json | jq -e --arg uid "$worker_vm_uid" \
    --arg mac "$mac" --arg firmware "$firmware" --arg claim "$worker_claim" '
    select(.metadata.uid==$uid and .metadata.deletionTimestamp==null and .spec.template.spec.hostname=="sqlite-worker" and
      .spec.template.spec.domain.firmware.uuid==$firmware and
      any(.spec.template.spec.domain.devices.interfaces[]; .name=="default" and .macAddress==$mac and has("masquerade")) and
      any(.spec.template.spec.domain.devices.disks[]; .name=="root" and .serial=="sqlite-worker-root") and
      any(.spec.template.spec.volumes[]; .name=="root" and .persistentVolumeClaim.claimName==$claim))'
}

create_fences() {
  kubectl create -f - >&2 <<EOF
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: {name: $pod_fence}
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - {apiGroups: [""], apiVersions: [v1], operations: [CREATE], resources: [pods]}
  matchConditions:
    - {name: sqlite-lab, expression: "request.namespace == '$namespace'"}
  validations:
    - expression: "!object.metadata.name.startsWith('$source_set-') && (!has(object.spec.volumes) || object.spec.volumes.all(v, !has(v.persistentVolumeClaim) || v.persistentVolumeClaim.claimName != '$source_claim'))"
      message: $fence_message
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: {name: $pod_fence}
spec: {policyName: $pod_fence, validationActions: [Deny]}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: {name: $scale_fence}
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - {apiGroups: [apps], apiVersions: [v1], operations: [CREATE, UPDATE], resources: [statefulsets, statefulsets/scale]}
  matchConditions:
    - {name: sqlite-lab, expression: "request.namespace == '$namespace' && request.name == '$source_set'"}
  validations:
    - expression: "request.operation == 'UPDATE' && has(object.spec.replicas) && object.spec.replicas == 0"
      message: $fence_message
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: {name: $scale_fence}
spec: {policyName: $scale_fence, validationActions: [Deny]}
EOF
}

source_fenced() {
  local denial
  if denial=$(kubectl -n "$namespace" create --dry-run=server -f - 2>&1 <<EOF
apiVersion: v1
kind: Pod
metadata: {name: sqlite-fence-proof}
spec:
  containers: [{name: proof, image: 'quay.io/k0sproject/k0s:v1.36.3-k0s.1'}]
  volumes:
    - {name: source, persistentVolumeClaim: {claimName: $source_claim}}
EOF
  ); then
    return 1
  fi
  [[ $denial == *"$fence_message"* && $denial == *"$pod_fence"* ]] || return 1
  if denial=$(kubectl -n "$namespace" patch statefulset "$source_set" --type=merge --dry-run=server -p '{"spec":{"replicas":1}}' 2>&1); then
    return 1
  fi
  [[ $denial == *"$fence_message"* && $denial == *"$scale_fence"* ]] || return 1
  if denial=$(kubectl -n "$namespace" scale statefulset "$source_set" --dry-run=server --replicas=1 2>&1); then
    return 1
  fi
  [[ $denial == *"$fence_message"* && $denial == *"$scale_fence"* ]]
}

source_stopped() {
  kubectl -n "$namespace" get statefulset "$source_set" -o json | jq -e --arg uid "$source_uid" '
    .metadata.deletionTimestamp==null and .spec.replicas==0 and .status.observedGeneration==.metadata.generation and
    (.status.replicas // 0)==0 and (.status.readyReplicas // 0)==0 and (.status.currentReplicas // 0)==0 and
    (.status.updatedReplicas // 0)==0 and (.status.availableReplicas // 0)==0 and
    any(.metadata.ownerReferences[]; .kind=="Cluster" and .uid==$uid and .controller==true)' >/dev/null &&
  kubectl_absent -n "$namespace" pod "$source_pod" && no_consumers "$source_claim" "$source_volume"
}

source_signer_refs() {
  control_plane | jq -ce '
    .spec.certificateRefs | sort_by(.type) |
    select((map(.type)==["ca","proxy","sa"] or map(.type)==["ca","etcd","proxy","sa"]) and
      all(.[]; .name | test("^[a-z0-9][a-z0-9.-]*$")) and (map(.name)|unique|length)==length)'
}

snapshot_inventory() {
  kubectl get --raw /apis/k0smotron.io/v1beta2/clusters >"$temp_dir/clusters"
  kubectl get --raw /apis/apps/v1/statefulsets >"$temp_dir/statefulsets"
  kubectl get --raw /api/v1/persistentvolumeclaims >"$temp_dir/claims"
  kubectl get --raw /api/v1/persistentvolumes >"$temp_dir/volumes"
  kubectl get --raw /api/v1/pods >"$temp_dir/pods"
  kubectl get --raw /apis/storage.k8s.io/v1/volumeattachments >"$temp_dir/attachments"
  kubectl get --raw "/api/v1/namespaces/$namespace/configmaps" >"$temp_dir/configmaps"
  jq -n --slurpfile clusters "$temp_dir/clusters" --slurpfile sets "$temp_dir/statefulsets" --slurpfile claims "$temp_dir/claims" \
    --slurpfile volumes "$temp_dir/volumes" --slurpfile pods "$temp_dir/pods" --slurpfile attachments "$temp_dir/attachments" \
    --slurpfile configs "$temp_dir/configmaps" '{version:1, clusters:$clusters[0], stateful_sets:$sets[0], persistent_volume_claims:$claims[0],
      persistent_volumes:$volumes[0], pods:$pods[0], volume_attachments:$attachments[0], config_maps:$configs[0]}' >"$inventory"
}

freeze() {
  local signer_refs lease
  [[ ! -e $freeze_receipt && ! -e $inventory && ! -e $old_config ]]
  check_service "$source_pod"
  control_plane >/dev/null
  bound_volume "$source_claim" "$source_claim_uid" "$source_volume" "$source_volume_uid" Filesystem block >/dev/null
  bound_volume "$worker_claim" "$worker_claim_uid" "$worker_volume" "$worker_volume_uid" Block block >/dev/null
  check_worker | jq -e '.spec.runStrategy=="Always"' >/dev/null
  expect_equal "$old_vmi_uid" "$(uid_of virtualmachineinstance.kubevirt.io "$worker" -n "$namespace")" 'existing worker VMI UID' >&2
  signer_refs=$(source_signer_refs)
  kubectl -n "$namespace" get secret "$source_cluster-kubeconfig" -o json | jq -er --arg uid "$source_uid" '
    select(any(.metadata.ownerReferences[]; .kind=="Cluster" and .uid==$uid and .controller==true)) | .data.value | select(length>0)' |
    base64 -d >"$old_config"
  expect_equal "$endpoint" "$(kubectl --kubeconfig "$old_config" config view --minify -o jsonpath='{.clusters[0].cluster.server}')" 'original kubeconfig endpoint' >&2
  start_child_client "$source_pod"
  check_child "$source_pod"
  hold_workload
  lease=$(child_kubectl "$source_pod" -n kube-node-lease get lease "$worker" -o jsonpath='{.spec.renewTime}')
  test -n "$lease"
  remove_child_client

  create_fences
  retry_until 90 source_fenced
  check_worker | jq -c '[{op:"test",path:"/metadata/uid",value:.metadata.uid},{op:"test",path:"/metadata/resourceVersion",value:.metadata.resourceVersion},
    {op:"test",path:"/spec/runStrategy",value:"Always"},{op:"replace",path:"/spec/runStrategy",value:"Halted"}]' >"$temp_dir/halt"
  kubectl -n "$namespace" patch virtualmachine.kubevirt.io "$worker" --type=json --patch-file "$temp_dir/halt" >&2
  wait_gone 360 -n "$namespace" virtualmachineinstance.kubevirt.io "$worker"
  retry_until 240 no_consumers "$worker_claim" "$worker_volume"
  kubectl -n "$namespace" get statefulset "$source_set" -o json | jq -ce --arg uid "$source_uid" '
    select(.metadata.deletionTimestamp==null and any(.metadata.ownerReferences[]; .kind=="Cluster" and .uid==$uid and .controller==true)) |
    [{op:"test",path:"/metadata/uid",value:.metadata.uid},{op:"test",path:"/metadata/resourceVersion",value:.metadata.resourceVersion},
     {op:"replace",path:"/spec/replicas",value:0}]' >"$temp_dir/stop"
  kubectl -n "$namespace" patch statefulset "$source_set" --type=json --patch-file "$temp_dir/stop" >&2
  retry_until 360 source_stopped
  source_fenced
  snapshot_inventory
  "$migrate" verify-frozen-legacy-storage "$inventory" "$source_uid" | jq -e --arg digest "$(file_hash "$inventory")" '
    .version==1 and .frozen_sqlite_control_planes==1 and .inventory_sha256==$digest' >/dev/null
  jq -n --arg guest "$guest_uid" --arg ns "$namespace_uid" --arg source "$source_uid" --arg service "$service_uid" \
    --arg ss "$source_sha" --arg ws "$worker_sha" --arg inventory "$(file_hash "$inventory")" --arg config "$(file_hash "$old_config")" \
    --arg lease "$lease" --argjson refs "$signer_refs" '{version:1, guest_uid:$guest, namespace_uid:$ns, source_cluster_uid:$source,
      service_uid:$service, source_receipt_sha256:$ss, worker_receipt_sha256:$ws, inventory_sha256:$inventory, kubeconfig_sha256:$config,
      old_lease_renew_time:$lease, certificate_refs:$refs}' >"$freeze_receipt"
  cat -- "$freeze_receipt"
}

approved_freeze() {
  private_file "$freeze_receipt"
  private_file "$inventory"
  private_file "$old_config"
  jq -e --arg guest "$guest_uid" --arg ns "$namespace_uid" --arg source "$source_uid" --arg service "$service_uid" \
    --arg ss "$source_sha" --arg ws "$worker_sha" --arg inventory "$(file_hash "$inventory")" --arg config "$(file_hash "$old_config")" '
      .version==1 and .guest_uid==$guest and .namespace_uid==$ns and .source_cluster_uid==$source and .service_uid==$service and
      .source_receipt_sha256==$ss and .worker_receipt_sha256==$ws and .inventory_sha256==$inventory and .kubeconfig_sha256==$config' "$freeze_receipt" >/dev/null
  private_file "$retained_receipt"
  expect_equal "$copy_digest" "$(file_hash "$retained_receipt")" 'approved retained copy receipt digest' >&2
  jq -e --arg freeze "$(file_hash "$freeze_receipt")" --arg claim "$target_claim" '
    .version==1 and .frozen_receipt_sha256==$freeze and .target_claim==$claim and .snapshot=="sqlite-source-frozen" and
    ([.datastore_sha256,.pki_sha256] | all(.[]; type=="string" and test("^[0-9a-f]{64}$")))' "$retained_receipt" >/dev/null
}

check_retained_copy() {
  local source_handle copy_handle snapshot_uid content content_uid class class_uid copy_claim_uid copy_volume copy_volume_uid params
  source_handle=$(bound_volume "$source_claim" "$source_claim_uid" "$source_volume" "$source_volume_uid" Filesystem block) || return 1
  snapshot_uid=$(live_field .snapshot_uid "$retained_receipt") || return 1
  content=$(live_field .content "$retained_receipt") || return 1
  content_uid=$(live_field .content_uid "$retained_receipt") || return 1
  class=$(live_field .snapshot_class "$retained_receipt") || return 1
  class_uid=$(live_field .snapshot_class_uid "$retained_receipt") || return 1
  copy_claim_uid=$(live_field .target_claim_uid "$retained_receipt") || return 1
  copy_volume=$(live_field .target_volume "$retained_receipt") || return 1
  copy_volume_uid=$(live_field .target_volume_uid "$retained_receipt") || return 1
  [[ $copy_claim_uid != "$source_claim_uid" && $copy_volume_uid != "$source_volume_uid" && $copy_volume != "$source_volume" ]] || return 1
  kubectl get volumesnapshotclass.snapshot.storage.k8s.io "$class" -o json |
    jq -e --arg uid "$class_uid" --arg driver "$driver" '.metadata.uid==$uid and .metadata.deletionTimestamp==null and .deletionPolicy=="Retain" and .driver==$driver' >/dev/null || return 1
  params=$(kubectl get storageclass block -o json | jq -ce '.parameters') || return 1
  kubectl get storageclass block-retain -o json |
    jq -e --arg uid "$(live_field .storage_class_uid "$retained_receipt")" --arg driver "$driver" --argjson params "$params" '
      .metadata.uid==$uid and .metadata.deletionTimestamp==null and .reclaimPolicy=="Retain" and .provisioner==$driver and
      .parameters==$params' >/dev/null || return 1
  kubectl -n "$namespace" get volumesnapshot.snapshot.storage.k8s.io sqlite-source-frozen -o json |
    jq -e --arg uid "$snapshot_uid" --arg class "$class" --arg source "$source_claim" --arg content "$content" '
      .metadata.uid==$uid and .metadata.deletionTimestamp==null and .spec.volumeSnapshotClassName==$class and
      .spec.source=={persistentVolumeClaimName:$source} and .status.readyToUse==true and .status.error==null and
      .status.boundVolumeSnapshotContentName==$content' >/dev/null || return 1
  kubectl get volumesnapshotcontent.snapshot.storage.k8s.io "$content" -o json |
    jq -e --arg uid "$content_uid" --arg driver "$driver" --arg class "$class" --arg ns "$namespace" \
      --arg snap_uid "$snapshot_uid" --arg handle "$source_handle" '
      .metadata.uid==$uid and .metadata.deletionTimestamp==null and .spec.deletionPolicy=="Retain" and .spec.driver==$driver and
      .spec.volumeSnapshotClassName==$class and .spec.sourceVolumeMode=="Filesystem" and .spec.source=={volumeHandle:$handle} and
      (.spec.volumeSnapshotRef | .apiVersion=="snapshot.storage.k8s.io/v1" and .kind=="VolumeSnapshot" and .namespace==$ns and
        .name=="sqlite-source-frozen" and .uid==$snap_uid) and
      .status.readyToUse==true and .status.error==null and (.status.snapshotHandle|length)>0 and .status.restoreSize==10737418240' >/dev/null || return 1
  copy_handle=$(bound_volume "$target_claim" "$copy_claim_uid" "$copy_volume" "$copy_volume_uid" Filesystem block-retain) || return 1
  [[ $copy_handle != "$source_handle" ]] || return 1
  kubectl get pv "$copy_volume" -o json | jq -e '.spec.persistentVolumeReclaimPolicy=="Retain"' >/dev/null || return 1
  kubectl -n "$namespace" get pvc "$target_claim" -o json | jq -e '
    .spec.resources.requests.storage=="10Gi" and
    .spec.dataSource=={apiGroup:"snapshot.storage.k8s.io",kind:"VolumeSnapshot",name:"sqlite-source-frozen"} and
    (.spec.dataSourceRef==null or .spec.dataSourceRef==.spec.dataSource)' >/dev/null
}

check_retained_data() {
  local frozen_hash datastore pki expected_refs role name cert key
  frozen_hash=$(live_field .inventory_sha256 "$freeze_receipt")
  datastore=$("$migrate" verify-legacy-sqlite-datastore "$inventory" "$frozen_hash" "$source_uid" "$proof_dir/source" "$proof_dir/retained")
  jq -e --arg digest "$(live_field .datastore_sha256 "$retained_receipt")" --arg frozen "$frozen_hash" '
    .version==1 and .approval_sha256==$digest and .inventory_sha256==$frozen' <<<"$datastore" >/dev/null
  pki=$("$migrate" verify-legacy-sqlite-pki "$proof_dir/source/pki" "$proof_dir/retained/pki")
  expected_refs=$(source_signer_refs)
  jq -e --argjson refs "$expected_refs" '.certificate_refs == $refs' "$freeze_receipt" >/dev/null
  jq -e --arg digest "$(live_field .pki_sha256 "$retained_receipt")" --argjson refs "$expected_refs" '
    .version==1 and .approval_sha256==$digest and .matched_signers==($refs|length)' <<<"$pki" >/dev/null
  while IFS=$'\t' read -r role name; do
    case $role in
      ca) cert=ca.crt; key=ca.key ;;
      proxy) cert=front-proxy-ca.crt; key=front-proxy-ca.key ;;
      sa) cert=sa.pub; key=sa.key ;;
      etcd) cert=etcd-ca.crt; key=etcd-ca.key ;;
      *) return 1 ;;
    esac
    kubectl -n "$namespace" get secret "$name" -o json | jq -er --arg uid "$source_uid" '
      select(.metadata.deletionTimestamp==null and any(.metadata.ownerReferences[];
        .kind=="Cluster" and .uid==$uid and .controller==true)) | .data["tls.crt"] | select(length>0)' |
      base64 -d | cmp -s - "$proof_dir/source/pki/$cert"
    kubectl -n "$namespace" get secret "$name" -o json | jq -er --arg uid "$source_uid" '
      select(.metadata.deletionTimestamp==null and any(.metadata.ownerReferences[];
        .kind=="Cluster" and .uid==$uid and .controller==true)) | .data["tls.key"] | select(length>0)' |
      base64 -d | cmp -s - "$proof_dir/source/pki/$key"
  done < <(jq -r '.[] | [.type,.name] | @tsv' <<<"$expected_refs")
}

restore_control_plane() {
  kubectl -n "$namespace" create -f - >&2 <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: sqlite-restored-ingress}
spec:
  podSelector:
    matchLabels: {statefulset.kubernetes.io/pod-name: $target_pod}
  policyTypes: [Ingress]
  ingress:
    - from: [{podSelector: {}}]
      ports: [{port: 6443, protocol: TCP}, {port: 8132, protocol: TCP}]
EOF
  control_plane | jq -e --arg target "$target_cluster" --arg set "$target_set" '
    {apiVersion:"k0smotron.io/v1beta2",kind:"Cluster",metadata:{name:$target},spec:(.spec | {
      version,replicas,externalAddress,k0sConfig,certificateRefs,service,storage,resources,persistence
    })} |
    .spec.persistence.persistentVolumeClaim.metadata.name=$set |
    .spec.persistence.autoDeletePVCs=false |
    .spec.persistence.persistentVolumeClaim.spec.storageClassName="block-retain" |
    .spec.persistence.persistentVolumeClaim.spec.dataSource={apiGroup:"snapshot.storage.k8s.io",kind:"VolumeSnapshot",name:"sqlite-source-frozen"}' |
    kubectl -n "$namespace" create -f - >&2
}

move_service() {
  local old
  old=$(kubectl -n "$namespace" get service "$service" -o json | jq -er '.metadata.resourceVersion')
  jq -nc --arg uid "$service_uid" --arg rv "$old" --arg ip "$address" --arg old "$source_pod" --arg target "$target_pod" '[
    {op:"test",path:"/metadata/uid",value:$uid},{op:"test",path:"/metadata/resourceVersion",value:$rv},
    {op:"test",path:"/spec/clusterIP",value:$ip},{op:"test",path:"/spec/selector",value:{"statefulset.kubernetes.io/pod-name":$old}},
    {op:"replace",path:"/spec/selector",value:{"statefulset.kubernetes.io/pod-name":$target}}]' >"$temp_dir/route"
  kubectl -n "$namespace" patch service "$service" --type=json --patch-file "$temp_dir/route" >&2
  check_service "$target_pod"
}

target_only_endpoints() {
  kubectl -n "$namespace" get endpointslices.discovery.k8s.io -l "kubernetes.io/service-name=$service" -o json |
    jq -e --arg service "$service_uid" --arg ns "$namespace" --arg pod "$target_pod" --arg uid "$1" '
      all(.items[]; any(.metadata.ownerReferences[]; .kind=="Service" and .uid==$service)) and
      ([.items[].endpoints[]] | length==1 and all(.[]; .conditions.ready==true and
        (.targetRef | .namespace==$ns and .kind=="Pod" and .name==$pod and .uid==$uid)))' >/dev/null
}

resume_worker() {
  check_worker | jq -ce '
    select(.spec.runStrategy=="Halted" and [.spec.template.spec.volumes[].name]==["root","bootstrap"] and
      [.spec.template.spec.domain.devices.disks[].name]==["root","bootstrap"] and
      .spec.template.spec.volumes[1].cloudInitNoCloud.secretRef.name=="sqlite-worker-bootstrap") |
    [{op:"test",path:"/metadata/uid",value:.metadata.uid},{op:"test",path:"/metadata/resourceVersion",value:.metadata.resourceVersion},
     {op:"test",path:"/spec/runStrategy",value:"Halted"},{op:"remove",path:"/spec/template/spec/volumes/1"},
     {op:"remove",path:"/spec/template/spec/domain/devices/disks/1"},{op:"replace",path:"/spec/runStrategy",value:"Always"}]' >"$temp_dir/resume"
  kubectl -n "$namespace" patch virtualmachine.kubevirt.io "$worker" --type=json --patch-file "$temp_dir/resume" >&2
}

fresh_lease() {
  local lease
  lease=$(child_kubectl "$target_pod" -n kube-node-lease get lease "$worker" -o jsonpath='{.spec.renewTime}') || return 1
  [[ $lease > $1 ]]
}

restore() {
  local target_uid target_pod_uid vmi_uid lease copy_volume
  approved_freeze
  source_fenced
  source_stopped
  check_service "$source_pod"
  kubectl_absent -n "$namespace" cluster.k0smotron.io "$target_cluster"
  kubectl_absent -n "$namespace" statefulset "$target_set"
  kubectl_absent -n "$namespace" virtualmachineinstance.kubevirt.io "$worker"
  check_worker | jq -e '.spec.runStrategy=="Halted"' >/dev/null
  no_consumers "$worker_claim" "$worker_volume"
  check_retained_copy
  copy_volume=$(live_field .target_volume "$retained_receipt")
  no_consumers "$target_claim" "$copy_volume"
  check_retained_data
  source_fenced
  source_stopped
  check_retained_copy
  no_consumers "$target_claim" "$copy_volume"

  restore_control_plane
  wait_not_empty 180 -n "$namespace" get pod "$target_pod" -o jsonpath='{.metadata.uid}'
  kubectl -n "$namespace" wait --for=condition=Ready "pod/$target_pod" --timeout=600s >&2
  wait_jsonpath 180 1 -n "$namespace" get cluster.k0smotron.io "$target_cluster" -o jsonpath='{.status.readyReplicas}'
  target_uid=$(uid_of cluster.k0smotron.io "$target_cluster" -n "$namespace")
  [[ -n $target_uid && $target_uid != "$source_uid" ]]
  kubectl -n "$namespace" get cluster.k0smotron.io "$target_cluster" -o json | jq -e --argjson refs "$(source_signer_refs)" '
    (.spec.certificateRefs | sort_by(.type))==$refs' >/dev/null
  kubectl -n "$namespace" get statefulset "$target_set" -o json | jq -e --arg uid "$target_uid" '
    any(.metadata.ownerReferences[]; .uid==$uid and .kind=="Cluster" and .controller==true) and
    .spec.replicas==1 and (.spec.persistentVolumeClaimRetentionPolicy.whenDeleted // "Retain")=="Retain"' >/dev/null
  target_pod_uid=$(kubectl -n "$namespace" get pod "$target_pod" -o json | jq -er --arg claim "$target_claim" \
    --arg set "$(uid_of statefulset "$target_set" -n "$namespace")" '
      select(.metadata.deletionTimestamp==null and
      any(.metadata.ownerReferences[]; .uid==$set and .kind=="StatefulSet" and .controller==true) and
      any(.spec.volumes[]; .persistentVolumeClaim.claimName?==$claim)) | .metadata.uid | select(length>0)')
  check_retained_copy
  source_fenced
  source_stopped
  start_child_client "$target_pod"
  move_service
  retry_until 120 target_only_endpoints "$target_pod_uid"
  retry_until 180 child_kubectl "$target_pod" get --raw=/readyz
  expect_equal "$marker_uid" "$(child_kubectl "$target_pod" -n default get configmap "$marker" -o jsonpath='{.metadata.uid}')" 'restored marker UID with the original kubeconfig' >&2
  resume_worker
  wait_jsonpath 900 Running -n "$namespace" get virtualmachineinstance.kubevirt.io "$worker" -o jsonpath='{.status.phase}'
  vmi_uid=$(kubectl -n "$namespace" get virtualmachineinstance.kubevirt.io "$worker" -o json | jq -er --arg vm "$worker_vm_uid" \
    --arg mac "$mac" --arg firmware "$firmware" --arg claim "$worker_claim" '
      select(any(.metadata.ownerReferences[]; .uid==$vm and .kind=="VirtualMachine" and .controller==true) and
        .spec.domain.firmware.uuid==$firmware and any(.spec.domain.devices.interfaces[]; .name=="default" and .macAddress==$mac) and
        any(.spec.domain.devices.disks[]; .name=="root" and .serial=="sqlite-worker-root") and
        [.spec.volumes[] | .persistentVolumeClaim.claimName? // empty]==[$claim] and
        all(.spec.volumes[]; (has("cloudInitNoCloud") or has("cloudInitConfigDrive"))|not)) | .metadata.uid | select(length>0)')
  [[ $vmi_uid != "$old_vmi_uid" ]]
  retry_until 1200 fresh_lease "$(live_field .old_lease_renew_time "$freeze_receipt")"
  retry_until 180 child_kubectl "$target_pod" wait --for=condition=Ready "node/$worker" --timeout=10s
  retry_until 180 child_kubectl "$target_pod" -n default wait --for=condition=Ready "pod/$workload" --timeout=10s
  retry_until 180 child_kubectl "$target_pod" -n default exec "$workload" -- test -r /data/marker
  check_child "$target_pod"
  kubectl_absent -n "$namespace" jointokenrequest.k0smotron.io "$worker"
  bound_volume "$worker_claim" "$worker_claim_uid" "$worker_volume" "$worker_volume_uid" Block block >/dev/null
  check_service "$target_pod"
  target_only_endpoints "$target_pod_uid"
  check_retained_copy
  source_stopped
  lease=$(child_kubectl "$target_pod" -n kube-node-lease get lease "$worker" -o jsonpath='{.spec.renewTime}')
  jq -n --arg target "$target_uid" --arg service "$service_uid" --arg endpoint "$endpoint" --arg node "$old_node_uid" \
    --arg lease "$lease" --arg vmi "$vmi_uid" --arg workload "$workload_uid" --arg digest "$workload_sha" --arg proof "$copy_digest" '
      {version:1,target_cluster_uid:$target,service_uid:$service,api_endpoint:$endpoint,node_uid:$node,lease_renew_time:$lease,
       resumed_vmi_uid:$vmi,workload_pod_uid:$workload,workload_data_sha256:$digest,retained_copy_sha256:$proof}'
}

temp_dir=$(mktemp -d "$proof_dir/.sqlite-proof.XXXXXX")
probe_name=
probe_uid=
probe_backend=

cleanup() {
  local status=$?
  trap - EXIT
  remove_child_client || status=1
  rm -rf -- "$temp_dir"
  exit "$status"
}
trap cleanup EXIT
"$stage"
