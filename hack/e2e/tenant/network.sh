fabric_by_label() {
  local resource=$1 kind=$2 name=$3
  kubectl get "$resource.fabric.cloudyfolks.io" -l "bedrock.cloudyfolks.io/kind=$kind,bedrock.cloudyfolks.io/name=$name,bedrock.cloudyfolks.io/project=$PROJECT" "${@:4}"
}

network_underlay() {
  underlay_is_ready
  wait_jsonpath 180 true get provider-networks.fabric.cloudyfolks.io public -o jsonpath='{.status.ready}'
  expect_equal 0 "$(kubectl get vlans.fabric.cloudyfolks.io public -o jsonpath='{.spec.id}')" "vlan id"
  expect_equal "192.0.2.0/24 public" "$(kubectl get subnets.fabric.cloudyfolks.io provider-public -o jsonpath='{.spec.cidrBlock} {.spec.vlan}')" "provider subnet"
  expect_equal true "$(kubectl get network "$TENANT" -o jsonpath='{.spec.enableExternal}')" "default network is external"
  expect_equal public "$(kubectl get subnets.fabric.cloudyfolks.io provider-public -o jsonpath='{.metadata.labels.usability}')" "tenant pool usability"
  wait_reason 180 network "$TENANT" Ready VpcReady
  wait_reason 180 network "$TENANT" ExternalReady Connected
  expect_equal "$TENANT" "$(kubectl get network "$TENANT" -o jsonpath='{.status.vpc}')" "network vpc"
  expect_contains "$(kubectl get network "$TENANT" -o jsonpath='{range .status.subnets[*]}{.namespace}/{.name}{"\n"}{end}')" "$NS/default" "network subnets"
  expect_equal 'true true ["provider-public"]' "$(kubectl get vpcs.fabric.cloudyfolks.io "$TENANT" -o jsonpath='{.status.standby} {.spec.enableExternal} {.spec.extraExternalSubnets}')" "vpc external"
  wait_reason 120 subnet default Ready Applied -n "$NS"
  expect_equal '{"cniVersion":"0.3.0","type":"fabric","server_socket":"/run/openvswitch/fabric-daemon.sock","provider":"default.'"$NS"'.fabric"}' "$(kubectl -n "$NS" get net-attach-def default -o jsonpath='{.spec.config}')" "network attachment"
  expect_equal "default.$NS.fabric $TENANT" "$(fabric_by_label subnets Subnet default -o jsonpath='{.items[0].spec.provider} {.items[0].spec.vpc}')" "fabric subnet"
}

network_frr_agent() {
  local node
  node=$(hostname | tr '[:upper:]' '[:lower:]')
  expect_equal true "$(kubectl get node "$node" -o jsonpath='{.metadata.labels.fabric\.cloudyfolks\.io/external-gw}')" "gateway node label"
  expect_equal true "$(kubectl -n kube-system get daemonset fabric-frr -o jsonpath='{.spec.template.spec.nodeSelector.fabric\.cloudyfolks\.io/external-gw}')" "fabric-frr node selector"
  wait_jsonpath 300 1 -n kube-system get daemonset fabric-frr -o jsonpath='{.status.numberReady}'
  retry_until 120 frr_pod_running_on "$node"
}

frr_pod_running_on() {
  kubectl -n kube-system get pods -o json |
    jq -e --arg node "$1" '[.items[] | select(.metadata.name | startswith("fabric-frr-")) | select(.spec.nodeName == $node and .status.phase == "Running")] | length == 1'
}

network_subnet_rules() {
  subnet_manifest bad-outside 10.200.1.0/24 | expect_refused "inside one of the cidrs of its Network" kubectl -n "$NS" apply -f -
  subnet_manifest bad-overlap 10.100.0.0/20 | expect_refused "overlaps another Subnet" kubectl -n "$NS" apply -f -
  subnet_manifest bad-host-bits 10.100.1.5/24 | expect_refused "cidr" kubectl -n "$NS" apply -f -
}

subnet_manifest() {
  cat <<SUBNET
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Subnet
metadata:
  name: $1
spec:
  networkRef: $TENANT
  cidr: $2
SUBNET
}

network_security_group() {
  wait_reason 120 securitygroup web Ready Programmed -n "$NS"
  expect_equal 8080 "$(fabric_by_label security-groups SecurityGroup web -o json | jq -r '[.items[0].spec.ingressRules[].portRangeMin] | map(select(. == 8080)) | .[0]')" "fabric ingress rule"
  retry_until 120 launcher_carries_group web
}

