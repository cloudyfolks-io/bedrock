# Network

This document describes the network area of Bedrock.
The network area turns Bedrock network objects into Fabric objects.
Fabric is the network layer of the cluster.
Tenants never touch Fabric objects.

## What the network area does

A project member creates a `Subnet`, an `ElasticIP`, a `LoadBalancer`, a `DNSZone` or a `SecurityGroup`.
The operator creates the Fabric objects that make them work.
The operator also creates the Fabric underlay from Settings.
The underlay is the `ProviderNetwork`, the `Vlan`, the external `Subnet` and the `BgpConf`.

Every Bedrock object of this area has a `Ready` condition.
A `Network` in external mode also has an `ExternalReady` condition.
Read the reason of the condition to find the problem.
The section "How to read conditions" lists every reason.

## Kinds and Fabric objects

| Bedrock object | Fabric or Kubernetes object | Name |
|---|---|---|
| each provider network | `ProviderNetwork` | the provider network name |
| each provider network | `Vlan` | the provider network name |
| each provider network | `Subnet` | `provider-<name>` |
| bgp mode | `BgpConf` | `bedrock` |
| `Network` | `Vpc` | the Network name |
| `Subnet` | `Subnet` | a short name made from namespace and name |
| `Subnet` | `NetworkAttachmentDefinition` | the Subnet name, in the project namespace |
| `ElasticIP` | `OvnEip` | a short name made from namespace and name |
| `ElasticIP` with a VM target | `OvnFip` | the same short name |
| `LoadBalancer` listener | `RouterLBRule` | one rule for each listener |
| `DNSZone` | `DNSZone` | one for each Network of the Subnets that name the zone |
| `SecurityGroup` | `SecurityGroup` | a short name made from namespace and name |
| `LoadBalancer` without an ElasticIP | `Vip` and `SwitchLBRule` | one `Vip`, and one rule for each listener |
| `SecurityGroup` | `NetworkPolicy` | `bedrock-sg-<name>` |
| project namespace | `SecurityGroup` | `default-<namespace>` |

Fabric objects have no namespace.
The short name has at most 63 characters.
It ends with 12 hex characters of a hash, so two projects can use the same name.
Every Fabric object has the labels `bedrock.cloudyfolks.io/kind` and `bedrock.cloudyfolks.io/name`.
It also has the label `network.bedrock.cloudyfolks.io/managed` and three owner annotations.
A sweeper deletes Fabric objects whose owner is gone.
The sweeper runs every 10 minutes and ignores objects that are younger than one minute.

## How a VM attaches

The compute area builds the VM pod.
It uses the strings in this section.
The function `PodNetworkMetadata` returns the same strings.

For a VM in namespace `N` with network entry `i`, the Subnet is `S` and the provider is `S.N.fabric`.
Follow these rules.

1. Do not use the `pod` network in the VM.
2. Add one `multus` network for each entry.
   The `networkName` is `N/S`.
   Set `multus.default: true` on entry 0.
   Use the `bridge` binding.
3. Do not start the VM before the Subnet has `Ready=True`.
4. Add these annotations to the pod template for each entry.
   The example uses the provider `default.t1-prod.fabric`.
   - `default.t1-prod.fabric.cloudyfolks.io/port_security: "true"`
   - `default.t1-prod.fabric.cloudyfolks.io/security_groups: "<fabric names, sorted, comma separated>"`.
     When the entry names no security group, the value is `default-<namespace>`.
   - `default.t1-prod.fabric.cloudyfolks.io/mac_address: "<mac>"`.
     Add it only when the entry has a MAC address.
   - `default.t1-prod.fabric.cloudyfolks.io/default_route: "false"`.
     Add it for every entry except the first.
5. Add the label `sg.bedrock.cloudyfolks.io/<group>: "true"` for each security group of the VM.
6. When the security groups of a running VM change, patch the same annotations and labels on the running pod.
   Fabric applies the change at once.
7. After the pod runs, Fabric writes `ip_address`, `mac_address`, `cidr`, `gateway` and `allocated` on the pod.
   The key prefix is the provider.

Example fragment of the pod template of a VM:

