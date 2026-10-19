# Kubernetes clusters

A `KubernetesCluster` is a Kubernetes cluster that a project owns.
The control plane runs as one pod on the Subnet of the project.
The workers are `VirtualMachine` objects on the same Subnet.
The cluster has one control plane and no high availability.

The `KubernetesCluster` kind needs the addon `clusters`.
The addon runs the k0smotron operator.
It is off by default.
An administrator turns it on with `kubectl patch addon clusters --type merge -p '{"spec":{"enabled":true}}'`.
See `docs/addons.md`.
Without the addon, a `KubernetesCluster` shows `Ready=False` with the reason `AddonDisabled`, and Bedrock creates nothing.

## Example

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: KubernetesCluster
metadata:
  name: prod
spec:
  version: v1.36.3+k0s.1
  controlPlane:
    securityGroups: [k8s]
  workers:
    - name: a
      image:
        name: ubuntu-24.04
      securityGroups: [k8s]
    - name: b
      compute:
        cores: 4
        memoryMiB: 8192
      rootDisk:
        sizeGi: 80
      image:
        name: ubuntu-24.04
      securityGroups: [k8s]
```

The name of the cluster has at most 40 characters.
The name of the cluster, a dash and the name of a worker have at most 52 characters.
The VM of a worker has the name `<cluster>-<worker>`.

## Fields

| Field | Rule |
|---|---|
| `version` | A k0s version such as `v1.36.3+k0s.1`. It is immutable. |
| `network.subnetRef` | The Subnet of the project. The default is `default`. It is immutable. |
| `network.podCIDR` | The pod range inside the child cluster. The default is `100.64.0.0/16`. It is immutable. |
| `network.serviceCIDR` | The service range inside the child cluster. The default is `100.65.0.0/16`. It is immutable. |
| `controlPlane.address` | An optional address on the Subnet. It is immutable. |
| `controlPlane.securityGroups` | At least one SecurityGroup. |
| `workers` | A list that is keyed by `name`. At most 50 entries. |
| `workers[].compute` | 1 to 16 cores, 1024 to 65536 MiB. The default is 2 cores and 4096 MiB. |
| `workers[].rootDisk.sizeGi` | 20 to 500 GiB. The default is 40. |
| `workers[].image` | The image of the VM. The scope is `cluster` or `project`. |
| `workers[].securityGroups` | At least one SecurityGroup. |
| `workers[].address` | An optional address on the Subnet. It is immutable. |
| `workers[].sshKeys` | At most 10 SSH keys. |
| `suspended` | `true` stops every worker. |

## Control plane address

The control plane pod has a fixed address.
The address is the `externalAddress` of the cluster and a name in the certificate of the API server.
A changed address would break every join token, so the address cannot change.

- With `controlPlane.address`, Bedrock uses this address. The address must be inside the Subnet and must not be the gateway, the network address, the broadcast address or an `excludeIPs` entry. Another pod, another cluster, or an older cluster that pins the same address gives `AddressInUse`.
- Without `controlPlane.address`, Bedrock picks the first free address from offset 10 of the Subnet up to the broadcast address. It skips the gateway, the `excludeIPs` ranges, every address that Fabric holds, and the addresses of other clusters. A full Subnet gives `AddressExhausted`.

Bedrock writes the address to `status.controlPlane.address` before it creates anything.
A restart of the operator keeps the address.

## Security groups

The default SecurityGroup of a project denies ingress, so a cluster needs rules.
Open these ports.

| Direction | Port | Between |
|---|---|---|
| Workers to control plane | 6443 TCP | The API server. |
| Workers to control plane | 8132 TCP | Konnectivity. `kubectl logs` and `kubectl exec` in the child need it. |
| Workers to control plane | 9443 TCP | The controller join API. |
| Worker to worker | all | The pod network of kube-router. Allow the SecurityGroup itself. |
| Worker to internet | all | The install of k0s. |

## Internet access

A worker installs k0s with `curl -sSLf https://get.k0s.sh | K0S_VERSION=<version> sh`.
The worker needs internet access for this.
The Network of the Subnet needs an external connection (`enableExternal`).
Without it, the cluster shows `NoExternalPath`.
An offline cluster needs a mirror of the install script. Bedrock does not build it.

## Workers

Add an entry to `workers` to add a worker.
Remove an entry to delete that worker.
Bedrock deletes only the VM of the entry, its root disk, its cloud-init Secret and its token request.
The other workers do not change.

Bedrock makes the workers one at a time.
For each worker, Bedrock makes a k0smotron `JoinTokenRequest` with an expiry of 24 hours, waits for the token, and writes the token into the cloud-init Secret `<cluster>-<worker>-cloudinit`.
Then it makes the VM.
The token is never in the VM spec.
Bedrock deletes the token request when the VM has been running for 15 minutes.
A deleted request invalidates the token, so Bedrock does not delete it earlier.

A worker can edit its size.
A running VM then needs a restart. See `docs/compute.md`.

## Versions

| Setting | Default | Meaning |
|---|---|---|
| `clusters.enabled` | `false` | First state of the `Addon` object `clusters`. |
| `clusters.versions` | `v1.36.3+k0s.1` | The allowed k0s versions. The value `any` allows every version that matches `v<major>.<minor>.<patch>+k0s.<n>`. |

