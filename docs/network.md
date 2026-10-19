# Network

This document describes the network area of Bedrock.
The network area turns Bedrock network objects into Fabric objects.
Fabric is the network layer of the cluster.
Tenants never touch Fabric objects.

## What the network area does

A project member creates a `Subnet`, an `ElasticIP`, a `LoadBalancer`, an `InternetGateway`, a `DNSZone` or a `SecurityGroup`.
The operator creates the Fabric objects that make them work.
The operator also creates the Fabric underlay from Settings.
The underlay is the `ProviderNetwork`, the `Vlan`, the external `Subnet` and the `BgpConf`.

Every Bedrock object of this area has a `Ready` condition.
A `Network` in external mode also has an `ExternalReady` condition.
A provisioned Network or Subnet that breaks a range Setting also has a `RangePolicy` condition.
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
| `Network` with the services plane | `OvnEip` | `<vpc>-provider-<pool>` for the router port, and `<vpc>-services-snat` for the identity address |
| `Network` with the services plane | `OvnSnatRule` | `<vpc>-services-snat-0` to `<vpc>-services-snat-2` |
| `NetworkPeering` | `Vpc` | `vpcPeerings` and `staticRoutes` of both Networks |
| `Network` | `Subnet` for the load balancer plane | `<network>-lb` |
| `Subnet` | `Subnet` | a short name made from namespace and name |
| `Subnet` | `NetworkAttachmentDefinition` | the Subnet name, in the project namespace |
| `ElasticIP` | `OvnEip` | a short name made from namespace and name |
| `ElasticIP` with a VM target | `OvnFip` | the same short name |
| `LoadBalancer` listener | `RouterLBRule` | one rule for each listener |
| `InternetGateway` | `OvnSnatRule` | `<network>-igw-<hash>`, one rule for each range of the Network, or one rule for the Subnet |
| `DNSZone` | `DNSZone` | one for each Network of the Subnets that name the zone |
| `SecurityGroup` | `SecurityGroup` | a short name made from namespace and name |
| `LoadBalancer` without an ElasticIP | `Vip` and `SwitchLBRule` | one `Vip` in the load balancer plane, and one rule for each listener |
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
   - `default.t1-prod.fabric.cloudyfolks.io/ip_address: "<address>"`.
     Add it only when the entry has a pinned address.
     Fabric writes the address that it allocated into the same key.
   - `default.t1-prod.fabric.cloudyfolks.io/default_route: "false"`.
     Add it for every entry except the first.
5. Add the label `sg.bedrock.cloudyfolks.io/<group>: "true"` for each security group of the VM.
6. When the security groups of a running VM change, patch the same annotations and labels on the running pod.
   Fabric applies the change at once.
   Never patch `ip_address` on a pod that exists. A pinned address applies only when the pod starts.
7. Check a pinned address before the VM starts.
   The address must be inside the Subnet CIDR.
   It must not be the gateway, the network address, the broadcast address or an excluded address.
   No other VM and no other Fabric IP of the Subnet may hold it.
   The VM does not start when a check fails.
   The condition `NetworkReady` names the reason: `AddressOutsideSubnet`, `AddressReserved` or `AddressInUse`.
   The operator checks `AddressInUse` again after 30 seconds.
   An address counts as held by the VM only when Fabric has written `allocated: "true"` for the NIC.
   While a pinned address is not allocated, the operator checks the VM again every 30 seconds.
8. After the pod runs, Fabric writes `ip_address`, `mac_address`, `cidr`, `gateway` and `allocated` on the pod.
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
The operator writes the DHCP options `mtu` and `dns_server`.
Fabric sets the `router` option to the gateway of the Subnet.
The `mtu` option and the `mtu` of the Fabric Subnet are `spec.mtu` of the Subnet.
When the Subnet has no `spec.mtu`, they are the Setting `network.mtu`.
When both are empty, the operator writes neither, and Fabric chooses the MTU.
The `dns_server` option is `spec.dnsServers` of the Subnet.
When the Subnet has no `spec.dnsServers`, it is the Setting `network.dns-servers`.
When no DNS server is set, the operator does not write the `dns_server` option.
When the operator writes no option at all, the Fabric Subnet has no `dhcpV4Options`.
Fabric joins the DNS servers with `;`.
`spec.mtu` is from 576 to 9000, and `spec.dnsServers` has at most 4 IPv4 addresses.

A change of `network.mtu` or `network.dns-servers` updates every Subnet that does not set the field.
A new MTU or DNS server list reaches a running guest at its next DHCP lease, which is 3600 seconds long.
The pod interface of a VM keeps its MTU until the VM pod is created again, which is a VM restart.
The operator does not restart a VM.
Raise the MTU only with a planned VM restart.

When no MTU is set, Fabric gives the guest the DHCP MTU 1400, which is 1500 minus 100.
It gives the pod interface the MTU of the node NIC minus 100.
Geneve adds 100 bytes to every packet between nodes.
Keep `network.mtu` and `spec.mtu` at or below the MTU of the node NICs minus 100.
A larger value drops or fragments the packets between nodes.
A cluster whose node NICs have an MTU of 1500 must leave `network.mtu` empty, or set 1400 or less.
Set `network.mtu` to 1500 only when the node NICs have an MTU of 1600 or more.

