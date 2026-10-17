#!/usr/bin/env bash
set -euo pipefail

prefix=adoptlab
ids=(1 2 3)
letters=(a b)
cpus=4
memory=14
root_gb=60
osd_gb=20
subnet=192.168.104
vip=$subnet.250
iface=lima0
platform_host=adopt-lab.bedrock.test
registry_port=${LAB_REGISTRY_PORT:-5002}
registry_name=$prefix-registry
min_free_gb=${LAB_MIN_FREE_GB:-25}
min_mem_gb=${LAB_MIN_MEM_GB:-46}
k0s_version=v1.36.3+k0s.0
rook_chart_version=v1.20.6
rook_chart_repo=https://charts.rook.io/release
s3_access_key=LABACCESSKEY0000001
s3_secret_key=LABSECRETKEY00000000000000000000000001
remote_key_file=/root/adopt-keys.yaml
remote_config_file=/root/adopt.yaml
vm_path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
base_images=(golang:1.27 gcr.io/distroless/static:nonroot registry:3 node:24-bookworm-slim)

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
lab_dir=$repo/dist/lab
state_file=$lab_dir/state.env
bundle=$repo/dist/bedrock-dev-bundle-$arch.tar.zst
bedrock_bin=$repo/dist/bedrock-dev-linux-$arch
k0s_source=$repo/dist/cache/k0s/$k0s_version/$arch

source "$repo/hack/e2e/lib.sh"

usage() {
  cat <<USAGE
usage: hack/lab-adopt.sh <stage>

stages, in order:
  up      build the bundle, start three lima VMs, check the network and the disks
  old     install k0s and Rook with the old values, write the test data
  export  write the adopt key file and the adopt config, record the old state
  stop    set noout, stop Rook, copy /var/lib/rook, run k0s reset
  adopt   run bedrock init and bedrock join, wait for StorageAdopted
  check   run every assertion, print PASS or FAIL for each
  down    remove the lab VMs, their disks and the lab secrets
  all     up, old, export, stop, adopt and check; the VMs stay, run down after

The lab needs a Linux host with KVM, about 46 GiB of free RAM and no running gate.
USAGE
}

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
  echo "$subnet.1$1"
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
  cat <<'FUNCTIONS'
rook_conf=--conf=/var/lib/rook/rook-ceph/rook-ceph.config
op_exec() { kubectl -n rook-ceph exec deploy/rook-ceph-operator -- "$@"; }
op_exec_stdin() { kubectl -n rook-ceph exec -i deploy/rook-ceph-operator -- "$@"; }
ceph_op() { op_exec ceph "$rook_conf" --connect-timeout=10 "$@"; }
rbd_op() { op_exec rbd "$rook_conf" "$@"; }
rgw_op() { op_exec radosgw-admin "$rook_conf" "$@"; }
sha_of() { sha256sum | cut -d' ' -f1; }
rgw_service() { kubectl -n rook-ceph get svc "rook-ceph-rgw-$1" -o jsonpath='{.spec.clusterIP}:{.spec.ports[0].port}'; }
s3() { curl -sSf --max-time 60 --aws-sigv4 "aws:amz:us-east-1:s3" --user "$S3_ACCESS:$S3_SECRET" -H "x-amz-content-sha256: UNSIGNED-PAYLOAD" "$@"; }
mon_ids() { ceph_op quorum_status -f json | jq -r '.quorum_names | sort | join(",")'; }
osd_map() { ceph_op osd tree -f json | jq -r '. as $t | [$t.nodes[] | select(.type == "host") | . as $h | (.children // [])[] as $c | $t.nodes[] | select(.id == $c and .type == "osd") | "osd.\($c)@\($h.name)"] | sort | join(",")'; }
crush_hosts() { ceph_op osd tree -f json | jq -r '[.nodes[] | select(.type == "host") | .name] | sort | join(",")'; }
pool_names() { ceph_op osd pool ls -f json | jq -r 'sort | join(",")'; }
pool_sizes() { ceph_op osd pool ls detail -f json | jq -r '[.[] | "\(.pool_name):\(.size)"] | sort | join(",")'; }
health_ok() { ceph_op status -f json | jq -e '.health.status == "HEALTH_OK"' >/dev/null; }
diagnose() { kubectl get pods -A -o wide || true; ceph_op status || true; }
phase_ready() {
  case "$(kubectl -n rook-ceph get "$1" "$2" -o jsonpath='{.status.phase}')" in
  Ready | Connected) return 0 ;;
  esac
  return 1
}
gen_data() {
  python3 -c '
import hashlib, sys
seed, size = sys.argv[1].encode(), int(sys.argv[2])
block = hashlib.sha256(seed).digest()
left = size
while left > 0:
    want = min(65536, left)
    chunk = b""
    while len(chunk) < want:
        block = hashlib.sha256(block).digest()
        chunk += block
    chunk = chunk[:want]
    sys.stdout.buffer.write(chunk)
    left -= want
' "$1" "$2"
}
fs_file() {
  op_exec_stdin python3 - "$1" "$2" <<'PY'
import cephfs, hashlib, os, sys
mode, name = sys.argv[1], sys.argv[2]
path = "/lab-file"
fs = cephfs.LibCephFS(conffile="/var/lib/rook/rook-ceph/rook-ceph.config")
fs.mount(filesystem_name=name)
if mode == "write":
    data = b"".join(hashlib.sha256(b"lab-fs-%d" % i).digest() for i in range(32768))
    fd = fs.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644)
    fs.write(fd, data, 0)
    fs.fsync(fd, 0)
    fs.close(fd)