launcher_carries_group() {
  [ "$(kubectl -n "$NS" get pod -l kubevirt.io=virt-launcher -o jsonpath="{.items[0].metadata.labels.sg\\.bedrock\\.cloudyfolks\\.io/$1}")" = true ]
}

guest_report_script() {
  cat <<'SCRIPT'
#!/bin/sh
{
  echo "PARITY-LINK $(ip -o link show eth0)"
  echo "PARITY-ADDR $(ip -4 -o addr show eth0 | awk '{print $4}')"
  echo "PARITY-DNS $(grep '^nameserver' /etc/resolv.conf | cut -d' ' -f2 | tr '\n' ' ')"
  echo "PARITY-DONE"
} > /dev/console
SCRIPT
}

parity_subnet_manifest() {
  cat <<SUBNET
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Subnet
metadata:
  name: parity
spec:
  networkRef: $TENANT
  cidr: 10.100.16.0/24
  mtu: 1350
  dnsServers: ["198.51.100.53", "198.51.100.54"]
SUBNET
}

parity_vm_manifest() {
  cat <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: $1
spec:
  instanceType: u1.nano
  preference: cirros
  image:
    name: cirros
    scope: cluster
  rootDisk:
    sizeGi: 2
  networks:
    - subnetRef: parity
      address: $2
  cloudInit:
    userData: |
$(guest_report_script | sed 's/^/      /')
  running: true
VM
}

launcher_annotation() {
  kubectl -n "$NS" get pods -l "vm.kubevirt.io/name=$1" -o json |
    jq -r --arg key "parity.$NS.fabric.cloudyfolks.io/$2" '.items[0].metadata.annotations[$key]'
}

guest_has_reported() {
  [[ "$(guest_console_log "$1")" == *PARITY-DONE* ]]
}

guest_value() {
  guest_console_log "$1" | tr -d '\r' | sed -n "s/.*PARITY-$2 //p" | sed 's/ *$//' | tail -1
}

expect_guest_dns() {
  local want=$1 got=$2
  if [ "$want" != "$got" ]; then
    echo "guest DNS is '$got', want '$want' from Subnet dnsServers: the guest does not take its DNS from Fabric DHCP, so the VM needs dnsPolicy None" >&2
    return 1
  fi
}

parity_cleanup() {
  if [ "$1" -ne 0 ]; then
    guest_console_log vm2 >&2 || true
    kubectl -n "$NS" get virtualmachine vm2 vm-bad -o yaml >&2 || true
    kubectl -n "$NS" describe pods -l 'vm.kubevirt.io/name in (vm2,vm-bad)' >&2 || true
  fi
  kubectl -n "$NS" delete virtualmachine vm2 vm-bad --ignore-not-found
  wait_gone 180 virtualmachine vm2 -n "$NS"
  wait_gone 180 virtualmachine vm-bad -n "$NS"
  wait_empty 180 -n "$NS" get pods -l 'vm.kubevirt.io/name in (vm2,vm-bad)'
  wait_gone 180 datavolume vm2-root -n "$NS"
  wait_gone 180 datavolume vm-bad-root -n "$NS"
  kubectl -n "$NS" delete subnet parity --ignore-not-found
  wait_gone 180 subnet parity -n "$NS"
}

parity_checks() {
  parity_subnet_manifest | kubectl -n "$NS" apply -f -
  wait_reason 120 subnet parity Ready Applied -n "$NS"
  expect_equal "1350" "$(fabric_by_label subnets Subnet parity -o jsonpath='{.items[0].spec.mtu}')" "fabric subnet mtu"
  expect_contains "$(fabric_by_label subnets Subnet parity -o jsonpath='{.items[0].spec.dhcpV4Options}')" "mtu=1350" "dhcp mtu option"
  expect_contains "$(fabric_by_label subnets Subnet parity -o jsonpath='{.items[0].spec.dhcpV4Options}')" "dns_server=198.51.100.53;198.51.100.54" "dhcp dns option"
  parity_vm_manifest vm-bad 10.100.16.1 | kubectl -n "$NS" apply -f -
  wait_reason 120 virtualmachine vm-bad NetworkReady AddressReserved -n "$NS"
  expect_equal "" "$(kubectl -n "$NS" get pods -l vm.kubevirt.io/name=vm-bad -o name)" "no pod for a reserved address"
  kubectl -n "$NS" delete virtualmachine vm-bad
  wait_gone 180 virtualmachine vm-bad -n "$NS"
  parity_vm_manifest vm2 10.100.16.50 | kubectl -n "$NS" apply -f -
  wait_jsonpath 600 Running -n "$NS" get virtualmachine vm2 -o jsonpath='{.status.phase}'
  expect_equal 10.100.16.50 "$(kubectl -n "$NS" get virtualmachine vm2 -o jsonpath='{.status.addresses[0].ip}')" "pinned address in the VM status"
  expect_equal "10.100.16.50 true" "$(launcher_annotation vm2 ip_address) $(launcher_annotation vm2 allocated)" "pinned address on the launcher pod"
  retry_until 300 guest_has_reported vm2
  expect_contains "$(guest_value vm2 LINK)" "mtu 1350" "guest mtu from Subnet mtu"
  expect_equal "10.100.16.50/24" "$(guest_value vm2 ADDR)" "guest address"
  expect_guest_dns "198.51.100.53 198.51.100.54" "$(guest_value vm2 DNS)"
}

