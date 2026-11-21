#!/usr/bin/env bash
set -euo pipefail
umask 077

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

uid_pattern='^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$'
sha_pattern='^[0-9a-f]{64}$'
if [[ ${E2E_MANAGED_ETCD_FREEZE:-} != 1 || $# != 4 || ! ${1:-} =~ $uid_pattern || ! ${3:-} =~ $sha_pattern ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_ETCD_FREEZE=1 bash hack/e2e-managed-etcd-freeze.sh <isolated-guest-kube-system-uid> <private-source.json> <approved-source-sha256> <empty-private-output-directory>' >&2
  exit 2
fi

guest_uid=$1
source_path=$2
source_sha=$3
out=$4
export KUBECONFIG=/var/lib/k0s/pki/admin.conf
[[ $(uname -s) == Linux && -s /var/lib/bedrock/e2e-admin-password && -f $KUBECONFIG && ! -L $KUBECONFIG ]] ||
  fail 'Run this fixture inside an initialized Bedrock development guest.'
for tool in kubectl jq python3 sha256sum realpath stat find mktemp ln; do
  command -v "$tool" >/dev/null
done
[[ $(kubectl --request-timeout=10s get namespace kube-system -o jsonpath='{.metadata.uid}') == "$guest_uid" ]] ||
  fail 'The API does not match the approved isolated guest.'
[[ $out == /* && -d $out && -O $out && $(realpath -e -- "$out") == "$out" && $(stat -c '%a' -- "$out") == 700 ]] ||
  fail 'Use an absolute mode-0700 output directory that you own.'
entries=$(find "$out" -mindepth 1 -maxdepth 1 -print -quit)
[[ -z $entries ]] || fail 'Use an empty output directory.'
temp_dir=$(mktemp -d "$out/.etcd-freeze.XXXXXX")
trap 'rm -rf -- "$temp_dir"' EXIT

approved=$temp_dir/source.json
python3 -I - "$source_path" "$source_sha" "$approved" <<'PY'
import hashlib
import json
import os
import stat
import sys

def pairs(items):
    result = dict(items)
    if len(result) != len(items):
        raise ValueError()
    return result

source, digest, approved = sys.argv[1:]
try:
    if not os.path.isabs(source):
        raise ValueError()
    with os.fdopen(os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as record:
        metadata = os.fstat(record.fileno())
        if not stat.S_ISREG(metadata.st_mode) or stat.S_IMODE(metadata.st_mode) != 0o600 or metadata.st_uid != os.getuid():
            raise ValueError()
        content = record.read(1024 * 1024 + 1)
    if not content or len(content) > 1024 * 1024 or hashlib.sha256(content).hexdigest() != digest:
        raise ValueError()
    json.loads(content, object_pairs_hook=pairs)
    with open(approved, "xb") as output:
        output.write(content)
except (OSError, ValueError, RecursionError):
    sys.exit("The source receipt must be an approved private JSON file.")
PY

source "$(dirname "${BASH_SOURCE[0]}")/e2e/lib.sh"
namespace=bedrock-e2e-etcd-source
cluster=etcd-source
controller=kmc-$cluster
etcd=$controller-etcd
storage=block-retain
driver=rook-ceph.rbd.csi.ceph.com
pod_fence=bedrock-e2e-etcd-freeze-pods
scale_fence=bedrock-e2e-etcd-freeze-scale
disk_fence=bedrock-e2e-etcd-freeze-disks
message='The isolated etcd source is frozen.'

jq -e --arg guest "$guest_uid" --arg ns "$namespace" --arg cluster "$cluster" --arg controller "$controller" \
  --arg etcd "$etcd" --arg uid "$uid_pattern" --arg sha "$sha_pattern" '
  def hex_id: test("^[0-9a-f]{16}$") and . != "0000000000000000";
  def positive: test("^[1-9][0-9]{0,19}$");
  def covers($previous): length > ($previous | length) or (length == ($previous | length) and . >= $previous);
  def role($set; $claim):
    .statefulset == $set and .pod == ($set + "-0") and .disk.claim == $claim and
    (.disk.volume | length <= 253 and test("^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$")) and
    (.image_id | test("sha256:[0-9a-f]{64}$")) and (.disk.handle_sha256 | test($sha));
  .version == 1 and .guest_uid == $guest and .namespace == $ns and .cluster == $cluster and
  .k0s_version == "v1.36.3+k0s.1" and .marker.name == "bedrock-etcd-continuity" and
  .api_service.name == "etcd-endpoint" and (.api_service.address | test("^[0-9]+([.][0-9]+){3}$")) and
  .api_endpoint == ("https://" + .api_service.address + ":6443") and .konnectivity_endpoint == (.api_service.address + ":8132") and
  .etcd.service.name == $etcd and (.marker.revision | positive) and .marker.revision as $revision |
  (.etcd.status | .version == "3.7.1" and .endpoint == ("https://" + $etcd + ":2379") and
    .peer_url == ("https://" + $etcd + "-0." + $etcd + ":2380") and (.cluster_id | hex_id) and (.member_id | hex_id) and
    ([.committed_revision,.raft_term,.raft_index,.raft_applied_index] | all(.[]; positive)) and
    (.committed_revision | covers($revision)) and .raft_applied_index as $applied | (.raft_index | covers($applied))) and
  (.controller | role($controller; ($controller + "-" + $controller + "-0"))) and
  (.etcd | role($etcd; ("etcd-data-" + $etcd + "-0"))) and
  ([.guest_uid,.namespace_uid,.cluster_uid,.api_service.uid,.etcd.service.uid,.marker.uid,
    (.controller,.etcd | .statefulset_uid,.pod_uid,.disk.claim_uid,.disk.volume_uid)] |
    all(.[]; test($uid) and . != "00000000-0000-0000-0000-000000000000") and (length == (unique | length))) and
  .controller.disk.volume != .etcd.disk.volume and .controller.disk.handle_sha256 != .etcd.disk.handle_sha256' "$approved" >/dev/null ||
  fail 'The source receipt does not match the isolated single-member fixture.'

controller_claim=$(jq -er .controller.disk.claim "$approved")
controller_volume=$(jq -er .controller.disk.volume "$approved")
etcd_claim=$(jq -er .etcd.disk.claim "$approved")
etcd_volume=$(jq -er .etcd.disk.volume "$approved")

source_identity() {
  kubectl get namespace "$namespace" -o json | jq -e --slurpfile approved "$approved" '
    .metadata.uid == $approved[0].namespace_uid and .metadata.deletionTimestamp == null and .status.phase == "Active" and
    .metadata.labels["bedrock.cloudyfolks.io/tenant"] == null and .metadata.labels["bedrock.cloudyfolks.io/project"] == null' >/dev/null &&
  kubectl -n "$namespace" get rolebindings -o json | jq -e '.items == []' >/dev/null &&
  kubectl get --raw "/apis/k0smotron.io/v1beta2/namespaces/$namespace/clusters/$cluster" | jq -e --slurpfile approved "$approved" '
    $approved[0] as $source |
    .metadata.uid == $source.cluster_uid and .metadata.deletionTimestamp == null and .spec.replicas == 1 and
    .metadata.labels["app.kubernetes.io/managed-by"] == "cluster-api" and
    .spec.version == $source.k0s_version and .spec.externalAddress == $source.api_service.address and
    .spec.persistence.type == "pvc" and (.spec.persistence.autoDeletePVCs // false) == false and
    .spec.persistence.persistentVolumeClaim.spec.resources.requests.storage == "10Gi" and
    .spec.storage.type == "etcd" and .spec.storage.etcd.image == "quay.io/k0sproject/etcd:v3.7.1" and
    .spec.storage.etcd.persistence.storageClass == "block-retain" and (.spec.storage.etcd.args // []) == [] and
    .spec.storage.etcd.persistence.size == "1Gi" and
    (.spec.storage.etcd.autoDeletePVCs // false) == false and (.spec.storage.etcd.defragJob.enabled // false) == false and
    (.spec.patches // []) == [] and .spec.remoteHostCluster == null and
    (.spec.controlPlaneFlags // []) == [] and (.spec.mounts // []) == [] and .spec.ingress == null' >/dev/null &&
  kubectl -n "$namespace" get service etcd-endpoint "$etcd" -o json | jq -e --slurpfile approved "$approved" '
    $approved[0] as $source |
    (.items | length == 2) and
    any(.items[]; .metadata.name == $source.api_service.name and .metadata.uid == $source.api_service.uid and
      .metadata.deletionTimestamp == null and .spec.type == "ClusterIP" and .spec.clusterIP == $source.api_service.address and
      (.spec.externalIPs // []) == [] and .spec.selector == {"statefulset.kubernetes.io/pod-name":$source.controller.pod} and
      ([.spec.ports[] | [.port,.targetPort,.protocol]] | sort) == [[6443,6443,"TCP"],[8132,8132,"TCP"]]) and
    any(.items[]; .metadata.name == $source.etcd.service.name and .metadata.uid == $source.etcd.service.uid and
      .metadata.deletionTimestamp == null and .spec.type == "ClusterIP" and .spec.clusterIP == "None" and .spec.publishNotReadyAddresses and
      ([.spec.ports[] | [.port,.targetPort,.protocol]] | sort) == [[2379,2379,"TCP"],[2380,2380,"TCP"]] and
      any(.metadata.ownerReferences[]; .kind == "Cluster" and .uid == $source.cluster_uid and .controller == true))' >/dev/null
}

source_set() {
  local role=$1 name
  name=$(jq -er --arg role "$role" '.[$role].statefulset' "$approved") || return 1
  kubectl -n "$namespace" get statefulset "$name" -o json | jq -ce --slurpfile approved "$approved" --arg role "$role" '
    $approved[0] as $source | $source[$role] as $set |
    select(.metadata.uid == $set.statefulset_uid and .metadata.deletionTimestamp == null and
      (.metadata.ownerReferences | length == 1 and any(.[]; .apiVersion == "k0smotron.io/v1beta2" and .kind == "Cluster" and
        .name == $source.cluster and .uid == $source.cluster_uid and .controller == true)) and
      (.spec.persistentVolumeClaimRetentionPolicy.whenScaled // "Retain") == "Retain" and
      (.spec.persistentVolumeClaimRetentionPolicy.whenDeleted // "Retain") == "Retain" and
      (.spec.volumeClaimTemplates | length == 1 and any(.[];
        (.metadata.name + "-" + $set.pod) == $set.disk.claim and .spec.storageClassName == "block-retain")))'
}

source_running() {
  local role=$1 name
  source_set "$role" | jq -e '.spec.replicas == 1' >/dev/null || return 1
  name=$(jq -er --arg role "$role" '.[$role].pod' "$approved") || return 1
  kubectl -n "$namespace" get pod "$name" -o json | jq -e --slurpfile approved "$approved" --arg role "$role" '
    $approved[0][$role] as $set |
    .metadata.uid == $set.pod_uid and .metadata.deletionTimestamp == null and .status.phase == "Running" and
    any(.metadata.ownerReferences[]; .kind == "StatefulSet" and .name == $set.statefulset and .uid == $set.statefulset_uid and .controller == true) and
    any(.spec.volumes[]; .persistentVolumeClaim.claimName == $set.disk.claim) and
    any(.status.containerStatuses[]; .name == $role and .ready and .imageID == $set.image_id)' >/dev/null
}

source_disk() {
  local role=$1 claim=$2 volume=$3 handle digest
  kubectl -n "$namespace" get pvc "$claim" -o json | jq -e --slurpfile approved "$approved" --arg role "$role" --arg storage "$storage" '
    $approved[0][$role].disk as $disk |
    .metadata.uid == $disk.claim_uid and .metadata.deletionTimestamp == null and .status.phase == "Bound" and
    .spec.volumeName == $disk.volume and .spec.storageClassName == $storage and
    (.spec.volumeMode // "Filesystem") == "Filesystem" and .spec.accessModes == ["ReadWriteOnce"]' >/dev/null || return 1
  handle=$(kubectl get pv "$volume" -o json | jq -ej --slurpfile approved "$approved" --arg role "$role" --arg storage "$storage" --arg driver "$driver" '
    $approved[0] as $source | $source[$role].disk as $disk |
    select(.metadata.uid == $disk.volume_uid and .metadata.deletionTimestamp == null and .status.phase == "Bound" and
      .spec.persistentVolumeReclaimPolicy == "Retain" and .spec.storageClassName == $storage and
      (.spec.volumeMode // "Filesystem") == "Filesystem" and .spec.csi.driver == $driver and (.spec.csi.readOnly // false) == false and
      (.spec.claimRef | .namespace == $source.namespace and .name == $disk.claim and .uid == $disk.claim_uid)) |
    .spec.csi.volumeHandle | select(type == "string" and length > 0)' | sha256sum) || return 1
  digest=$(jq -er --arg role "$role" '.[$role].disk.handle_sha256' "$approved") || return 1
  [[ ${handle%% *} == "$digest" ]]
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
    - {name: etcd-fixture, expression: "request.namespace == '$namespace'"}
  validations:
    - expression: >-
        (!has(object.metadata.name) || !object.metadata.name.matches('^$controller(-etcd)?-[0-9]+$')) &&
        (!has(object.metadata.generateName) || !(object.metadata.generateName in ['$controller-','$etcd-'])) &&
        (!has(object.metadata.ownerReferences) || object.metadata.ownerReferences.all(o,
          o.kind != 'StatefulSet' || !(o.name in ['$controller','$etcd']))) &&
        (!has(object.spec.volumes) || object.spec.volumes.all(v, !has(v.persistentVolumeClaim) ||
          !(v.persistentVolumeClaim.claimName in ['$controller_claim','$etcd_claim'])))
      message: $message
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
      - {apiGroups: [apps], apiVersions: [v1], operations: [CREATE, UPDATE, DELETE], resources: [statefulsets, statefulsets/scale]}
  matchConditions:
    - name: etcd-fixture
      expression: >-
        request.namespace == '$namespace' && (request.name in ['$controller','$etcd'] ||
          (request.operation == 'CREATE' && has(object.metadata.name) && object.metadata.name in ['$controller','$etcd']))
  validations:
    - expression: "request.operation == 'UPDATE' && has(object.spec.replicas) && object.spec.replicas == 0"
      message: $message
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: {name: $scale_fence}
spec: {policyName: $scale_fence, validationActions: [Deny]}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: {name: $disk_fence}
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - {apiGroups: [""], apiVersions: [v1], operations: [DELETE], resources: [persistentvolumeclaims, persistentvolumes]}
  matchConditions:
    - name: etcd-fixture
      expression: >-
        (request.resource.resource == 'persistentvolumeclaims' && request.namespace == '$namespace' &&
          request.name in ['$controller_claim','$etcd_claim']) ||
        (request.resource.resource == 'persistentvolumes' &&
          request.name in ['$controller_volume','$etcd_volume'])
  validations:
    - {expression: "false", message: $message}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: {name: $disk_fence}
spec: {policyName: $disk_fence, validationActions: [Deny]}
EOF
}

denied() {
  local policy=$1 output
  shift
  if output=$("$@" 2>&1); then
    return 1
  fi
  [[ $output == *"$policy"* && $output == *"$message"* ]]
}

fenced() {
  local role=$1 set=$2 claim=$3 volume=$4
  denied "$pod_fence" kubectl -n "$namespace" create --dry-run=server -f - <<EOF || return 1
apiVersion: v1
kind: Pod
metadata: {name: etcd-disk-fence-proof-$role}
spec:
  containers:
    - name: proof
      image: quay.io/k0sproject/etcd:v3.7.1
      volumeMounts: [{name: source, mountPath: /source}]
  volumes: [{name: source, persistentVolumeClaim: {claimName: $claim}}]
EOF
  denied "$scale_fence" kubectl -n "$namespace" patch statefulset "$set" --type=merge --dry-run=server -p '{"spec":{"replicas":1}}' &&
  denied "$scale_fence" kubectl -n "$namespace" patch statefulset "$set" --subresource=scale --type=merge --dry-run=server -p '{"spec":{"replicas":1}}' &&
  denied "$scale_fence" kubectl -n "$namespace" delete statefulset "$set" --dry-run=server --wait=false &&
  denied "$disk_fence" kubectl -n "$namespace" delete pvc "$claim" --dry-run=server --wait=false &&
  denied "$disk_fence" kubectl delete pv "$volume" --dry-run=server --wait=false
}

pods_match() {
  local role=$1 expected=$2
  kubectl get --raw "/api/v1/namespaces/$namespace/pods" | jq -e --slurpfile approved "$approved" \
    --arg role "$role" --argjson expected "$expected" '
    $approved[0][$role] as $set |
    .kind == "PodList" and (.metadata.continue // "") == "" and (.items | type == "array") and
    ([.items[] | select(.metadata.name == $set.pod or .metadata.uid == $set.pod_uid or
      any(.metadata.ownerReferences[]?; .uid == $set.statefulset_uid) or
      any(.spec.volumes[]?; .persistentVolumeClaim.claimName == $set.disk.claim)) | .metadata.uid] | sort) ==
    (if $expected then [$set.pod_uid] else [] end)' >/dev/null
}

stop_source() {
  local role=$1 set=$2
  source_running "$role" && pods_match "$role" true ||
    fail 'The original workload changed before it could stop.'
  source_set "$role" | jq -ce '
    select(.spec.replicas == 1) |
    [{op:"test",path:"/metadata/uid",value:.metadata.uid},{op:"test",path:"/metadata/resourceVersion",value:.metadata.resourceVersion},
     {op:"test",path:"/spec/replicas",value:1},{op:"replace",path:"/spec/replicas",value:0}]' >"$temp_dir/stop"
  kubectl -n "$namespace" patch statefulset "$set" --type=json --patch-file "$temp_dir/stop" >&2
}

stopped_and_detached() {
  local role=$1 volume=$2
  source_set "$role" | jq -e '
    .spec.replicas == 0 and .status.observedGeneration == .metadata.generation and
    ([.status.replicas,.status.readyReplicas,.status.currentReplicas,.status.updatedReplicas,.status.availableReplicas] |
      all(.[]; (. // 0) == 0))' >/dev/null &&
  pods_match "$role" false &&
  kubectl get --raw /apis/storage.k8s.io/v1/volumeattachments | jq -e --arg volume "$volume" '
    .kind == "VolumeAttachmentList" and (.metadata.continue // "") == "" and (.items | type == "array") and
    all(.items[]; .spec.source.persistentVolumeName != $volume)' >/dev/null
}

capture_list() {
  local path=$1 api=$2 kind=$3 file=$4
  kubectl --request-timeout=60s get --raw "$path" >"$temp_dir/$file"
  jq -se --arg api "$api" --arg kind "$kind" '
    length == 1 and (.[0] | .apiVersion == $api and .kind == $kind and
    (.metadata.resourceVersion | type == "string" and length > 0) and
    (.metadata.continue // "") == "" and (.metadata.remainingItemCount // 0) == 0 and (.items | type == "array"))' \
    "$temp_dir/$file" >/dev/null
}

fence_evidence() {
  local name=$1 policy binding
  policy=$(kubectl get validatingadmissionpolicy "$name" -o json | jq -er --arg uid "$uid_pattern" '
    select(.metadata.deletionTimestamp == null and .spec.failurePolicy == "Fail") | .metadata.uid | select(test($uid))') || return 1
  binding=$(kubectl get validatingadmissionpolicybinding "$name" -o json | jq -er --arg name "$name" --arg uid "$uid_pattern" '
    select(.metadata.deletionTimestamp == null and .spec.policyName == $name and .spec.validationActions == ["Deny"]) |
    .metadata.uid | select(test($uid))') || return 1
  jq -cn --arg policy "$policy" --arg binding "$binding" '{policy_uid:$policy, binding_uid:$binding}'
}

source_identity || fail 'The approved namespace, cluster or service changed.'
source_disk controller "$controller_claim" "$controller_volume" && source_disk etcd "$etcd_claim" "$etcd_volume" ||
  fail 'An approved retained RBD binding changed.'
source_running controller && pods_match controller true && source_running etcd && pods_match etcd true ||
  fail 'The original two source Pods are not the only claim consumers.'

create_fences
retry_until 90 fenced controller "$controller" "$controller_claim" "$controller_volume"
retry_until 90 fenced etcd "$etcd" "$etcd_claim" "$etcd_volume"
stop_source controller "$controller"
retry_until 360 stopped_and_detached controller "$controller_volume"
stop_source etcd "$etcd"
retry_until 360 stopped_and_detached etcd "$etcd_volume"

source_identity || fail 'The source identity changed during the freeze.'
source_disk controller "$controller_claim" "$controller_volume" && source_disk etcd "$etcd_claim" "$etcd_volume" ||
  fail 'An approved retained RBD binding changed during the freeze.'
fenced controller "$controller" "$controller_claim" "$controller_volume"
fenced etcd "$etcd" "$etcd_claim" "$etcd_volume"
capture_list /apis/k0smotron.io/v1beta2/clusters k0smotron.io/v1beta2 ClusterList clusters
capture_list /apis/apps/v1/statefulsets apps/v1 StatefulSetList sets
capture_list /api/v1/persistentvolumeclaims v1 PersistentVolumeClaimList claims
capture_list /api/v1/persistentvolumes v1 PersistentVolumeList volumes
capture_list /api/v1/pods v1 PodList pods
capture_list /apis/storage.k8s.io/v1/volumeattachments storage.k8s.io/v1 VolumeAttachmentList attachments
capture_list /api/v1/namespaces v1 NamespaceList namespaces
capture_list "/api/v1/namespaces/$namespace/services" v1 ServiceList services
capture_list "/api/v1/namespaces/$namespace/configmaps" v1 ConfigMapList configs
jq -n --slurpfile clusters "$temp_dir/clusters" --slurpfile sets "$temp_dir/sets" --slurpfile claims "$temp_dir/claims" \
  --slurpfile volumes "$temp_dir/volumes" --slurpfile pods "$temp_dir/pods" --slurpfile attachments "$temp_dir/attachments" \
  --slurpfile namespaces "$temp_dir/namespaces" --slurpfile services "$temp_dir/services" --slurpfile configs "$temp_dir/configs" '
  {version:1, clusters:$clusters[0], stateful_sets:$sets[0], persistent_volume_claims:$claims[0], persistent_volumes:$volumes[0],
   pods:$pods[0], volume_attachments:$attachments[0], namespaces:$namespaces[0], services:$services[0], config_maps:$configs[0]}' \
  >"$temp_dir/frozen-inventory.json"
stopped_and_detached controller "$controller_volume" && stopped_and_detached etcd "$etcd_volume" ||
  fail 'A source workload or attachment changed during inventory capture.'
inventory_sha=$(sha256sum -- "$temp_dir/frozen-inventory.json")
pod_proof=$(fence_evidence "$pod_fence")
scale_proof=$(fence_evidence "$scale_fence")
disk_proof=$(fence_evidence "$disk_fence")
jq -n --slurpfile approved "$approved" --arg input "$source_sha" --arg inventory "${inventory_sha%% *}" \
  --argjson pod "$pod_proof" --argjson scale "$scale_proof" --argjson disk "$disk_proof" '
  $approved[0] as $source |
  {version:1, guest_uid:$source.guest_uid, namespace_uid:$source.namespace_uid, source_cluster_uid:$source.cluster_uid,
   source_receipt_sha256:$input, inventory_sha256:$inventory, fences:{pods:$pod, scale:$scale, retained_disks:$disk}}' \
  >"$temp_dir/frozen.json"
ln -T -- "$temp_dir/frozen-inventory.json" "$out/frozen-inventory.json"
ln -T -- "$temp_dir/frozen.json" "$out/frozen.json"
cat -- "$out/frozen.json"
