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
  vm_left "$target" "$namespace" "$vm" || {
    echo "$vm still runs on $target" >&2
    return 1
  }
  wait_vm_running 300 "$namespace" "$vm"
  wait_guest 300 "$namespace" "$vm" BOOT
  expect_equal "$boot" "$(guest_boot "$namespace" "$vm")" "guest boot id of $vm across the drain"
  test "$(drain_migrations "$namespace" "$vm")" -ge 1
}

check_drain() {
  local target if_possible_boot always_boot
  target=$(lab_node 3)
  drain_vm_manifest lab-drain-ifp LiveMigrateIfPossible "$target" | kubectl apply -f -
  drain_vm_manifest lab-drain-lm LiveMigrate "$target" | kubectl apply -f -
  start_guest "$NS" lab-drain-ifp
  start_guest "$NS" lab-drain-lm
  expect_equal "$target" "$(vm_node "$NS" lab-drain-ifp)" "node of the LiveMigrateIfPossible VM before the drain"
  expect_equal "$target" "$(vm_node "$NS" lab-drain-lm)" "node of the LiveMigrate VM before the drain"
  if_possible_boot=$(guest_boot "$NS" lab-drain-ifp)
  always_boot=$(guest_boot "$NS" lab-drain-lm)
  kubectl drain "$target" --ignore-daemonsets --delete-emptydir-data --timeout=1800s || {
    kubectl uncordon "$target"
    return 1
  }
  kubectl uncordon "$target"
  assert_migrated "$NS" lab-drain-ifp "$target" "$if_possible_boot"
  assert_migrated "$NS" lab-drain-lm "$target" "$always_boot"
  echo "info: a drain of $target moved a LiveMigrateIfPossible VM to $(vm_node "$NS" lab-drain-ifp) and a LiveMigrate VM to $(vm_node "$NS" lab-drain-lm) with no guest restart"
  delete_vms "$NS" lab-drain-ifp lab-drain-lm
  retry_until 3600 health_ok || {
    ceph_op status >&2
    return 1
  }
}