network_guest_parity() {
  (
    trap 'parity_cleanup "$?"' EXIT
    parity_checks
  )
}

fabric_equals() {
  local want=$1 resource=$2 kind=$3 name=$4 path=$5
  [ "$(fabric_by_label "$resource" "$kind" "$name" -o jsonpath="$path" 2>/dev/null)" = "$want" ]
}

provider_pool_address() {
  case "$1" in
  192.0.2.1 | 192.0.2.250) return 1 ;;
  192.0.2.[0-9]*) return 0 ;;
  *) return 1 ;;
  esac
}

provider_peer_resolves() {
  ip netns exec bdrext ping -c 1 -W 2 "$1" >/dev/null 2>&1
  ip -n bdrext neigh show "$1" | grep -q lladdr
}

vm_address() {
  kubectl -n "$NS" get virtualmachine vm1 -o jsonpath='{.status.addresses[0].ip}'
}

elastic_ip_address() {
  kubectl -n "$NS" get elasticip "$1" -o jsonpath='{.status.address}'
}

network_elastic_ip() {
  local address
  kubectl -n "$NS" apply -f - <<ELASTICIP
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ElasticIP
metadata:
  name: ip1
spec:
  providerNetwork: public
ELASTICIP
  wait_reason 120 elasticip ip1 Ready Reserved -n "$NS"
  address=$(elastic_ip_address ip1)
  provider_pool_address "$address"
  expect_equal "$address" "$(fabric_by_label ovn-eips ElasticIP ip1 -o jsonpath='{.items[0].status.v4Ip}')" "ovn eip address"
  kubectl -n "$NS" patch elasticip ip1 --type merge -p '{"spec":{"target":{"kind":"VirtualMachine","name":"vm1"}}}'
  wait_reason 180 elasticip ip1 Ready Attached -n "$NS"
  retry_until 120 fabric_equals true ovn-fips ElasticIP ip1 '{.items[0].status.ready}'
  expect_contains "$(kubectl -n kube-system exec deployment/ovn-central -- ovn-nbctl lr-nat-list "$TENANT")" "$address" "nat row"
  expect_contains "$(kubectl -n kube-system exec deployment/ovn-central -- ovn-nbctl lr-nat-list "$TENANT")" "$(vm_address)" "nat row"
  expect_equal "$address" "$(kubectl -n "$NS" get virtualmachine vm1 -o jsonpath='{.status.elasticIP}')" "vm elastic ip"
  retry_until 300 provider_peer_resolves "$address"
  retry_until 600 ip netns exec bdrext ping -c 3 -W 2 "$address"
  kubectl -n "$NS" patch elasticip ip1 --type merge -p '{"spec":{"target":null}}'
  wait_reason 180 elasticip ip1 Ready Reserved -n "$NS"
  expect_equal "$address" "$(elastic_ip_address ip1)" "address after detach"
  wait_empty 120 get ovn-fips.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=ip1
  kubectl -n "$NS" delete elasticip ip1
  wait_empty 180 get ovn-eips.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=ip1
}

network_elastic_ip_pool_choice() {
  local address
  elastic_ip_manifest ip-auto | kubectl -n "$NS" apply -f -
  wait_reason 120 elasticip ip-auto Ready Reserved -n "$NS"
  expect_equal public "$(kubectl -n "$NS" get elasticip ip-auto -o jsonpath='{.status.providerNetwork}')" "chosen pool in the status"
  address=$(elastic_ip_address ip-auto)
  provider_pool_address "$address"
  expect_equal "$address" "$(fabric_by_label ovn-eips ElasticIP ip-auto -o jsonpath='{.items[0].status.v4Ip}')" "ovn eip address of the chosen pool"
}