else:
    size = fs.stat(path).st_size
    fd = fs.open(path, os.O_RDONLY, 0)
    data = fs.read(fd, 0, size)
    fs.close(fd)
fs.shutdown()
print(hashlib.sha256(data).hexdigest())
PY
}
FUNCTIONS
}

vm_run() {
  local id=$1 script=$2
  shift 2
  limactl shell --workdir / "$(vm_name "$id")" sudo env HOME=/root PATH="$vm_path" S3_ACCESS="$s3_access_key" S3_SECRET="$s3_secret_key" "$@" bash -c "$(remote_prelude)
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
  make build release binaries VERSION=dev IMAGE="$image" PIN_DIGESTS=1
  retry 3 docker build -t "$image" -f Containerfile .
  retry 3 docker push "$image"
  make bundle VERSION=dev IMAGE="$image" PIN_DIGESTS=1 BUNDLE_ARCH="$arch"
  registry_down
  remove_bundle_images
  test -f "$bundle"
  test -x "$bedrock_bin"
  test -f "$k0s_source"
  install -m 0755 "$(command -v helm)" "$lab_dir/helm"
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
    args+=("$arg,serial=${rest%%/*}")
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
for _ in $(seq 1 120); do
  if ip -4 -o addr show dev lima0 | grep -q inet; then
    break
  fi
  sleep 1
done
gateway=$(ip -4 route show default dev lima0 | awk 'NR==1 {print $3}')
cat >/etc/netplan/60-adoptlab.yaml <<NETPLAN
network:
  version: 2
  ethernets:
    lima0:
      dhcp4: false
      addresses: [@IP@/24]
      routes:
      - to: default
        via: ${gateway:-@GATEWAY@}
        metric: 50
NETPLAN
chmod 600 /etc/netplan/60-adoptlab.yaml
netplan apply
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
$(vm_provision | sed "s/@IP@/$(node_ip "$id")/;s/@GATEWAY@/$subnet.1/" | sed 's/^/    /')
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
test "$(ip -4 -o addr show dev lima0 | grep -cw "$EXPECT_IP/24")" -ge 1
test "$(ip -json route show default | jq -r '.[0].dev')" = lima0
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

verify_vm() {
  local id=$1 peers=() other
  for other in "${ids[@]}"; do
    if [ "$other" != "$id" ]; then
      peers+=("$(node_ip "$other")")
    fi
  done
  vm_run "$id" "$(script_verify_vm)" EXPECT_HOSTNAME="$(node_name "$id")" EXPECT_IP="$(node_ip "$id")" DEVICES="$(node_devices "$id")" PEERS="${peers[*]}"
}

stage_up() {
  require_host
  mkdir -p "$lab_dir"
  lab_down
  guard_host
  bundle_build
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
  for id in "${ids[@]}"; do
    log "checking $(vm_name "$id")"
    verify_vm "$id"
  done
}

script_old_controller() {
  cat <<'SCRIPT'
install -m 0755 "$K0S_SRC" /usr/local/bin/k0s
mkdir -p /etc/k0s
cat >/etc/k0s/k0s.yaml <<CONFIG
apiVersion: k0s.k0sproject.io/v1beta1
kind: ClusterConfig
metadata:
  name: k0s
spec:
  api:
    address: $IP
    sans:
    - $IP
CONFIG
k0s install controller --enable-worker --no-taints --config /etc/k0s/k0s.yaml --kubelet-extra-args="--node-ip=$IP"
k0s start
retry_until 300 k0s kubectl get nodes
SCRIPT
}

script_old_worker() {
  cat <<'SCRIPT'
install -m 0755 "$K0S_SRC" /usr/local/bin/k0s
mkdir -p /etc/k0s
install -m 0600 /dev/null /etc/k0s/join-token
printf '%s' "$TOKEN" >/etc/k0s/join-token
k0s install worker --token-file /etc/k0s/join-token --kubelet-extra-args="--node-ip=$IP"
k0s start
SCRIPT
}

script_old_nodes_ready() {
  cat <<'SCRIPT'
three_ready() { [ "$(kubectl get nodes --no-headers | grep -cw Ready)" -eq 3 ]; }
retry_until 600 three_ready || { kubectl get nodes -o wide; exit 1; }
kubectl get nodes -o wide
SCRIPT
}

script_old_rook() {
  cat <<'SCRIPT'
install -m 0755 "$HELM_SRC" /usr/local/bin/helm
helm repo add rook-release "$CHART_REPO"
helm repo update
helm install rook-ceph rook-release/rook-ceph --version "$CHART_VERSION" --namespace rook-ceph --create-namespace --wait --timeout 10m
helm install rook-ceph-cluster rook-release/rook-ceph-cluster --version "$CHART_VERSION" --namespace rook-ceph --values "$VALUES" --timeout 10m
ceph_settled() {
  [ "$(kubectl -n rook-ceph get cephcluster rook-ceph -o jsonpath='{.status.ceph.health}')" = HEALTH_OK ] &&
    [ "$(ceph_op osd stat -f json | jq -r .num_up_osds)" = 6 ]
}
retry_until 2400 ceph_settled || { diagnose; exit 1; }
retry_until 600 phase_ready cephblockpool storage-block || { diagnose; exit 1; }
retry_until 600 phase_ready cephfilesystem filesystem-data || { diagnose; exit 1; }
retry_until 600 phase_ready cephobjectstore objectstore || { diagnose; exit 1; }
ceph_op status
SCRIPT
}

script_old_data() {
  cat <<'SCRIPT'
for n in 1 2; do
  size=$((n * 16777216))
  gen_data "lab-image-$n" "$size" | op_exec_stdin rbd "$rook_conf" import --no-progress - "storage-block/lab-img$n"
  echo "STATE:IMG_SHA_$n=$(gen_data "lab-image-$n" "$size" | sha_of)"
done
echo "STATE:FS_SHA=$(fs_file write filesystem-data)"
rgw_op --rgw-realm=objectstore --rgw-zonegroup=objectstore --rgw-zone=objectstore user create --uid=lab-user --display-name=lab-user --access-key="$S3_ACCESS" --secret="$S3_SECRET" >/dev/null
endpoint=$(rgw_service objectstore)
retry_until 300 curl -s -o /dev/null "http://$endpoint/"
s3 -X PUT "http://$endpoint/lab-bucket"
for n in 1 2; do
  gen_data "lab-object-$n" "$((n * 262144))" >"/tmp/lab-object-$n"
  s3 -X PUT --data-binary "@/tmp/lab-object-$n" "http://$endpoint/lab-bucket/lab-object-$n"
  echo "STATE:S3_SHA_$n=$(sha_of <"/tmp/lab-object-$n")"
  rm -f "/tmp/lab-object-$n"
done
rbd_op ls storage-block
SCRIPT
}

stage_old() {
  local id token out
  state_load
  log "k0s $k0s_version on $(vm_name 1)"
  vm_run 1 "$(script_old_controller)" IP="$(node_ip 1)" K0S_SRC="$k0s_source"
  token=$(vm_run 1 'k0s token create --role worker --expiry 2h')
  for id in 2 3; do
    log "k0s $k0s_version worker on $(vm_name "$id")"
    vm_run "$id" "$(script_old_worker)" IP="$(node_ip "$id")" K0S_SRC="$k0s_source" TOKEN="$token"
  done
  vm_run 1 "$(script_old_nodes_ready)"
  log "Rook $rook_chart_version with the old values"
  vm_run 1 "$(script_old_rook)" HELM_SRC="$lab_dir/helm" CHART_REPO="$rook_chart_repo" CHART_VERSION="$rook_chart_version" VALUES="$repo/hack/lab/old-rook-values.yaml"
  log "test data"
  out=$(vm_run 1 "$(script_old_data)")
  printf '%s\n' "$out"
  state_capture <<<"$out"
}

script_export_identity() {
  cat <<'SCRIPT'
kubectl -n rook-ceph get secret rook-ceph-mon -o go-template='{{range $k, $v := .data}}{{$k}}: {{$v | base64decode}}{{"\n"}}{{end}}' | grep -E '^(fsid|mon-secret|ceph-username|ceph-secret): '
SCRIPT
}

script_export_monitors() {
  cat <<'SCRIPT'
kubectl -n rook-ceph get configmap rook-ceph-mon-endpoints -o json | jq -r '.data.mapping | fromjson | .node | to_entries | sort_by(.key)[] | "\(.key) \(.value.Name) \(.value.Address)"'
SCRIPT
}

script_export_facts() {
  cat <<'SCRIPT'
echo "STATE:FSID=$(ceph_op fsid)"
echo "STATE:MON_IDS=$(mon_ids)"
echo "STATE:OSD_MAP=$(osd_map)"
echo "STATE:OLD_POOLS=$(pool_sizes)"
flags=$(ceph_op fs get filesystem-data -f json | jq -c '.mdsmap.flags_state')
echo "flags_state of the old CephFS: $flags"
printf '%s' "$flags" | jq -e 'has("joinable") and has("refuse_client_session")' >/dev/null
SCRIPT
}

export_files=(
  "rook-ceph-mon.secret.yaml|kubectl -n rook-ceph get secret rook-ceph-mon -o yaml"
  "rook-ceph-mon-endpoints.configmap.yaml|kubectl -n rook-ceph get configmap rook-ceph-mon-endpoints -o yaml"
  "cephcluster.yaml|kubectl -n rook-ceph get cephcluster rook-ceph -o yaml"
  "pv-pvc.yaml|kubectl get pv,pvc -A -o yaml"
  "ceph-versions.json|ceph_op versions -f json"
  "ceph-osd-tree.json|ceph_op osd tree -f json"
  "ceph-pool-detail.json|ceph_op osd pool ls detail -f json"
  "ceph-fs-dump.json|ceph_op fs dump -f json"
  "ceph-auth-ls.json|ceph_op auth ls -f json"
  "rgw-realm.json|rgw_op realm get --rgw-realm=objectstore"
  "rgw-zonegroup.json|rgw_op zonegroup get --rgw-zonegroup=objectstore"
  "rgw-zone.json|rgw_op zone get --rgw-zone=objectstore"
)

export_old_files() {
  local entry
  for entry in "${export_files[@]}"; do
    vm_run 1 "${entry#*|}" >"$lab_dir/export/${entry%%|*}"
  done
}

monitors_flow() {
  awk 'BEGIN {printf "["} NR > 1 {printf ", "} {printf "{id: %s, node: %s, address: %s}", $1, $2, $3} END {print "]"}'
}

hosts_flow() {
  local id out=""
  for id in "${ids[@]}"; do
    out+="${out:+, }{node: $(node_name "$id"), devices: [$(node_devices "$id" | sed 's/ /, /g')]}"
  done
  printf '[%s]' "$out"
}

render_adopt_config() {
  local monitors=$1 hosts=$2 emulation=$3
  VERSION=dev VIP=$vip IFACE=$iface PLATFORM_HOST=$platform_host FSID=$FSID KEY_FILE=$remote_key_file MONITORS=$monitors HOSTS=$hosts EMULATION=$emulation \
    envsubst "\${VERSION} \${VIP} \${IFACE} \${PLATFORM_HOST} \${FSID} \${KEY_FILE} \${MONITORS} \${HOSTS} \${EMULATION}" <"$repo/hack/lab/adopt.yaml.tmpl"
}

stage_export() {
  local out monitors emulation
  state_load
  require_state IMG_SHA_1 IMG_SHA_2 FS_SHA S3_SHA_1 S3_SHA_2
  umask 077
  mkdir -p "$lab_dir/export"
  log "adopt key file"
  vm_run 1 "$(script_export_identity)" >"$lab_dir/adopt-keys.yaml"
  chmod 600 "$lab_dir/adopt-keys.yaml"
  test "$(wc -l <"$lab_dir/adopt-keys.yaml")" -eq 4
  log "old state"
  out=$(vm_run 1 "$(script_export_facts)")
  printf '%s\n' "$out"
  state_capture <<<"$out"
  state_load
  expect_equal "$FSID" "$(sed -n 's/^fsid: //p' "$lab_dir/adopt-keys.yaml")" "fsid of the key file"
  log "old files"
  export_old_files
  monitors=$(vm_run 1 "$(script_export_monitors)" | monitors_flow)
  emulation=$(vm_run 1 'if test -c /dev/kvm; then echo false; else echo true; fi')
  render_adopt_config "$monitors" "$(hosts_flow)" "$emulation" >"$lab_dir/adopt.yaml"
  cat "$lab_dir/adopt.yaml"
}

script_stop_quiesce() {
  cat <<'SCRIPT'
no_running_pods() { [ -z "$(kubectl -n rook-ceph get pods --field-selector=status.phase=Running -o name)" ]; }
ceph_op osd set noout
kubectl -n rook-ceph scale deployment/rook-ceph-operator --replicas=0
kubectl -n rook-ceph scale deployment --all --replicas=0
retry_until 600 no_running_pods || { kubectl -n rook-ceph get pods; exit 1; }
SCRIPT
}

script_stop_copy() {
  cat <<'SCRIPT'
tar --numeric-owner --xattrs --acls -C / -cpf "/tmp/rook-$ID.tar" var/lib/rook
sha256sum "/tmp/rook-$ID.tar" | cut -d' ' -f1
SCRIPT
}

script_stop_reset() {
  cat <<'SCRIPT'
k0s stop
k0s reset
SCRIPT
}

script_stop_verify() {
  cat <<'SCRIPT'
test "$(ss -ltn | grep -Ec ':(3300|6789)[[:space:]]')" -eq 0
test "$(ps -eo comm= | grep -Ec '^(ceph-(mon|osd|mgr|mds)|radosgw)$')" -eq 0
for device in $DEVICES; do
  real=$(readlink -f "$device")
  holder=$(find /proc/[0-9]*/fd -lname "$real" -print -quit 2>/dev/null || true)
  test -z "$holder"
  test "$(blkid -p -s TYPE -o value "$device")" = ceph_bluestore
  test "$(head -c 4096 "$device" | grep -ac "$FSID")" -ge 1
done
test -d /var/lib/rook
SCRIPT
}

copy_rook_directory() {
  local id=$1 sum
  sum=$(vm_run "$id" "$(script_stop_copy)" ID="$id")
  vm_get "$id" "/tmp/rook-$id.tar" "$lab_dir/rook-$id.tar"
  expect_equal "$sum" "$(sha256sum "$lab_dir/rook-$id.tar" | cut -d' ' -f1)" "sha256 of the /var/lib/rook copy of $(vm_name "$id")"
  test "$(tar -tf "$lab_dir/rook-$id.tar" | grep -cE '^var/lib/rook/mon-[a-z]+/data/store\.db/CURRENT$')" -eq 1
  test "$(tar -tf "$lab_dir/rook-$id.tar" | grep -c 'keyring')" -ge 1
  vm_run "$id" "rm -f /tmp/rook-$id.tar"
}

stage_stop() {
  local id
  state_load
  require_state FSID
  log "noout and Rook scale down"
  vm_run 1 "$(script_stop_quiesce)"
  for id in "${ids[@]}"; do
    log "copying /var/lib/rook of $(vm_name "$id")"
    copy_rook_directory "$id"
  done
  for id in 3 2 1; do
    log "k0s reset on $(vm_name "$id")"
    vm_run "$id" "$(script_stop_reset)"
  done
  for id in "${ids[@]}"; do
    log "checking the disks of $(vm_name "$id")"
    vm_run "$id" "$(script_stop_verify)" DEVICES="$(node_devices "$id")" FSID="$FSID"
  done
}

script_adopt_prepare() {
  cat <<'SCRIPT'
mkdir -p /etc/k0s/containerd.d/certs.d/_default
printf '[plugins."io.containerd.cri.v1.images".registry]\nconfig_path = "/etc/k0s/containerd.d/certs.d"\n' >/etc/k0s/containerd.d/cri-registry.toml
printf 'server = "https://127.0.0.1:1"\n' >/etc/k0s/containerd.d/certs.d/_default/hosts.toml
install -m 0600 /tmp/adopt.yaml "$CONFIG_FILE"
rm -f /tmp/adopt.yaml
if [ -f /tmp/adopt-keys.yaml ]; then
  install -m 0600 /tmp/adopt-keys.yaml "$KEY_FILE"
  rm -f /tmp/adopt-keys.yaml
fi
SCRIPT
}

script_adopt_init() {
  cat <<'SCRIPT'
cd "$REPO"
"$BIN" init -f "$CONFIG_FILE" --bundle "$BUNDLE" --timeout 45m 2>&1 | awk '!/^admin password: /'
SCRIPT
}

script_adopt_join() {
  cat <<'SCRIPT'
cd "$REPO"
"$BIN" join --token "$TOKEN" --bundle "$BUNDLE" --adopt-config "$CONFIG_FILE" 2>&1
SCRIPT
}

script_adopt_wait() {
  cat <<'SCRIPT'
last=
deadline=$((SECONDS + 5400))
while :; do
  reason=$(condition_reason cluster cluster StorageAdopted || true)
  if [ "$reason" != "$last" ]; then
    echo "StorageAdopted: ${reason:-none}: $(kubectl get cluster cluster -o jsonpath='{.status.conditions[?(@.type=="StorageAdopted")].message}' || true)"
    last=$reason
  fi
  case "$reason" in
  Adopted) break ;;
  RenameFailed | PoolSizeTooSmall)
    diagnose
    exit 1
    ;;
  esac
  if [ "$SECONDS" -ge "$deadline" ]; then
    diagnose
    echo "timeout while waiting for Adopted" >&2
    exit 1
  fi
  sleep 10
