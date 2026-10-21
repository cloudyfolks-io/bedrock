pin_hosts() {
  local vip=$1 host=$2 escaped
  escaped=${host//./\\.}
  sed -i "/[[:space:]]sso\\.$escaped\\([[:space:]]\\|\$\\)/d" /etc/hosts
  printf '%s %s sso.%s api.%s console.%s upload.%s\n' "$vip" "$host" "$host" "$host" "$host" "$host" >>/etc/hosts
}

dump_api_group() {
  local group=$1 resource
  for resource in $(kubectl api-resources --api-group="$group" --verbs=list -o name 2>/dev/null); do
    echo "--- $resource"
    kubectl get "$resource" -A -o yaml || true
  done
}

dump_logs() {
  local namespace=$1 selector=$2
  kubectl -n "$namespace" logs -l "$selector" --tail=200 --all-containers --prefix || true
}

dump_nmstate() {
  local pod
  echo "--- nmstate"
  kubectl -n nmstate get pods,ds,deploy -o wide || true
  kubectl -n nmstate describe pods || true
  for pod in $(kubectl -n nmstate get pods -o name 2>/dev/null || true); do
    kubectl -n nmstate logs "$pod" --all-containers --prefix --tail=100 || true
    kubectl -n nmstate logs "$pod" --all-containers --prefix --tail=100 --previous || true
  done
}

retry_until() {
  local timeout=$1 deadline
  shift
  deadline=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "timeout after ${timeout}s: $1" >&2
      return 1
    fi
    sleep 3
  done
}

jsonpath_of() {
  kubectl "$@" 2>/dev/null
}

jsonpath_equals() {
  local want=$1
  shift
  [ "$(jsonpath_of "$@")" = "$want" ]
}

jsonpath_not_empty() {
  [ -n "$(jsonpath_of "$@")" ]
}

wait_jsonpath() {
  local timeout=$1 want=$2
  shift 2
  retry_until "$timeout" jsonpath_equals "$want" "$@" || {
    echo "want '$want' from: kubectl $*; got '$(jsonpath_of "$@")'" >&2
    return 1
  }
}

wait_not_empty() {
  local timeout=$1
  shift
  retry_until "$timeout" jsonpath_not_empty "$@" || {
    echo "empty result from: kubectl $*" >&2
    return 1
  }
}

wait_gone() {
  local timeout=$1
  shift
  retry_until "$timeout" kubectl_absent "$@" || {
    echo "still present: kubectl $*" >&2
    return 1
  }
}

list_empty() {
  [ -z "$(kubectl "$@" -o name 2>/dev/null)" ]
}

wait_empty() {
  local timeout=$1
  shift
  retry_until "$timeout" list_empty "$@" || {
    echo "objects remain: kubectl $*" >&2
    kubectl "$@" -o name >&2 || true
    return 1
  }
}

kubectl_absent() {
  ! kubectl get "$@" >/dev/null 2>&1
}

condition_status() {
  local kind=$1 name=$2 type=$3
  shift 3
  kubectl "$@" get "$kind" "$name" -o jsonpath="{.status.conditions[?(@.type==\"$type\")].status}" 2>/dev/null
}

condition_reason() {
  local kind=$1 name=$2 type=$3
  shift 3
  kubectl "$@" get "$kind" "$name" -o jsonpath="{.status.conditions[?(@.type==\"$type\")].reason}" 2>/dev/null
}

wait_condition() {
  local timeout=$1 kind=$2 name=$3 type=$4 status=$5
  shift 5
  retry_until "$timeout" condition_is "$status" "$kind" "$name" "$type" "$@" || {
    echo "condition $type of $kind/$name is '$(condition_status "$kind" "$name" "$type" "$@")', want $status" >&2
    kubectl "$@" get "$kind" "$name" -o jsonpath='{.status.conditions}' >&2 || true
    return 1
  }
}

condition_is() {
  local status=$1
  shift
  [ "$(condition_status "$@")" = "$status" ]
}

wait_reason() {
  local timeout=$1 kind=$2 name=$3 type=$4 reason=$5
  shift 5
  retry_until "$timeout" reason_is "$reason" "$kind" "$name" "$type" "$@" || {
    echo "reason of $type on $kind/$name is '$(condition_reason "$kind" "$name" "$type" "$@")', want $reason" >&2
    return 1
  }
}

reason_is() {
  local reason=$1
  shift
  [ "$(condition_reason "$@")" = "$reason" ]
}

expect_equal() {
  local want=$1 got=$2 what=$3
  if [ "$want" != "$got" ]; then
    echo "$what: want '$want', got '$got'" >&2
    return 1
  fi
}

expect_contains() {
  local text=$1 needle=$2 what=$3
  case "$text" in
  *"$needle"*) ;;
  *)
    echo "$what: '$needle' not found in: $text" >&2
    return 1
    ;;
  esac
}