The pod MTU is a separate value.
It is `spec.network.podMTU` of the cluster configuration, from 576 to 9000.
When it is empty, Fabric detects the pod MTU from the node NIC and subtracts 100.
When it is set, the installer adds `--mtu=<value>` to the `cni-server` container of the Fabric agent.
This is the `networking.pods.mtu` key of the Fabric chart.
The release render does not change, so an empty value gives the same manifests as before.
The value is part of the `Cluster` object (`spec.podMTU`).
The operator applies a changed value at the next release install or upgrade.
A running pod keeps its MTU until the pod is created again.
Set `podMTU` to 1500 on a cluster whose guests use `network.mtu` 1500, such as ir-central-1, and use node NICs with an MTU of 1600 or more.

To set the pod MTU on a cluster that runs already, patch the `Cluster` object before the next upgrade:

```
kubectl patch cluster cluster --type merge -p '{"spec":{"podMTU":1500}}'
```

The operator renders the value into the Fabric agent when the `components` phase of the next upgrade runs.
To apply it without an upgrade, run `bedrock release apply --dir <release dir> --pod-mtu 1500`.
The Fabric agent restarts on each node.
Create the pods again, and restart the VMs, so that they take the new MTU.
The flag accepts 576 to 9000 only.

Only the platform may add, change or remove network selection annotations on a pod.
The annotations are all keys with the prefix `k8s.v1.cni.cncf.io/`, `v1.multus-cni.io/` or `ovn.kubernetes.io/`, and all keys with the text `cloudyfolks.io/`.
The admission policy `bedrock-network-attachments` refuses every other identity in a project namespace.
The allowed identities are the service accounts `kubevirt-controller` (namespace `kubevirt`), `ovn`, `fabric-cni`, `multus`, `replicaset-controller`, `statefulset-controller` and `job-controller` (namespace `kube-system`) and `bedrock-operator` (namespace `bedrock-system`).
The controllers of Kubernetes create the pods of a workload.
They copy the annotations from the pod template of the workload.

The admission policy `bedrock-network-attachments-workloads` closes this path.
It refuses the same annotations in the pod template of a Deployment, ReplicaSet, StatefulSet, DaemonSet, Job or CronJob in a project namespace.
For a CronJob it reads `spec.jobTemplate.spec.template`.
Only `bedrock-operator` and the Kubernetes controllers `deployment-controller` and `cronjob-controller` can create such a template with the annotations.
The two controllers copy a template that `bedrock-operator` made.
A user cannot attach a pod to a network, directly or through a workload.
Only the platform can.
A workload of a user without these annotations works as before.

## How a container attaches

The container area builds the pod template of a Container.
The function `PodAttachment` returns its annotations and labels.
A Container has one interface, on one Subnet `S` of the namespace `N`.
The pod follows rules 4 to 7 of the section above for this one entry, with these changes.

1. Add the annotation `v1.multus-cni.io/default-network: "N/S"`.
   This is the override of the default network that KubeVirt sets for a VM.
2. Never add `default_route`.
3. A Container with a pinned address has one replica and the strategy `Recreate`.
   The address check ignores the pods of the Container itself.
4. A change of the security groups of a Container changes the pod template, so the pods roll.

The Container kind is in `docs/containers.md`.

## Networks and external access

A `Network` becomes a Fabric `Vpc`.
Set `enableExternal` on the Network to give it an external connection.
The default Network of a new tenant has `enableExternal: true`.
The operator does not change the value of a Network that already exists.
The external connection is a router port on each provider network.
It also adds a default route to the first provider network.

A Network holds one or more IPv4 ranges in `spec.cidrs`.
A Network can have at most 8 ranges.
Each range must be a network address, and the ranges must not overlap each other.
An older Network has `spec.cidr` with one range instead.
The operator reads `cidr` as a list with one entry, and it does not change the object.
Set exactly one of `cidr` and `cidrs`.
To move an older Network to `cidrs`, replace `cidr` with a `cidrs` list that holds the old range, in one update.
Use a merge patch:

```
kubectl patch network <name> --type merge -p '{"spec":{"cidr":null,"cidrs":["<old cidr>","<new range>"]}}'
```

`kubectl apply` cannot do this move.
The tenancy controller owns the field `cidr` of the default Network of a tenant, so an apply from another manager cannot remove it.
Both fields stay set, and the API server refuses the object.
You can add a range to `cidrs`.
You cannot remove a range, and you cannot go back to `cidr`.

Two Settings limit the ranges.
`network.reserved-cidrs` holds the ranges that the platform keeps for its own planes.
No range of a Network and no Subnet can overlap them.
A Network with a range that overlaps a reserved range gets `Ready=False` with the reason `CIDRReserved`, and the operator does not make its Fabric Vpc.
`network.subnet-allowed-ranges` holds the ranges where Subnets can be.
The Subnet reconciler refuses a Subnet outside them with the reason `CIDROutsideAllowedRanges`.
The admission policy does not read Settings, so only the reconciler makes these two checks.