tenant_elastic_ip_count() {
  local namespaces namespace names count=0
  namespaces=$(kubectl get namespaces -l "bedrock.cloudyfolks.io/tenant=$TENANT" -o jsonpath='{.items[*].metadata.name}') || return 1
  for namespace in $namespaces; do
    names=$(kubectl -n "$namespace" get elasticips -o name) || return 1
    if [ -n "$names" ]; then
      count=$((count + $(wc -l <<<"$names")))
    fi
  done
  echo "$count"
}

set_elastic_ip_quota() {
  kubectl patch tenant "$TENANT" --type merge -p "{\"spec\":{\"quota\":{\"elasticIPs\":${1:-null}}}}"
}

quota_cleanup() {
  set_elastic_ip_quota "$1"
  kubectl -n "$NS" delete elasticip quota-first quota-second ip-auto --ignore-not-found
  wait_empty 180 get ovn-eips.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=quota-first
  wait_empty 180 get ovn-eips.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=quota-second
  wait_empty 180 get ovn-eips.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=ip-auto
}

quota_checks() {
  local count
  count=$(tenant_elastic_ip_count)
  set_elastic_ip_quota "$((count + 1))"
  elastic_ip_manifest quota-first | kubectl -n "$NS" apply -f -
  wait_reason 120 elasticip quota-first Ready Reserved -n "$NS"
  elastic_ip_manifest quota-second | kubectl -n "$NS" apply -f -
  wait_reason 120 elasticip quota-second Ready QuotaExceeded -n "$NS"
  expect_equal "" "$(kubectl -n "$NS" get elasticip quota-second -o jsonpath='{.status.providerNetwork}')" "pool of an over-quota elastic ip"
  expect_equal "" "$(fabric_by_label ovn-eips ElasticIP quota-second -o name)" "ovn eip of an over-quota elastic ip"
  kubectl -n "$NS" delete elasticip quota-first
  wait_reason 120 elasticip quota-second Ready Reserved -n "$NS"
}

network_elastic_ip_quota() {
  local previous
  previous=$(kubectl get tenant "$TENANT" -o jsonpath='{.spec.quota.elasticIPs}')
  (
    trap 'quota_cleanup "$previous"' EXIT
    quota_checks
  )
}

elastic_ip_manifest() {
  cat <<ELASTICIP
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ElasticIP
metadata:
  name: $1
spec: {}
ELASTICIP
}

network_load_balancer_and_dns() {
  local address
  kubectl -n "$NS" apply -f - <<BALANCER
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: LoadBalancer
metadata:
  name: lb1
spec:
  elasticIPRef: lbip
  listeners:
    - port: 8080
      protocol: TCP
      targets:
        - vmRef: vm1
          port: 8080
---
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ElasticIP
metadata:
  name: lbip
spec:
  providerNetwork: public
  target:
    kind: LoadBalancer
    name: lb1
BALANCER
  wait_reason 180 loadbalancer lb1 Ready Programmed -n "$NS"
  address=$(elastic_ip_address lbip)
  expect_contains "$(kubectl -n kube-system exec deployment/ovn-central -- ovn-nbctl list Load_Balancer)" "\"$address:8080\"" "load balancer vip"
  kubectl -n "$NS" delete loadbalancer lb1
  wait_empty 180 get router-lb-rules.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=lb1
  kubectl -n "$NS" delete elasticip lbip
  kubectl -n "$NS" apply -f - <<ZONE
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: DNSZone
metadata:
  name: internal
spec:
  zone: internal.e2e
  records:
    - name: db
      ips: ["$(vm_address)"]
ZONE
  kubectl -n "$NS" patch subnet default --type merge -p '{"spec":{"dnsZoneRef":"internal"}}'
  wait_reason 120 dnszone internal Ready Programmed -n "$NS"
  expect_equal "$TENANT" "$(fabric_by_label dns-zones DNSZone internal -o jsonpath='{.items[0].spec.vpc}')" "fabric dns zone vpc"
  kubectl -n "$NS" patch subnet default --type merge -p '{"spec":{"dnsZoneRef":null}}'
  wait_reason 120 dnszone internal Ready NotAttached -n "$NS"
}

network_isolation() {
  local images pause
  images=$(k0s ctr --namespace k8s.io images ls -q)
  pause=$(grep -m1 '/pause:' <<<"$images")
  isolated_pod_manifest "$pause" k8s.v1.cni.cncf.io/networks other-ns/default |
    expect_refused "only the platform may set network selection annotations" kubectl --as=bedrock:e2e-user --as-group=system:masters -n "$NS" apply -f -
  isolated_pod_manifest "$pause" fabric.cloudyfolks.io/logical_switch default |
    expect_refused "only the platform may set network selection annotations" kubectl --as=bedrock:e2e-user --as-group=system:masters -n "$NS" apply -f -
  expect_equal Running "$(kubectl -n "$NS" get virtualmachine vm1 -o jsonpath='{.status.phase}')" "vm1 under the pod policy"
}

