#!/usr/bin/env bash
repo=${repo:?}
prefix=${prefix:?}
cpus=${cpus:?}
memory=${memory:?}
root_gb=${root_gb:?}
osd_gb=${osd_gb:?}
subnet=${subnet:?}
vip=${vip:?}
registry_port=${registry_port:?}
registry_name=${registry_name:?}
min_free_gb=${min_free_gb:?}
min_mem_gb=${min_mem_gb:?}
vm_path=${vm_path:?}
lab_dir=${lab_dir:?}
state_file=${state_file:?}
arch=${arch:?}
bundle=${bundle:?}
bedrock_bin=${bedrock_bin:?}
ids=("${ids[@]}")
letters=("${letters[@]}")
base_images=("${base_images[@]}")
vm_env=("${vm_env[@]}")
bundle_make_args=("${bundle_make_args[@]}")
source "$repo/hack/e2e/lib.sh"

log() {
  printf '== %s\n' "$*"
}

vm_name() {
  echo "$prefix-$1"
}

node_name() {
  echo "lima-$prefix-$1"
}

node_ip() {
  local name=IP_$1
  echo "${!name}"
}

disk_name() {
  echo "$prefix-$1-$2"
}

disk_path() {
  echo "/dev/disk/by-id/virtio-$(disk_name "$1" "$2")"
}

node_devices() {
  local id=$1 letter paths=()
  for letter in "${letters[@]}"; do
    paths+=("$(disk_path "$id" "$letter")")
  done
  echo "${paths[*]}"
}

qemu_variable() {
  echo "QEMU_SYSTEM_$(uname -m | tr '[:lower:]' '[:upper:]')"
}

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

state_set() {
  local key=$1 value=$2 tmp
  touch "$state_file"
  tmp=$(mktemp "$lab_dir/.state.XXXXXX")
  grep -v "^$key=" "$state_file" >"$tmp" || true
  printf '%s=%q\n' "$key" "$value" >>"$tmp"
  mv "$tmp" "$state_file"
}

state_load() {
  if [ -f "$state_file" ]; then
    source /dev/stdin <"$state_file"
  fi
}

state_capture() {
  local line key
  while IFS= read -r line; do
    case "$line" in
    STATE:*)
      key=${line#STATE:}
      if [ -z "${key#*=}" ]; then
        echo "empty value for ${key%%=*}" >&2
        return 1
      fi
      state_set "${key%%=*}" "${key#*=}"
      ;;
    esac
  done
}

require_state() {
  local name
  for name in "$@"; do
    if [ -z "${!name:-}" ]; then
      echo "$name is not recorded, run the earlier stages first" >&2
      return 1
    fi
  done
}

remote_prelude() {
  cat <<PRELUDE
set -euo pipefail
export KUBECONFIG=/var/lib/k0s/pki/admin.conf
. "$repo/hack/e2e/lib.sh"
PRELUDE
  remote_functions
}

vm_run() {
  local id=$1 script=$2
  shift 2
  limactl shell --workdir / "$(vm_name "$id")" sudo env HOME=/root PATH="$vm_path" "${vm_env[@]}" "$@" bash -c "$(remote_prelude)
$script" </dev/null
}

vm_put() {
  limactl copy "$2" "$(vm_name "$1"):$3"
}

vm_get() {
  limactl copy "$(vm_name "$1"):$2" "$3"
}

vm_remove() {
  if limactl list --quiet 2>/dev/null | grep -qx "$1"; then
    limactl delete --force "$1"
  fi
}

disk_remove() {
  limactl disk delete --force "$1" >/dev/null 2>&1 || true
}

lab_down() {
  local id letter
  for id in "${ids[@]}"; do
    vm_remove "$(vm_name "$id")"
  done
  for id in "${ids[@]}"; do
    for letter in "${letters[@]}"; do
      disk_remove "$(disk_name "$id" "$letter")"
    done
  done
  registry_down
}

require_host() {
  local tool missing=0
  for tool in limactl docker go helm make curl envsubst jq python3; do
    if ! command -v "$tool" >/dev/null; then
      echo "$tool is required" >&2
      missing=1
    fi
  done
  if [ "$(uname -s)" != Linux ]; then
    echo "hack/lab-adopt.sh runs on a Linux host with KVM" >&2
    missing=1
  fi
  if [ ! -w /dev/kvm ]; then
    echo "/dev/kvm must be writable for $(id -un)" >&2
    missing=1
  fi
  return "$missing"
}

