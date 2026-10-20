compute_prepare() {
  ssh-keygen -q -t ed25519 -N '' -f "$workdir/k1"
  kubectl -n "$NS" apply -f - <<KEY
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: SSHKey
metadata:
  name: k1
spec:
  publicKey: $(cut -d' ' -f1,2 "$workdir/k1.pub")
KEY
  wait_reason 60 sshkey k1 Ready Valid -n "$NS"
  kubectl get virtualmachineclusterinstancetype u1.nano
  kubectl get virtualmachineclusterpreference cirros
  expect_equal yes "$(as_user dana auth can-i list virtualmachineclusterinstancetypes)" "owner lists instance types"
  expect_equal no "$(as_user dana -n "$NS" auth can-i create virtualmachines.kubevirt.io)" "owner creates kubevirt VMs"
  expect_equal "4 100 2" "$(kubectl -n kubevirt get kubevirt kubevirt -o jsonpath='{.spec.configuration.developerConfiguration.cpuAllocationRatio} {.spec.configuration.developerConfiguration.memoryOvercommit} {.spec.configuration.migrations.parallelMigrationsPerCluster}')" "kubevirt tuning"
}

compute_volume() {
  kubectl -n "$NS" apply -f - <<VOLUME
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Volume
metadata:
  name: data1
spec:
  sizeGi: 1
VOLUME
  wait_jsonpath 180 Available -n "$NS" get volume data1 -o jsonpath='{.status.phase}'
  kubectl -n "$NS" get datavolume data1 -o jsonpath='{.metadata.labels}' | grep -q 'bedrock.cloudyfolks.io/project'
  kubectl -n "$NS" get pvc data1
}

compute_vm() {
  kubectl -n "$NS" apply -f - <<SECURITYGROUP
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: SecurityGroup
metadata:
  name: web
spec:
  ingress:
    - protocol: tcp
      port: 8080
      cidr: 0.0.0.0/0
    - protocol: icmp
      cidr: 0.0.0.0/0
  egress:
    - protocol: all
      cidr: 0.0.0.0/0
SECURITYGROUP
  wait_condition 180 securitygroup web Ready True -n "$NS"
  kubectl -n "$NS" apply -f - <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: vm1
spec:
  instanceType: u1.nano
  preference: cirros
  image:
    name: cirros
    scope: cluster
  rootDisk:
    sizeGi: 2
  networks:
    - subnetRef: default
      securityGroups: [web]
  cloudInit:
    userData: "#cloud-config\n"
  sshKeys: [k1]
  running: true
VM
  wait_jsonpath 300 Succeeded -n "$NS" get datavolume vm1-root -o jsonpath='{.status.phase}'
  kubectl -n "$NS" get secret vm1-cloudinit
  expect_contains "$(kubectl -n "$NS" get secret vm1-cloudinit -o jsonpath='{.data.userdata}' | base64 -d)" "$(cut -d' ' -f2 "$workdir/k1.pub")" "cloud-init carries the SSH key"
  expect_equal '{"default":true,"networkName":"acme-web/default"}' "$(kubectl -n "$NS" get virtualmachines.kubevirt.io vm1 -o jsonpath='{.spec.template.spec.networks[0].multus}' | jq -cS .)" "multus network"
  expect_equal "" "$(kubectl -n "$NS" get virtualmachines.kubevirt.io vm1 -o jsonpath='{.spec.template.spec.networks[?(@.pod)]}')" "no pod network"
  expect_equal "" "$(kubectl -n "$NS" get virtualmachines.kubevirt.io vm1 -o jsonpath='{.spec.template.spec.nodeName}')" "no node name"
  expect_equal true "$(kubectl -n "$NS" get virtualmachines.kubevirt.io vm1 -o jsonpath='{.spec.template.spec.domain.devices.logSerialConsole}')" "serial console log"
  wait_jsonpath 600 Running -n "$NS" get virtualmachine vm1 -o jsonpath='{.status.phase}'
  expect_equal "$(hostname | tr '[:upper:]' '[:lower:]')" "$(kubectl -n "$NS" get virtualmachine vm1 -o jsonpath='{.status.node}')" "vm node"
  expect_equal default "$(kubectl -n "$NS" get virtualmachine vm1 -o jsonpath='{.status.addresses[0].subnet}')" "vm address subnet"
  wait_not_empty 60 -n "$NS" get virtualmachine vm1 -o jsonpath='{.status.addresses[0].ip}'
  wait_not_empty 60 -n "$NS" get virtualmachine vm1 -o jsonpath='{.status.addresses[0].mac}'
  wait_not_empty 60 -n "$NS" get virtualmachine vm1 -o jsonpath='{.status.console.vncPath}'
  wait_not_empty 60 -n "$NS" get virtualmachine vm1 -o jsonpath='{.status.console.serialPath}'
  wait_reason 60 virtualmachine vm1 Ready Running -n "$NS"
}

