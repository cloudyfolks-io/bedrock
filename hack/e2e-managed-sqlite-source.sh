#!/usr/bin/env bash
set -euo pipefail

if [[ ${E2E_MANAGED_SQLITE_SOURCE:-} != 1 || $# != 1 || ! ${1:-} =~ ^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$ ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_SQLITE_SOURCE=1 bash hack/e2e-managed-sqlite-source.sh <isolated-guest-kube-system-uid>' >&2
  exit 2
fi

guest_uid=$1
export KUBECONFIG=/var/lib/k0s/pki/admin.conf
if [[ $(uname -s) != Linux || ! -s /var/lib/bedrock/e2e-admin-password || ! -f $KUBECONFIG || -L $KUBECONFIG ]]; then
  printf '%s\n' 'Run this fixture inside an initialized Bedrock development guest.' >&2
  exit 1
fi
for tool in kubectl jq sha256sum awk; do
  command -v "$tool" >/dev/null
done
if [[ $(kubectl --request-timeout=10s get namespace kube-system -o jsonpath='{.metadata.uid}') != "$guest_uid" ]]; then
  printf '%s\n' 'The API does not match the approved isolated guest.' >&2
  exit 1
fi

script_dir=$(dirname "${BASH_SOURCE[0]}")
source "$script_dir/e2e/lib.sh"
source "$script_dir/e2e/tenant/clusters.sh"

namespace=bedrock-e2e-sqlite-source
cluster=sqlite-source
service=sqlite-endpoint
statefulset=kmc-$cluster
pod=$statefulset-0
claim=$statefulset-$pod
controller_config=$statefulset-config
entrypoint_config=kmc-entrypoint-$cluster-config
entrypoint_sha=8192a05440e684a6b447f7aeaabe6675fed911dc2b8ee87b89abadf0b47502c9
marker=bedrock-sqlite-continuity

kubectl get storageclass block -o json |
  jq -e '.provisioner == "rook-ceph.rbd.csi.ceph.com" and (.volumeBindingMode // "Immediate") == "Immediate"' >/dev/null
namespace_uid=$(kubectl create namespace "$namespace" -o jsonpath='{.metadata.uid}')
test -n "$namespace_uid"
clusters_proof_enable_addon >&2
operator_image=$(kubectl -n k0smotron get deployment k0smotron-controller-manager -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}')
expect_equal quay.io/k0sproject/k0smotron:v2.1.0 "${operator_image%@*}" 'the k0smotron controller image'

kubectl -n "$namespace" create -f - >&2 <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: sqlite-source-ingress
spec:
  podSelector:
    matchLabels:
      statefulset.kubernetes.io/pod-name: $pod
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector: {}
      ports:
        - port: 6443
          protocol: TCP
        - port: 8132
          protocol: TCP
---
apiVersion: v1
kind: Service
metadata:
  name: $service
spec:
  type: ClusterIP
  selector:
    statefulset.kubernetes.io/pod-name: $pod
  ports:
    - name: api
      port: 6443
      targetPort: 6443
    - name: konnectivity
      port: 8132
      targetPort: 8132
EOF

address=$(kubectl -n "$namespace" get service "$service" -o jsonpath='{.spec.clusterIP}')
[[ $address =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]
kubectl -n "$namespace" create -f - >&2 <<EOF
apiVersion: k0smotron.io/v1beta2
kind: Cluster
metadata:
  name: $cluster
  labels:
    app.kubernetes.io/managed-by: cluster-api
spec:
  version: $clusters_proof_version
  replicas: 1
  externalAddress: $address
  service:
    type: ClusterIP
    apiPort: 6443
    konnectivityPort: 8132
  storage:
    type: kine
    kine:
      dataSourceURL: sqlite:///var/lib/k0s/state.db
  persistence:
    type: pvc
    autoDeletePVCs: false
    persistentVolumeClaim:
      spec:
        accessModes: [ReadWriteOnce]
        storageClassName: block
        volumeMode: Filesystem
        resources:
          requests:
            storage: 10Gi
  resources:
    requests:
      cpu: 500m
      memory: 1Gi
    limits:
      cpu: "2"
      memory: 4Gi
  k0sConfig:
    apiVersion: k0s.k0sproject.io/v1beta1
    kind: ClusterConfig
    spec:
      network:
        provider: kuberouter
        podCIDR: $clusters_proof_pod_cidr
        serviceCIDR: $clusters_proof_service_cidr
EOF

wait_not_empty 180 -n "$namespace" get pod "$pod" -o jsonpath='{.metadata.uid}'
kubectl -n "$namespace" wait --for=condition=Ready "pod/$pod" --timeout=600s >&2
wait_jsonpath 180 1 -n "$namespace" get "cluster.k0smotron.io/$cluster" -o jsonpath='{.status.readyReplicas}'
wait_not_empty 180 -n "$namespace" get secret "$cluster-kubeconfig" -o jsonpath='{.metadata.uid}'

cluster_uid=$(kubectl -n "$namespace" get "cluster.k0smotron.io/$cluster" -o jsonpath='{.metadata.uid}')
test -n "$cluster_uid"
kubectl -n "$namespace" get statefulset "$statefulset" -o json |
  jq -e --arg uid "$cluster_uid" --arg claim "$statefulset" '
    any(.metadata.ownerReferences[]; .kind == "Cluster" and .uid == $uid and .controller == true) and
    .spec.replicas == 1 and (.spec.persistentVolumeClaimRetentionPolicy.whenScaled // "Retain") == "Retain" and
    (.spec.persistentVolumeClaimRetentionPolicy.whenDeleted // "Retain") == "Retain" and
    ([.spec.volumeClaimTemplates[].metadata.name] == [$claim])' >/dev/null

for config in "$controller_config" "$entrypoint_config"; do
  kubectl -n "$namespace" get configmap "$config" -o json |
    jq -e --arg uid "$cluster_uid" '
      any(.metadata.ownerReferences[]; .kind == "Cluster" and .uid == $uid and .controller == true) and
      (.data | length) == 1 and ((.binaryData // {}) | length) == 0' >/dev/null
done
actual_entrypoint=$(kubectl -n "$namespace" get configmap "$entrypoint_config" -o json |
  jq -ej '.data["k0smotron-entrypoint.sh"] | select(type == "string")' | sha256sum)
expect_equal "$entrypoint_sha" "${actual_entrypoint%% *}" 'the generated SQLite entrypoint digest'
kubectl -n "$namespace" get configmap "$controller_config" -o json | jq -er '.data.K0SMOTRON_K0S_YAML' |
  awk '
    /^  storage:$/ { in_storage=1; storage++; next }
    /^  [^ ]/ { in_storage=0 }
    in_storage && /^    type: kine$/ { kine++ }
    in_storage && /^      dataSource: sqlite:\/\/\/var\/lib\/k0s\/state\.db$/ { sqlite++ }
    END { if (storage != 1 || kine != 1 || sqlite != 1) exit 1 }
  '

claim_json=$(kubectl -n "$namespace" get pvc "$claim" -o json)
jq -e '.status.phase == "Bound" and .spec.storageClassName == "block" and .spec.volumeMode == "Filesystem" and .spec.accessModes == ["ReadWriteOnce"]' <<< "$claim_json" >/dev/null
claim_uid=$(jq -er '.metadata.uid | select(length > 0)' <<< "$claim_json")
volume=$(jq -er '.spec.volumeName | select(length > 0)' <<< "$claim_json")
volume_uid=$(kubectl get pv "$volume" -o json |
  jq -er --arg ns "$namespace" --arg claim "$claim" --arg uid "$claim_uid" '
    select(.status.phase == "Bound" and .spec.storageClassName == "block" and
      .spec.csi.driver == "rook-ceph.rbd.csi.ceph.com" and (.spec.csi.volumeHandle | length) > 0 and
      (.spec.claimRef | .namespace == $ns and .name == $claim and .uid == $uid)) |
    .metadata.uid | select(length > 0)')

child_kubectl() {
  kubectl -n "$namespace" exec "$pod" -c controller -- k0s kubectl --server="https://$address:6443" --request-timeout=10s "$@"
}

retry_until 180 child_kubectl get --raw=/readyz
child_kubectl -n default create configmap "$marker" --from-literal=state=before-handoff >&2
marker_uid=$(child_kubectl -n default get configmap "$marker" -o jsonpath='{.metadata.uid}')
test -n "$marker_uid"

jq -n --arg ns "$namespace" --arg ns_uid "$namespace_uid" --arg cluster "$cluster" --arg uid "$cluster_uid" \
  --arg service "$service" --arg address "$address" --arg statefulset "$statefulset" --arg marker "$marker" \
  --arg pvc "$claim" --arg claim_uid "$claim_uid" --arg pv "$volume" --arg volume_uid "$volume_uid" --arg marker_uid "$marker_uid" \
  '{namespace:$ns, namespace_uid:$ns_uid, cluster:$cluster, cluster_uid:$uid, statefulset:$statefulset, service:$service,
    api_endpoint:("https://" + $address + ":6443"), konnectivity_endpoint:($address + ":8132"), claim:$pvc, claim_uid:$claim_uid,
    volume:$pv, volume_uid:$volume_uid, marker:$marker, marker_uid:$marker_uid}'