done
rm -f "$KEY_FILE"
ceph_op osd unset noout
wait_condition 3600 cluster cluster StorageReady True || { diagnose; exit 1; }
ceph_op status
SCRIPT
}

join_node() {
  local id=$1 token=$2
  vm_run "$id" "$(script_adopt_join)" REPO="$repo" BIN="$bedrock_bin" BUNDLE="$bundle" CONFIG_FILE="$remote_config_file" TOKEN="$token"
}

join_nodes() {
  local token=$1 id pid failed=0 pids=()
  for id in 2 3; do
    join_node "$id" "$token" >"$lab_dir/join-$id.log" 2>&1 &
    pids+=($!)
  done
  for pid in "${pids[@]}"; do
    wait "$pid" || failed=1
  done
  for id in 2 3; do
    log "join log of $(vm_name "$id")"
    tail -n 25 "$lab_dir/join-$id.log"
  done
  return "$failed"
}

stage_adopt() {
  local id token
  state_load
  require_state FSID
  test -f "$bundle"
  test -f "$lab_dir/adopt.yaml"
  test -f "$lab_dir/adopt-keys.yaml"
  for id in "${ids[@]}"; do
    vm_put "$id" "$lab_dir/adopt.yaml" /tmp/adopt.yaml
  done
  vm_put 1 "$lab_dir/adopt-keys.yaml" /tmp/adopt-keys.yaml
  for id in "${ids[@]}"; do
    vm_run "$id" "$(script_adopt_prepare)" CONFIG_FILE="$remote_config_file" KEY_FILE="$remote_key_file"
  done
  log "bedrock init on $(vm_name 1)"
  vm_run 1 "$(script_adopt_init)" REPO="$repo" BIN="$bedrock_bin" BUNDLE="$bundle" CONFIG_FILE="$remote_config_file"
  token=$(vm_run 1 "$bedrock_bin token create --roles ceph-osd,workload --expiry 1h")
  log "bedrock join on $(vm_name 2) and $(vm_name 3)"
  join_nodes "$token"
  log "waiting for StorageAdopted"
  vm_run 1 "$(script_adopt_wait)" KEY_FILE="$remote_key_file"
}