The offline bundle holds the k0s image of the default version only.
Other versions need the registry mirror.
Bedrock has no upgrade of a running cluster.

## Suspend

`spec.suspended: true` stops every worker VM.
The control plane keeps running.
The phase is `Suspended` when every worker is stopped.
Set `suspended` to `false` to start the workers again.

## Kubeconfig

k0smotron makes the Secret `<cluster>-kubeconfig` in the project namespace.
The key is `value`.
The field `status.kubeconfigSecret` names it.
A project viewer cannot read Secrets.
An editor and an owner can.

```
kubectl -n acme-web get secret prod-kubeconfig -o jsonpath='{.data.value}' | base64 -d > prod.kubeconfig
```

The server of the kubeconfig is `https://<control plane address>:6443`.
The address is inside the VPC, and the cluster has no public API address.
To reach the API from the internet, make a bastion VM on the same Subnet and give it an ElasticIP.
Run `kubectl` on the bastion, or forward the port with SSH.

## Delete

A delete goes in this order.
Each step shows the phase `Deleting` and the step in the message of `Ready`.

1. Bedrock deletes the worker VMs and waits until they are gone.
2. Bedrock deletes the token requests and the cloud-init Secrets.
3. Bedrock deletes the k0smotron `Cluster` and waits until the StatefulSet and the volume claim are gone.
4. Bedrock releases the finalizer `bedrock.cloudyfolks.io/kubernetescluster`.

A worker VM that does not stop blocks step 3.
Bedrock cannot see the nodes of the child cluster, because the platform has no route into a tenant VPC.
After you remove a worker, delete its stale Node with `kubectl delete node <cluster>-<worker>` in the child cluster.

## Quota and roles

The quota key `kubernetesClusters` of a project sets the count `count/kubernetesclusters.bedrock.cloudyfolks.io`.
The worker VMs count as VMs in the quota `vms`.
The control plane pod counts its requests (500m CPU, 1Gi memory) as a pod.

| Role | Access |
|---|---|
| viewer | Read a cluster. |
| editor | Create, change and delete a cluster. |
| owner | All editor rights, and delete many clusters at once. |

## Status

| Field | Meaning |
|---|---|
| `status.phase` | `Pending`, `Provisioning`, `Ready`, `Suspended`, `Failed` or `Deleting`. |
| `status.controlPlane.address` | The address of the control plane pod. |
| `status.controlPlane.ready` | `true` when the pod is Ready and the kubeconfig exists. |
| `status.endpoint` | `https://<address>:6443`. |
| `status.kubeconfigSecret` | The Secret with the kubeconfig. |
| `status.workers` | One entry for each worker with the phase of its VM. |

The phase of a worker follows the VM only.
Bedrock does not see if the Node joined the child cluster.

## Reasons

The condition `Ready` has one of these reasons.

| Reason | Cause | Action |
|---|---|---|
| `NotInProject` | The namespace is not a project namespace. | Make the cluster in a project namespace. |
| `InvalidSpec` | The version or a CIDR has a wrong form. | Read the message and fix the field. |
| `AddonDisabled` | The addon `clusters` is off or its kinds are not served. | Ask an administrator to turn the addon on. |
| `SubnetNotFound` | The Subnet does not exist. | Make the Subnet or change `network.subnetRef`. |
| `SubnetNotReady` | The Subnet or its Network is not ready. | Wait, or read the Subnet conditions. |
| `NoExternalPath` | The Network has no external connection. | Set `enableExternal` on the Network. |
| `SecurityGroupNotReady` | A SecurityGroup does not exist or is not ready. | Make the group or wait. |
| `CIDROverlap` | A cluster CIDR overlaps the other CIDR, a Subnet of the Network or a platform range. | Choose other CIDRs. They are immutable, so make a new cluster. |
| `VersionNotAllowed` | The version is not in the Setting `clusters.versions`. | Use an allowed version, or ask an administrator to change the Setting. |
| `AddressOutsideSubnet` | `controlPlane.address` is not inside the Subnet. | Use an address of the Subnet. |
| `AddressReserved` | `controlPlane.address` is the gateway, the network or broadcast address, or an excluded address. | Use another address. |
| `AddressInUse` | Another pod or cluster holds `controlPlane.address`. | Use another address or leave the field empty. |
| `AddressExhausted` | The Subnet has no free address for the control plane. | Free an address or use a bigger Subnet. |
| `ExcludeIPsInvalid` | An `excludeIPs` entry of the Subnet is wrong. | Fix the Subnet. |
| `ApplyFailed` | The API server refused an object of the cluster. | Read the operator log and fix the spec. |
| `NameConflict` | An object with the name of a cluster object exists and the cluster does not own it. | Rename the object or the cluster. |
| `ControlPlaneNotReady` | The control plane pod is not ready, or the kubeconfig does not exist. | Wait. Check the pod `kmc-<cluster>-0` and the security groups. |
| `WorkersNotReady` | A worker has no VM yet or its VM does not run. | Wait. Check the VM. |
| `WorkerFailed` | A worker VM has the phase `Failed`. | Check the VM and recreate the worker. |
| `Suspending` | The workers are still stopping. | Wait. |
| `Suspended` | Every worker is stopped. | Set `suspended` to `false` to start them. |
| `Ready` | The control plane is ready and every worker VM runs. | None. |
| `Deleting` | Bedrock deletes the cluster. The message names the step. | Wait. |
