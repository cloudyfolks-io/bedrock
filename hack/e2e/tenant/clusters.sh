clusters_proof_name=cproof
clusters_proof_address=10.100.0.220
clusters_proof_version=v1.36.3+k0s.1
clusters_proof_pod_cidr=100.64.0.0/16
clusters_proof_service_cidr=100.65.0.0/16

clusters_proof_enable_addon() {
  kubectl patch addon clusters --type merge -p '{"spec":{"enabled":true}}'
  wait_condition 300 addon clusters Ready True
  wait_jsonpath 120 1 -n k0smotron get deployment k0smotron-controller-manager -o jsonpath='{.status.availableReplicas}'
}

clusters_proof_annotations() {
  local group
  group=$(fabric_by_label security-groups SecurityGroup web -o jsonpath='{.items[0].metadata.name}')
  cat <<ANNOTATIONS
v1.multus-cni.io/default-network: $NS/default
default.$NS.fabric.cloudyfolks.io/port_security: "true"
default.$NS.fabric.cloudyfolks.io/security_groups: $group
default.$NS.fabric.cloudyfolks.io/ip_address: $clusters_proof_address
ANNOTATIONS
}

clusters_proof_patch() {
  cat <<PATCH
spec:
  template:
    metadata:
      annotations:
$(clusters_proof_annotations | sed 's/^/        /')
PATCH
}

clusters_proof_cluster() {
  cat <<CLUSTER
apiVersion: k0smotron.io/v1beta2
kind: Cluster
metadata:
  name: $clusters_proof_name
spec:
  version: $clusters_proof_version
  replicas: 1
  externalAddress: $clusters_proof_address
  service:
    type: ClusterIP
    apiPort: 6443
    konnectivityPort: 8132
  storage:
    type: kine
    kine:
      dataSourceURL: sqlite:///var/lib/k0s/db/state.db
  persistence:
    type: pvc
    autoDeletePVCs: true
    persistentVolumeClaim:
      spec:
        accessModes: [ReadWriteOnce]
        storageClassName: block
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
      api:
        sans: ["$clusters_proof_address"]
      network:
        provider: kuberouter
        podCIDR: $clusters_proof_pod_cidr
        serviceCIDR: $clusters_proof_service_cidr
  patches:
    - target:
        kind: StatefulSet
        component: control-plane
      patch:
        type: strategic
        content: |
$(clusters_proof_patch | sed 's/^/          /')
CLUSTER
}

clusters_proof_token_request() {
  cat <<REQUEST
apiVersion: k0smotron.io/v1beta2
kind: JoinTokenRequest
metadata:
  name: $clusters_proof_name-w1
spec:
  clusterName: $clusters_proof_name
  expiry: 24h
  role: worker
REQUEST
}

clusters_proof_pod() {
  echo "kmc-$clusters_proof_name-0"
}