compute_hotplug() {
  kubectl -n "$NS" patch virtualmachine vm1 --type merge -p '{"spec":{"disks":[{"volumeRef":"data1","bus":"scsi"}]}}'
  wait_jsonpath 180 InUse -n "$NS" get volume data1 -o jsonpath='{.status.phase}'
  expect_equal vm1 "$(kubectl -n "$NS" get volume data1 -o jsonpath='{.status.attachedTo}')" "volume attachment"
  kubectl -n "$NS" patch volume data1 --type merge -p '{"spec":{"sizeGi":2}}'
  wait_jsonpath 180 2 -n "$NS" get volume data1 -o jsonpath='{.status.actualSizeGi}'
}

compute_snapshot() {
  kubectl -n "$NS" apply -f - <<SNAPSHOT
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VolumeSnapshot
metadata:
  name: snap1
spec:
  volumeRef: data1
  retention: 1h
SNAPSHOT
  wait_jsonpath 180 Ready -n "$NS" get volumesnapshots.bedrock.cloudyfolks.io snap1 -o jsonpath='{.status.phase}'
  expect_equal true "$(kubectl -n "$NS" get volumesnapshots.snapshot.storage.k8s.io snap1 -o jsonpath='{.status.readyToUse}')" "csi snapshot"
  kubectl -n "$NS" apply -f - <<VOLUME
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Volume
metadata:
  name: data1-restore
spec:
  sizeGi: 2
  source:
    snapshotRef: snap1
VOLUME
  wait_jsonpath 300 Available -n "$NS" get volume data1-restore -o jsonpath='{.status.phase}'
}

compute_token_env() {
  TOKEN_OWNER=$(cat "$TOKEN_DIR/dana")
  TOKEN_VIEWER=$(cat "$TOKEN_DIR/carol")
  TOKEN_STRANGER=$(cat "$TOKEN_DIR/erin")
  TOKEN_ADMIN=$(cat "$TOKEN_DIR/admin")
  export TOKEN_OWNER TOKEN_VIEWER TOKEN_STRANGER TOKEN_ADMIN
  export BEDROCK_HOST=$HOST VM=vm1
}

compute_proxy_actions() {
  compute_token_env
  "$test_bin" -test.count=1 -test.v -test.timeout 40m -test.run '^TestProxyActions$'
}

compute_console() {
  compute_token_env
  "$test_bin" -test.count=1 -test.v -test.timeout 15m -test.run '^TestConsoleSerial$'
}

compute_guest_script() {
  cat <<'SCRIPT'
#!/bin/sh
boot=$(cat /proc/sys/kernel/random/boot_id)
{
  echo "COMPUTE-BOOT $boot"
  echo "COMPUTE-CPUS $(grep -c '^processor' /proc/cpuinfo)"
  echo "COMPUTE-MEMKIB $(awk '/^MemTotal/ {print $2}' /proc/meminfo)"
} >/dev/console
cat >/tmp/diskio.sh <<'LOOP'
#!/bin/sh
while :; do
  d=$(ls /sys/block | grep '^sd' | head -n 1)
  if [ -n "$d" ]; then
    printf hotplug | dd of="/dev/$d" bs=7 count=1 2>/dev/null
    sync
    echo "COMPUTE-DISKIO $1 $d $(dd if="/dev/$d" bs=7 count=1 2>/dev/null)" >/dev/console
    exit 0
  fi
  sleep 5
done
LOOP
setsid sh /tmp/diskio.sh "$boot" </dev/null >/dev/null 2>&1 &
SCRIPT
}

compute_size_vm_manifest() {
  cat <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: size-vm
spec:
  compute:
    cores: 2
    memoryMiB: 512
  preference: cirros
  image:
    name: cirros
    scope: cluster
  rootDisk:
    sizeGi: 2
  networks:
    - subnetRef: default
  cloudInit:
    userData: |
$(compute_guest_script | sed 's/^/      /')
  running: true
VM
}

compute_boot_vm_manifest() {
  cat <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: boot-vm
spec:
  instanceType: u1.nano
  preference: cirros
  rootDisk:
    volumeRef: boot1
  networks:
    - subnetRef: default
  cloudInit:
    userData: |
$(compute_guest_script | sed 's/^/      /')
  running: true
VM
}

compute_guest_value() {
  guest_console_log "$1" | tr -d '\r' | sed -n "s/.*COMPUTE-$2 //p" | sed 's/ *$//' | tail -1
}

compute_guest_reported() {
  [ -n "$(compute_guest_value "$1" "$2")" ]
}

expect_between() {
  local low=$1 high=$2 got=$3 what=$4
  if [ -z "$got" ] || [ "$got" -lt "$low" ] || [ "$got" -gt "$high" ]; then
    echo "$what: want from $low to $high, got '$got'" >&2
    return 1
  fi
}