```yaml
metadata:
  annotations:
    default.t1-prod.fabric.cloudyfolks.io/port_security: "true"
    default.t1-prod.fabric.cloudyfolks.io/security_groups: "t1-prod-web-0123456789ab"
  labels:
    sg.bedrock.cloudyfolks.io/web: "true"
```

A VM with no security group uses the group `default-<namespace>`.
`PodNetworkMetadata` puts the group into the `security_groups` annotation of every NIC that names no group.
The pod gets no `sg.bedrock.cloudyfolks.io` label for this group, because it has no Bedrock object.
The network area creates this group for every project namespace.
It allows all egress and denies all ingress.

DHCP in the guest comes from the Fabric Subnet.
The router option is the gateway.
The DNS servers come from the Setting `network.dns-servers`.

Only the platform may add, change or remove network selection annotations on a pod.
The annotations are all keys with the prefix `k8s.v1.cni.cncf.io/`, `v1.multus-cni.io/` or `ovn.kubernetes.io/`, and all keys with the text `cloudyfolks.io/`.
The admission policy `bedrock-network-attachments` refuses every other identity in a project namespace.
The allowed identities are the service accounts `kubevirt-controller` (namespace `kubevirt`), `ovn`, `fabric-cni` and `multus` (namespace `kube-system`) and `bedrock-operator` (namespace `bedrock-system`).
A tenant user, a Deployment and a Job cannot attach a pod to a network.

## Networks and external access

A `Network` becomes a Fabric `Vpc`.
Set `enableExternal` on the Network to give it an external connection.
The external connection is a router port on each provider network.
It also adds a default route to the first provider network.

A Subnet admission policy checks each new Subnet.
The CIDR of a Subnet must be inside the CIDR of its Network.
The CIDR must not overlap another Subnet of the same Network.
The Subnet must name a Network of its own tenant.
The Subnet reconciler makes the same checks.
When two Subnets overlap, the older Subnet wins.

A Network does not give a VM access to the internet.
A VM without an ElasticIP has no route to the internet.

## Elastic IPs

An `ElasticIP` reserves an address from a provider network.
The address stays reserved until you delete the ElasticIP.
Set `spec.target` to attach the address to a VM or to a LoadBalancer.
Remove `spec.target` to detach it.
Detach keeps the address.

The target of a VM names the VM.
Set `subnetRef` to choose a VM interface.
Without `subnetRef` the first interface is used.
The field `elasticIPRef` of a VM interface is a back reference.
The operator never writes it.
When it names another ElasticIP, the ElasticIP reports `TargetMismatch`.

### L2 mode

Set `network.eip-mode` to `l2`.
OVN answers ARP for the address on the provider network.
Every Network with `enableExternal` takes one address on each provider network for its router port.
With many tenants this uses many public addresses.
Use bgp mode to avoid this cost.

### BGP mode

Set `network.eip-mode` to `bgp`.
Set the Settings `network.bgp.local-asn`, `network.bgp.peers` and `network.bgp.transit-network`.
The transit network carries the router ports.
The other provider networks hold the elastic IPs.
The FRR agent advertises the addresses.
Every Network with `enableExternal` gets `enableBfd` and `dynamicRouting.localOnly` on its Fabric `Vpc`.
Fabric advertises the NAT and load balancer addresses only from the chassis that holds the port.
The agent is the DaemonSet `kube-system/fabric-frr`.
The ElasticIP reports `FRRAgentMissing` until the agent exists.
Enable `frr` in the values of the Fabric chart to install it.

The Settings `network.bgp.bfd`, `network.bgp.hold-time`, `network.bgp.keepalive-time`, `network.bgp.graceful-restart`, `network.bgp.advertise-filter` and `network.bgp.redistribute-tables` tune the BGP sessions.
Bedrock writes them to the `BgpConf` named `bedrock`.
Bedrock updates the `BgpConf` when one of them changes.
The advertise filter limits the routes that the agent sends to the peers.
When the filter is empty, the agent sends all routes.

Do not change `network.eip-mode` on a cluster that has ElasticIPs.
The mode is an install-time choice.

## Load balancers

A `LoadBalancer` becomes one `RouterLBRule` for each listener.
The ElasticIP of the LoadBalancer is the frontend.
The ElasticIP must name the LoadBalancer as its target.
The LoadBalancer must name the ElasticIP in `elasticIPRef`.
All targets of a listener must use the same target port.
All targets of a LoadBalancer must be on one Network.
When `healthCheck` is set, Fabric health checks are on.
When it is not set, they are off.
The other health check fields are not used.

