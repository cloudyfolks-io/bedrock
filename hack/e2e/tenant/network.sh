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
  kubectl -n "$NS" get pods -l "kubevirt.io/domain=$1" -o json |
    jq -r --arg key "parity.$NS.fabric.cloudyfolks.io/$2" '.items[0].metadata.annotations[$key]'
}

guest_console_log() {
  kubectl -n "$NS" logs -l "kubevirt.io/domain=$1" -c guest-console-log --tail=-1
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

network_guest_parity() {
  parity_subnet_manifest | kubectl -n "$NS" apply -f -
  wait_reason 120 subnet parity Ready Applied -n "$NS"
  expect_equal "1350" "$(fabric_by_label subnets Subnet parity -o jsonpath='{.items[0].spec.mtu}')" "fabric subnet mtu"
  expect_contains "$(fabric_by_label subnets Subnet parity -o jsonpath='{.items[0].spec.dhcpV4Options}')" "mtu=1350" "dhcp mtu option"
  expect_contains "$(fabric_by_label subnets Subnet parity -o jsonpath='{.items[0].spec.dhcpV4Options}')" "dns_server=198.51.100.53;198.51.100.54" "dhcp dns option"
  parity_vm_manifest vm-bad 10.100.16.1 | kubectl -n "$NS" apply -f -
  wait_reason 120 virtualmachine vm-bad NetworkReady AddressReserved -n "$NS"
  expect_equal "" "$(kubectl -n "$NS" get pods -l kubevirt.io/domain=vm-bad -o name)" "no pod for a reserved address"
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
  kubectl -n "$NS" delete virtualmachine vm2
  wait_gone 180 virtualmachine vm2 -n "$NS"
  wait_empty 180 -n "$NS" get pods -l kubevirt.io/domain=vm2
  kubectl -n "$NS" delete subnet parity
  wait_gone 180 subnet parity -n "$NS"
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
  local namespace count=0
  for namespace in $(kubectl get namespaces -l "bedrock.cloudyfolks.io/tenant=$TENANT" -o jsonpath='{.items[*].metadata.name}'); do
    count=$((count + $(kubectl -n "$namespace" get elasticips -o name | wc -l)))
  done
  echo "$count"
}

set_elastic_ip_quota() {
  kubectl patch tenant "$TENANT" --type merge -p "{\"spec\":{\"quota\":{\"elasticIPs\":${1:-null}}}}"
}

network_elastic_ip_quota() {
  local previous count
  previous=$(kubectl get tenant "$TENANT" -o jsonpath='{.spec.quota.elasticIPs}')
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
  kubectl -n "$NS" delete elasticip quota-second ip-auto
  set_elastic_ip_quota "$previous"
  wait_empty 180 get ovn-eips.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=quota-second
  wait_empty 180 get ovn-eips.fabric.cloudyfolks.io -l bedrock.cloudyfolks.io/name=ip-auto
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
