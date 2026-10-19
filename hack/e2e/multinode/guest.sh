xfer_bytes=10485760
xfer_port=9000

guest_script() {
  local role=$1 peer=$2
  cat <<SCRIPT
#!/bin/sh
ROLE=$role
PEER=$peer
PORT=$xfer_port
SCRIPT
  cat <<'SCRIPT'
say() { echo "LAB-$*" >/dev/console; }
serve() { while :; do dd if=/dev/zero bs=1048576 count=10 2>/dev/null | nc -l -p "$PORT"; done; }
disks() { for d in /sys/block/sd* /sys/block/vd*; do [ -e "$d" ] && echo "${d##*/}:$(cat "$d/size")"; done | tr '\n' ' '; }
exercise_disk() {
  d=$(ls /sys/block | grep '^sd' | head -n 1)
  [ -n "$d" ] || return 0
  printf labdata | dd of="/dev/$d" bs=7 count=1 2>/dev/null
  sync
  say "DISKIO $d $(dd if="/dev/$d" bs=7 count=1 2>/dev/null)"
  tested=1
}
main() {
  boot=$(cat /proc/sys/kernel/random/boot_id)
  seq=0
  tested=
  [ "$ROLE" = server ] && serve &
  while :; do
    seq=$((seq + 1))
    say "BOOT $boot $(cut -d' ' -f1 /proc/uptime)"
    say "NET $(cat /sys/class/net/eth0/address) $(ip -4 -o addr show eth0 | awk '{print $4}') $(cat /sys/class/net/eth0/mtu)"
    say "DMI $(cat /sys/class/dmi/id/product_uuid)"
    say "DISKS $(disks)"
    if [ "$ROLE" = hot ] && [ -z "$tested" ]; then exercise_disk; fi
    if [ "$ROLE" = client ]; then say "XFER $seq $(nc -w 15 "$PEER" "$PORT" | wc -c)"; fi
    sleep 5
  done
}
main </dev/null >/dev/null 2>&1 &
SCRIPT
}

vm_manifest() {
  local name=$1 namespace=$2 userdata=$3 extra=$4
  cat <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: $name
  namespace: $namespace
spec:
  instanceType: u1.nano
  preference: cirros
  image:
    name: cirros
    scope: cluster
  rootDisk:
    sizeGi: 2
  cloudInit:
    userData: |
$(printf '%s\n' "$userdata" | sed 's/^/      /')
  running: true
$extra
VM
}

network_yaml() {
  printf '  networks:\n    - subnetRef: %s\n' "$1"
  if [ -n "${2:-}" ]; then
    printf '      address: %s\n' "$2"
  fi
}

prefer_node_yaml() {
  cat <<AFFINITY
  scheduling:
    affinity:
      nodeAffinity:
        preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 100
            preference:
              matchExpressions:
                - key: kubernetes.io/hostname
                  operator: In
                  values: [$1]
AFFINITY
}

avoid_node_yaml() {
  cat <<AFFINITY
  scheduling:
    affinity:
      nodeAffinity:
        requiredDuringSchedulingIgnoredDuringExecution:
          nodeSelectorTerms:
            - matchExpressions:
                - key: kubernetes.io/hostname
                  operator: NotIn
                  values: [$1]
AFFINITY
}

pin_node_yaml() {
  printf '  scheduling:\n    nodeSelector:\n      kubernetes.io/hostname: %s\n' "$1"
}

eviction_yaml() {
  printf '  evictionStrategy: %s\n' "$1"
}

lab_node() {
  kubectl get nodes -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | sort | sed -n "${1}p"
}

launcher_pod() {
  kubectl -n "$1" get pods -l "kubevirt.io/domain=$2" -o json |
    jq -r '[.items[] | select(.status.phase == "Running")] | sort_by(.metadata.creationTimestamp) | last | .metadata.name // empty'
}

guest_lines() {
  local pod
  pod=$(launcher_pod "$1" "$2")
  [ -n "$pod" ] || return 1
  kubectl -n "$1" logs "$pod" -c guest-console-log --tail=400 | tr -d '\r' | sed -n 's/^.*LAB-//p'
}

guest_value() {
  guest_lines "$1" "$2" | awk -v key="$3" '$1 == key { i = index($0, " "); value = (i ? substr($0, i + 1) : "") } END { print value }'
}

guest_boot() {
  guest_value "$1" "$2" BOOT | cut -d' ' -f1
}

guest_has() {
  [ -n "$(guest_value "$1" "$2" "$3")" ]
}

wait_guest() {
  local timeout=$1
  shift
  retry_until "$timeout" guest_has "$@" || {
    echo "guest $1/$2 printed no $3 line" >&2
    return 1
  }
}

wait_vm_running() {
  wait_jsonpath "$1" Running -n "$2" get virtualmachine "$3" -o jsonpath='{.status.phase}'
}

start_guest() {
  wait_vm_running 900 "$1" "$2"
  wait_guest 600 "$1" "$2" BOOT
}

vm_node() {
  kubectl -n "$1" get virtualmachine "$2" -o jsonpath='{.status.node}'
}

vm_ip() {
  kubectl -n "$1" get virtualmachine "$2" -o jsonpath='{.status.addresses[0].ip}'
}

vm_mac() {
  kubectl -n "$1" get virtualmachine "$2" -o jsonpath='{.status.addresses[0].mac}'
}

vm_left() {
  local before=$1
  shift
  [ "$(vm_node "$@")" != "$before" ]
}

delete_vms() {
  local namespace=$1 name
  shift
  for name in "$@"; do
    kubectl -n "$namespace" delete virtualmachine "$name" --ignore-not-found
  done
  for name in "$@"; do
    wait_gone 300 virtualmachine "$name" -n "$namespace"
    wait_empty 300 -n "$namespace" get pods -l "kubevirt.io/domain=$name"
  done
}

migration_phase() {
  kubectl -n "$1" get "$2" -o jsonpath='{.status.phase}'
}

migration_settled() {
  case "$(migration_phase "$1" "$2")" in
  Succeeded | Failed) return 0 ;;
  esac
  return 1
}

migrate_vm() {
  local migration
  migration=$(kubectl -n "$1" create -o name -f - <<MIGRATION
apiVersion: kubevirt.io/v1
kind: VirtualMachineInstanceMigration
metadata:
  generateName: $2-lab-
spec:
  vmiName: $2
MIGRATION
)
  retry_until 900 migration_settled "$1" "$migration" || {
    echo "migration $migration did not end" >&2
    return 1
  }
  expect_equal Succeeded "$(migration_phase "$1" "$migration")" "phase of $migration"
}
