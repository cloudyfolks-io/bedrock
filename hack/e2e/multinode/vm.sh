subnet_base() {
  kubectl -n "$1" get subnet "$2" -o jsonpath='{.spec.cidr}' | cut -d. -f1-3
}

underlay_mtu() {
  cat "/sys/class/net/$(ip -json route show default | jq -r '.[0].dev')/mtu"
}

check_volume_access_mode() {
  kubectl -n "$NS" apply -f - <<VOLUME
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Volume
metadata:
  name: lab-rwx
spec:
  sizeGi: 1
VOLUME
  wait_jsonpath 300 Available -n "$NS" get volume lab-rwx -o jsonpath='{.status.phase}'
  expect_equal "ReadWriteMany Block" "$(kubectl -n "$NS" get pvc lab-rwx -o jsonpath='{.spec.accessModes[*]} {.spec.volumeMode}')" "access mode and volume mode of the PVC of a block Volume"
  echo "info: Q5: a Volume on the block class gets $(kubectl -n "$NS" get pvc lab-rwx -o jsonpath='{.spec.accessModes[*]} {.spec.volumeMode}') with no accessModes in the DataVolume"
  kubectl -n "$NS" delete volume lab-rwx --wait=false
  wait_gone 300 volume lab-rwx -n "$NS"
}

check_migration_ip_mac() {
  local before ip mac net boot
  vm_manifest lab-mig "$NS" "$(guest_script plain '')" "$(network_yaml default)" | kubectl apply -f -
  start_guest "$NS" lab-mig
  wait_guest 120 "$NS" lab-mig NET
  before=$(vm_node "$NS" lab-mig)
  ip=$(vm_ip "$NS" lab-mig)
  mac=$(vm_mac "$NS" lab-mig)
  net=$(guest_value "$NS" lab-mig NET)
  boot=$(guest_boot "$NS" lab-mig)
  test -n "$ip"
  test -n "$mac"
  migrate_vm "$NS" lab-mig
  retry_until 300 vm_left "$before" "$NS" lab-mig
  expect_equal "$ip" "$(vm_ip "$NS" lab-mig)" "IP in the VM status after the migration"
  expect_equal "$mac" "$(vm_mac "$NS" lab-mig)" "MAC in the VM status after the migration"
  wait_guest 300 "$NS" lab-mig NET
  expect_equal "$net" "$(guest_value "$NS" lab-mig NET)" "guest MAC, address and MTU after the migration"
  expect_equal "$boot" "$(guest_boot "$NS" lab-mig)" "guest boot id across the migration"
  echo "info: Q7: a live migration from $before to $(vm_node "$NS" lab-mig) kept the IP $ip and the MAC $mac, and the guest kept its boot id"
  delete_vms "$NS" lab-mig
}

xfer_server_ip() {
  echo "$(subnet_base "$NS" default).50"
}

xfer_pair() {
  local server_ip client_ip
  server_ip=$(xfer_server_ip)
  client_ip=$(subnet_base "$NS" default).51
  vm_manifest lab-xfer-server "$NS" "$(guest_script server '')" "$(
    network_yaml default "$server_ip"
    avoid_node_yaml "$(lab_node 2)"
  )" | kubectl apply -f -
  vm_manifest lab-xfer-client "$NS" "$(guest_script client "$server_ip")" "$(
    network_yaml default "$client_ip"
    pin_node_yaml "$(lab_node 2)"
  )" | kubectl apply -f -
  start_guest "$NS" lab-xfer-server
  start_guest "$NS" lab-xfer-client
}

xfer_complete() {
  guest_lines "$NS" lab-xfer-client | awk -v floor="$1" -v want="$xfer_bytes" '$1 == "XFER" && $2 + 0 > floor + 0 && $3 + 0 == want + 0 { found = 1 } END { exit !found }'
}

xfer_floor() {
  guest_lines "$NS" lab-xfer-client | awk '$1 == "XFER" && $2 + 0 > m { m = $2 + 0 } END { print m + 0 }'
}

