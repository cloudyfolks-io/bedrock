# Tenancy

## Tenants and projects

A Tenant is a customer of the cluster.
A Tenant owns one virtual network and many projects.
A Project is one workspace of a tenant.
Each project has its own namespace.
A cluster administrator creates Tenants.
A tenant owner creates Projects for the own tenant.

## Create a tenant

Apply a Tenant object.

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Tenant
metadata:
  name: acme
spec:
  displayName: Acme
  owners:
    - kind: User
      name: alice
    - kind: Group
      name: ops
  quota:
    cpu: "8"
    memory: 16Gi
    storage: 200Gi
    elasticIPs: 2
    projects: 2
  network:
    vpcCIDR: 10.100.0.0/16
```

The tenant name uses only lowercase letters and digits.
The name has at most 30 characters.
The name has no hyphen.

Check the result.

```
kubectl get tenant acme
```

The operator creates these objects for the tenant:

- A Network with the name of the tenant.
- A ClusterRole `tenant:acme:owner`.
- A ClusterRoleBinding `tenant:acme:owner`.

The field `status.vpc` shows the name of the virtual network.
The controller follows changes of the Setting `network.provider-networks`. If the Setting cannot be read, the condition `ProviderNetworksKnown` is false with the reason `SettingInvalid`, and the old list stays.
The field `status.providerNetworks` lists the names of the provider networks that the Networks of the tenant can use.
The optional field `spec.network.vrfId` sets the VRF id of the default Network. Only an administrator can set it, and only when the Tenant is created.
The field `status.usage` shows the measured use of the tenant.

## Owners and members

An owner or member refers to a `User` or a `Group`.
The operator adds the prefix `bedrock:` to each name.
The ref `User alice` becomes the Kubernetes user `bedrock:alice`.
The ref `Group ops` becomes the Kubernetes group `bedrock:ops`.
The prefix stops a name such as `system:masters` from binding a built-in group.

A project has three roles.

| Role | Can do |
|---|---|
| viewer | Read project objects (also KubernetesClusters), pods, logs, services and events. Read the logs of containers. Cannot read Secrets. |
| editor | All viewer rights. Create and change project objects (also KubernetesClusters), pods, Deployments and Secrets. Run `exec`. Start, stop, restart and snapshot VMs. Open VM consoles. Upload images. Run `exec` in a Container and restart it. |
| owner | All editor rights. Delete many objects at once, also KubernetesClusters. Write NetworkPolicies. |

No project role can migrate a VM.
Only a cluster administrator can migrate a VM.

The owners of a tenant are owners of every project of the tenant.
The operator creates the RoleBindings only after it applies the project quota.
Without the quota component, the project has no member access.
If the quota object or its CRD disappears later, the operator removes the RoleBindings.
It creates them again when the quota is back.

## Create a project

Create a Project object as a tenant owner.

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Project
metadata:
  name: web
spec:
  tenantRef: acme
  members:
    - ref: {kind: User, name: bob}
      role: editor
    - ref: {kind: User, name: carol}
      role: viewer
  quota:
    cpu: "4"
    memory: 8Gi
    storage: 100Gi
    vms: 5
```

A Project is cluster scoped.
The project name is unique in the whole cluster.
The namespace of the project is `<tenant>-<project>`.
In the example the namespace is `acme-web`.

The operator creates these objects:

- The Namespace.
- A Subnet `default`.
- An ApplicationAwareResourceQuota `project-quota`.
- A RoleBinding for each role that has subjects.
- A NetworkPolicy `default`.

If the tenant limits `cpu`, `memory` or `storage`, the project must set that resource.
A project that leaves it out gets no namespace.
The condition `Admitted` then shows the reason `QuotaRequired`.

The operator never takes a namespace that it did not create.
If the namespace exists, the condition `NamespaceReady` shows the reason `NamespaceConflict`.
Rename the project to fix this.

## Quota

The quota of a project becomes one ApplicationAwareResourceQuota.

| Project field | Quota key |
|---|---|
| cpu | requests.cpu |
| memory | requests.memory |
| storage | requests.storage |
| vms | count/virtualmachines.bedrock.cloudyfolks.io |
| containers | count/containers.bedrock.cloudyfolks.io |
| kubernetesClusters | count/kubernetesclusters.bedrock.cloudyfolks.io |
| registries | count/registries.bedrock.cloudyfolks.io |

A VM counts its CPU and memory under the same keys as a Pod.
A Container counts the requests of its pods under the same keys.
The worker VMs of a KubernetesCluster count as VMs, and the control plane pod counts its requests as a pod.
The pods of a Container run in Kata, so the quota also counts the overhead of the RuntimeClass `kata`, which is 512Mi memory and 250m CPU for each pod.