isolated_pod_manifest() {
  cat <<POD
apiVersion: v1
kind: Pod
metadata:
  name: isolation-probe
  annotations:
    $2: $3
spec:
  containers:
    - name: pause
      image: $1
      imagePullPolicy: Never
POD
}

probe_script() {
  local tag=$1 address=$2
  cat <<SCRIPT
#!/bin/sh
result=fail
attempt=0
while [ \$attempt -lt 60 ]; do
  if ping -c 1 -W 2 $address >/dev/null 2>&1; then
    result=ok
    break
  fi
  attempt=\$((attempt + 1))
  sleep 2
done
echo "PROBE-$tag \$result" > /dev/console
SCRIPT
}

probe_vm_manifest() {
  local name=$1 subnet=$2 tag=$3 address=$4
  cat <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: $name
spec:
  instanceType: u1.nano
  preference: cirros
  image:
    name: cirros
    scope: cluster
  rootDisk:
    sizeGi: 2
  networks:
    - subnetRef: $subnet
      securityGroups: [web]
  cloudInit:
    userData: |
$(probe_script "$tag" "$address" | sed 's/^/      /')
  running: true
VM
}

probe_value() {
  guest_console_log "$1" | tr -d '\r' | sed -n "s/.*PROBE-$2 //p" | sed 's/ *$//' | tail -1
}

probe_reported() {
  [ -n "$(probe_value "$1" "$2")" ]
}

probe_vm_succeeds() {
  local name=$1 subnet=$2 tag=$3 address=$4
  probe_vm_manifest "$name" "$subnet" "$tag" "$address" | kubectl -n "$NS" apply -f -
  wait_jsonpath 600 Running -n "$NS" get virtualmachine "$name" -o jsonpath='{.status.phase}'
  retry_until 300 probe_reported "$name" "$tag"
  expect_equal ok "$(probe_value "$name" "$tag")" "guest ping $tag to $address"
}

probe_vm_cleanup() {
  local name=$1 status=$2
  if [ "$status" -ne 0 ]; then
    guest_console_log "$name" >&2 || true
    kubectl -n "$NS" get virtualmachine "$name" -o yaml >&2 || true
  fi
  kubectl -n "$NS" delete virtualmachine "$name" --ignore-not-found
  wait_gone 180 virtualmachine "$name" -n "$NS"
  wait_empty 180 -n "$NS" get pods -l "vm.kubevirt.io/name=$name"
  wait_gone 180 datavolume "$name-root" -n "$NS"
}

internet_gateway_manifest() {
  cat <<GATEWAY
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ElasticIP
metadata:
  name: igw-ip
spec:
  providerNetwork: public
  target:
    kind: InternetGateway
    name: igw1
---
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: InternetGateway
metadata:
  name: igw1
spec:
  networkRef: $TENANT
  elasticIPRef: igw-ip
GATEWAY
}

internet_gateway_cleanup() {
  probe_vm_cleanup igw-probe "$1"
  kubectl -n "$NS" delete internetgateway igw1 --ignore-not-found
  wait_gone 180 internetgateway igw1 -n "$NS"
  kubectl -n "$NS" delete elasticip igw-ip --ignore-not-found
  wait_gone 180 elasticip igw-ip -n "$NS"
  wait_empty 180 get ovn-snat-rules.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=igw1
  wait_empty 180 get ovn-eips.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=igw-ip
}

internet_gateway_checks() {
  local address
  internet_gateway_manifest | kubectl -n "$NS" apply -f -
  wait_reason 120 elasticip igw-ip Ready Attached -n "$NS"
  wait_reason 180 internetgateway igw1 Ready Programmed -n "$NS"
  address=$(elastic_ip_address igw-ip)
  provider_pool_address "$address"
  expect_equal 1 "$(fabric_by_label ovn-snat-rules InternetGateway igw1 -o name | wc -l)" "one snat rule for the one range of the Network"
  expect_contains "$(kubectl -n kube-system exec deployment/ovn-central -- ovn-nbctl lr-nat-list "$TENANT")" "$address" "snat row of the gateway address"
  probe_vm_succeeds igw-probe default igw 192.0.2.250
}

network_internet_gateway() {
  (
    trap 'internet_gateway_cleanup "$?"' EXIT
    internet_gateway_checks
  )
}

peer_network_manifest() {
  cat <<NETWORK
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Network
metadata:
  name: peer
spec:
  tenantRef: $TENANT
  cidrs: ["10.101.0.0/16"]
---
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: NetworkPeering
metadata:
  name: $TENANT-peer
spec:
  networkRefs: [$TENANT, peer]
NETWORK
}