These checks never stop an object that is already provisioned.
A Subnet whose Fabric Subnet exists stays `Ready`, and a Network whose Fabric Vpc exists stays `Ready`.
The object gets the extra condition `RangePolicy=False` with the same reason and message as the refusal.
The condition goes when the ranges and the Settings agree again.
A new Subnet or Network is refused as described above.

Before you upgrade, list the ranges that exist and compare them with the two Settings:

```
kubectl get subnets -A -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,CIDR:.spec.cidr
kubectl get networks -o custom-columns=NAME:.metadata.name,CIDR:.spec.cidr,CIDRS:.spec.cidrs
```

After the upgrade, find the objects that the Settings do not allow with `kubectl get subnets,networks -A -o yaml` and look for the condition `RangePolicy`.
To remove a mismatch, widen a Setting.
For example, set `network.subnet-allowed-ranges` to `0.0.0.0/0`, or set `network.reserved-cidrs` to a range that no Network uses, such as `192.0.2.0/24`.

A Subnet admission policy checks each new Subnet.
The CIDR of a Subnet must be inside one of the ranges of its Network.
The CIDR must not overlap another Subnet of the same Network.
The Subnet must name a Network of its own tenant.
The Subnet reconciler makes the same checks, and it also checks the reserved ranges and the allowed ranges.
When two Subnets overlap, the older Subnet wins.

A Network does not give a VM access to the internet.
A VM without an ElasticIP has no route to the internet, unless the Network has an InternetGateway.

## Peering and static routes

A `NetworkPeering` joins two Networks.
It is cluster scoped, and only an administrator can create, change or delete it.
`spec.networkRefs` names the two Networks, and it cannot change.
A Network cannot peer with itself, and two peerings cannot join the same two Networks.
If a second peering names the same pair, the older one stays and the other one gets the reason `PeeringExists`.

Each peering has a connect network, which is a /30.
Set `spec.connectCIDR` to choose it.
Without `spec.connectCIDR`, the operator takes the lowest free /30 from the Setting `network.peering.range`.
The operator writes the connect network to `status.connectCIDR`, and it keeps it.
No two peerings share a connect network.
If an older peering holds the connect network that you chose, the new peering gets the reason `ConnectCIDRInUse`.
The same reason shows when the range has no free /30.
The first address of the connect network belongs to the first Network in `networkRefs`.
The second address belongs to the second Network.

The Network controller is the only writer of a Fabric `Vpc`.
For each Network, it reads every peering that names the Network.
It writes `vpcPeerings` with the connect address of its own side.
It also writes one static route for each range of the other Network.
The route goes to the connect address of the other side.
When you delete a peering, the next reconcile removes the peering and its routes from both Vpcs.
A peering shows `Ready` when both Vpcs list each other.
The condition has the reason `Pending` until Fabric programs both sides.

`spec.staticRoutes` of a Network adds more routes to its Vpc.
Each route has a `cidr` and a `nextHop`.
The `cidr` must be an IPv4 network address, and it cannot be the default route.
Only an administrator can set or change `spec.staticRoutes`.
A route of `spec.staticRoutes` wins over a route that a peering writes for the same `cidr`.

## Services plane

The services plane is a shared provider network that every external Network attaches to.
It gives guests a way to reach the services of the region, for example a resolver or an image cache.
Bedrock does not run these services.
Add them as add-ons.

Three Settings turn it on:

- `network.services.provider-network` names a provider network with the role `services`.
- `network.services.identity-range` is an IPv4 network inside the CIDR of that provider network.
  The usable addresses of the range must be inside one `excludeIPs` entry of the provider network, so Fabric does not give them to router ports.
  If not, the Network gets the reason `ServicesNotConfigured`.
- `network.services.resolver` is the address of the resolver.
  DHCP gives it to guests when `network.dns-servers` is empty.

The first /24 of the services provider network is for the addresses of the services.
Put the resolver there.

For each Network with `enableExternal`, the Network controller does these steps:

1. It adds the provider network to `extraExternalSubnets` of the Vpc, after the other entries.
   An existing Vpc keeps every entry that it has.
2. It writes the router port as an `OvnEip` of type `lrp`, named `<vpc>-provider-<pool>`.
3. It takes one address from `network.services.identity-range` and writes it to `status.servicesIdentity`.
   The lowest free address is taken.
   The controller reads the other Networks and the `OvnEip` objects of the provider network from the API before it writes.
   So two Networks never get the same address, also after a restart.
4. It writes the `OvnEip` `<vpc>-services-snat` with this address.
5. It writes three `OvnSnatRule` objects, one for each of `10.0.0.0/8`, `172.16.0.0/12` and `192.168.0.0/16`.