expect_refused() {
  local needle=$1 out
  shift
  if out=$("$@" 2>&1); then
    echo "expected a refusal containing '$needle': $*" >&2
    return 1
  fi
  expect_contains "$out" "$needle" "refusal of $*"
}

as_user() {
  local user=$1
  shift
  kubectl --as="bedrock:$user" "$@"
}

guest_console_log() {
  kubectl -n "$NS" logs -l "vm.kubevirt.io/name=$1" -c guest-console-log --tail=-1
}

state_dir() {
  echo "${E2E_STATE_DIR:-/run/bedrock-e2e}"
}

state_write() {
  mkdir -p "$(state_dir)"
  printf '%s\n' "$2" >"$(state_dir)/$1"
}

state_read() {
  cat "$(state_dir)/$1" 2>/dev/null || true
}

state_clear() {
  rm -f "$(state_dir)/$1"
}

disk_is_blank() {
  local disk=$1 out
  command -v lsblk wipefs findmnt >/dev/null || return 1
  [ -b "$disk" ] || return 1
  out=$(lsblk -bdno SIZE "$disk") || return 1
  [ "${out:-0}" -gt 0 ] || return 1
  out=$(lsblk -dno PTTYPE "$disk") || return 1
  [ -z "$out" ] || return 1
  out=$(lsblk -dno FSTYPE "$disk") || return 1
  [ -z "$out" ] || return 1
  out=$(lsblk -nro NAME "$disk") || return 1
  [ "$(printf '%s\n' "$out" | wc -l)" -eq 1 ] || return 1
  out=$(lsblk -nro MOUNTPOINTS "$disk") || return 1
  [ -z "$out" ] || return 1
  out=$(wipefs -n "$disk") || return 1
  [ -z "$out" ] || return 1
  ! findmnt -rn -S "$disk" >/dev/null
}

blank_disks() {
  local listing disk
  listing=$(lsblk -bdnpo NAME,TYPE,RO,SIZE) || return 1
  for disk in $(printf '%s\n' "$listing" | awk '$2 == "disk" && $3 == 0 && $4 > 0 {print $1}'); do
    if disk_is_blank "$disk"; then
      echo "$disk"
    fi
  done
}

osd_device_blank() {
  local candidates count
  candidates=$(blank_disks) || return 1
  count=$(printf '%s\n' "$candidates" | grep -c . || true)
  if [ "$count" -ne 1 ]; then
    echo "want exactly one blank disk, found $count: $candidates" >&2
    return 1
  fi
  echo "$candidates"
}

osd_device_path() {
  if ! disk_is_blank "$1"; then
    echo "$1 is not a blank disk" >&2
    return 1
  fi
  echo "$1"
}

osd_image() {
  echo "${E2E_OSD_IMAGE:-/var/lib/bedrock/e2e-osd.img}"
}

osd_loop_remove() {
  local loop image
  loop=$(state_read loop)
  image=$(state_read image)
  if [ -n "$loop" ]; then
    losetup -d "$loop" || true
  fi
  if [ -n "$image" ]; then
    rm -f "$image"
  fi
  state_clear loop
  state_clear image
}

osd_device_loop() {
  local image loop
  image=$(osd_image)
  osd_loop_remove
  if [ -e "$image" ]; then
    echo "$image exists and this run did not create it" >&2
    return 1
  fi
  mkdir -p "$(dirname "$image")"
  truncate -s "${E2E_OSD_LOOP_GIB:-40}G" "$image"
  state_write image "$image"
  loop=$(losetup -f --show "$image")
  state_write loop "$loop"
  echo "$loop"
}

resolve_osd_device() {
  case "${E2E_OSD_DEVICE:-}" in
  "") ;;
  blank) osd_device_blank ;;
  loop) osd_device_loop ;;
  *) osd_device_path "$E2E_OSD_DEVICE" ;;
  esac
}

