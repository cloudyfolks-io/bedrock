#!/usr/bin/env bash
set -euo pipefail

cluster=bedrock-e2e
image=ghcr.io/cloudyfolks-labs/bedrock:dev
tmpdir=$(mktemp -d)
crane=${CRANE:-crane}

retry() {
  local attempts=$1 i code=1
  shift
  for i in $(seq 1 "$attempts"); do
    "$@" && return 0
    code=$?
    sleep "$((i * 2))"
  done
  return "$code"
}

cleanup() {
  local status=$?
  if [ "$status" -ne 0 ]; then
    kubectl -n bedrock-system logs deploy/bedrock-operator --tail=200 || true
    kubectl get setting,host -o yaml || true
    kubectl get cluster,release -o yaml || true
  fi
  rm -rf "$tmpdir"
  kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
}
trap cleanup EXIT

kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
kind create cluster --name "$cluster" --wait 120s
make release VERSION=dev RELEASE_CONFIG=hack/e2e/components-kind.yaml
docker build -t "$image" -f Containerfile .
kind load docker-image "$image" --name "$cluster"
arch=$(docker version --format '{{.Server.Arch}}')
while read -r ref || [ -n "$ref" ]; do
  [ -z "$ref" ] && continue
  [ "$ref" = "$image" ] && continue
  retry 3 "$crane" pull --platform "linux/$arch" "$ref" "$tmpdir/image.tar"
  kind load image-archive "$tmpdir/image.tar" --name "$cluster"
done < dist/release/images.txt

kubectl apply -f manifests/00-crds
kubectl apply -f manifests/90-bedrock
kubectl -n bedrock-system rollout status deployment/bedrock-operator --timeout=120s

wait_for_resource() {
  local resource=$1
  local timeout=$2
  local waited=0
  until kubectl get "$resource" >/dev/null 2>&1; do
    waited=$((waited + 1))
    if [ "$waited" -ge "$timeout" ]; then
      return 1
    fi
    sleep 1
  done
}

wait_for_resource setting/platform.tls-mode 60
kubectl wait --for=jsonpath='{.status.default}'=SelfSigned setting/platform.tls-mode --timeout=60s

kubectl apply -f - <<EOF2
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Cluster
metadata:
  name: cluster
spec:
  desiredVersion: dev
  api:
    vip: 10.0.0.10
    vipMode: arp
  nodeConcurrency: 1
EOF2
wait_for_resource cluster/cluster 60
kubectl wait --for=jsonpath='{.status.version}'=dev cluster/cluster --timeout=900s
kubectl wait --for=condition=Available cluster/cluster --timeout=60s
kubectl -n cert-manager rollout status deployment/cert-manager --timeout=120s
kubectl -n cert-manager rollout status deployment/cert-manager-webhook --timeout=120s
kubectl -n kube-system rollout status deployment/monitoring-metrics-server --timeout=120s
kubectl -n bedrock-system get configmap bedrock-release-inventory -o jsonpath='{.data.objects}' | grep -q cert-manager
kubectl get release dev -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' | grep -q True

node=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
kubectl apply -f - <<EOF2
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Host
metadata:
  name: ${node}
spec:
  roles: [control-plane, ceph-osd, workload]
EOF2
kubectl wait --for=condition=Ready "host/${node}" --timeout=60s
kubectl get node "$node" -o jsonpath='{.metadata.labels}' | grep -q 'role-ceph-osd'

echo "e2e-kind passed"