guard_host() {
  local free mem
  free=$(df -Pk "$repo" | awk 'NR==2 {print int($4/1048576)}')
  mem=$(awk '/^MemAvailable:/ {print int($2/1048576)}' /proc/meminfo)
  if [ "$free" -lt "$min_free_gb" ]; then
    echo "only ${free}GiB free on $repo, need ${min_free_gb}GiB" >&2
    return 1
  fi
  if [ "$mem" -lt "$min_mem_gb" ]; then
    echo "only ${mem}GiB RAM available, need ${min_mem_gb}GiB for three VMs" >&2
    return 1
  fi
  if limactl list --quiet 2>/dev/null | grep -qx bedrock-ci; then
    echo "the VM bedrock-ci exists, a gate runs or left it behind" >&2
    return 1
  fi
  if limactl list --quiet 2>/dev/null | grep -E '^(adoptlab|multilab)-' | grep -qv "^$prefix-"; then
    echo "a VM of another lab exists, both labs share the network user-v2 and the address $vip" >&2
    return 1
  fi
}

ensure_images() {
  local image
  for image in "${base_images[@]}"; do
    docker image inspect "$image" >/dev/null 2>&1 || retry 5 docker pull "$image"
  done
}

registry_up() {
  registry_down
  docker run --rm -d -p "127.0.0.1:$registry_port:5000" --name "$registry_name" registry:3
  retry_until 60 curl -sf "http://localhost:$registry_port/v2/"
}

registry_down() {
  docker rm -f "$registry_name" >/dev/null 2>&1 || true
}

remove_bundle_images() {
  docker image rm "localhost:$registry_port/bedrock:dev" >/dev/null 2>&1 || true
}

bundle_build() {
  local image=localhost:$registry_port/bedrock:dev
  ensure_images
  registry_up
  make build release binaries VERSION=dev IMAGE="$image" PIN_DIGESTS=1 "$@"
  retry 3 docker build -t "$image" -f Containerfile .
  retry 3 docker push "$image"
  make bundle VERSION=dev IMAGE="$image" PIN_DIGESTS=1 BUNDLE_ARCH="$arch" "$@"
  registry_down
  remove_bundle_images
  test -f "$bundle"
  test -x "$bedrock_bin"
}