A Network keeps its identity address until you delete the Network.
The controller deletes the `OvnEip` `<vpc>-services-snat` and the rules only when the Network is no longer external, or when you delete the Network.
An empty or wrong Setting never deletes them.
In that case a Network with an identity gets `ExternalReady=False` with the reason `ServicesNotConfigured`.
The address stays in `status.servicesIdentity`.
The router port and the entry in `extraExternalSubnets` stay.
When you delete a Network, the controller deletes these objects before it deletes the Vpc.

Each provider network with the role `services` has an ACL with the direction `to-lport` and the priority 2000.
It drops traffic when the source and the destination are both in the provider network and both outside the first /24.
So guests of different Networks cannot reach each other on the plane, but they can reach the services.
In bgp mode a provider network with the role `services` has its own `ProviderNetwork` and `Vlan`, as the transit network has.
The ACL, the `ProviderNetwork` and the `Vlan` depend on the role only, so a wrong Setting never removes them.

If a Setting is wrong, the Network gets `ExternalReady=False` with the reason `ServicesNotConfigured`.
The Vpc keeps its other entries, and the controller does not delete the identity objects.
If the identity range has no free address, the Network gets `Ready=False` with the reason `NoPoolCapacity`.

## Elastic IPs

An `ElasticIP` reserves an address from a provider network.
The address stays reserved until you delete the ElasticIP.
`spec.providerNetwork` is optional.
Without it, the operator takes the `tenant` provider network with the lowest used share, which is `v4usingIPs` divided by the sum of `v4usingIPs` and `v4availableIPs` of its Fabric `Subnet`.
A tie goes to the lower name.
A full provider network is not a candidate.
The choice is written to `status.providerNetwork`, and the ElasticIP keeps that network until you delete it.
When every `tenant` provider network is full, or none exists, the ElasticIP reports `NoPoolCapacity` and the operator creates no Fabric object.
The tenant field `quota.elasticIPs` limits the ElasticIPs of all projects of the tenant.
An ElasticIP over the limit reports `QuotaExceeded`, takes no provider network and has no Fabric object.
The older ElasticIPs go first.
An ElasticIP that holds an address or a provider network keeps it when the quota is lowered.
Without `quota.elasticIPs`, the number is not limited.
Set `spec.target` to attach the address to a VM, to a LoadBalancer or to an InternetGateway.
Remove `spec.target` to detach it.
Detach keeps the address.

The target of a VM names the VM.
Set `subnetRef` to choose a VM interface.
Without `subnetRef` the first interface is used.
The field `elasticIPRef` of a VM interface is a back reference.
The operator never writes it.
When it names another ElasticIP, the ElasticIP reports `TargetMismatch`.

### Provider network roles

Each provider network has a `role` in the cluster configuration: `tenant` (the default), `platform`, `transit` or `services`.
The operator writes the role to the label `usability` of the Fabric `Subnet` `provider-<name>`: `public` for `tenant`, `private` for `platform`, `transit` for `transit` and `services` for `services`.
A provider network without a role is a `tenant` network.
Only a member of `bedrock:bedrock-admins` can create an ElasticIP that names a provider network whose `usability` is not `public`.
The policy `bedrock-elasticip-pool` reads the label `usability` of the Fabric `Subnet` objects.

### L2 mode

Set `network.eip-mode` to `l2`.
OVN answers ARP for the address on the provider network.
Every Network with `enableExternal` takes one address on each provider network of the role `tenant` for its router port.
Provider networks of the roles `platform`, `services` and `transit` are not attached to a Network.
The default route of a new Network goes to the first `tenant` provider network by name.
A Network that has no `tenant` provider network gets `ExternalReady=False` with the reason `NoProviderNetwork`.
A Vpc that already has external subnets keeps them and keeps its default route.
A new `tenant` provider network is added to it, and a role change does not detach a subnet from it.
With many tenants this uses many public addresses.
Use bgp mode to avoid this cost.
The `fabric-frr` DaemonSet runs on the gateway nodes in L2 mode too, but the operator does not check it, and it does not change `ExternalReady` or the ElasticIP status.
In L2 mode the agent has no work and stays idle.

### BGP mode

Set `network.eip-mode` to `bgp`.
Set the Settings `network.bgp.local-asn`, `network.bgp.peers` and `network.bgp.transit-network`.
The transit network carries the router ports.
The other provider networks hold the elastic IPs.
The FRR agent advertises the addresses.
Every Network with `enableExternal` gets `enableBfd` and `dynamicRouting.localOnly` on its Fabric `Vpc`.
Fabric advertises the NAT and load balancer addresses only from the chassis that holds the port.
A Network can pin its VRF id with `spec.vrfId`, a number from 1 to 65535 that is not 253, 254 or 255.
Only a member of the group `bedrock:bedrock-admins` can set it.
You can set it only when you create the Network.
Nobody can change or remove it after that.
Use it to keep the VRF id of a VPC that moves from another platform.
Without `vrfId`, the operator picks the lowest free id from 1000 and records it in the annotation `network.bedrock.cloudyfolks.io/vrf-id`.
The operator never picks an id that a Network pins.
A Network gets `Ready=False` with the reason `VRFInUse` when another Network or another Fabric `Vpc` holds the id, and the operator does not make its Fabric Vpc.
When two Networks pin the same id, the older Network keeps it.
A Network gets `Ready=False` with the reason `VRFReserved` when the id is in `network.bgp.redistribute-tables`.
A provisioned Network that breaks this rule after a Setting change stays `Ready`, keeps its id, and shows the same text in the condition `VRFPolicy`.
The agent is the DaemonSet `kube-system/fabric-frr`.
The release installs it.
It runs only on the nodes that have the label `fabric.cloudyfolks.io/external-gw=true`.
Bedrock puts this label on each Host that has the role `fabric-gateway`, and removes it when the role is removed.
The ElasticIP and the `ExternalReady` condition of the Network report `FRRAgentMissing` until one agent pod is ready on a gateway node.