The quota of a tenant has three kinds of ceiling.

- `projects` is the maximum count of projects.
- `cpu`, `memory` and `storage` limit the sum of the project quotas.
- `elasticIPs` limits the public addresses. The network controller enforces it. An ElasticIP over the limit reports `QuotaExceeded`. A lowered limit does not remove an ElasticIP that already has an address.

The field `status.usage` shows measured use.
For a project the use comes from the quota object.
For a tenant the use is the sum over its projects.

A project in `NamespaceConflict` does not use a project slot.
The operator provisions projects in creation order.
A project over a ceiling gets no namespace.
The operator never removes a project that already exists, when an administrator lowers a ceiling.

The quota system adds a scheduling gate to each Pod of a project.
A Pod with `spec.nodeName` fails in a project namespace.

## Network

Each tenant has one Network with the name of the tenant.
The Network holds the field `vpcCIDR` of the tenant as the only entry of `spec.cidrs`.
A new default Network has `enableExternal: true`, so it is routed.
The operator does not change `enableExternal` of a default Network that already exists.
A Network from an older release has `spec.cidr` with the same range.
The operator keeps `spec.cidr` on that Network and does not change it.
When an administrator moves the Network to `spec.cidrs`, the operator keeps the ranges of the administrator.
The move needs a merge patch that sets `cidrs` and `cidr: null`, as docs/network.md shows.
The prefix of `vpcCIDR` is at most 24.
Only IPv4 is possible.

Each project gets a Subnet `default`.
The Subnet is the first free /24 block of the Network.
A Subnet of another size counts as used for each /24 block that it touches.
The field `status.subnet` of the project shows the block.
When no block is free, the condition `SubnetReady` shows the reason `CIDRExhausted`.

## Default network policy

The NetworkPolicy `default` selects all Pods except the KubeVirt launcher Pods.
It allows ingress from all namespaces of the same tenant.
It does not limit egress.
Traffic from outside the tenant stays blocked.
A project owner adds more NetworkPolicy objects to open more traffic.
VM interfaces use security groups, not this policy.

## Pod restrictions

A project user with the prefix `bedrock:` cannot create a Pod or Deployment with these fields:

- `hostNetwork`, `hostPID` or `hostIPC`.
- A `hostPath` volume.
- A privileged container or init container.
- Added capabilities other than `NET_BIND_SERVICE`.
- A `procMount` other than `Default`.
- Ephemeral containers with added capabilities or privileges.
- A `hostPort`.
- `spec.nodeName`.

The rule applies only in namespaces with the label `bedrock.cloudyfolks.io/tenant`.
It covers Pods and the pod templates of Deployments, ReplicaSets, StatefulSets, DaemonSets, Jobs and CronJobs.
A project user also cannot set network selection annotations on a Pod or in a pod template.
Only the platform can.
See `docs/network.md`.
Only the operator can set or change the labels `bedrock.cloudyfolks.io/tenant` and `bedrock.cloudyfolks.io/project` on a namespace.

## Pod network admission

The setting `tenancy.pod-network-admission` controls the admission of the network shape of a tenant pod.
It is `off` by default.

| Value | Effect |
|---|---|
| `off` | Bedrock removes the bindings. The policies do nothing. |
| `audit` | A pod that breaks a rule is admitted. The API server counts the failure and writes it in its audit log. |
| `deny` | A pod that breaks a rule is rejected. |

The policies are `bedrock-tenant-pod-network`, `bedrock-tenant-pod-network-subnet` and `bedrock-tenant-pod-network-groups`.
The operator makes and removes their bindings from the setting.
A change of the setting acts at once.
Use `audit` first.
Move to `deny` when the alert `TenantPodNetworkAudit` is quiet.

The policies work only in a namespace that has the label `bedrock.cloudyfolks.io/tenant`.
They check a Pod when it is created, and the subresource `pods/ephemeralcontainers` when it is updated.
They do not check other updates of a Pod, so a label change of a running Pod passes.
The shape rules apply at creation only.

A Pod must have these fields, which `docs/network.md` explains:

- The label `network.cloudyfolks.com/class` with the value `workload` or `managed`.
- The annotation `k8s.v1.cni.cncf.io/networks` with the value `kube-system/vpc`.
- The annotation `fabric.cloudyfolks.io/logical_switch` with the value `tenant-workload` for `workload` and `tenant-managed` for `managed`.
- The annotations `fabric.cloudyfolks.io/port_security` and `vpc.kube-system.fabric.cloudyfolks.io/port_security` with the value `true`.
- The annotation `vpc.kube-system.fabric.cloudyfolks.io/logical_switch` with the name of a Fabric Subnet of the project of the Pod.
- The annotation `vpc.kube-system.fabric.cloudyfolks.io/security_groups` with only Fabric security groups of the project, or empty.

A Pod of the class `workload` must also have `automountServiceAccountToken: false`.
It must not add the capability `NET_BIND_SERVICE`.
The policy `bedrock-tenant-pods` already denies every other added capability, so the two policies never deny the same capability.

Fabric names are hashed, so Bedrock does not check a name prefix.
The two parameter policies read the Fabric `Subnet` and `SecurityGroup` objects.
An object belongs to the project when its annotation `bedrock.cloudyfolks.io/owner-namespace` is the namespace of the Pod.
The default security group `default-<namespace>` belongs to the project through its labels.
The policies check only the objects that exist.
A name that no object has fails later, when the Pod starts.

These identities are exempt: the groups `system:masters` and `system:nodes`, the service accounts of `kubevirt`, `cdi`, `aaq`, `rook-ceph` and `velero`, the other service accounts of `kube-system`, `system:kube-scheduler` and `system:kube-controller-manager`.
The workload controllers of `kube-system` (ReplicaSet, StatefulSet, DaemonSet and Job) and the service accounts of `bedrock-system` are not exempt.
They create the Pods of the pod builders, and the policy must check those Pods.

The alerts `TenantPodNetworkAudit`, `TenantPodNetworkDenied` and `TenantPodNetworkError` use the metric `apiserver_validating_admission_policy_check_total`.
Turn the setting on only after `network.tenant-management.enabled` is `true` and a pod builder makes the new shape.
Before that, every tenant Pod breaks the rules.

## Deletion

Delete a Project to remove its namespace and all objects in it.
Delete a Tenant to remove its Projects, its Network and its owner role.
The Tenant has a finalizer.
The operator first deletes the Projects and waits until they and their namespaces are gone.
Then it deletes the Networks and waits until they are gone.
Then it deletes the owner role and releases the finalizer.
While it waits, the Tenant condition `Ready` is False with the reason `WaitingForProjects`, `WaitingForNamespaces` or `WaitingForNetwork`.
The message gives the count of objects that remain.

## Conditions

The Tenant has these conditions.

| Condition | Reason | Action |
|---|---|---|
| NetworkReady | WaitingForNetwork | Wait for the network controller. |
| NetworkReady | ApplyFailed | Read the message and fix the cause. |
| OwnersBound | ApplyFailed | Read the message and fix the cause. |
| WithinQuota | ProjectsOverQuota | Raise `quota.projects` or delete projects. |
| WithinQuota | AllocationOverQuota | Raise the ceiling or lower a project quota. |
| Ready | Ready | None. |

The Project has these conditions.

| Condition | Reason | Action |
|---|---|---|
| Admitted | TenantNotFound | Create the tenant. |
| Admitted | ProjectQuotaExceeded | Raise `quota.projects` of the tenant. |
| Admitted | TenantQuotaExceeded | Lower a project quota or raise the tenant ceiling. |
| Admitted | QuotaRequired | Set the resource that the tenant limits in the project quota. |
| NamespaceReady | NamespaceConflict | Rename the project. |
| NamespaceReady | NamespaceTerminating | Wait until the old namespace is gone. |
| NamespaceReady | ApplyFailed | Read the message and fix the cause. |
| SubnetReady | WaitingForSubnet | Wait for the network controller. |
| SubnetReady | CIDRExhausted | Use a larger `vpcCIDR` for new tenants. |
| QuotaReady | QuotaUnavailable | Check that the quota component runs. |
| AccessReady | QuotaUnavailable | Wait until the project quota is enforced. |
| QuotaReady | TenantQuotaExceeded | Lower the project quota or raise the tenant ceiling. |
| AccessReady | ApplyFailed | Read the message and fix the cause. |
| Ready | Ready | None. |

The conditions `Progressing` and `Degraded` show a wait or a fault.

## Limits

- An owner cannot list all projects. Use the panel to list them.
- An owner creates a project with `kubectl create`. The command `kubectl apply` reads the object first, and the owner role reads only existing projects.
- Project names are unique in the cluster.
- A network of a tenant is named like the tenant or starts with the tenant name and a hyphen.
- The quota component must run in the cluster.
- Only IPv4 is possible.
- The prefix of `vpcCIDR` is at most 24.
