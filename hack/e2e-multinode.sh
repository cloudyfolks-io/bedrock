#!/usr/bin/env bash
set -euo pipefail

prefix=multilab
ids=(1 2 3)
letters=(a b)
cpus=4
memory=14
root_gb=100
osd_gb=20
subnet=192.168.104
vip=$subnet.250
iface=eth0
platform_host=multinode-lab.bedrock.test
registry_port=${LAB_REGISTRY_PORT:-5002}
registry_name=$prefix-registry
min_free_gb=${LAB_MIN_FREE_GB:-25}
min_mem_gb=${LAB_MIN_MEM_GB:-46}
remote_config_file=/root/multinode.yaml
join_roles=control-plane,ceph-osd,fabric-gateway,workload
release_config=dist/components-e2e.yaml
remote_files=(tenant/tenancy tenant/images multinode/cluster multinode/guest)
vm_env=()
bundle_make_args=("RELEASE_CONFIG=$release_config")
vm_path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
base_images=(golang:1.27 gcr.io/distroless/static:nonroot registry:3 node:24-bookworm-slim)

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
lab_dir=$repo/dist/lab-multinode
state_file=$lab_dir/state.env
bundle=$repo/dist/bedrock-dev-bundle-$arch.tar.zst
bedrock_bin=$repo/dist/bedrock-dev-linux-$arch
snapshot_tag=after-tenant

source "$repo/hack/lab/lib.sh"

usage() {
  cat <<USAGE
usage: hack/e2e-multinode.sh <stage>

stages, in order:
  up        build the bundle, start three lima VMs, check the network and the disks
  install   run bedrock init on node 1 and bedrock join on nodes 2 and 3, wait for the storage
  tenant    create a tenant, two projects and the cirros image
  check     run every check, print PASS or FAIL for each, write dist/lab-multinode/result.txt
  snapshot  stop the VMs, snapshot every disk, start the VMs; run it after tenant
  rewind    restore the snapshot of every disk and start the VMs again
  down      remove the lab VMs and their disks
  all       up, install, tenant and check; the VMs stay, run down after

The lab needs a Linux host with KVM, about 46 GiB of free RAM and no running gate or adopt lab.
USAGE
}

remote_functions() {
  cat <<FUNCTIONS
repo=$repo
bedrock_bin=$bedrock_bin
bundle=$bundle
cd "\$repo"
for file in ${remote_files[*]}; do
  . "\$repo/hack/e2e/\$file.sh"
done
FUNCTIONS
}

remote_check() {
  vm_run 1 "$1"
}

node_devices_json() {
  node_devices "$1" | tr ' ' '\n' | jq -R . | jq -sc .
}

render_config() {
  local emulation=$1
  VERSION=dev VIP=$vip IFACE=$iface PLATFORM_HOST=$platform_host DEVICES=$(flow_list "$(node_devices 1 | sed 's/ /, /g')") EMULATION=$emulation \
    envsubst "\${VERSION} \${VIP} \${IFACE} \${PLATFORM_HOST} \${DEVICES} \${EMULATION}" <"$repo/hack/lab/multinode.yaml.tmpl"
}

stage_up() {
  "$repo/hack/e2e-release-config.sh" "$repo/release/components.yaml" "$repo/$release_config"
  lab_up
}

script_install_config() {
  cat <<'SCRIPT'
install -m 0600 /tmp/multinode.yaml "$CONFIG_FILE"
rm -f /tmp/multinode.yaml
SCRIPT
}

script_init() {
  cat <<'SCRIPT'
cd "$REPO"
"$BIN" init -f "$CONFIG_FILE" --bundle "$BUNDLE" --timeout 45m 2>&1 | awk '!/^admin password: /'
SCRIPT
}

script_join() {
  cat <<'SCRIPT'
cd "$REPO"
"$BIN" join --token "$TOKEN" --bundle "$BUNDLE" 2>&1
SCRIPT
}

script_host_devices() {
  cat <<'SCRIPT'
retry_until 600 kubectl get host "$NODE"
kubectl patch host "$NODE" --type merge -p "{\"spec\":{\"storage\":{\"devices\":$DEVICES}}}"
SCRIPT
}

join_node() {
  local id=$1 token=$2
  vm_run "$id" "$(script_join)" REPO="$repo" BIN="$bedrock_bin" BUNDLE="$bundle" TOKEN="$token"
}

stage_install() {
  local id token emulation
  state_load
  require_state IP_1 IP_2 IP_3
  test -f "$bundle"
  mkdir -p "$lab_dir"
  emulation=$(vm_run 1 'if test -c /dev/kvm; then echo false; else echo true; fi')
  render_config "$emulation" >"$lab_dir/multinode.yaml"
  cat "$lab_dir/multinode.yaml"
  vm_put 1 "$lab_dir/multinode.yaml" /tmp/multinode.yaml
  for id in "${ids[@]}"; do
    vm_run "$id" "$(script_offline_registry)"
  done
  vm_run 1 "$(script_install_config)" CONFIG_FILE="$remote_config_file"
  log "bedrock init on $(vm_name 1)"
  vm_run 1 "$(script_init)" REPO="$repo" BIN="$bedrock_bin" BUNDLE="$bundle" CONFIG_FILE="$remote_config_file"
  for id in 2 3; do
    token=$(vm_run 1 "$bedrock_bin token create --roles $join_roles --expiry 1h")
    log "bedrock join on $(vm_name "$id")"
    join_node "$id" "$token"
    log "OSD devices of $(node_name "$id")"
    vm_run 1 "$(script_host_devices)" NODE="$(node_name "$id")" DEVICES="$(node_devices_json "$id")"
  done
  log "waiting for the cluster"
  vm_run 1 wait_install
}

stage_tenant() {
  state_load
  vm_run 1 setup_tenant
}

check_entries=(
  "three control-plane nodes|remote_check check_control_plane"
  "Ceph runs 3 monitors and 6 OSDs on 3 hosts|remote_check check_ceph_topology"
  "every replicated pool has size 3|remote_check check_pool_sizes"
)

collect_diagnostics() {
  vm_run 1 diagnose_vms >"$lab_dir/diag-$1.log" 2>&1 || true
}

run_checks() {
  local failed=0 entry n=0 words
  state_load
  for entry in "${check_entries[@]}"; do
    n=$((n + 1))
    read -r -a words <<<"${entry#*|}"
    if ! verdict "${entry%%|*}" "${words[@]}"; then
      failed=1
      collect_diagnostics "$n"
    fi
  done
  if [ "$failed" -eq 1 ]; then
    echo "e2e-multinode: at least one check failed"
  fi
  return "$failed"
}

stage_check() {
  mkdir -p "$lab_dir"
  rm -f "$lab_dir"/diag-*.log
  run_checks | tee "$lab_dir/result.txt"
}

stage_snapshot() {
  lab_snapshot "$snapshot_tag"
}

stage_rewind() {
  lab_rewind "$snapshot_tag"
}

stage_down() {
  lab_down
  remove_bundle_images
  rm -f "$state_file" "$lab_dir"/diag-*.log "$lab_dir/multinode.yaml"
}

stage_all() {
  stage_up
  stage_install
  stage_tenant
  stage_check
}

main() {
  cd "$repo"
  case "${1:-}" in
  up | install | tenant | check | snapshot | rewind | down | all) "stage_$1" ;;
  *)
    usage >&2
    return 2
    ;;
  esac
}

main "$@"