The Settings `network.bgp.bfd`, `network.bgp.hold-time`, `network.bgp.keepalive-time`, `network.bgp.graceful-restart`, `network.bgp.advertise-filter` and `network.bgp.redistribute-tables` tune the BGP sessions.
Bedrock writes them to the `BgpConf` named `bedrock`.
Bedrock updates the `BgpConf` when one of them changes.
The advertise filter limits the routes that the agent sends to the peers.
The load balancer plane is the range of `network.load-balancer.cidr`.
Every Network uses the same range, so the agent must never advertise it.
Bedrock removes the plane from the filter before it writes the `BgpConf`.
The `BgpConf` can therefore show more entries than the Setting holds.
When the filter is empty, Bedrock writes the single entry `0.0.0.0/32`, which matches no route.
Fabric adds the NAT addresses of the ElasticIPs to the filter.
An empty filter therefore advertises only the ElasticIP routes, and with no ElasticIP it advertises nothing.
An entry that overlaps the plane is split into the parts outside the plane, and an entry inside the plane is dropped.
When nothing is left, the filter is the same single entry `0.0.0.0/32`.

Do not change `network.eip-mode` on a cluster that has ElasticIPs.
The mode is an install-time choice.

## Load balancers

A `LoadBalancer` becomes one `RouterLBRule` for each listener.
The ElasticIP of the LoadBalancer is the frontend.
The ElasticIP must name the LoadBalancer as its target.
The LoadBalancer must name the ElasticIP in `elasticIPRef`.
All targets of a listener must use the same target port.
All targets of a LoadBalancer must be on one Network.
A target names a VM with `vmRef` or a Container with `containerRef`. Set exactly one.
For a Container the operator uses the addresses of its ready pods as endpoints.
It reads the Fabric `IP` object of each pod. A pod without a Fabric `IP` object has no endpoint yet.
The endpoints follow the pods when a rolling update changes them.
When a Container has no ready pod, the operator keeps the rule with no endpoint and sets `Ready=True` with the reason `NoReadyPods`.
One listener can hold VMs and Containers.
When `healthCheck` is set, Fabric health checks are on.
When it is not set, they are off.
The other health check fields are not used.

A LoadBalancer without `elasticIPRef` is an internal balancer.
It becomes one Fabric `SwitchLBRule` for each listener and one Fabric `Vip`.
The `Vip` is a plain Fabric `Vip` in the load balancer plane of the Network, and Fabric allocates it.
Bedrock never writes the `Vip` type `switch_lb_vip`.
All targets of an internal LoadBalancer must use one Subnet.

The field `status.address` shows the address of the LoadBalancer.
For an internal LoadBalancer, it is the address of the `Vip` in the load balancer plane.
For a LoadBalancer with an ElasticIP, it is the address of the ElasticIP.
The field is empty while the address does not exist, and when the `Vip` or the ElasticIP goes.

### Load balancer plane

Every Network has one hidden Fabric `Subnet` named `<network>-lb`.
It has no Bedrock `Subnet` object, so tenants do not see it.
The range comes from `network.load-balancer.cidr` and `network.load-balancer.gateway`.
The Subnet is distributed, has no outgoing NAT and no DHCP, and has the MTU 1500.
The gateway is excluded, so no `Vip` takes it.
Every Network uses the same range, because the planes are separate in each Vpc.
Keep the range inside `network.reserved-cidrs`, so no Subnet can overlap it.
BGP never advertises the range.
Set the range before the first Network exists.
Fabric does not change the range of a Subnet, so a new value stops the reconcile of the Networks that exist.
A LoadBalancer waits with `FabricNotReady` until the plane Subnet of its Network exists.
The plane Subnet goes away with its Network.

An older release put the `Vip` in the Subnet of the targets.
That `Vip` used the MAC address of the router port, and it cut off the default gateway of a tenant.
The plane replaces it.

## Internet gateways

An `InternetGateway` gives the VMs of a Network a shared source address for traffic to the internet.
A VM that has no ElasticIP uses this address.
A VM that has an ElasticIP keeps using its own address.
The InternetGateway names a Network in `networkRef` and an ElasticIP in `elasticIPRef`.
It can also name a Subnet in `subnetRef`.
The ElasticIP must name the InternetGateway as its target with `kind: InternetGateway`.
The InternetGateway must name the ElasticIP in `elasticIPRef`.
An ElasticIP has one target, so an address that serves an InternetGateway cannot serve a VM or a LoadBalancer.
When the ElasticIP has another target, the InternetGateway reports `TargetMismatch` and has no rules.
The ElasticIP counts toward `quota.elasticIPs` like any other ElasticIP.
The Network must be external, and its Vpc must be connected to the provider network of the ElasticIP.