check_large_transfer() {
  local mtu underlay
  xfer_pair
  retry_until 900 xfer_complete 0 || {
    guest_lines "$NS" lab-xfer-client | tail -n 20 >&2
    return 1
  }
  mtu=$(guest_value "$NS" lab-xfer-client NET | awk '{print $3}')
  underlay=$(underlay_mtu)
  if [ "$((mtu + 100))" -gt "$underlay" ]; then
    echo "guest MTU $mtu plus 100 bytes of Geneve is more than the underlay MTU $underlay" >&2
    return 1
  fi
  echo "info: 8b: a read of $xfer_bytes bytes crossed from $(vm_node "$NS" lab-xfer-server) to $(vm_node "$NS" lab-xfer-client) with guest MTU $mtu on an underlay of $underlay"
}

check_pinned_after_migration() {
  local ip before boot floor
  xfer_pair
  ip=$(xfer_server_ip)
  retry_until 900 xfer_complete 0
  before=$(vm_node "$NS" lab-xfer-server)
  boot=$(guest_boot "$NS" lab-xfer-server)
  migrate_vm "$NS" lab-xfer-server
  retry_until 300 vm_left "$before" "$NS" lab-xfer-server
  expect_equal "$ip" "$(vm_ip "$NS" lab-xfer-server)" "pinned address in the VM status after the migration"
  wait_guest 300 "$NS" lab-xfer-server NET
  expect_contains "$(guest_value "$NS" lab-xfer-server NET)" "$ip/24" "pinned address in the guest after the migration"
  expect_equal "$boot" "$(guest_boot "$NS" lab-xfer-server)" "guest boot id across the migration"
  floor=$(xfer_floor)
  retry_until 600 xfer_complete "$((floor + 1))" || {
    guest_lines "$NS" lab-xfer-client | tail -n 20 >&2
    return 1
  }
  echo "info: a VM with the pinned address $ip moved from $before to $(vm_node "$NS" lab-xfer-server) and the client on $(vm_node "$NS" lab-xfer-client) read $xfer_bytes bytes from it again"
  delete_vms "$NS" lab-xfer-server lab-xfer-client
}

guest_sees_disk() {
  guest_value "$1" "$2" DISKS | grep -E 'sd[a-z]+:2097152' >/dev/null
}

guest_sees_no_scsi_disk() {
  ! guest_value "$1" "$2" DISKS | grep -E 'sd[a-z]+:' >/dev/null
}

check_hotplug() {
  local boot before
  kubectl -n "$NS" apply -f - <<VOLUME
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Volume
metadata:
  name: lab-hot-data
spec:
  sizeGi: 1
VOLUME
  wait_jsonpath 300 Available -n "$NS" get volume lab-hot-data -o jsonpath='{.status.phase}'
  vm_manifest lab-hot "$NS" "$(guest_script hot '')" "$(network_yaml default)" | kubectl apply -f -
  start_guest "$NS" lab-hot
  wait_guest 120 "$NS" lab-hot DISKS
  guest_sees_no_scsi_disk "$NS" lab-hot || {
    echo "the guest shows a SCSI disk before the hot-plug: $(guest_value "$NS" lab-hot DISKS)" >&2
    return 1
  }
  boot=$(guest_boot "$NS" lab-hot)
  kubectl -n "$NS" patch virtualmachine lab-hot --type merge -p '{"spec":{"disks":[{"volumeRef":"lab-hot-data","bus":"scsi"}]}}'
  wait_jsonpath 300 InUse -n "$NS" get volume lab-hot-data -o jsonpath='{.status.phase}'
  retry_until 300 guest_sees_disk "$NS" lab-hot || {
    echo "the guest does not show a 1 GiB SCSI disk: $(guest_value "$NS" lab-hot DISKS)" >&2
    return 1
  }
  expect_equal "$boot" "$(guest_boot "$NS" lab-hot)" "guest boot id after the hot-plug"
  wait_guest 120 "$NS" lab-hot DISKIO
  expect_contains "$(guest_value "$NS" lab-hot DISKIO)" labdata "write and read of the hot-plugged disk"
  echo "info: Q2: a Volume added to spec.disks of a running VM shows in the guest with no restart: $(guest_value "$NS" lab-hot DISKS)"
  before=$(vm_node "$NS" lab-hot)
  migrate_vm "$NS" lab-hot
  retry_until 300 vm_left "$before" "$NS" lab-hot
  retry_until 300 guest_sees_disk "$NS" lab-hot
  expect_equal "$boot" "$(guest_boot "$NS" lab-hot)" "guest boot id across the migration with the disk attached"
  echo "info: Q5: a live migration of a VM with a block Volume attached worked from $before to $(vm_node "$NS" lab-hot)"
  delete_vms "$NS" lab-hot
  kubectl -n "$NS" delete volume lab-hot-data --wait=false
  wait_gone 300 volume lab-hot-data -n "$NS"
}