peer_subnet_manifest() {
  cat <<SUBNET
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Subnet
metadata:
  name: peer
spec:
  networkRef: peer
  cidr: 10.101.0.0/24
SUBNET
}

peering_cleanup() {
  probe_vm_cleanup peer-probe "$1"
  kubectl delete networkpeering "$TENANT-peer" --ignore-not-found
  wait_gone 180 networkpeering "$TENANT-peer"
  kubectl -n "$NS" delete subnet peer --ignore-not-found
  wait_gone 180 subnet peer -n "$NS"
  kubectl delete network peer --ignore-not-found
  wait_gone 180 network peer
}

vpc_has_no_route_to() {
  local routes
  routes=$(kubectl get vpcs.fabric.cloudyfolks.io "$1" -o jsonpath='{.spec.staticRoutes}') || return 1
  [[ "$routes" != *"\"$2\""* ]]
}

peering_checks() {
  peer_network_manifest | kubectl apply -f -
  wait_reason 180 network peer Ready VpcReady
  peer_subnet_manifest | kubectl -n "$NS" apply -f -
  wait_reason 120 subnet peer Ready Applied -n "$NS"
  wait_reason 180 networkpeering "$TENANT-peer" Ready Programmed
  expect_contains "$(kubectl get networkpeering "$TENANT-peer" -o jsonpath='{.status.connectCIDR}')" "169.254." "connect network from the peering range"
  expect_contains "$(kubectl get vpcs.fabric.cloudyfolks.io "$TENANT" -o jsonpath='{.spec.vpcPeerings}')" peer "peering in the Vpc of the tenant Network"
  expect_contains "$(kubectl get vpcs.fabric.cloudyfolks.io peer -o jsonpath='{.spec.vpcPeerings}')" "$TENANT" "peering in the Vpc of the peer Network"
  expect_contains "$(kubectl get vpcs.fabric.cloudyfolks.io "$TENANT" -o jsonpath='{.spec.staticRoutes}')" 10.101.0.0/16 "route to the peer Network"
  probe_vm_succeeds peer-probe peer peering "$(vm_address)"
  kubectl delete networkpeering "$TENANT-peer"
  wait_gone 180 networkpeering "$TENANT-peer"
  wait_jsonpath 180 "[]" get vpcs.fabric.cloudyfolks.io "$TENANT" -o jsonpath='{.spec.vpcPeerings}'
  wait_jsonpath 180 "[]" get vpcs.fabric.cloudyfolks.io peer -o jsonpath='{.spec.vpcPeerings}'
  retry_until 180 vpc_has_no_route_to "$TENANT" 10.101.0.0/16
}

network_peering() {
  (
    trap 'peering_cleanup "$?"' EXIT
    peering_checks
  )
}

internal_load_balancer_cleanup() {
  kubectl -n "$NS" delete loadbalancer ilb1 --ignore-not-found
  wait_gone 180 loadbalancer ilb1 -n "$NS"
  wait_empty 180 get vips.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=ilb1
  wait_empty 180 get switch-lb-rules.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=ilb1
}

internal_load_balancer_checks() {
  local address
  kubectl -n "$NS" apply -f - <<BALANCER
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: LoadBalancer
metadata:
  name: ilb1
spec:
  listeners:
    - port: 8080
      protocol: TCP
      targets:
        - vmRef: vm1
          port: 8080
BALANCER
  wait_reason 180 loadbalancer ilb1 Ready Programmed -n "$NS"
  address=$(kubectl -n "$NS" get loadbalancer ilb1 -o jsonpath='{.status.address}')
  expect_equal "100.69.0.0/24 100.69.0.1" "$(kubectl get subnets.fabric.cloudyfolks.io "$TENANT-lb" -o jsonpath='{.spec.cidrBlock} {.spec.gateway}')" "load balancer plane"
  expect_equal "$TENANT-lb" "$(fabric_by_label vips LoadBalancer ilb1 -o jsonpath='{.items[0].spec.subnet}')" "vip subnet"
  expect_equal "$address" "$(fabric_by_label vips LoadBalancer ilb1 -o jsonpath='{.items[0].status.v4ip}')" "vip address"
  case "$address" in
  100.69.0.[0-9]*) ;;
  *)
    echo "vip $address is not in the load balancer plane" >&2
    return 1
    ;;
  esac
  expect_equal "" "$(fabric_by_label vips LoadBalancer ilb1 -o jsonpath='{.items[0].spec.type}')" "vip type"
  expect_contains "$(kubectl -n kube-system exec deployment/ovn-central -- ovn-nbctl list Load_Balancer)" "\"$address:8080\"" "load balancer vip in OVN"
}