A LoadBalancer without `elasticIPRef` is an internal balancer.
It becomes one Fabric `SwitchLBRule` for each listener and one Fabric `Vip`.
The `Vip` is an address of the Subnet of the targets, and Fabric allocates it.
All targets of an internal LoadBalancer must use one Subnet.

## DNS zones

A Fabric `DNSZone` applies to a whole VPC.
A Bedrock `DNSZone` becomes one Fabric zone for each Network of the Subnets that name it in `dnsZoneRef`.
All Subnets of that Network see the names.
Use a single label, `@` or a full name in the zone as a record name.
Names are exact.
Wildcards are not supported.
An IPv4 address gives an A record.
An IPv6 address gives an AAAA record.

## Security groups

A `SecurityGroup` becomes a Fabric group.
All rules allow traffic.
A group with no egress rule allows all egress.
A group with at least one egress rule allows only those rules.
Traffic between two members of one group is not allowed unless a rule names the group.

The operator also creates a `NetworkPolicy` for each group.
The policy mirrors the rules.
It selects pods with the label `sg.bedrock.cloudyfolks.io/<group>`.
It names only the attachments of the namespace.
It therefore never reaches the pod network `eth0`.
Live migration and probes use `eth0`.
The policy never allows more than the Fabric group.

A group that a VM uses cannot be deleted.
Remove the group from the VM first.

## Settings

| Key | Default | Meaning |
|---|---|---|
| `network.eip-mode` | `l2` | How elastic IPs reach the physical network: `l2` or `bgp`. |
| `network.provider-networks` | empty | JSON list of provider networks with name, interface, vlan, cidr, gateway and excludeIPs. |
| `network.bgp.local-asn` | empty | Local AS number of the FRR agent. |
| `network.bgp.router-id` | empty | BGP router id. |
| `network.bgp.peers` | empty | Comma list of `<ipv4>@<asn>`. |
| `network.bgp.transit-network` | empty | Provider network that carries the router ports in bgp mode. |
| `network.bgp.bfd` | `true` | Enable BFD on every BGP peer. |
| `network.bgp.hold-time` | `9s` | BGP hold time in whole seconds, from `3s` to `300s`. A peer refuses a hold time of `1s` or `2s`. |
| `network.bgp.keepalive-time` | `3s` | BGP keepalive time in whole seconds, from `1s` to `300s`. |
| `network.bgp.graceful-restart` | `true` | Keep the BGP routes while a peer restarts. |
| `network.bgp.advertise-filter` | empty | Comma list of `<cidr> ge <n> le <n>` entries. The mask length must not be more than `ge`, and `ge` must not be more than `le`, which is at most 32. The agent advertises only the matching routes. |
| `network.bgp.redistribute-tables` | empty | Comma list of host routing table ids from 1 to 999. 253, 254 and 255 are reserved. Ids from 1000 up are the VRF ids of the VPCs, and the agent refuses a host table that equals one of them. |
| `network.dns-servers` | empty | Comma list of IPv4 resolvers that DHCP gives to guests. |

`bedrock init` writes `network.eip-mode` and `network.provider-networks` from the cluster configuration.
A provider network can have an `excludeIPs` list.
Fabric does not give out these addresses.

## How to read conditions

Read the reason of the `Ready` condition.
Then take the action in the table.