The operator writes one `OvnSnatRule` for each IPv4 range of the Network.
The rule names the Fabric `OvnEip` of the ElasticIP, the Vpc of the Network and the range in `v4IpCidr`.
When you add a range to the Network, the operator writes a rule for it.
When a range leaves the Network, the operator deletes its rule.
The rule name is `<network>-igw-` and 12 hex characters of a hash of the Network and the range.
The name does not depend on the order of the ranges.
With `subnetRef`, the operator writes one rule that names the Fabric `Subnet`, and no rule for the ranges.
The Subnet must be in the same project and on the same Network.
Use `subnetRef` when only one Subnet needs the shared address.

A Network has at most one InternetGateway.
When two InternetGateways name the same Network, the older one works.
The other one reports `GatewayExists` and writes no rule.
When the older one is deleted, the other one takes over.
The InternetGateway reports `Programmed` when Fabric has programmed every rule.
When the ElasticIP is deleted or changes its target, the operator deletes the rules.
When you delete the InternetGateway, the operator deletes the rules and then releases the object.
The sweeper deletes rules whose InternetGateway is gone.

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

A group that a VM or a Container uses cannot be deleted.
Remove the group from the VM or the Container first.

## Settings

| Key | Default | Meaning |
|---|---|---|
| `network.eip-mode` | `l2` | How elastic IPs reach the physical network: `l2` or `bgp`. |
| `network.provider-networks` | empty | JSON list of provider networks with name, interface, vlan, cidr, gateway, excludeIPs and role. |
| `network.bgp.local-asn` | empty | Local AS number of the FRR agent. |
| `network.bgp.router-id` | empty | BGP router id. |
| `network.bgp.peers` | empty | Comma list of `<ipv4>@<asn>`. |
| `network.bgp.transit-network` | empty | Provider network that carries the router ports in bgp mode. |
| `network.bgp.bfd` | `true` | Enable BFD on the BGP peers of the FRR agent. The Fabric `Vpc` of a Network in bgp mode always has BFD, and this Setting does not change it. |
| `network.bgp.hold-time` | `9s` | BGP hold time in whole seconds, from `3s` to `300s`. A peer refuses a hold time of `1s` or `2s`. |
| `network.bgp.keepalive-time` | `3s` | BGP keepalive time in whole seconds, from `1s` to `300s`. |
| `network.bgp.graceful-restart` | `true` | Keep the BGP routes while a peer restarts. |
| `network.bgp.advertise-filter` | empty | Comma list of `<cidr> ge <n> le <n>` entries. The mask length must not be more than `ge`, and `ge` must not be more than `le`, which is at most 32. The agent advertises only the matching routes and the ElasticIP routes. Empty advertises only the ElasticIP routes. |
| `network.bgp.redistribute-tables` | empty | Comma list of host routing table ids from 1 to 999. 253, 254 and 255 are reserved. Ids from 1000 up are the VRF ids of the VPCs, and the agent refuses a host table that equals one of them. The list must not hold a VRF id that a Network pins in `spec.vrfId`. |
| `network.dns-servers` | empty | Comma list of IPv4 resolvers that DHCP gives to guests. A Subnet that sets `dnsServers` uses its own list. Empty uses `network.services.resolver`. |
| `network.load-balancer.cidr` | `100.69.0.0/24` | IPv4 network of the load balancer plane. Every Network gets one hidden Fabric Subnet with this range, and internal load balancers take their virtual addresses there. Keep it inside `network.reserved-cidrs`, and set it before the first Network exists. BGP never advertises it. |
| `network.load-balancer.gateway` | `100.69.0.1` | IPv4 address of the plane gateway. It must be inside `network.load-balancer.cidr` and must not be its network or broadcast address. |
| `network.peering.range` | `169.254.0.0/16` | IPv4 network from which each NetworkPeering without `connectCIDR` gets a /30 connect network. The prefix must be 30 or shorter. |
| `network.services.provider-network` | empty | Name of the provider network with the role `services`. Every external Network attaches to it. Empty turns the services plane off. |
| `network.services.identity-range` | empty | IPv4 network inside the services provider network. Each Network gets one address from it for source NAT. The prefix must be 30 or shorter. |
| `network.services.resolver` | empty | IPv4 address of the resolver on the services plane. DHCP gives it to guests when `network.dns-servers` is empty. |
| `network.reserved-cidrs` | `100.64.0.0/10` | Comma list of IPv4 networks. No Network range and no Subnet can overlap them. They belong to the platform planes. |
| `network.subnet-allowed-ranges` | `10.0.0.0/8,172.16.0.0/12,192.168.0.0/16` | Comma list of IPv4 networks. Every Subnet must be inside one of them. Use `0.0.0.0/0` to allow every address. |
| `network.mtu` | empty | MTU in bytes, from 576 to 9000, of every Subnet that sets no `mtu`. Empty lets Fabric choose. A running guest learns a new value at its next DHCP lease, and the pod interface of a VM keeps its MTU until the VM restarts. |