network_internal_load_balancer() {
  (
    trap internal_load_balancer_cleanup EXIT
    internal_load_balancer_checks
  )
}

services_settings() {
  local provider=$1 range=$2 resolver=$3
  kubectl patch setting network.services.provider-network --type merge -p "{\"spec\":{\"value\":\"$provider\"}}"
  kubectl patch setting network.services.identity-range --type merge -p "{\"spec\":{\"value\":\"$range\"}}"
  kubectl patch setting network.services.resolver --type merge -p "{\"spec\":{\"value\":\"$resolver\"}}"
}

services_checks() {
  local identity
  services_settings services 203.0.115.240/29 203.0.112.10
  wait_not_empty 180 get network "$TENANT" -o jsonpath='{.status.servicesIdentity}'
  wait_reason 180 network "$TENANT" ExternalReady Connected
  identity=$(kubectl get network "$TENANT" -o jsonpath='{.status.servicesIdentity}')
  case "$identity" in
  203.0.115.24[1-6]) ;;
  *)
    echo "services identity $identity is not a usable address of 203.0.115.240/29" >&2
    return 1
    ;;
  esac
  expect_equal "$identity" "$(kubectl get ovn-eips.fabric.cloudyfolks.io "$TENANT-services-snat" -o jsonpath='{.spec.v4Ip}')" "identity address of the Fabric eip"
  expect_equal 3 "$(kubectl get ovn-snat-rules.fabric.cloudyfolks.io -o name | grep -c "/$TENANT-services-snat-")" "identity snat rules"
  expect_contains "$(kubectl get vpcs.fabric.cloudyfolks.io "$TENANT" -o jsonpath='{.spec.extraExternalSubnets}')" provider-services "services plane in the Vpc"
}

network_services_identity() {
  services_checks
}

guard_namespace=guard-e2e
guard_leg_address=192.0.2.249
guard_leg_interface=br-public
guard_node_port=30180

host_default_interface() {
  ip -json route show default | jq -r '.[0].dev'
}

host_management_address() {
  ip -json -4 addr show dev "$(host_default_interface)" | jq -r '.[0].addr_info[0].local'
}

host_management_cidr() {
  local interface_address
  interface_address=$(ip -json -4 addr show dev "$(host_default_interface)" | jq -r '.[0].addr_info[0] | "\(.local)/\(.prefixlen)"')
  python3 -c 'import ipaddress,sys; print(ipaddress.ip_interface(sys.argv[1]).network)' "$interface_address"
}

ensure_management_cidr() {
  local want=$1 current
  current=$(kubectl get cluster cluster -o jsonpath='{.spec.managementCIDR}')
  if [ -z "$current" ]; then
    kubectl patch cluster cluster --type merge -p "{\"spec\":{\"managementCIDR\":\"$want\"}}"
    return
  fi
  expect_equal "$want" "$current" "management cidr of the cluster"
}

guard_backend_manifest() {
  cat <<BACKEND
apiVersion: v1
kind: Namespace
metadata:
  name: $guard_namespace
---
apiVersion: v1
kind: Pod
metadata:
  name: backend
  namespace: $guard_namespace
  labels:
    app: backend
spec:
  containers:
    - name: httpd
      image: docker.io/library/busybox:1.37.0
      imagePullPolicy: IfNotPresent
      command: ["sh", "-c", "mkdir -p /www && echo guard-e2e > /www/index.html && exec httpd -f -p 8080 -h /www"]
      readinessProbe:
        tcpSocket:
          port: 8080
        periodSeconds: 3
---
apiVersion: v1
kind: Service
metadata:
  name: backend
  namespace: $guard_namespace
spec:
  type: NodePort
  selector:
    app: backend
  ports:
    - port: 8080
      targetPort: 8080
      nodePort: $guard_node_port
BACKEND
}

guard_probe_script() {
  cat <<SCRIPT
#!/bin/sh
reach() {
  reply=\$(printf 'GET / HTTP/1.0\r\n\r\n' | nc -w 3 $guard_leg_address \$1 2>/dev/null)
  if [ -n "\$reply" ]; then echo ok; else echo fail; fi
}
round=0
while [ \$round -lt 300 ]; do
  round=\$((round + 1))
  ssh_result=\$(reach 22)
  api_result=\$(reach 6443)
  port_result=\$(reach $guard_node_port)
  control=fail
  if ping -c 1 -W 2 192.0.2.250 >/dev/null 2>&1; then control=ok; fi
  echo "GUARD-ROUND \$round p22=\$ssh_result p6443=\$api_result np=\$port_result ctl=\$control" > /dev/console
  sleep 2
done
SCRIPT
}