verdict() {
  local name=$1 check=$2 out
  if out=$("$check" 2>&1); then
    printf 'PASS %s\n' "$name"
    printf '%s\n' "$out" | grep '^info: ' || true
    return 0
  fi
  printf 'FAIL %s: %s\n' "$name" "$(printf '%s' "$out" | tail -n 1 | cut -c1-240)"
  return 1
}

lab_ceph() {
  vm_run 1 "ceph_op $*"
}

check_fsid() {
  expect_equal "$FSID" "$(lab_ceph fsid)" "ceph fsid" || return 1
  expect_equal "$FSID" "$(vm_run 1 'kubectl -n rook-ceph get secret rook-ceph-mon -o jsonpath="{.data.fsid}" | base64 -d')" "fsid of the Secret rook-ceph-mon"
}

check_monitor_ids() {
  expect_equal "$MON_IDS" "$(vm_run 1 mon_ids)" "monitor ids"
}

check_osd_ids() {
  expect_equal "$OSD_MAP" "$(vm_run 1 osd_map)" "OSD ids and hosts"
}

script_disks_kept() {
  cat <<'SCRIPT'
for device in $DEVICES; do
  test "$(blkid -p -s TYPE -o value "$device")" = ceph_bluestore
  test "$(head -c 4096 "$device" | grep -ac "$FSID")" -ge 1
done
SCRIPT
}