`bedrock init` writes `network.eip-mode` and `network.provider-networks` from the cluster configuration.
A provider network can have an `excludeIPs` list.
Fabric does not give out these addresses.

## How to read conditions

Read the reason of the `Ready` condition.
Then take the action in the table.

| Reason | Meaning and action |
|---|---|
| `AddressInUse` | An older ElasticIP holds this address, or, on a VM, another VM or another Fabric IP in the Subnet holds the pinned address. Choose another address. |
| `AddressOutsideProviderNetwork` | The address is not in the provider network. Choose an address in its CIDR. |
| `AddressOutsideSubnet` | The pinned address of a VM network is not in the Subnet CIDR. Choose an address in the CIDR. |
| `AddressReserved` | The address is the gateway, the network address, the broadcast address or an excluded address of the provider network or of the Subnet. Choose another one. |
| `Allocating` | Fabric has not given an address yet. Wait. |
| `Applied` | The Subnet is programmed. No action. |
| `Attached` | The ElasticIP is attached to its target. No action. |
| `BGPNotConfigured` | Bgp mode misses a Setting. Set the keys that the message names. |
| `CIDRReserved` | The Subnet CIDR overlaps a provider network, the join subnet, a management range or a range of `network.reserved-cidrs`. A Network gets this reason when one of its ranges overlaps a range of `network.reserved-cidrs`. Use another CIDR. A provisioned Subnet or Network shows the same text in the condition `RangePolicy`, and it stays `Ready`. |
| `CIDROutsideNetwork` | The Subnet CIDR is not inside any range of the Network. Use another CIDR, or add a range to `cidrs` of the Network. |
| `CIDROutsideAllowedRanges` | The Subnet CIDR is not inside a range of `network.subnet-allowed-ranges`. Use another CIDR, or ask an administrator to change the Setting. A provisioned Subnet shows the same text in the condition `RangePolicy`, and it stays `Ready`. |
| `CIDROverlap` | An older Subnet overlaps this one. Use another CIDR. |
| `ConnectCIDRInUse` | An older NetworkPeering holds the connect network, or `network.peering.range` has no free /30. Set another `connectCIDR`, or widen the range. |
| `Connected` | The external connection works. No action. |
| `ElasticIPNotFound` | The ElasticIP of the LoadBalancer or of the InternetGateway does not exist, or it is being deleted. Create it. |
| `ElasticIPNotReady` | The ElasticIP is not ready. Wait or fix the ElasticIP. |
| `ExcludeIPsInvalid` | An excluded address or range is wrong. Use addresses inside the CIDR and ascending ranges. |
| `ExternalNotConnected` | The Vpc has no working external port. Check the Network and the gateway nodes. |
| `FRRAgentMissing` | No fabric-frr pod is ready on a gateway node. Give a Host the role `fabric-gateway` and check the DaemonSet `kube-system/fabric-frr`. |
| `FabricConflict` | A Fabric object with this name belongs to another owner. Rename the Bedrock object. |
| `FabricNotReady` | Fabric has not finished. Wait. If it stays, read the message. |
| `GatewayExists` | An older InternetGateway already serves this Network. Delete one of the two. |
| `GatewayInvalid` | The gateway is outside the CIDR or is the network or broadcast address. Use a usable address. |
| `InUse` | VMs, Containers or addresses still use the object. Delete or change them first. |
| `InvalidRecords` | A DNS record has a bad name or address. Fix the record. |
| `InvalidRules` | A security group rule is wrong. Use one of cidr and securityGroupRef, valid ports and a known protocol. |
| `NamespaceNotLabeled` | The namespace is not a project namespace. Create the object in a project namespace. |
| `NetworkNotOwned` | The Network belongs to another tenant. Use a Network of your tenant. |
| `NetworkNotExternal` | The Network has no external connection. Set `enableExternal` on the Network. |
| `NetworkNotFound` | The Network does not exist. Fix `networkRef`. |
| `NetworkNotReady` | The Network is not ready. Wait. |
| `NoPoolCapacity` | The ElasticIP names no provider network, and every `tenant` provider network is full or none exists. Add addresses or a `tenant` provider network. For a Network, `network.services.identity-range` has no free address. Widen the range. The operator checks again every few seconds. |
| `NoProviderNetwork` | The Network wants an external connection and no provider network exists. In L2 mode a provider network of the role `tenant` must exist. Set `network.provider-networks`. |
| `NoReadyPods` | A Container target has no ready pod. The rule stays with no endpoint for it. Wait for the pods, or look at the Container. |
| `NoTargets` | No target has an address yet. Wait for the VMs. |
| `NotAttached` | No Subnet names this zone in `dnsZoneRef`. Set `dnsZoneRef` on a Subnet. |
| `PeeringExists` | An older NetworkPeering already joins these two Networks. Delete this one. |
| `Pending` | Fabric has not connected the Network or the peering yet. Wait. |
| `Programmed` | Fabric has the object. No action. |
| `RefNotFound` | A rule names a security group that does not exist, or an InternetGateway names a Subnet that does not exist. Create it or fix the reference. |
| `QuotaExceeded` | The tenant already has as many ElasticIPs as `quota.elasticIPs` allows. Delete an ElasticIP or raise the quota of the tenant. The operator checks again when the quota or the count changes. |
| `Reserved` | The ElasticIP holds an address and has no target. No action. |
| `ServicesNotConfigured` | The services plane misses a Setting or a Setting is wrong. Set the keys that the message names. The Vpc keeps its other entries. |
| `TargetMismatch` | The target does not point back to this object. Fix the reference. |
| `TargetInUse` | An older ElasticIP already uses this VM interface. Delete one of the two ElasticIPs. |
| `TargetNetworkMismatch` | The targets are on more than one Network, or the Subnet of an InternetGateway is on another Network. Use targets of one Network. |
| `TargetNotFound` | The target does not exist. Fix the target. |
| `TargetPortsDiffer` | A listener has targets with different ports. Use one port. |
| `Terminating` | The object waits for its children. Delete the children first. |
| `UnknownProviderNetwork` | The provider network is not in `network.provider-networks`. Use a known name. |
| `Unsupported` | A Fabric kind is missing, a rule needs IPv6, or the request cannot be done. Read the message. |
| `VpcPending` | Fabric has not finished the Vpc. Wait. |
| `VpcReady` | The Vpc works. No action. |
| `VRFInUse` | Another Network or a Fabric Vpc holds the VRF id that `spec.vrfId` pins. Pin another id, or free the id. |
| `VRFReserved` | The VRF id that `spec.vrfId` pins is a host table in `network.bgp.redistribute-tables`. Pin another id, or remove the table from the Setting. A provisioned Network shows the same text in the condition `VRFPolicy`, and it stays `Ready`. |
| `WaitingForDefaultSecurityGroup` | Fabric has not programmed the default security group of the namespace yet. Wait. |
| `WaitingForAddress` | A VM has no address yet. Wait for the VM to start. |