clusters_proof_pod_ready() {
  [ "$(kubectl -n "$NS" get pod "$(clusters_proof_pod)" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ]
}

clusters_proof_pod_uid() {
  kubectl -n "$NS" get pod "$(clusters_proof_pod)" -o jsonpath='{.metadata.uid}'
}

clusters_proof_token_ids() {
  kubectl -n "$NS" exec "$(clusters_proof_pod)" -- k0s token list --role worker
}

clusters_proof_token_listed() {
  [[ "$(clusters_proof_token_ids)" == *"$1"* ]]
}

clusters_proof_first_pod() {
  clusters_proof_cluster | kubectl -n "$NS" apply -f -
  wait_not_empty 180 -n "$NS" get pod "$(clusters_proof_pod)" -o jsonpath='{.metadata.uid}'
  first_uid=$(clusters_proof_pod_uid)
  wait_not_empty 120 -n "$NS" get pod "$(clusters_proof_pod)" -o jsonpath='{.status.podIP}'
  expect_equal "$clusters_proof_address" "$(kubectl -n "$NS" get pod "$(clusters_proof_pod)" -o jsonpath='{.status.podIP}')" "address of the first control plane pod"
  retry_until 600 clusters_proof_pod_ready
  expect_equal "$first_uid" "$(clusters_proof_pod_uid)" "the first pod never restarted"
  expect_equal 0 "$(kubectl -n "$NS" get pod "$(clusters_proof_pod)" -o jsonpath='{.status.containerStatuses[0].restartCount}')" "restarts of the control plane container"
  expect_equal "$(kubectl -n "$NS" get statefulset "kmc-$clusters_proof_name" -o jsonpath='{.status.currentRevision}')" \
    "$(kubectl -n "$NS" get statefulset "kmc-$clusters_proof_name" -o jsonpath='{.status.updateRevision}')" "no StatefulSet rollout after the create"
}

clusters_proof_shape() {
  local probes
  expect_equal control-plane "$(kubectl -n "$NS" get statefulset "kmc-$clusters_proof_name" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/component}')" "component label of the StatefulSet"
  probes=$(kubectl -n "$NS" get statefulset "kmc-$clusters_proof_name" -o json | jq -c '.spec.template.spec.containers[0] | {readiness: .readinessProbe.exec.command, liveness: .livenessProbe.exec.command, httpGet: [.readinessProbe.httpGet, .livenessProbe.httpGet], tcp: [.readinessProbe.tcpSocket, .livenessProbe.tcpSocket]}')
  echo "PROOF-PROBES $probes"
  expect_equal '{"readiness":["k0s","status"],"liveness":["k0s","status"],"httpGet":[null,null],"tcp":[null,null]}' "$probes" "probes of the control plane container"
  expect_equal "6443 8132" "$(kubectl -n "$NS" get statefulset "kmc-$clusters_proof_name" -o jsonpath='{.spec.template.spec.containers[0].ports[*].containerPort}')" "container ports"
  expect_equal 1 "$(kubectl -n "$NS" get cluster.k0smotron.io "$clusters_proof_name" -o jsonpath='{.status.readyReplicas}')" "readyReplicas of the Cluster"
  expect_equal 1 "$(kubectl -n "$NS" get pod "$(clusters_proof_pod)" -o json | jq '[.status.podIPs[]] | length')" "one pod address"
  expect_equal 1 "$(kubectl -n "$NS" exec "$(clusters_proof_pod)" -- sh -c "ip -o link show | grep -vc ' lo:'")" "control plane pod interfaces"
  expect_contains "$(kubectl -n "$NS" exec "$(clusters_proof_pod)" -- cat /etc/k0s/k0s.yaml)" "agentPort: 8132" "konnectivity port in the k0s config"
  expect_contains "$(kubectl -n "$NS" exec "$(clusters_proof_pod)" -- cat /etc/k0s/k0s.yaml)" "externalAddress: $clusters_proof_address" "external address in the k0s config"
  expect_contains "$(kubectl -n "$NS" exec "$(clusters_proof_pod)" -- cat /etc/k0s/k0s.yaml)" "$clusters_proof_address" "address in the API sans"
}

clusters_proof_kubeconfig() {
  local config
  wait_not_empty 180 -n "$NS" get secret "$clusters_proof_name-kubeconfig" -o jsonpath='{.data.value}'
  config=$(kubectl -n "$NS" get secret "$clusters_proof_name-kubeconfig" -o jsonpath='{.data.value}' | base64 -d)
  echo "PROOF-KUBECONFIG server $(printf '%s' "$config" | sed -n 's/^ *server: //p')"
  expect_equal "https://$clusters_proof_address:6443" "$(printf '%s' "$config" | sed -n 's/^ *server: //p')" "server of the kubeconfig"
  expect_equal "$clusters_proof_name-kubeconfig" "$(kubectl -n "$NS" get secret "$clusters_proof_name-kubeconfig" -o jsonpath='{.metadata.name}')" "name of the kubeconfig Secret"
}

clusters_proof_join_token() {
  local token_id
  clusters_proof_token_request | kubectl -n "$NS" apply -f -
  wait_not_empty 180 -n "$NS" get jointokenrequest.k0smotron.io "$clusters_proof_name-w1" -o jsonpath='{.status.tokenID}'
  token_id=$(kubectl -n "$NS" get jointokenrequest.k0smotron.io "$clusters_proof_name-w1" -o jsonpath='{.status.tokenID}')
  wait_not_empty 60 -n "$NS" get secret "$clusters_proof_name-w1" -o jsonpath='{.data.token}'
  echo "PROOF-TOKEN secret $NS/$clusters_proof_name-w1 key token prefix $(kubectl -n "$NS" get secret "$clusters_proof_name-w1" -o jsonpath='{.data.token}' | base64 -d | head -c 12)"
  retry_until 60 clusters_proof_token_listed "$token_id"
  kubectl -n "$NS" delete joinTokenRequest.k0smotron.io "$clusters_proof_name-w1"
  wait_gone 120 -n "$NS" joinTokenRequest.k0smotron.io "$clusters_proof_name-w1"
  wait_gone 60 -n "$NS" secret "$clusters_proof_name-w1"
  if clusters_proof_token_listed "$token_id"; then
    echo "PROOF-TOKEN deleting the request keeps the token valid"
  else
    echo "PROOF-TOKEN deleting the request invalidates the token"
  fi
}

clusters_proof_cleanup() {
  if [ "$1" -ne 0 ]; then
    kubectl -n "$NS" get cluster.k0smotron.io,joinTokenRequest.k0smotron.io,statefulset,pod,pvc,secret -o wide >&2 || true
    kubectl -n "$NS" describe pod "$(clusters_proof_pod)" >&2 || true
    kubectl -n "$NS" logs "$(clusters_proof_pod)" --tail=100 >&2 || true
    kubectl -n k0smotron logs deploy/k0smotron-controller-manager --tail=200 >&2 || true
  fi
  kubectl -n "$NS" delete joinTokenRequest.k0smotron.io "$clusters_proof_name-w1" --ignore-not-found --wait=false
  kubectl -n "$NS" delete cluster.k0smotron.io "$clusters_proof_name" --ignore-not-found
  wait_gone 300 -n "$NS" statefulset "kmc-$clusters_proof_name"
  wait_gone 300 -n "$NS" pvc "kmc-$clusters_proof_name-kmc-$clusters_proof_name-0"
}

clusters_proof() {
  if ! kubectl get storageclass block >/dev/null 2>&1; then
    echo "no block storage class: skipping the cluster proof"
    return 0
  fi
  (
    local first_uid
    trap 'clusters_proof_cleanup "$?"' EXIT
    clusters_proof_enable_addon
    clusters_proof_first_pod
    clusters_proof_shape
    clusters_proof_kubeconfig
    clusters_proof_join_token
  )
}
