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
