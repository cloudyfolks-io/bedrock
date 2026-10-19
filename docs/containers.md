# Containers

## What the container area does

The container area runs customer containers for projects.
A user creates a Container in a project namespace.
The operator turns the Container into a Deployment and, with `autoscale`, a HorizontalPodAutoscaler.
The operator writes the result to the `status` field of the Container.
The proxy gives the logs, the exec stream and the restart.

The project namespace must have the labels `bedrock.cloudyfolks.io/tenant` and `bedrock.cloudyfolks.io/project`.
Without these labels the operator creates nothing.
The condition `Ready` shows the reason `NotInProject`.

## Kinds

### Container

| Field | Meaning |
|---|---|
| `image` | The image reference. Required. |
| `command`, `args` | Replace the entry point and the arguments of the image. |
| `env` | A list of variables. Each variable has a `value` or a `valueFrom.secretKeyRef`. Set exactly one. |
| `ports` | At most 8 ports with a `port`, an optional `name` and a `protocol` (`TCP`, `UDP` or `SCTP`). The default is `TCP`. |
| `resources` | `requests` and `limits` for `cpu` and `memory`. The defaults are 100m, 500m, 128Mi and 512Mi. A request cannot be above its limit. A limit cannot be above 32 CPUs or 256Gi. |
| `replicas` | From 0 to 20. Without `replicas` and without `autoscale` the container has one replica. |
| `strategy` | `Recreate` or `RollingUpdate`. |
| `fsGroup` | The group of the mounted volumes. |
| `probes` | `liveness` and `readiness`. Only `exec` probes exist. |
| `autoscale` | `min`, `max`, `cpuPercent` and `memoryPercent`. |
| `volumes` | At most 8 Volumes with a `volumeRef` and a `mountPath`. |
| `imagePullSecrets` | Names of Secrets of the type `kubernetes.io/dockerconfigjson` in the namespace. |
| `network` | `subnetRef` (the default is `default`), an optional `address` and `securityGroups`. |

The name of a Container has at most 52 characters and is a DNS label.
The `subnetRef` and the `address` cannot change after the create.
A reference must not hold `/`, `,`, `{`, a space or an upper-case letter.

These rules are checked by the API server.

- `replicas` and `autoscale` exclude each other.
- An `address` and `volumes` need `replicas` of at most 1 and no `autoscale`.
- An `address` and `volumes` need the strategy `Recreate`.
- A probe needs an `exec` command.

The operator checks these rules after the API server.
It reports the reason `InvalidSpec`.

- The image has no space.
- A port name is used once.
- A resource value is above zero.
- A liveness probe has `successThreshold` 1.
- A mount path is a clean absolute path and not inside `/proc`, `/sys` or `/dev`.

## Objects the operator creates

Each object has the same name as the Container.
Each object carries the labels `kind`, `name`, `tenant` and `project` of the group `bedrock.cloudyfolks.io`.
The Container owns each object. Delete the Container to delete them all.
The operator applies the objects with server-side apply under the field manager `bedrock-container`.
If an object of the same name exists and the Container does not own it, the reason is `NameConflict` and the operator does not touch the object.

| Object | When |
|---|---|
| Deployment | Always. The pod has one container named `app`. The selector is `kind=Container` and `name=<name>`. |
| HorizontalPodAutoscaler | When the Container has `autoscale`. |

Kata isolates the Containers of a project, and later the Builds.
A Pod or a Deployment that a project editor makes directly runs with the runtime class that the editor sets.
The platform adds no policy that forces a runtime class on them.

The operator never sets `hostNetwork`, `hostPID`, `hostIPC`, a `hostPath` volume, a privileged container, `nodeName`, a `hostPort`, added capabilities or `procMount`.
The pod does not mount a service account token and has no service links.

## Networks

A pod has one interface. It is on the Subnet of the Container.
The pod uses the Multus default network override, as a VM does.
The pod has no cluster network, no ClusterIP and no cluster DNS.
When the Subnet or the Setting `network.dns-servers` has DNS servers, the pod gets them with `dnsPolicy: None`.
Without DNS servers the pod keeps the default DNS settings.

The node cannot reach the pod network.
A probe of the kind `httpGet` or `tcpSocket` never succeeds, so only `exec` probes exist.

The operator gives the pod the same annotations as a VM pod.
They are in `docs/network.md`.
Only the operator can set these annotations, and a user cannot set them on a Deployment (see the admission policy `bedrock-network-attachments-workloads`).

### Security groups

`network.securityGroups` names SecurityGroups of the project.
Without a name, the default group of the project applies.
A pod is not reachable until a group allows the traffic.
The reason `SecurityGroupNotReady` shows a group that is not ready.

### Fixed address

Set `network.address` to give the pod one address of the Subnet.
The Container then has one replica and the strategy `Recreate`, so two pods never use the address at the same time.
The reasons are `AddressOutsideSubnet`, `AddressReserved` and `AddressInUse`.
The address is also `AddressInUse` when another Container pins it, or when another pod holds it.
The older Container keeps the address.

## Volumes

A Volume of a Container must have the storage class `filesystem`.
The reason is `VolumeClassUnsupported` for another class.
A Volume has one holder, a VM or a Container. The oldest user is the holder.
The reason `VolumeInUse` shows another holder, for example `Container/web`.
`Volume.status.attachedTo` shows `Container/<name>`.
The Volume cannot be deleted while a Container uses it.
Delete the Container to free the Volume. The data stays.

## Autoscale

`autoscale` sets the HorizontalPodAutoscaler.
It targets the Deployment.
`cpuPercent` and `memoryPercent` are the target use of the requests.
Without a target the autoscaler uses 80 percent of the CPU requests.
The controller leaves `replicas` of the Deployment to the autoscaler.
To stop an autoscaled container, remove `autoscale` and set `replicas: 0`.

