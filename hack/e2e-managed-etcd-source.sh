#!/usr/bin/env bash
set -euo pipefail

if [[ ${E2E_MANAGED_ETCD_SOURCE:-} != 1 || $# != 1 || ! ${1:-} =~ ^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$ ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_ETCD_SOURCE=1 bash hack/e2e-managed-etcd-source.sh <isolated-guest-kube-system-uid>' >&2
  exit 2
fi

guest_uid=$1
export KUBECONFIG=/var/lib/k0s/pki/admin.conf
if [[ $(uname -s) != Linux || ! -s /var/lib/bedrock/e2e-admin-password || ! -f $KUBECONFIG || -L $KUBECONFIG ]]; then
  printf '%s\n' 'Run this fixture inside an initialized Bedrock development guest.' >&2
  exit 1
fi
for tool in kubectl k0s jq python3 sha256sum awk; do
  command -v "$tool" >/dev/null
done
if [[ $(kubectl --request-timeout=10s get namespace kube-system -o jsonpath='{.metadata.uid}') != "$guest_uid" ]]; then
  printf '%s\n' 'The API does not match the approved isolated guest.' >&2
  exit 1
fi

script_dir=$(dirname "${BASH_SOURCE[0]}")
source "$script_dir/e2e/lib.sh"
source "$script_dir/e2e/tenant/clusters.sh"

namespace=bedrock-e2e-etcd-source
cluster=etcd-source
service=etcd-endpoint
controller_set=kmc-$cluster
controller_pod=$controller_set-0
etcd_set=$controller_set-etcd
etcd_pod=$etcd_set-0
etcd_endpoint=https://$etcd_set:2379
etcd_peer=https://$etcd_pod.$etcd_set:2380
etcd_version=3.7.1
etcd_image=quay.io/k0sproject/etcd:v$etcd_version
controller_image=quay.io/k0sproject/k0s:${clusters_proof_version/+k0s./-k0s.}
storage_class=block-retain
driver=rook-ceph.rbd.csi.ceph.com
marker=bedrock-etcd-continuity

kubectl get storageclass "$storage_class" -o json | jq -e --arg driver "$driver" '
  .metadata.deletionTimestamp == null and .provisioner == $driver and .reclaimPolicy == "Retain" and
  (.volumeBindingMode // "Immediate") == "Immediate"' >/dev/null
if ! k0s ctr --namespace k8s.io images ls | awk -v image="$etcd_image" '
  $1 == image && length($3) == 71 && $3 ~ /^sha256:[0-9a-f]+$/ { matches++ } END { exit matches != 1 }'; then
  printf '%s\n' 'Preload the pinned etcd image in the isolated guest.' >&2
  exit 1
fi

namespace_uid=$(kubectl create namespace "$namespace" -o jsonpath='{.metadata.uid}')
test -n "$namespace_uid"
kubectl get namespace "$namespace" -o json | jq -e --arg uid "$namespace_uid" '
  .metadata.uid == $uid and .status.phase == "Active" and .metadata.labels["bedrock.cloudyfolks.io/tenant"] == null and
  .metadata.labels["bedrock.cloudyfolks.io/project"] == null' >/dev/null
kubectl -n "$namespace" get rolebindings.rbac.authorization.k8s.io -o json | jq -e '.items == []' >/dev/null
clusters_proof_enable_addon >&2
operator_image=$(kubectl -n k0smotron get deployment k0smotron-controller-manager -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}')
expect_equal quay.io/k0sproject/k0smotron:v2.1.0 "${operator_image%@*}" 'the k0smotron controller image' >&2

kubectl -n "$namespace" create -f - >&2 <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: etcd-source-ingress}
spec:
  podSelector:
    matchExpressions:
      - key: migration.cloudyfolks.io/purpose
        operator: NotIn
        values: [etcd-member-recovery, etcd-native-restart]
  policyTypes: [Ingress]
  ingress:
    - from: [{podSelector: {}}]
      ports:
        - {port: 6443, protocol: TCP}
        - {port: 8132, protocol: TCP}
        - {port: 2379, protocol: TCP}
        - {port: 2380, protocol: TCP}
---
apiVersion: v1
kind: Service
metadata: {name: $service}
spec:
  type: ClusterIP
  selector: {statefulset.kubernetes.io/pod-name: $controller_pod}
  ports:
    - {name: api, port: 6443, targetPort: 6443, protocol: TCP}
    - {name: konnectivity, port: 8132, targetPort: 8132, protocol: TCP}
EOF

api_service=$(kubectl -n "$namespace" get service "$service" -o json | jq -cer --arg pod "$controller_pod" '
  select(.spec.type == "ClusterIP" and (.spec.externalIPs // []) == [] and
    .spec.selector == {"statefulset.kubernetes.io/pod-name":$pod} and
    ([.spec.ports[] | [.port,.targetPort,.protocol]] | sort) == [[6443,6443,"TCP"],[8132,8132,"TCP"]]) |
  {name:.metadata.name, uid:.metadata.uid, address:.spec.clusterIP} | select((.uid | length) > 0)')
address=$(jq -er '.address | select(test("^[0-9]+(\\.[0-9]+){3}$"))' <<<"$api_service")
kubectl -n "$namespace" create -f - >&2 <<EOF
apiVersion: k0smotron.io/v1beta2
kind: Cluster
metadata:
  name: $cluster
  labels: {app.kubernetes.io/managed-by: cluster-api}
spec:
  version: $clusters_proof_version
  replicas: 1
  externalAddress: $address
  service: {type: ClusterIP, apiPort: 6443, konnectivityPort: 8132}
  storage:
    type: etcd
    etcd:
      image: $etcd_image
      persistence: {storageClass: $storage_class, size: 1Gi}
      autoDeletePVCs: false
      defragJob: {enabled: false}
  persistence:
    type: pvc
    autoDeletePVCs: false
    persistentVolumeClaim:
      spec:
        accessModes: [ReadWriteOnce]
        storageClassName: $storage_class
        volumeMode: Filesystem
        resources: {requests: {storage: 10Gi}}
  resources:
    requests: {cpu: 500m, memory: 1Gi}
    limits: {cpu: "2", memory: 4Gi}
  k0sConfig:
    apiVersion: k0s.k0sproject.io/v1beta1
    kind: ClusterConfig
    spec:
      network: {provider: kuberouter, podCIDR: $clusters_proof_pod_cidr, serviceCIDR: $clusters_proof_service_cidr}
EOF

for pod in "$etcd_pod" "$controller_pod"; do
  wait_not_empty 180 -n "$namespace" get pod "$pod" -o jsonpath='{.metadata.uid}'
  kubectl -n "$namespace" wait --for=condition=Ready "pod/$pod" --timeout=600s >&2
done
wait_jsonpath 180 1 -n "$namespace" get "cluster.k0smotron.io/$cluster" -o jsonpath='{.status.readyReplicas}'
cluster_uid=$(kubectl -n "$namespace" get "cluster.k0smotron.io/$cluster" -o json | jq -er \
  --arg version "$clusters_proof_version" --arg image "$etcd_image" --arg storage "$storage_class" --arg address "$address" '
  select(.apiVersion == "k0smotron.io/v1beta2" and .spec.replicas == 1 and .spec.version == $version and .spec.externalAddress == $address and
    .spec.storage.type == "etcd" and .spec.storage.etcd.image == $image and .spec.storage.etcd.persistence.storageClass == $storage and
    (.spec.storage.etcd.args // []) == [] and (.spec.storage.etcd.autoDeletePVCs // false) == false and
    (.spec.storage.etcd.defragJob.enabled // false) == false and (.spec.patches // []) == [] and .spec.remoteHostCluster == null and
    (.spec.controlPlaneFlags // []) == [] and (.spec.mounts // []) == [] and .spec.ingress == null and
    .spec.persistence.type == "pvc" and (.spec.persistence.autoDeletePVCs // false) == false) |
  .metadata.uid | select(length > 0)')

statefulset_evidence() {
  local set=$1 pod=$2 template=$3 container=$4 path=$5 image=$6 set_uid
  set_uid=$(kubectl -n "$namespace" get statefulset "$set" -o json | jq -er \
    --arg uid "$cluster_uid" --arg cluster "$cluster" --arg claim "$template" --arg storage "$storage_class" \
    --arg container "$container" --arg image "$image" --arg path "$path" '
    select(.metadata.deletionTimestamp == null and
      any(.metadata.ownerReferences[]; .kind == "Cluster" and .name == $cluster and .uid == $uid and .controller == true) and
      .spec.replicas == 1 and (.spec.persistentVolumeClaimRetentionPolicy.whenScaled // "Retain") == "Retain" and
      (.spec.persistentVolumeClaimRetentionPolicy.whenDeleted // "Retain") == "Retain" and
      ([.spec.volumeClaimTemplates[] | [.metadata.name,.spec.storageClassName]] == [[$claim,$storage]]) and
      any(.spec.template.spec.containers[]; .name == $container and .image == $image and
        any(.volumeMounts[]; .name == $claim and .mountPath == $path and (.subPath // "") == "" and (.readOnly // false) == false))) |
    .metadata.uid | select(length > 0)') || return 1
  kubectl -n "$namespace" get pod "$pod" -o json | jq -cer \
    --arg set "$set" --arg uid "$set_uid" --arg claim "$template-$pod" --arg volume "$template" --arg container "$container" '
    select(.metadata.deletionTimestamp == null and .status.phase == "Running" and
      any(.metadata.ownerReferences[]; .kind == "StatefulSet" and .name == $set and .uid == $uid and .controller == true) and
      any(.spec.volumes[]; .name == $volume and .persistentVolumeClaim.claimName == $claim)) |
    {statefulset:$set, statefulset_uid:$uid, pod:.metadata.name, pod_uid:.metadata.uid,
     image_id:(.status.containerStatuses[] | select(.name == $container and .ready) | .imageID)} |
    select((.image_id | test("sha256:[0-9a-f]{64}$")) and (.pod_uid | length > 0))'
}

disk_evidence() {
  local claim=$1 binding uid volume pv handle
  binding=$(kubectl -n "$namespace" get pvc "$claim" -o json | jq -cer --arg storage "$storage_class" '
    select(.metadata.deletionTimestamp == null and .status.phase == "Bound" and .spec.storageClassName == $storage and
      (.spec.volumeMode // "Filesystem") == "Filesystem" and .spec.accessModes == ["ReadWriteOnce"]) |
    {claim:.metadata.name, claim_uid:.metadata.uid, volume:.spec.volumeName} |
    select([.claim_uid,.volume] | all(.[]; type == "string" and length > 0))') || return 1
  uid=$(jq -er .claim_uid <<<"$binding") || return 1
  volume=$(jq -er .volume <<<"$binding") || return 1
  pv=$(kubectl get pv "$volume" -o json | jq -cer --arg ns "$namespace" --arg claim "$claim" --arg uid "$uid" \
    --arg storage "$storage_class" --arg driver "$driver" '
    select(.metadata.deletionTimestamp == null and .status.phase == "Bound" and .spec.persistentVolumeReclaimPolicy == "Retain" and
      .spec.storageClassName == $storage and (.spec.volumeMode // "Filesystem") == "Filesystem" and .spec.csi.driver == $driver and
      (.spec.csi.readOnly // false) == false and (.spec.claimRef | .namespace == $ns and .name == $claim and .uid == $uid)) |
    {volume_uid:.metadata.uid, handle:.spec.csi.volumeHandle} |
    select([.volume_uid,.handle] | all(.[]; type == "string" and length > 0))') || return 1
  handle=$(jq -ej .handle <<<"$pv" | sha256sum) || return 1
  jq -c --argjson pv "$pv" --arg hash "${handle%% *}" '. + {volume_uid:$pv.volume_uid, handle_sha256:$hash}' <<<"$binding"
}

controller=$(statefulset_evidence "$controller_set" "$controller_pod" "$controller_set" controller /var/lib/k0s "$controller_image")
etcd=$(statefulset_evidence "$etcd_set" "$etcd_pod" etcd-data etcd /var/lib/k0s/etcd "$etcd_image")
controller_disk=$(disk_evidence "$controller_set-$controller_pod")
etcd_disk=$(disk_evidence "etcd-data-$etcd_pod")
[[ $(jq -er .handle_sha256 <<<"$controller_disk") != "$(jq -er .handle_sha256 <<<"$etcd_disk")" ]]
etcd_service=$(kubectl -n "$namespace" get service "$etcd_set" -o json | jq -cer --arg uid "$cluster_uid" '
  select(.spec.type == "ClusterIP" and .spec.clusterIP == "None" and .spec.publishNotReadyAddresses and
    .spec.selector.component == "etcd" and
    ([.spec.ports[] | [.port,.targetPort,.protocol]] | sort) == [[2379,2379,"TCP"],[2380,2380,"TCP"]] and
    any(.metadata.ownerReferences[]; .kind == "Cluster" and .uid == $uid and .controller == true)) |
  {name:.metadata.name, uid:.metadata.uid} | select((.uid | length) > 0)')
controller_config=$(kubectl -n "$namespace" get configmap "$controller_set-config" -o json | jq -er --arg uid "$cluster_uid" '
  select(any(.metadata.ownerReferences[]; .kind == "Cluster" and .uid == $uid and .controller == true)) |
  .data.K0SMOTRON_K0S_YAML | select(type == "string" and length > 0)')
awk -v endpoint="$etcd_endpoint" -v prefix="$cluster" '
  /^  storage:$/ { storage=1; next }
  /^  [^ ]/ { storage=0 }
  storage && $0 == "    type: etcd" { type_count++ }
  storage && $0 == "      externalCluster:" { external_count++ }
  storage && $0 == "        - " endpoint { endpoint_count++ }
  storage && $0 == "        etcdPrefix: " prefix { prefix_count++ }
  END { exit type_count != 1 || external_count != 1 || endpoint_count != 1 || prefix_count != 1 }' <<<"$controller_config"

child_kubectl() {
  kubectl -n "$namespace" exec "$controller_pod" -c controller -- k0s kubectl --kubeconfig=/var/lib/k0s/pki/admin.conf \
    --server=https://127.0.0.1:6443 --tls-server-name="$address" --request-timeout=10s "$@"
}

etcdctl_fixture() {
  kubectl -n "$namespace" exec "$etcd_pod" -c etcd -- /bin/bash -ec '
    [[ $ETCDCTL_ENDPOINTS == "$1" && $ETCDCTL_CACERT == /var/lib/k0s/pki/etcd/ca.crt &&
      $ETCDCTL_CERT == /var/lib/k0s/pki/etcd/server.crt && $ETCDCTL_KEY == /var/lib/k0s/pki/etcd/server.key ]]
    test -s /var/lib/k0s/etcd/member/snap/db
    shift
    exec etcdctl --dial-timeout=5s --command-timeout=10s --write-out=json "$@"
  ' etcdctl "$etcd_endpoint" "$@"
}

retry_until 180 child_kubectl get --raw=/readyz
child_kubectl -n default create configmap "$marker" --from-literal=state=before-handoff >&2
marker_evidence=$(child_kubectl -n default get configmap "$marker" -o json | jq -cer '
  select(.data == {state:"before-handoff"} and (.metadata.uid | length) > 0 and (.metadata.resourceVersion | test("^[1-9][0-9]*$"))) |
  {name:.metadata.name, uid:.metadata.uid, revision:.metadata.resourceVersion}')
members=$(etcdctl_fixture member list)
endpoint_status=$(etcdctl_fixture endpoint status)
etcd_status=$(python3 -I -c '
import json
import sys

try:
    member_list, endpoints = map(json.loads, sys.argv[1:3])
    expected_name, peer, endpoint, version, marker_revision = sys.argv[3:]
    member, = member_list["members"]
    endpoint_record, = endpoints
    status = endpoint_record["Status"]
    header = status["header"]
    cluster_id, member_id = header["cluster_id"], member["ID"]
    revision, term, index, applied = header["revision"], status["raftTerm"], status["raftIndex"], status["raftAppliedIndex"]
    if not all(type(value) is int and 0 < value < 2**64 for value in (cluster_id, member_id, revision, term, index, applied)):
        raise ValueError()
    if not all((member["name"] == expected_name, member["peerURLs"] == [peer], member.get("isLearner", False) is False,
                endpoint_record["Endpoint"] == endpoint, status["version"] == version, not status.get("errors"),
                status.get("isLearner", False) is False, status["leader"] == member_id == header["member_id"],
                member_list["header"]["cluster_id"] == cluster_id, member_list["header"]["member_id"] == member_id,
                header["raft_term"] == term, revision >= int(marker_revision), index >= applied)):
        raise ValueError()
except (KeyError, TypeError, ValueError):
    sys.exit("The fixture does not have a verified single etcd member covering the committed marker")
print(json.dumps({"cluster_id": f"{cluster_id:016x}", "member_id": f"{member_id:016x}", "peer_url": peer, "endpoint": endpoint,
                  "version": version, "committed_revision": str(revision), "raft_term": str(term),
                  "raft_index": str(index), "raft_applied_index": str(applied)}))
' "$members" "$endpoint_status" "$etcd_pod" "$etcd_peer" "$etcd_endpoint" "$etcd_version" "$(jq -er .revision <<<"$marker_evidence")")

jq -n --arg guest "$guest_uid" --arg ns "$namespace" --arg ns_uid "$namespace_uid" --arg cluster "$cluster" --arg uid "$cluster_uid" \
  --arg version "$clusters_proof_version" --argjson controller "$controller" --argjson etcd "$etcd" \
  --argjson controller_disk "$controller_disk" --argjson etcd_disk "$etcd_disk" --argjson api_service "$api_service" \
  --argjson etcd_service "$etcd_service" --argjson marker "$marker_evidence" --argjson status "$etcd_status" '
  {version:1, guest_uid:$guest, namespace:$ns, namespace_uid:$ns_uid, cluster:$cluster, cluster_uid:$uid, k0s_version:$version,
   api_endpoint:("https://" + $api_service.address + ":6443"), konnectivity_endpoint:($api_service.address + ":8132"),
   api_service:$api_service, controller:($controller + {disk:$controller_disk}),
   etcd:($etcd + {disk:$etcd_disk, service:$etcd_service, status:$status}), marker:$marker}'