qemu_wrapper() {
  cat <<'WRAPPER'
#!/usr/bin/env bash
set -euo pipefail
args=()
for arg in "$@"; do
  case "$arg" in
  file=*/_disks/*/datadisk,if=virtio,*)
    rest=${arg#*/_disks/}
    name=${rest%%/*}
    args+=("${arg/,if=virtio,/,if=none,id=$name,}" -device "virtio-blk-pci,drive=$name,serial=$name")
    ;;
  socket,id=*,fd=*)
    descriptor=${arg##*,fd=}
    rest=${arg#socket,}
    args+=("stream,${rest%,fd=*},server=off,addr.type=fd,addr.str=$descriptor")
    ;;
  *) args+=("$arg") ;;
  esac
done
exec qemu-system-@ARCH@ "${args[@]}"
WRAPPER
}

qemu_wrapper_write() {
  qemu_wrapper | sed "s/@ARCH@/$(uname -m)/" >"$lab_dir/qemu-serial.sh"
  chmod +x "$lab_dir/qemu-serial.sh"
}

vm_provision() {
  cat <<'SCRIPT'
#!/bin/sh
set -e
sysctl -w fs.inotify.max_user_instances=8192 fs.inotify.max_user_watches=1048576 vm.max_map_count=262144
cat >/usr/local/bin/kubectl <<'KUBECTL'
#!/bin/sh
exec /usr/local/bin/k0s kubectl "$@"
KUBECTL
chmod +x /usr/local/bin/kubectl
SCRIPT
}

disk_entries() {
  local id=$1 letter
  for letter in "${letters[@]}"; do
    printf -- '- name: %s\n  format: false\n' "$(disk_name "$id" "$letter")"
  done
}

vm_config() {
  local id=$1
  cat <<CONFIG
base:
- template:_images/ubuntu-24.04
vmType: qemu
cpus: $cpus
memory: ${memory}GiB
disk: ${root_gb}GiB
mounts:
- location: "$repo"
  writable: false
containerd:
  system: false
  user: false
networks:
- lima: user-v2
additionalDisks:
$(disk_entries "$id")
provision:
- mode: system
  script: |
$(vm_provision | sed 's/^/    /')
CONFIG
}

vm_up() {
  local id=$1 vm letter
  vm=$(vm_name "$id")
  for letter in "${letters[@]}"; do
    limactl disk create "$(disk_name "$id" "$letter")" --size "${osd_gb}GiB" --format raw --tty=false
  done
  vm_config "$id" >"$lab_dir/$vm.yaml"
  env "$(qemu_variable)=$lab_dir/qemu-serial.sh" limactl start --name "$vm" --tty=false "$lab_dir/$vm.yaml"
}

script_prepare_vm() {
  cat <<'SCRIPT'
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq jq "linux-modules-extra-$(uname -r)" >/dev/null
modprobe openvswitch
modprobe geneve
SCRIPT
}

vm_prepare() {
  vm_run "$1" "$(script_prepare_vm)"
}

script_verify_vm() {
  cat <<'SCRIPT'
test "$(hostname)" = "$EXPECT_HOSTNAME"
test "$(ip -4 -o addr show dev eth0 | grep -cw "$EXPECT_IP/24")" -ge 1
test "$(ip -json route show default | jq -r '.[0].dev')" = eth0
for device in $DEVICES; do
  test -b "$device"
done
curl -sS --max-time 20 -o /dev/null https://quay.io/
for peer in $PEERS; do
  ping -c 2 -W 2 "$peer" >/dev/null
done
if test -c /dev/kvm; then
  echo "kvm: yes"
else
  echo "kvm: no, virtualization.emulation will be true"
fi
SCRIPT
}

discover_ip() {
  vm_run "$1" "ip -4 -o addr show dev eth0 | awk '{print \$4}' | cut -d/ -f1 | head -n 1"
}

record_ips() {
  local id ip
  for id in "${ids[@]}"; do
    ip=$(discover_ip "$id")
    case "$ip" in
    "$subnet".*) state_set "IP_$id" "$ip" ;;
    *)
      echo "$(vm_name "$id") has the address '$ip', not in $subnet.0/24" >&2
      return 1
      ;;
    esac
  done
  state_load
}

verify_vm() {
  local id=$1 peers=() other
  for other in "${ids[@]}"; do
    if [ "$other" != "$id" ]; then
      peers+=("$(node_ip "$other")")
    fi
  done
  vm_run "$id" "$(script_verify_vm)" EXPECT_HOSTNAME="$(node_name "$id")" EXPECT_IP="$(node_ip "$id")" DEVICES="$(node_devices "$id")" PEERS="${peers[*]}"
}

lab_up() {
  require_host
  mkdir -p "$lab_dir"
  lab_down
  rm -f "$state_file"
  guard_host
  bundle_build "${bundle_make_args[@]}"
  qemu_wrapper_write
  local id
  for id in "${ids[@]}"; do
    log "starting $(vm_name "$id")"
    vm_up "$id"
  done
  for id in "${ids[@]}"; do
    log "preparing $(vm_name "$id")"
    vm_prepare "$id"
  done
  record_ips
  for id in "${ids[@]}"; do
    log "checking $(vm_name "$id")"
    verify_vm "$id"
  done
}

script_offline_registry() {
  cat <<'SCRIPT'
mkdir -p /etc/k0s/containerd.d/certs.d/_default
printf '[plugins."io.containerd.cri.v1.images".registry]\nconfig_path = "/etc/k0s/containerd.d/certs.d"\n' >/etc/k0s/containerd.d/cri-registry.toml
printf 'server = "https://127.0.0.1:1"\n' >/etc/k0s/containerd.d/certs.d/_default/hosts.toml
SCRIPT
}

verdict() {
  local name=$1 check=$2 out log status=0
  shift 2
  log=$lab_dir/check-$(printf '%s' "$name" | tr -c 'A-Za-z0-9' '-' | cut -c1-60).log
  out=$("$check" "$@" 2>&1) || status=$?
  printf '%s\n' "$out" >"$log"
  if [ "$status" -eq 0 ]; then
    printf 'PASS %s\n' "$name"
    printf '%s\n' "$out" | grep '^info: ' || true
    return 0
  fi
  printf 'FAIL %s: %s\n' "$name" "$(printf '%s' "$out" | tail -n 1 | cut -c1-240)"
  printf '%s\n' "$out" | grep '^info: ' || true
  printf 'output: %s\n' "$log"
  return 1
}

vm_disk_files() {
  local id=$1 letter
  echo "${LIMA_HOME:-$HOME/.lima}/$(vm_name "$id")/disk"
  for letter in "${letters[@]}"; do
    echo "${LIMA_HOME:-$HOME/.lima}/_disks/$(disk_name "$id" "$letter")/datadisk"
  done
}

vm_start_existing() {
  env "$(qemu_variable)=$lab_dir/qemu-serial.sh" limactl start "$(vm_name "$1")" --tty=false
}

lab_snapshot() {
  local tag=$1 id file
  for id in "${ids[@]}"; do
    limactl stop "$(vm_name "$id")"
  done
  for id in "${ids[@]}"; do
    while IFS= read -r file; do
      qemu-img snapshot -c "$tag" "$file"
    done < <(vm_disk_files "$id")
  done
  for id in "${ids[@]}"; do
    vm_start_existing "$id"
  done
}

lab_rewind() {
  local tag=$1 id file
  for id in "${ids[@]}"; do
    limactl stop --force "$(vm_name "$id")"
  done
  for id in "${ids[@]}"; do
    while IFS= read -r file; do
      qemu-img snapshot -a "$tag" "$file"
    done < <(vm_disk_files "$id")
  done
  for id in "${ids[@]}"; do
    vm_start_existing "$id"
  done
}