## Run state

| Phase | Meaning |
|---|---|
| `Pending` | The container starts, or a dependency is missing. |
| `Running` | All wanted replicas are ready. |
| `Stopped` | `replicas` is 0. The condition `Ready` is true. |
| `Failed` | The Deployment cannot make progress. |

`status.addresses` lists the addresses of the ready pods in sorted order.
`status.replicas` and `status.readyReplicas` count the pods.
Only pods that the Container owns count. The owner chain is the Container, its Deployment, a ReplicaSet and the pod.
A pod with the same labels that someone made directly is never a load balancer target, a log source or an exec target.
When the quota of the project refuses a replica, the condition `Ready` shows `QuotaExceeded` at once.

## Logs, exec and restart

The proxy gives three subresources of `containers`.
The rights are `containers/logs` (`get`), `containers/exec` (`get`) and `containers/restart` (`update`).
A viewer reads the logs. An editor and an owner also use exec and restart.

| Path | Method | Meaning |
|---|---|---|
| `containers/<name>/logs` | GET | Streams the logs of the container `app`. |
| `containers/<name>/exec` | GET with a WebSocket upgrade | Opens a shell or runs a command in a ready pod. |
| `containers/<name>/restart` | POST | Sets the annotation `bedrock.cloudyfolks.io/restarted-at`. The operator rolls the pods. |

The logs take these query values.

| Query | Meaning |
|---|---|
| `pod` | One pod of the container. A pod of another container gives 404. |
| `tail` | From 1 to 10000 lines. The default is 200. |
| `follow`, `previous`, `timestamps` | `true` or `false`. |

With one pod the stream is raw.
With several pods and no `pod` the proxy merges the streams and starts each line with `[<pod>] `.

Exec takes `pod` (the default is the first ready pod), `command` (repeat it for each word, the default is `/bin/sh`) and `tty`.
The proxy checks the `Origin` header and refuses a pod of another container.
The WebSocket uses the sub-protocols of the Kubernetes API server, for example `v5.channel.k8s.io`.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `container.runtime-class` | `kata` | The RuntimeClass of the pods. The value `none` sets no RuntimeClass. |

Use `none` only on a cluster with no KVM. A pod then shares the kernel of the node.
When the RuntimeClass does not exist, the reason is `RuntimeUnavailable` and the operator creates no Deployment.
The addon `kata` creates the RuntimeClass `kata`. See `docs/addons.md`.

## Limits

- At most 20 replicas, 8 ports, 8 volumes and 8 security groups.
- One container in a pod. The container is named `app`.
- Only `exec` probes.
- A Container with an `address` or with `volumes` has at most one replica.
- The quota key `count/containers.bedrock.cloudyfolks.io` limits the number of Containers of a project. The quota of memory and CPU counts the requests of the pods and the overhead of Kata.
- The two-interface model, custom domains, ingress, container plans and revisions are not part of the Container kind.

## Troubleshooting

Run `kubectl describe container <name>` and read the condition `Ready`.

| Reason | Cause | Action |
|---|---|---|
| `NotInProject` | The namespace has no project labels. | Create the Container in a project namespace. |
| `InvalidSpec` | A field is wrong. | Read the message and correct the field. |
| `SubnetNotFound` | The Subnet does not exist. | Check `network.subnetRef`. |
| `SubnetNotReady` | The Subnet or its Network is not ready. | Check the Subnet condition. |
| `AddressOutsideSubnet` | The `address` is not in the CIDR of the Subnet. | Use an address in the CIDR. |
| `AddressReserved` | The `address` is the gateway, the network address, the broadcast address or an excluded address. | Use another address. |
| `AddressInUse` | Another Container or pod holds the `address`. | Use another address or delete the other object. |
| `SecurityGroupNotReady` | A named SecurityGroup is missing or not ready. | Create the group or check its condition. |
| `WaitingForDefaultSecurityGroup` | The default group of the project does not exist yet. | Wait. The operator creates it. |
| `VolumeNotFound` | A Volume does not exist. | Check `volumes[].volumeRef`. |
| `VolumeInUse` | A VM or another Container holds the Volume. | Free the Volume or use another one. |
| `VolumeClassUnsupported` | The Volume has not the storage class `filesystem`. | Create the Volume with the storage class `filesystem`. |
| `RuntimeUnavailable` | The RuntimeClass of the Setting does not exist. | Turn on the addon `kata` on a host with KVM, or set the Setting to `none` on a cluster with no KVM. |
| `PullSecretNotFound` | A pull Secret does not exist. | Create the Secret or correct `imagePullSecrets`. |
| `ApplyFailed` | The cluster refused the Deployment or the autoscaler. | Read the operator log. |
| `NameConflict` | An object of the same name exists and another owner has it. | Rename the Container or delete the other object. |
| `QuotaExceeded` | The quota of the project refuses a pod of the Container. The message is the text of the quota. | Free quota, lower `replicas` or the requests, or ask for a larger quota. |
| `Deploying` | The replicas are not all ready. | Wait, or look at the pods. |
| `Running` | All wanted replicas are ready. | None. |
| `Stopped` | `replicas` is 0. | Set `replicas` to start. |
| `Failed` | The Deployment did not make progress in time. | Read the message and the pod events. |
| `ImagePullFailed` | A pod cannot pull the image. | Check the image name, the registry and the pull Secret. The message is the text of the kubelet. |
| `ContainerConfigError` | A pod cannot be configured, for example a Secret of `env` is missing. | Read the message and create the missing object. |
