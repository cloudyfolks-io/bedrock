fabric_by_label() {
  local resource=$1 kind=$2 name=$3
  kubectl get "$resource.fabric.cloudyfolks.io" -l "bedrock.cloudyfolks.io/kind=$kind,bedrock.cloudyfolks.io/name=$name,bedrock.cloudyfolks.io/project=$PROJECT" "${@:4}"
}

network_underlay() {
  underlay_is_ready
  wait_jsonpath 180 true get provider-networks.fabric.cloudyfolks.io public -o jsonpath='{.status.ready}'
  expect_equal 0 "$(kubectl get vlans.fabric.cloudyfolks.io public -o jsonpath='{.spec.id}')" "vlan id"
  expect_equal "192.0.2.0/24 public" "$(kubectl get subnets.fabric.cloudyfolks.io provider-public -o jsonpath='{.spec.cidrBlock} {.spec.vlan}')" "provider subnet"
  if [ "$(kubectl get network "$TENANT" -o jsonpath='{.spec.enableExternal}')" != true ]; then
    kubectl patch network "$TENANT" --type merge -p '{"spec":{"enableExternal":true}}'
  fi
  wait_reason 180 network "$TENANT" Ready VpcReady
  wait_reason 180 network "$TENANT" ExternalReady Connected
  expect_equal "$TENANT" "$(kubectl get network "$TENANT" -o jsonpath='{.status.vpc}')" "network vpc"
  expect_contains "$(kubectl get network "$TENANT" -o jsonpath='{range .status.subnets[*]}{.namespace}/{.name}{"\n"}{end}')" "$NS/default" "network subnets"
  expect_equal 'true true ["provider-public"]' "$(kubectl get vpcs.fabric.cloudyfolks.io "$TENANT" -o jsonpath='{.status.standby} {.spec.enableExternal} {.spec.extraExternalSubnets}')" "vpc external"
  wait_reason 120 subnet default Ready Applied -n "$NS"
  expect_equal '{"cniVersion":"0.3.0","type":"fabric","server_socket":"/run/openvswitch/fabric-daemon.sock","provider":"default.'"$NS"'.fabric"}' "$(kubectl -n "$NS" get net-attach-def default -o jsonpath='{.spec.config}')" "network attachment"
  expect_equal "default.$NS.fabric $TENANT" "$(fabric_by_label subnets Subnet default -o jsonpath='{.items[0].spec.provider} {.items[0].spec.vpc}')" "fabric subnet"
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