check_disks_kept() {
  local id
  for id in "${ids[@]}"; do
    vm_run "$id" "$(script_disks_kept)" DEVICES="$(node_devices "$id")" FSID="$FSID" || {
      echo "a disk of $(vm_name "$id") lost its BlueStore label"
      return 1
    }
  done
}

check_pool_names() {
  local names want old
  names=$(vm_run 1 pool_names)
  for want in bedrock-block bedrock-fs-metadata bedrock-fs-data0 bedrock-rgw.rgw.meta bedrock-rgw.rgw.buckets.data .rgw.root; do
    case ",$names," in
    *",$want,"*) ;;
    *)
      echo "pool $want is missing: $names"
      return 1
      ;;
    esac
  done
  for old in storage-block filesystem-data objectstore.; do
    case ",$names" in
    *",$old"*)
      echo "a pool with the old name $old remains: $names"
      return 1
      ;;
    esac
  done
}

check_pool_sizes() {
  local sizes want off
  sizes=$(vm_run 1 pool_sizes) || return 1
  if [ -z "$sizes" ]; then
    echo "the pool list is empty"
    return 1
  fi
  for want in bedrock-block bedrock-fs-metadata bedrock-fs-data0 bedrock-rgw.rgw.meta bedrock-rgw.rgw.buckets.data .rgw.root; do
    case ",$sizes," in
    *",$want:3,"*) ;;
    *)
      echo "pool $want does not exist with size 3: $sizes"
      return 1
      ;;
    esac
  done
  off=$(tr ',' '\n' <<<"$sizes" | grep -E '^(bedrock-|\.rgw\.root:)' | grep -v ':3$' | tr '\n' ' ' || true)
  if [ -n "$off" ]; then
    echo "pools with a size other than 3: $off"
    return 1
  fi
  case ",$sizes" in
  *",storage-block:"* | *",filesystem-data"* | *",objectstore."*)
    echo "a pool with an old name remains: $sizes"
    return 1
    ;;
  esac
}