vmi_uuid() {
  kubectl -n "$1" get virtualmachineinstances.kubevirt.io "$2" -o jsonpath='{.spec.domain.firmware.uuid}'
}

lowercase() {
  tr '[:upper:]' '[:lower:]'
}

check_smbios() {
  local first second other
  vm_manifest lab-smbios "$NS" "$(guest_script plain '')" "$(network_yaml default)" | kubectl apply -f -
  vm_manifest lab-smbios "$NS2" "$(guest_script plain '')" "$(network_yaml default)" | kubectl apply -f -
  vm_manifest lab-smbios-other "$NS" "$(guest_script plain '')" "$(network_yaml default)" | kubectl apply -f -
  start_guest "$NS" lab-smbios
  start_guest "$NS2" lab-smbios
  start_guest "$NS" lab-smbios-other
  wait_guest 120 "$NS" lab-smbios DMI
  wait_guest 120 "$NS2" lab-smbios DMI
  wait_guest 120 "$NS" lab-smbios-other DMI
  first=$(guest_value "$NS" lab-smbios DMI | lowercase)
  second=$(guest_value "$NS2" lab-smbios DMI | lowercase)
  other=$(guest_value "$NS" lab-smbios-other DMI | lowercase)
  test -n "$first"
  test -n "$second"
  test -n "$other"
  expect_equal "$(vmi_uuid "$NS" lab-smbios | lowercase)" "$first" "guest UUID and the firmware UUID of the VMI"
  expect_equal "$(vmi_uuid "$NS2" lab-smbios | lowercase)" "$second" "guest UUID and the firmware UUID of the VMI in the second namespace"
  echo "info: Q3: one VM name in two namespaces gives $([ "$first" = "$second" ] && echo the same UUID || echo different UUIDs): $first and $second"
  echo "info: Q3: two VM names in one namespace give $([ "$first" = "$other" ] && echo the same UUID || echo different UUIDs)"
  delete_vms "$NS" lab-smbios lab-smbios-other
  delete_vms "$NS2" lab-smbios
}

xfer_lines_are_at_least() {
  [ "$(guest_lines "$1" "$2" | grep -c '^XFER ')" -ge "$3" ]
}

xfer_best() {
  guest_lines "$1" "$2" | awk '$1 == "XFER" && $3 + 0 > m { m = $3 + 0 } END { print m + 0 }'
}

check_mtu_1500_finding() {
  local base best verdict
  kubectl -n "$NS" apply -f - <<SUBNET
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Subnet
metadata:
  name: lab-mtu1500
spec:
  networkRef: $TENANT
  cidr: 10.100.16.0/24
  mtu: 1500
SUBNET
  wait_reason 120 subnet lab-mtu1500 Ready Applied -n "$NS"
  base=10.100.16
  vm_manifest lab-mtu-server "$NS" "$(guest_script server '')" "$(
    network_yaml lab-mtu1500 "$base.50"
    avoid_node_yaml "$(lab_node 2)"
  )" | kubectl apply -f -
  vm_manifest lab-mtu-client "$NS" "$(guest_script client "$base.50")" "$(
    network_yaml lab-mtu1500 "$base.51"
    pin_node_yaml "$(lab_node 2)"
  )" | kubectl apply -f -
  start_guest "$NS" lab-mtu-server
  start_guest "$NS" lab-mtu-client
  retry_until 900 xfer_lines_are_at_least "$NS" lab-mtu-client 3
  best=$(xfer_best "$NS" lab-mtu-client)
  verdict=stalled
  if [ "$best" -ge "$xfer_bytes" ]; then
    verdict=completed
  fi
  echo "info: 8b: with Subnet mtu 1500 on an underlay of $(underlay_mtu), guest MTU $(guest_value "$NS" lab-mtu-client NET | awk '{print $3}'), the read of $xfer_bytes bytes $verdict at $best bytes"
  delete_vms "$NS" lab-mtu-server lab-mtu-client
  kubectl -n "$NS" delete subnet lab-mtu1500 --wait=false
  wait_gone 300 subnet lab-mtu1500 -n "$NS"
}
