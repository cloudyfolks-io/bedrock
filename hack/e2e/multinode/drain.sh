drain_migrations() {
  kubectl -n "$1" get virtualmachineinstancemigrations.kubevirt.io -o json |
    jq --arg vm "$2" '[.items[] | select(.spec.vmiName == $vm and .status.phase == "Succeeded")] | length'
}

drain_vm_manifest() {
  local name=$1 strategy=$2 target=$3
  vm_manifest "$name" "$NS" "$(guest_script plain '')" "$(
    network_yaml default
    eviction_yaml "$strategy"
    prefer_node_yaml "$target"
  )"
}

assert_migrated() {
  local namespace=$1 vm=$2 target=$3 boot=$4
  retry_until 300 vm_left "$target" "$namespace" "$vm" || {
    echo "$vm still runs on $target" >&2
    return 1
  }
  wait_vm_running 300 "$namespace" "$vm"
  wait_guest 300 "$namespace" "$vm" BOOT
  expect_equal "$boot" "$(guest_boot "$namespace" "$vm")" "guest boot id of $vm across the drain"
  test "$(drain_migrations "$namespace" "$vm")" -ge 1
}

drain_nodes() {
  local node status=0
  for node in "$@"; do
    kubectl drain "$node" --ignore-daemonsets --delete-emptydir-data --timeout=1800s || {
      status=1
      break
    }
  done
  for node in "$@"; do
    kubectl uncordon "$node"
  done
  return "$status"
}

check_drain() {
  local preferred if_possible_node always_node if_possible_boot always_boot nodes
  preferred=$(lab_node 3)
  drain_vm_manifest lab-drain-ifp LiveMigrateIfPossible "$preferred" | kubectl apply -f -
  drain_vm_manifest lab-drain-lm LiveMigrate "$preferred" | kubectl apply -f -
  start_guest "$NS" lab-drain-ifp
  start_guest "$NS" lab-drain-lm
  if_possible_node=$(vm_node "$NS" lab-drain-ifp)
  always_node=$(vm_node "$NS" lab-drain-lm)
  echo "info: drain: the preferred node is $preferred, the LiveMigrateIfPossible VM started on $if_possible_node and the LiveMigrate VM on $always_node"
  if_possible_boot=$(guest_boot "$NS" lab-drain-ifp)
  always_boot=$(guest_boot "$NS" lab-drain-lm)
  mapfile -t nodes < <(printf '%s\n' "$if_possible_node" "$always_node" | sort -u)
  drain_nodes "${nodes[@]}" || return 1
  assert_migrated "$NS" lab-drain-ifp "$if_possible_node" "$if_possible_boot"
  assert_migrated "$NS" lab-drain-lm "$always_node" "$always_boot"
  echo "info: drain: the LiveMigrateIfPossible VM moved from $if_possible_node to $(vm_node "$NS" lab-drain-ifp) and the LiveMigrate VM from $always_node to $(vm_node "$NS" lab-drain-lm) with no guest restart"
  delete_vms "$NS" lab-drain-ifp lab-drain-lm
  retry_until 3600 health_ok || {
    ceph_op status >&2
    return 1
  }
}