check_rbd_data() {
  local n want got
  for n in 1 2; do
    want=IMG_SHA_$n
    got=$(vm_run 1 "op_exec sh -c \"rbd \$rook_conf export --no-progress bedrock-block/lab-img$n - | sha256sum\" | cut -d' ' -f1")
    expect_equal "${!want}" "$got" "sha256 of the RBD image lab-img$n" || return 1
  done
}

check_cephfs_file() {
  expect_equal "$FS_SHA" "$(vm_run 1 'fs_file read bedrock-fs')" "sha256 of the CephFS file"
}

script_s3_get() {
  cat <<'SCRIPT'
endpoint=$(rgw_service bedrock-rgw)
get_object() { s3 -o /dev/null "http://$endpoint/lab-bucket/lab-object-1"; }
retry_until 600 get_object
s3 "http://$endpoint/lab-bucket/lab-object-$N" | sha_of
SCRIPT
}

check_s3_objects() {
  local n want
  for n in 1 2; do
    want=S3_SHA_$n
    expect_equal "${!want}" "$(vm_run 1 "$(script_s3_get)" N="$n")" "sha256 of the S3 object lab-object-$n read with the old key" || return 1
  done
}

check_adopted_condition() {
  expect_equal "True/Adopted" "$(vm_run 1 "echo \"\$(condition_status cluster cluster StorageAdopted)/\$(condition_reason cluster cluster StorageAdopted)\"")" "condition StorageAdopted"
}

