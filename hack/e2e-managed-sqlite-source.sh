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
for tool in kubectl jq sha256sum awk base64 timeout; do
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

service_uid=$(kubectl -n "$namespace" get service "$service" -o jsonpath='{.metadata.uid}')
controller=$(kubectl -n "$namespace" get pod "$pod" -o json)
pod_uid=$(jq -er '.metadata.uid | select(length > 0)' <<<"$controller")
pod_address=$(jq -er '.status.podIP | select(length > 0)' <<<"$controller")
probe_node=$(jq -er '.spec.nodeName | select(length > 0)' <<<"$controller")
probe_image=$(jq -er '.spec.containers[] | select(.name == "controller") | .image' <<<"$controller")
test -n "$service_uid"

probe=$(jq -n --arg node "$probe_node" --arg image "$probe_image" '
  {apiVersion:"v1",kind:"Pod",metadata:{generateName:"sqlite-source-probe-"},spec:{nodeName:$node,automountServiceAccountToken:false,
    restartPolicy:"Never",activeDeadlineSeconds:420,containers:[{name:"client",image:$image,imagePullPolicy:"Never",
      command:["/bin/sh","-c","exec sleep 420"],securityContext:{allowPrivilegeEscalation:false,capabilities:{drop:["ALL"]}}}]}}' |
  kubectl -n "$namespace" create -f - -o json)
probe_name=$(jq -er '.metadata.name | select(test("^sqlite-source-probe-[a-z0-9]+$"))' <<<"$probe")
probe_uid=$(jq -er '.metadata.uid | select(length > 0)' <<<"$probe")

remove_probe() {
  local status=$?
  trap - EXIT
  if ! jq -nc --arg uid "$probe_uid" '{apiVersion:"v1",kind:"DeleteOptions",preconditions:{uid:$uid}}' |
    kubectl --request-timeout=10s delete --raw "/api/v1/namespaces/$namespace/pods/$probe_name" -f - >/dev/null; then
    printf '%s\n' 'Could not remove the SQLite client Pod by UID.' >&2
    exit 1
  fi
  exit "$status"
}
trap remove_probe EXIT
kubectl -n "$namespace" wait --for=condition=Ready "pod/$probe_name" --timeout=90s >&2

child_credentials() {
  kubectl --request-timeout=10s -n "$namespace" get secret "$cluster-kubeconfig" -o json |
    jq -er --arg uid "$cluster_uid" '
      select(any(.metadata.ownerReferences[]; .kind == "Cluster" and .uid == $uid and .controller == true)) |
      .data.value | select(type == "string" and length > 0)' | base64 -d
}

if ! configured_endpoint=$(child_credentials | kubectl --kubeconfig=/dev/stdin config view --minify -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null) ||
  [[ $configured_endpoint != "https://$address:6443" ]]; then
  printf '%s\n' 'The generated child kubeconfig does not identify the private API endpoint.' >&2
  exit 1
fi

child_kubectl() {
  child_credentials | timeout 18s kubectl --request-timeout=15s -n "$namespace" exec -i "$probe_name" -c client -- \
    k0s kubectl --kubeconfig=/dev/stdin --request-timeout=10s "$@"
}

endpoint_slices() {
  kubectl --request-timeout=7s -n "$namespace" get endpointslices.discovery.k8s.io -l "kubernetes.io/service-name=$service" -o json
}

wait_private_api() {
  local deadline=$((SECONDS + 180)) first=true slices result reason direct phase
  while :; do
    slices=$(endpoint_slices 2>/dev/null) || slices='{"items":[]}'
    if ! jq -e --arg service "$service_uid" --arg pod "$pod_uid" --arg ip "$pod_address" '
      .items | length == 1 and all(.[]; .addressType == "IPv4" and any(.metadata.ownerReferences[]; .kind == "Service" and .uid == $service) and
        ([.ports[] | [.port,.protocol]] | sort) == [[6443,"TCP"],[8132,"TCP"]] and
        (.endpoints | length == 1 and all(.[]; .conditions.ready == true and .targetRef.uid == $pod and .addresses == [$ip])))
    ' <<<"$slices" >/dev/null 2>&1; then
      reason='the Service has no matching Ready backend for both ports'
    elif result=$(child_kubectl get --raw=/readyz 2>&1) && [[ $result == ok ]]; then
      return 0
    else
      case $result in
        *x509*|*'TLS handshake'*) reason='TLS validation or handshake failed' ;;
        *Unauthorized*|*Forbidden*|*'provide credentials'*) reason='the child API refused client authentication or access' ;;
        *'connection refused'*|*'no route to host'*|*timeout*|*'deadline exceeded'*) reason='the client could not reach the private API in time' ;;
        *'config'*|*'certificate data'*) reason='the client could not use the generated kubeconfig' ;;
        *) reason='the child API request failed or returned an unexpected readiness result' ;;
      esac
    fi
    if [[ $first == true ]] || (( SECONDS >= deadline )); then
      phase=first
      if (( SECONDS >= deadline )); then phase=last; fi
      printf 'The %s SQLite private API probe from %s to %s reported that %s.\n' "$phase" "$probe_name" "$address" "$reason" >&2
      jq -c '[.items[:4][] | {ports:[(.ports // [])[:4][] | [.port,.protocol]],ready:[(.endpoints // [])[:4][] | .conditions.ready]}]' <<<"$slices" >&2
      if direct=$(child_kubectl --server="https://$pod_address:6443" --tls-server-name="$address" get --raw=/readyz 2>&1) && [[ $direct == ok ]]; then
        printf '%s\n' 'The direct controller probe passed with the same TLS server identity.' >&2
      else
        printf '%s\n' 'The direct controller probe did not return a verified readiness response.' >&2
      fi
      first=false
    fi
    (( SECONDS < deadline )) || return 1
    sleep 3
  done
}

wait_private_api
if ! child_kubectl -n default create configmap "$marker" --from-literal=state=before-handoff >/dev/null 2>&1; then
  printf '%s\n' 'The verified private API did not create the continuity marker.' >&2
  exit 1
fi
if ! marker_uid=$(child_kubectl -n default get configmap "$marker" -o jsonpath='{.metadata.uid}' 2>/dev/null) || [[ -z $marker_uid ]]; then
  printf '%s\n' 'The verified private API did not return the continuity marker UID.' >&2
  exit 1
fi

jq -n --arg ns "$namespace" --arg ns_uid "$namespace_uid" --arg cluster "$cluster" --arg uid "$cluster_uid" \
  --arg service "$service" --arg address "$address" --arg statefulset "$statefulset" --arg marker "$marker" \
  --arg pvc "$claim" --arg claim_uid "$claim_uid" --arg pv "$volume" --arg volume_uid "$volume_uid" --arg marker_uid "$marker_uid" \
  '{namespace:$ns, namespace_uid:$ns_uid, cluster:$cluster, cluster_uid:$uid, statefulset:$statefulset, service:$service,
    api_endpoint:("https://" + $address + ":6443"), konnectivity_endpoint:($address + ":8132"), claim:$pvc, claim_uid:$claim_uid,
    volume:$pv, volume_uid:$volume_uid, marker:$marker, marker_uid:$marker_uid}'