## Limits

- Only IPv4 works.
- A Network range is checked only against `network.reserved-cidrs`. A wide range can hold a provider network or the join subnet, and the Subnet check refuses the Subnets that overlap them.
- A Subnet CIDR must not overlap a provider network, the join subnet, a range of `network.reserved-cidrs`, the API address or a node address. The operator does not check the pod and service ranges, because tenant VPCs are isolated. The management range is known only as single addresses.
- A VM without an ElasticIP has no route to the internet, unless the Network has an InternetGateway.
- A Network has one InternetGateway.
- A health check is on or off. The other health check fields are not used.
- L2 mode has one default route, to the first `tenant` provider network by name.
- You cannot change the elastic IP mode on a cluster that has ElasticIPs.
- An internal LoadBalancer has no health checks.

## Upgrade notes

Internal load balancers move to the load balancer plane.
After the upgrade, every internal LoadBalancer gets a new virtual address from `<network>-lb`, so the address of the LoadBalancer changes.
Bedrock deletes the old `Vip` and writes a new one with a new name.
Update every client, DNS record and firewall rule that holds the old address.
List the addresses before the upgrade with `kubectl get vips -l network.bedrock.cloudyfolks.io/managed=true`.
A cluster that runs no internal LoadBalancer has no change.

The Network controller is now the only writer of the `vpcPeerings`, `staticRoutes` and `extraExternalSubnets` of a Fabric `Vpc` that a Network owns.
It writes each list as a whole from the Network, its `NetworkPeering` objects and its `spec.staticRoutes`.
A peering or a route that someone added to the Vpc by hand is removed at the next reconcile.
Before the upgrade, list them with `kubectl get vpcs -o yaml`.
Move every one of them to a `NetworkPeering` or to `spec.staticRoutes` of the Network.
An entry of `extraExternalSubnets` that the Vpc already has stays.

The services resolver becomes the default DNS server of guests.
When `network.services.resolver` is set, a Subnet with no `dnsServers` and an empty `network.dns-servers` gives the resolver to its guests through DHCP.
A running guest takes it at its next DHCP lease.
Before the upgrade, set `network.dns-servers` to the resolvers that the guests use now, so the default does not change.

An empty `network.bgp.advertise-filter` now advertises only the ElasticIP routes.
Before, a BGP cluster with no ElasticIP and an empty filter advertised all routes.
After this release, such a cluster stops advertising its tenant routes.
Set `network.bgp.advertise-filter` to the prefixes that you want to advertise, to keep them.

## Inspect Fabric

List the Fabric objects of Bedrock:

```
kubectl get vpcs,subnets,ovn-eips,ovn-fips,ovn-snat-rules -l network.bedrock.cloudyfolks.io/managed=true
```

Read the NAT rules of a Vpc:

```
kubectl -n kube-system exec deployment/ovn-central -- ovn-nbctl lr-nat-list <vpc>
```