check_health_ok() {
  vm_run 1 'retry_until 900 health_ok || { ceph_op health detail; exit 1; }'
}

check_storage_ready() {
  vm_run 1 'wait_condition 900 cluster cluster StorageReady True'
}

check_pool_objects() {
  vm_run 1 'phase_ready cephblockpool bedrock-block && phase_ready cephfilesystem bedrock-fs && phase_ready cephobjectstore bedrock-rgw' ||
    {
      echo "a pool object of Bedrock is not Ready"
      return 1
    }
}

check_classes() {
  vm_run 1 'kubectl get storageclass block block-retain filesystem bucket >/dev/null && kubectl get volumesnapshotclass block >/dev/null'
}

check_host_devices() {
  local id
  for id in "${ids[@]}"; do
    expect_equal "$(node_devices "$id" | awk '{printf "[\"%s\",\"%s\"]", $1, $2}')" "$(vm_run 1 "kubectl get host $(node_name "$id") -o jsonpath='{.spec.storage.devices}'")" "devices of the Host $(node_name "$id")" || return 1
  done
}

check_cephfs_flags() {
  local flags
  flags=$(vm_run 1 "ceph_op fs get bedrock-fs -f json | jq -c '.mdsmap.flags_state'")
  echo "info: flags_state of bedrock-fs: $flags"
  printf '%s' "$flags" | jq -e '.joinable == true and .refuse_client_session == false' >/dev/null
}

script_rgw_period() {
  cat <<'SCRIPT'
period=$(rgw_op period get --rgw-realm=bedrock-rgw)
printf '%s' "$period" | jq -e '.realm_name == "bedrock-rgw" and ([.period_map.zonegroups[].name] == ["bedrock-rgw"]) and ([.period_map.zonegroups[].zones[].name] == ["bedrock-rgw"])' >/dev/null
echo "info: the committed RGW period has epoch $(printf '%s' "$period" | jq -r .epoch) and realm $(printf '%s' "$period" | jq -r .realm_name)"
rgw_op zone get --rgw-zone=bedrock-rgw | jq -e '([.. | strings | select(startswith("objectstore.rgw."))] | length == 0) and ([.. | strings | select(startswith("bedrock-rgw.rgw."))] | length > 0)' >/dev/null
SCRIPT
}

check_rgw_period() {
  vm_run 1 "$(script_rgw_period)"
}

script_period_output() {
  cat <<'SCRIPT'
out=$(rgw_op period update --commit --rgw-realm=bedrock-rgw)
echo "info: period update --commit printed $(printf '%s' "$out" | wc -c) bytes"
if printf '%s' "$out" | grep -Eq 'system_key|access_key|secret_key'; then
  echo "the stdout of period update --commit holds key fields"
  exit 1
fi
SCRIPT
}

check_period_output() {
  vm_run 1 "$(script_period_output)"
}

check_crush_hosts() {
  local want id names=()
  for id in "${ids[@]}"; do
    names+=("$(node_name "$id")")
  done
  want=$(printf '%s\n' "${names[@]}" | sort | paste -sd, -)
  echo "info: CRUSH host buckets $(vm_run 1 crush_hosts), node names $want, no node name has a dot"
  expect_equal "$want" "$(vm_run 1 crush_hosts)" "CRUSH host buckets"
}

