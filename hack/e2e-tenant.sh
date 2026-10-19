#!/usr/bin/env bash
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/e2e/lib.sh"

admin_password_file=${ADMIN_PASSWORD_FILE:-/var/lib/bedrock/e2e-admin-password}
bin=${BEDROCK_BIN:-bin/bedrock}
test_bin=${E2E_TEST:-dist/e2e-tenant.test}
cache=${E2E_TENANT_CACHE:-dist/cache/e2e-tenant}
steps_dir=$(dirname "${BASH_SOURCE[0]}")/e2e/tenant
workdir=$(mktemp -d)
export KUBECONFIG=/var/lib/k0s/pki/admin.conf
export BEDROCK_CA=$workdir/platform-ca.crt
export TOKEN_DIR=$workdir/tokens
export TENANT=acme
export PROJECT=web
export NS=acme-web
export S3_NS=backup-e2e

dump_cluster() {
  echo "--- bedrock kinds"
  dump_api_group bedrock.cloudyfolks.io
  echo "--- fabric kinds"
  dump_api_group fabric.cloudyfolks.io
  echo "--- kubevirt kinds"
  dump_api_group kubevirt.io
  dump_api_group snapshot.kubevirt.io
  dump_api_group instancetype.kubevirt.io
  echo "--- cdi kinds"
  dump_api_group cdi.kubevirt.io
  echo "--- snapshot kinds"
  dump_api_group snapshot.storage.k8s.io
  echo "--- velero kinds"
  dump_api_group velero.io
  echo "--- rook kinds"
  dump_api_group ceph.rook.io
  dump_api_group objectbucket.io
  echo "--- aaq kinds"
  dump_api_group aaq.kubevirt.io
  echo "--- storage"
  kubectl get storageclass,volumesnapshotclass,pv -o wide || true
  kubectl get pvc -A -o wide || true
}

dump_workloads() {
  echo "--- pods"
  kubectl get pods -A -o wide || true
  echo "--- project pods"
  kubectl -n "$NS" get pods,vmi,dv,pvc -o wide || true
  kubectl -n "$NS" describe pods || true
  echo "--- operator log"
  kubectl -n bedrock-system logs deploy/bedrock-operator --tail=500 || true
  echo "--- proxy log"
  kubectl -n bedrock-system logs deploy/bedrock-proxy --tail=300 --all-containers || true
  echo "--- authn log"
  kubectl -n bedrock-system logs deploy/bedrock-authn --tail=100 --all-containers || true
  echo "--- kubevirt logs"
  dump_logs kubevirt kubevirt.io=virt-controller
  dump_logs kubevirt kubevirt.io=virt-handler
  dump_logs kubevirt kubevirt.io=virt-api
  echo "--- launcher logs"
  kubectl -n "$NS" logs -l kubevirt.io=virt-launcher --tail=200 --all-containers || true
  echo "--- cdi logs"
  dump_logs cdi app=containerized-data-importer
  echo "--- aaq logs"
  kubectl -n aaq get pods -o wide || true
  kubectl -n aaq logs -l app.kubernetes.io/part-of=aaq --tail=100 --all-containers --prefix || true
  echo "--- fabric logs"
  kubectl -n kube-system logs deployment/fabric-controller --tail=300 --all-containers || true
  kubectl -n kube-system logs daemonset/fabric-cni --tail=100 --all-containers || true
  echo "--- ovn nat and load balancers"
  kubectl -n kube-system exec deployment/ovn-central -- ovn-nbctl lr-nat-list "$TENANT" 2>&1 || true
  kubectl -n kube-system exec deployment/ovn-central -- ovn-nbctl list Load_Balancer 2>&1 || true
  echo "--- underlay"
  ip -br addr show bdrpub0 2>&1 || true
  ip -n bdrext -br addr show 2>&1 || true
  ip -n bdrext neigh show 2>&1 || true
  echo "--- velero"
  kubectl -n velero get pods,backups,restores,schedules,backupstoragelocations -o wide || true
  kubectl -n velero logs deploy/velero --tail=300 || true
  kubectl -n velero logs daemonset/node-agent --tail=100 --all-containers || true
  echo "--- seaweedfs"
  kubectl -n "$S3_NS" get pods,jobs -o wide || true
  kubectl -n "$S3_NS" logs deploy/seaweedfs --tail=200 || true
  kubectl -n "$S3_NS" logs job/make-bucket --tail=50 || true
  echo "--- object storage"
  kubectl -n rook-ceph get pods,cephobjectstore,cephobjectstoreuser -o wide || true
  kubectl -n rook-ceph logs deploy/rook-ceph-operator --tail=150 || true
  kubectl -n objectstorage-e2e get jobs,pods -o wide || true
  kubectl -n objectstorage-e2e logs -l job-name --tail=50 --prefix || true
  dump_nmstate
  echo "--- monitoring and logging"
  kubectl -n monitoring get pods,pvc,addons -o wide || true
  kubectl get addons -o yaml || true
  echo "--- events"
  kubectl get events -A --sort-by=.lastTimestamp | tail -150 || true
  kubectl -n default get events --field-selector involvedObject.kind=Setting || true
}