| Reason | Meaning and action |
|---|---|
| `AddressInUse` | An older ElasticIP holds this address. Choose another address. |
| `AddressOutsideProviderNetwork` | The address is not in the provider network. Choose an address in its CIDR. |
| `AddressReserved` | The address is the gateway, the network address, the broadcast address or an excluded address. Choose another one. |
| `Allocating` | Fabric has not given an address yet. Wait. |
| `Applied` | The Subnet is programmed. No action. |
| `Attached` | The ElasticIP is attached to its target. No action. |
| `BGPNotConfigured` | Bgp mode misses a Setting. Set the keys that the message names. |
| `CIDRReserved` | The Subnet CIDR overlaps a provider network, the join subnet or a management range. Use another CIDR. |
| `CIDROutsideNetwork` | The Subnet CIDR is not inside the Network CIDR. Use another CIDR. |
| `CIDROverlap` | An older Subnet overlaps this one. Use another CIDR. |
| `Connected` | The external connection works. No action. |
| `ElasticIPNotFound` | The ElasticIP of the LoadBalancer does not exist. Create it. |
| `ElasticIPNotReady` | The ElasticIP is not ready. Wait or fix the ElasticIP. |
| `ExcludeIPsInvalid` | An excluded address or range is wrong. Use addresses inside the CIDR and ascending ranges. |
| `ExternalNotConnected` | The Vpc has no working external port. Check the Network and the gateway nodes. |
| `FRRAgentMissing` | The fabric-frr agent is missing. Enable `frr` in the Fabric chart values. |
| `FabricConflict` | A Fabric object with this name belongs to another owner. Rename the Bedrock object. |
| `FabricNotReady` | Fabric has not finished. Wait. If it stays, read the message. |
| `GatewayInvalid` | The gateway is outside the CIDR or is the network or broadcast address. Use a usable address. |
| `InUse` | VMs or addresses still use the object. Delete or change them first. |
| `InvalidRecords` | A DNS record has a bad name or address. Fix the record. |
| `InvalidRules` | A security group rule is wrong. Use one of cidr and securityGroupRef, valid ports and a known protocol. |
| `NamespaceNotLabeled` | The namespace is not a project namespace. Create the object in a project namespace. |
| `NetworkNotOwned` | The Network belongs to another tenant. Use a Network of your tenant. |
| `NetworkNotExternal` | The Network has no external connection. Set `enableExternal` on the Network. |
| `NetworkNotFound` | The Network does not exist. Fix `networkRef`. |
| `NetworkNotReady` | The Network is not ready. Wait. |
| `NoProviderNetwork` | The Network wants an external connection and no provider network exists. Set `network.provider-networks`. |
| `NoTargets` | No target has an address yet. Wait for the VMs. |
| `NotAttached` | No Subnet names this zone in `dnsZoneRef`. Set `dnsZoneRef` on a Subnet. |
| `Pending` | Fabric has not connected the Network yet. Wait. |
| `Programmed` | Fabric has the object. No action. |
| `RefNotFound` | A rule names a security group that does not exist. Create it or fix the rule. |
| `Reserved` | The ElasticIP holds an address and has no target. No action. |
| `TargetMismatch` | The target does not point back to this object. Fix the reference. |
| `TargetInUse` | An older ElasticIP already uses this VM interface. Delete one of the two ElasticIPs. |
| `TargetNetworkMismatch` | The targets are on more than one Network. Use targets of one Network. |
| `TargetNotFound` | The target does not exist. Fix the target. |
| `TargetPortsDiffer` | A listener has targets with different ports. Use one port. |
| `Terminating` | The object waits for its children. Delete the children first. |
| `UnknownProviderNetwork` | The provider network is not in `network.provider-networks`. Use a known name. |
| `Unsupported` | A Fabric kind is missing, a rule needs IPv6, or the request cannot be done. Read the message. |
| `VpcPending` | Fabric has not finished the Vpc. Wait. |
| `VpcReady` | The Vpc works. No action. |
| `WaitingForDefaultSecurityGroup` | Fabric has not programmed the default security group of the namespace yet. Wait. |
| `WaitingForAddress` | A VM has no address yet. Wait for the VM to start. |

## Limits

- Only IPv4 works.
- A Subnet CIDR must not overlap a provider network, the join subnet, the API address or a node address. The operator does not check the pod and service ranges, because tenant VPCs are isolated. The management range is known only as single addresses.
- A VM without an ElasticIP has no route to the internet.
- A health check is on or off. The other health check fields are not used.
- L2 mode has one default route, to the first provider network.
- You cannot change the elastic IP mode on a cluster that has ElasticIPs.
- An internal LoadBalancer has no health checks.

## Inspect Fabric

List the Fabric objects of Bedrock:

```
kubectl get vpcs,subnets,ovn-eips,ovn-fips -l network.bedrock.cloudyfolks.io/managed=true
```

Read the NAT rules of a Vpc:

```
kubectl -n kube-system exec deployment/ovn-central -- ovn-nbctl lr-nat-list <vpc>
```