script_ceph_release() {
  cat <<'SCRIPT'
image=$(kubectl -n rook-ceph get cephcluster rook-ceph -o jsonpath='{.spec.cephVersion.image}')
major=$(sed -E 's/.*:v?([0-9]+)\..*/\1/' <<<"${image%%@*}")
ceph_op versions -f json | jq -e --arg major "$major" '[to_entries[] | select(.key != "overall") | .value | keys[] | select(startswith("ceph version " + $major + ".") | not)] | length == 0' >/dev/null
SCRIPT
}

check_ceph_release() {
  vm_run 1 "$(script_ceph_release)"
}

check_pgs_clean() {
  vm_run 1 "ceph_op status -f json | jq -e '([.pgmap.pgs_by_state[] | select(.state_name != \"active+clean\")] | length == 0) and ((.pgmap.misplaced_objects // 0) == 0)' >/dev/null"
}

script_key_leak() {
  cat <<'SCRIPT'
dump=$(mktemp)
{
  kubectl get cluster,setting,host,hostconfig -o yaml
  kubectl get events -A -o yaml
  kubectl -n bedrock-system logs deployment/bedrock-operator --tail=-1
  journalctl -u bedrock-agent --no-pager
} >"$dump" 2>&1
found=0
if grep -qF -e "$MON_SECRET" -e "$CEPH_SECRET" "$dump"; then
  found=1
fi
rm -f "$dump"
if [ "$found" -eq 1 ]; then
  echo "key material found in the cluster objects, events or logs"
  exit 1
fi
if [ -e "$KEY_FILE" ]; then
  echo "$KEY_FILE still exists"
  exit 1
fi
SCRIPT
}

check_no_key_material() {
  local mon_secret ceph_secret
  mon_secret=$(sed -n 's/^mon-secret: //p' "$lab_dir/adopt-keys.yaml")
  ceph_secret=$(sed -n 's/^ceph-secret: //p' "$lab_dir/adopt-keys.yaml")
  vm_run 1 "$(script_key_leak)" MON_SECRET="$mon_secret" CEPH_SECRET="$ceph_secret" KEY_FILE="$remote_key_file"
}

script_operator_restart() {
  cat <<'SCRIPT'
before=$(pool_names)
kubectl -n bedrock-system rollout restart deployment/bedrock-operator
kubectl -n bedrock-system rollout status deployment/bedrock-operator --timeout=300s
sleep 120
test "$(condition_reason cluster cluster StorageAdopted)" = Adopted
test "$(pool_names)" = "$before"
retry_until 600 health_ok
SCRIPT
}

check_operator_restart() {
  vm_run 1 "$(script_operator_restart)"
}

stage_check() {
  local failed=0 entry
  state_load
  require_state FSID MON_IDS OSD_MAP IMG_SHA_1 IMG_SHA_2 FS_SHA S3_SHA_1 S3_SHA_2
  for entry in \
    "fsid is unchanged|check_fsid" \
    "monitor ids are unchanged|check_monitor_ids" \
    "OSD ids are unchanged|check_osd_ids" \
    "no disk was wiped|check_disks_kept" \
    "pools have the Bedrock names|check_pool_names" \
    "Bedrock pools have size 3|check_pool_sizes" \
    "old RBD images match their sha256|check_rbd_data" \
    "CephFS file is readable|check_cephfs_file" \
    "S3 GET with the old key returns the old objects|check_s3_objects" \
    "condition StorageAdopted is Adopted|check_adopted_condition" \
    "HEALTH_OK|check_health_ok" \
    "condition StorageReady is True|check_storage_ready" \
    "pool objects are Ready|check_pool_objects" \
    "storage classes exist|check_classes" \
    "Host objects carry the adopt devices|check_host_devices" \
    "CephFS flags_state keys work|check_cephfs_flags" \
    "RGW period holds the Bedrock names|check_rgw_period" \
    "period update --commit prints no key|check_period_output" \
    "CRUSH host buckets equal the node names|check_crush_hosts" \
    "all daemons run the bundle Ceph release|check_ceph_release" \
    "all PGs active+clean and nothing misplaced|check_pgs_clean" \
    "no key material in cluster or logs|check_no_key_material" \
    "operator restart keeps the adoption|check_operator_restart"; do
    verdict "${entry%|*}" "${entry#*|}" || failed=1
  done
  if [ "$failed" -eq 1 ]; then
    echo "lab-adopt: at least one check failed"
  fi
  return "$failed"
}

stage_down() {
  lab_down
  remove_bundle_images
  rm -rf "$lab_dir/export" "$lab_dir/adopt-keys.yaml" "$lab_dir"/rook-*.tar
}

stage_all() {
  stage_up
  stage_old
  stage_export
  stage_stop
  stage_adopt
  stage_check
}

main() {
  cd "$repo"
  case "${1:-}" in
  up | old | export | stop | adopt | check | down | all) "stage_$1" ;;
  *)
    usage >&2
    return 2
    ;;
  esac
}

main "$@"