guard_probe_manifest() {
  cat <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: guard-probe
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
    userData: |
$(guard_probe_script | sed 's/^/      /')
  running: true
VM
}

guard_line() {
  guest_console_log guard-probe | tr -d '\r' | sed -n 's/.*GUARD-ROUND //p' | sed 's/ *$//' | tail -1
}

guard_round() {
  local line
  line=$(guard_line)
  echo "${line%% *}"
}

guard_line_has() {
  local minimum=$1 want=$2 line
  line=$(guard_line)
  [ "${line%% *}" -ge "$minimum" ] && [[ " $line " == *" $want "* ]]
}

legs_table_lists_interfaces() {
  local table
  table=$(nft list table inet bedrock_legs) || return 1
  [[ "$table" == *'"br-public"'* && "$table" == *'"br-services"'* ]]
}

legs_chain_counts_drops() {
  local chain
  chain=$(nft list chain inet bedrock_legs "$1") || return 1
  [[ "$chain" =~ counter\ packets\ [1-9][0-9]*\ bytes ]]
}

legs_table_absent() {
  ! nft list table inet bedrock_legs >/dev/null 2>&1
}

host_node_name() {
  hostname | tr '[:upper:]' '[:lower:]'
}

enable_host_management() {
  local node=$1
  kubectl patch host "$node" --type merge -p '{"spec":{"management":{"enabled":true}}}'
  wait_condition 300 host "$node" ManagementApplied True
}

host_serves_node_port() {
  [ "$(curl -sS --max-time 5 "http://$1:$guard_node_port/")" = guard-e2e ]
}

host_guard_cleanup() {
  local status=$1
  if [ "$status" -ne 0 ]; then
    guard_line >&2 || true
    nft list table inet bedrock_legs >&2 || true
    ip -br addr show "$guard_leg_interface" >&2 || true
    kubectl -n "$guard_namespace" get pods,svc -o wide >&2 || true
  fi
  probe_vm_cleanup guard-probe "$status"
  kubectl patch cluster cluster --type merge -p '{"spec":{"tenantLegs":null}}' || true
  retry_until 120 legs_table_absent || true
  ip addr del "$guard_leg_address/24" dev "$guard_leg_interface" 2>/dev/null || true
  kubectl delete namespace "$guard_namespace" --ignore-not-found
  wait_gone 180 namespace "$guard_namespace"
  internet_gateway_cleanup 0
}

host_guard_checks() {
  local management_address before later
  management_address=$(host_management_address)
  guard_backend_manifest | kubectl apply -f -
  kubectl -n "$guard_namespace" wait --for=condition=Ready pod/backend --timeout=300s
  retry_until 120 ip link show "$guard_leg_interface"
  ip addr add "$guard_leg_address/24" dev "$guard_leg_interface"
  ip link set "$guard_leg_interface" up
  internet_gateway_manifest | kubectl -n "$NS" apply -f -
  wait_reason 120 elasticip igw-ip Ready Attached -n "$NS"
  wait_reason 180 internetgateway igw1 Ready Programmed -n "$NS"
  guard_probe_manifest | kubectl -n "$NS" apply -f -
  wait_jsonpath 600 Running -n "$NS" get virtualmachine guard-probe -o jsonpath='{.status.phase}'
  retry_until 600 guard_line_has 1 "p22=ok"
  ensure_management_cidr "$(host_management_cidr)"
  enable_host_management "$(host_node_name)"
  kubectl patch cluster cluster --type merge -p '{"spec":{"tenantLegs":[{"interface":"br-public"},{"interface":"br-services"}]}}'
  wait_condition 300 cluster cluster TenantLegsGuarded True
  wait_condition 300 cluster cluster NodePortsScoped True
  retry_until 600 legs_table_lists_interfaces
  retry_until 120 host_serves_node_port "$management_address"
  before=$(guard_round)
  retry_until 300 guard_line_has "$((before + 2))" "p22=fail p6443=fail np=fail ctl=ok"
  later=$(guard_round)
  retry_until 300 guard_line_has "$((later + 2))" "p22=fail p6443=fail np=fail ctl=ok"
  retry_until 60 legs_chain_counts_drops leg_input
  retry_until 60 legs_chain_counts_drops prerouting
  host_serves_node_port "$management_address"
}

network_host_guard() {
  (
    trap 'host_guard_cleanup "$?"' EXIT
    host_guard_checks
  )
}