dump() {
  local status=$?
  if [ "$status" -ne 0 ]; then
    dump_cluster
    dump_workloads
  fi
  if [ -z "${E2E_FROM:-}${E2E_ONLY:-}" ]; then
    e2e_cleanup
  fi
  rm -rf "$workdir"
}
trap dump EXIT

test -f "$admin_password_file"
test -x "$test_bin"
command -v jq >/dev/null
mkdir -p "$TOKEN_DIR"

vip=$(kubectl get cluster cluster -o jsonpath='{.spec.api.vip}')
host=$(kubectl get setting platform.host -o jsonpath='{.spec.value}')
test -n "$host"
export VIP=$vip HOST=$host
pin_hosts "$vip" "$host"
kubectl -n cert-manager get secret bedrock-ca -o jsonpath='{.data.ca\.crt}' | base64 -d >"$BEDROCK_CA"
test -s "$BEDROCK_CA"
install -m 0755 "$bin" /usr/local/bin/bedrock

k0s ctr --namespace k8s.io images import "$cache/seaweedfs.tar"
k0s ctr --namespace k8s.io images import "$cache/aws-cli.tar"

for step_file in encryption addons tenancy images compute network objectstorage backup panel metering cascade; do
  source "$steps_dir/$step_file.sh"
done

run_step() {
  local started=$SECONDS
  echo "=== $1"
  "$1"
  echo "=== $1 passed in $((SECONDS - started))s"
}

select_steps() {
  local from=${E2E_FROM:-} only=${E2E_ONLY:-} step reached=0
  for step in "$@"; do
    if [ -n "$only" ]; then
      [ "$step" = "$only" ] && echo "$step"
      continue
    fi
    if [ -z "$from" ] || [ "$step" = "$from" ]; then
      reached=1
    fi
    if [ "$reached" -eq 1 ]; then
      echo "$step"
    fi
  done
}

steps=(
  encryption_join_fetch
  addons_seeded
  addons_kata
  addons_loki_off
  addons_loki_on
  addons_invalid_values
  tenancy_users
  tenancy_tenant
  tenancy_projects
  tenancy_workloads
  tenancy_quota_edges
  images_cluster_image
  images_upload
  compute_prepare
  compute_volume
  compute_vm
  compute_hotplug
  compute_snapshot
  compute_proxy_actions
  compute_console
  network_underlay
  network_frr_agent
  network_subnet_rules
  network_security_group
  network_guest_parity
  network_elastic_ip
  network_elastic_ip_pool_choice
  network_elastic_ip_quota
  network_load_balancer_and_dns
  network_isolation
  objectstorage_user_and_bucket
  objectstorage_roundtrip
  backup_target
  backup_once
  backup_restore_replace
  backup_restore_deleted
  panel_served
  panel_login_and_stream
  metering_usage
  metering_series
  metering_network_totals
  metering_proxy_range
  metering_logs
  metering_reader_policy
  addons_grafana_and_alerts
  cascade_project
  cascade_tenant
)

selected=$(select_steps "${steps[@]}")
test -n "$selected"
if [ -n "${E2E_FROM:-}${E2E_ONLY:-}" ] && ! grep -qx tenancy_users <<<"$selected"; then
  issue_tokens
fi
for step in $selected; do
  run_step "$step"
done
echo "e2e-tenant passed"