compute_vm_cleanup() {
  local name=$1 status=$2
  if [ "$status" -ne 0 ]; then
    guest_console_log "$name" >&2 || true
    kubectl -n "$NS" get virtualmachine "$name" -o yaml >&2 || true
  fi
  kubectl -n "$NS" delete virtualmachine "$name" --ignore-not-found
  wait_gone 180 virtualmachine "$name" -n "$NS"
  wait_empty 180 -n "$NS" get pods -l "kubevirt.io/domain=$name"
  wait_gone 180 datavolume "$name-root" -n "$NS"
}

compute_size_cleanup() {
  compute_vm_cleanup size-vm "$1"
  kubectl -n "$NS" delete volume hot1 --ignore-not-found
  wait_gone 180 volume hot1 -n "$NS"
}

compute_size_checks() {
  kubectl -n "$NS" apply -f - <<VOLUME
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Volume
metadata:
  name: hot1
spec:
  sizeGi: 1
VOLUME
  wait_jsonpath 180 Available -n "$NS" get volume hot1 -o jsonpath='{.status.phase}'
  compute_size_vm_manifest | kubectl -n "$NS" apply -f -
  wait_jsonpath 600 Running -n "$NS" get virtualmachine size-vm -o jsonpath='{.status.phase}'
  expect_equal "2 1 1 512Mi" "$(kubectl -n "$NS" get virtualmachines.kubevirt.io size-vm -o jsonpath='{.spec.template.spec.domain.cpu.sockets} {.spec.template.spec.domain.cpu.cores} {.spec.template.spec.domain.cpu.threads} {.spec.template.spec.domain.memory.guest}')" "size in the KubeVirt VM"
  expect_equal "" "$(kubectl -n "$NS" get virtualmachines.kubevirt.io size-vm -o jsonpath='{.spec.instancetype.name}')" "no instance type on a custom size"
  retry_until 1200 compute_guest_reported size-vm MEMKIB || {
    guest_console_log size-vm | tail -80 >&2
    kubectl -n "$NS" get vmi size-vm -o jsonpath='{.spec.domain.cpu}{"\n"}{.spec.domain.memory}{"\n"}{.status.phase}{"\n"}' >&2
    return 1
  }
  expect_equal 2 "$(compute_guest_value size-vm CPUS)" "guest cpu count"
  expect_between 400000 524288 "$(compute_guest_value size-vm MEMKIB)" "guest memory in KiB"
  kubectl -n "$NS" patch virtualmachine size-vm --type merge -p '{"spec":{"disks":[{"volumeRef":"hot1","bus":"scsi"}]}}'
  wait_jsonpath 180 InUse -n "$NS" get volume hot1 -o jsonpath='{.status.phase}'
  retry_until 1200 compute_guest_reported size-vm DISKIO
  expect_contains "$(compute_guest_value size-vm DISKIO)" hotplug "guest write and read of the hot-plugged disk"
  expect_contains "$(compute_guest_value size-vm DISKIO)" "$(compute_guest_value size-vm BOOT)" "guest boot id after the hot-plug"
}

compute_size_and_hotplug() {
  (
    trap 'compute_size_cleanup "$?"' EXIT
    compute_size_checks
  )
}

compute_boot_cleanup() {
  compute_vm_cleanup boot-vm "$1"
  kubectl -n "$NS" delete volume boot1 --ignore-not-found
  wait_gone 180 volume boot1 -n "$NS"
}

compute_boot_checks() {
  kubectl -n "$NS" apply -f - <<VOLUME
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Volume
metadata:
  name: boot1
spec:
  sizeGi: 2
  source:
    image:
      name: cirros
      scope: cluster
VOLUME
  wait_jsonpath 300 Available -n "$NS" get volume boot1 -o jsonpath='{.status.phase}'
  compute_boot_vm_manifest | kubectl -n "$NS" apply -f -
  wait_jsonpath 600 Running -n "$NS" get virtualmachine boot-vm -o jsonpath='{.status.phase}'
  expect_equal "" "$(kubectl -n "$NS" get datavolume boot-vm-root -o name 2>/dev/null)" "no root claim of the VM"
  wait_jsonpath 60 InUse -n "$NS" get volume boot1 -o jsonpath='{.status.phase}'
  expect_equal boot-vm "$(kubectl -n "$NS" get volume boot1 -o jsonpath='{.status.attachedTo}')" "boot volume attachment"
  retry_until 1200 compute_guest_reported boot-vm BOOT
  kubectl -n "$NS" delete virtualmachine boot-vm
  wait_gone 180 virtualmachine boot-vm -n "$NS"
  wait_jsonpath 180 Available -n "$NS" get volume boot1 -o jsonpath='{.status.phase}'
  kubectl -n "$NS" get pvc boot1
}

compute_boot_volume() {
  (
    trap 'compute_boot_cleanup "$?"' EXIT
    compute_boot_checks
  )
}

compute_block_access() {
  expect_equal "ReadWriteMany Block" "$(kubectl -n "$NS" get pvc data1 -o jsonpath='{.spec.accessModes[*]} {.spec.volumeMode}')" "access mode and volume mode of a block Volume"
}