flow_list() {
  if [ "$#" -eq 0 ] || [ -z "$1" ]; then
    echo '[]'
  else
    echo "[$1]"
  fi
}

assert_no_storage() {
  kubectl get cluster cluster -o jsonpath='{.status.conditions[?(@.type=="StorageReady")].reason}' | grep -qx NoDevices
  ! kubectl get storageclass block 2>/dev/null
}

assert_storage_ready() {
  local class
  kubectl wait --for=condition=StorageReady cluster/cluster --timeout=1800s
  for class in block block-retain filesystem; do
    kubectl get storageclass "$class"
  done
  kubectl get volumesnapshotclass block
}

etcd_value() {
  local key_base64
  key_base64=$(printf '%s' "$1" | base64 | tr -d '\n')
  curl -sSf --cacert /var/lib/k0s/pki/etcd/ca.crt --cert /var/lib/k0s/pki/apiserver-etcd-client.crt --key /var/lib/k0s/pki/apiserver-etcd-client.key \
    -X POST -d "{\"key\": \"$key_base64\"}" https://127.0.0.1:2379/v3/kv/range |
    python3 -c 'import base64,json,sys; sys.stdout.buffer.write(base64.b64decode(json.load(sys.stdin)["kvs"][0]["value"]))'
}

assert_secret_encrypted() {
  expect_equal 'k8s:enc:aesgcm:v1:key1:' "$(etcd_value "/registry/secrets/$1/$2" | head -c 23)" "secret $1/$2 is encrypted in etcd"
}

underlay_remove() {
  local veth netns
  veth=$(state_read veth)
  netns=$(state_read netns)
  if [ -n "$netns" ]; then
    ip netns del "$netns" 2>/dev/null || true
  fi
  if [ -n "$veth" ]; then
    ip link del "$veth" 2>/dev/null || true
  fi
  state_clear veth
  state_clear netns
}

underlay_is_ready() {
  ip -d link show bdrpub0 | grep -qw veth &&
    ip -n bdrext -4 addr show dev bdrpub0p | grep -qw 192.0.2.250/24
}

provider_underlay_up() {
  underlay_remove
  if ip link show bdrpub0 >/dev/null 2>&1 || ip link show bdrsvc0 >/dev/null 2>&1 || ip netns list | grep -qw bdrext; then
    echo "bdrpub0, bdrsvc0 or bdrext exists and this run did not create it" >&2
    return 1
  fi
  ip netns add bdrext
  state_write netns bdrext
  ip link add bdrpub0 type veth peer name bdrpub0p
  state_write veth bdrpub0
  ip link set bdrpub0 up
  ip link set bdrpub0p netns bdrext
  ip -n bdrext link set lo up
  ip -n bdrext link set bdrpub0p up
  ip -n bdrext addr add 192.0.2.250/24 dev bdrpub0p
  ip link add bdrsvc0 type veth peer name bdrsvc0p
  ip link set bdrsvc0 up
  ip link set bdrsvc0p netns bdrext
  ip -n bdrext link set bdrsvc0p up
  ip -n bdrext addr add 203.0.113.250/24 dev bdrsvc0p
  underlay_is_ready
}

e2e_cleanup() {
  underlay_remove
  osd_loop_remove
}

provider_network_entry() {
  echo "{name: $1, interface: bdrpub0, cidr: 192.0.2.0/24, gateway: 192.0.2.1}"
}

services_network_entry() {
  echo "{name: services, interface: bdrsvc0, cidr: 203.0.113.0/24, gateway: 203.0.113.1, excludeIPs: [\"203.0.113.240..203.0.113.254\"], role: services}"
}

docs_restore_script() {
  awk -v intro="$1" '
    /^## / { in_section = ($0 == "## Etcd snapshots"); armed = 0; next }
    in_section && index($0, intro) == 1 { armed = 1; next }
    armed && $0 == "```sh" { inside = 1; next }
    inside && $0 == "```" { exit }
    inside { print }
  ' "$2"
}

docs_restore_script_valid() {
  head -n 1 "$1" | grep -Eq "^SNAPSHOT=[^ ]+ sh -eu <<'EOF'\$" &&
    tail -n 1 "$1" | grep -qx EOF &&
    grep -q '^k0s restore ' "$1" &&
    grep -q '^  for FILE in /var/lib/bedrock/images/\*/\*/\*.tar; do$' "$1"
}
