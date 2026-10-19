# End-to-end jobs

This page describes the end-to-end jobs of the repository.
Each job starts a real single-node cluster and checks the product from the outside.

## Jobs

| Job | What it proves |
| --- | --- |
| `e2e-kind` | The operator and the CRDs work in a kind cluster. |
| `e2e-bundle` | `bedrock init` builds a cluster from the offline bundle. |
| `e2e-authn` | Local, LDAP and OIDC login, tokens and the Kubernetes API webhook. |
| `e2e-tenant` | Tenants, projects, images, VMs, networks, backups, object storage, the panel and the metering, all together. |
| `e2e-upgrade`, `e2e-upgrade-abort`, `e2e-upgrade-from-release` | The cluster upgrade and its rollback. The backup archives stay sealed. |

The job `e2e-upgrade-from-release` starts from a build of a released Git tag.
`CI_RELEASE_REF` sets the tag. The default is `v0.5.0`.
This release writes its upgrade backup as a plain archive.
The new agent seals this archive after the upgrade, and the job checks it.

## Run a job

Use the local runner on a Linux host. The host needs a writable `/dev/kvm`, Lima, Docker, Go, Helm and kind.

```
hack/local-ci.sh e2e-tenant
```

The runner builds the release and the bundle, starts a Lima VM, runs `hack/e2e-init.sh` and then the job script.
The runner deletes the VM at the end.

Without an argument, the runner runs eight jobs in this order.
The full run takes about 6 hours.

1. `test`
2. `e2e-kind`
3. `e2e-bundle`
4. `e2e-upgrade`
5. `e2e-upgrade-abort`
6. `e2e-authn`
7. `e2e-tenant`
8. `e2e-upgrade-from-release`

GitHub runs the same jobs from `.github/workflows/ci.yml`.
The `make e2e-tenant` target runs the tenant job on a host that you prepared.

## Shared files

| File | Purpose |
| --- | --- |
| `hack/e2e-init.sh` | Creates the cluster and checks the platform. |
| `hack/e2e/lib.sh` | Host pins, dump helpers, wait helpers, the etcd read and encryption check, and the OSD and underlay functions. |
| `hack/e2e/cluster.yaml.tmpl` | The `ClusterConfig` of the e2e cluster. |
| `hack/e2e-release-config.sh` | Writes a release config without the `catalog` component. |

## Host names

The e2e cluster uses the platform host `e2e.bedrock.test`.
No public DNS exists for it.
The function `pin_hosts` in `hack/e2e/lib.sh` writes one line to `/etc/hosts`.
The line maps the apex name, `sso.`, `api.`, `console.` and `upload.` to the cluster VIP.
Every job script calls `pin_hosts` before it uses these names.

## Ceph with a real OSD

By default the e2e cluster has no storage device.
The condition `StorageReady` then has the reason `NoDevices`, and no `block` StorageClass exists.
Set `E2E_OSD_DEVICE` to give the cluster a device.

| Value | Result |
| --- | --- |
| empty | No device. The cluster keeps `NoDevices`. |
| `blank` | The one blank disk of the host. The job fails when no blank disk exists or when more than one exists. The local runner adds a second 60 GiB disk to the Lima VM for this. |
| `loop` | A loop device on a sparse file. GitHub uses this value. `E2E_OSD_LOOP_GIB` sets the size, 40 GiB by default. |
| a path | That block device. The device must pass the blank check. |

A disk is blank when all of these are true.

- It is a block device with a size above zero.
- It has no partition table, no file system signature and no partition.
- It is not mounted.
- The tools `lsblk`, `wipefs` and `findmnt` run without an error.

If one check cannot run, the disk is not blank.
The scripts never write to a disk.

When a device exists, `hack/e2e-init.sh` waits for `StorageReady` to be true.
It then checks the StorageClasses `block`, `block-retain` and `filesystem` and the VolumeSnapshotClass `block`.

The condition `StorageReady` stays false with the reason `OSDsNotUp` until one OSD is up for each listed device.
The release sets `allowLoopDevices` for the Rook operator, so Rook accepts a loop device that a host lists.

## Secrets encryption

A new cluster encrypts Secrets at rest from its first start.
The jobs check this in three places.

| Job | Check |
| --- | --- |
| `hack/e2e-init.sh` | The file `/etc/bedrock/encryption/encryption.yaml` has mode `600` and owner `kube-apiserver`. The directory `/etc/bedrock/encryption` has mode `700` and owner `kube-apiserver`. The Secret `kube-system/bedrock-secrets-encryption` is encrypted in etcd. |
| `hack/e2e-upgrade.sh` | The script opens the upgrade backup. The opened copy holds no entry that ends in `encryption.yaml`, no entry named `host.key`, and no file with the text `AGE-SECRET-KEY-`, in the outer archive and in the k0s backup inside it. After the upgrade, the script creates the Secret `default/e2e-encryption` and checks that etcd holds it encrypted. The script deletes the Secret. |
| `hack/e2e-tenant.sh` | The step `encryption_join_fetch` creates a control-plane token. The test `TestJoinFetchesEncryption` fetches the encryption file with the same function as `bedrock join`. The test compares it with the file on the node. |

The function `assert_secret_encrypted` in `hack/e2e/lib.sh` reads the stored value of the Secret from etcd.
It uses the JSON gateway `https://127.0.0.1:2379/v3/kv/range` and the client certificate of the API server.
It requires the prefix `k8s:enc:aesgcm:v1:key1:` on the stored value.

The upgrade script checks the encrypted Secret after the upgrade only when `/etc/bedrock/encryption/encryption.yaml` exists.
The check of the opened backup always runs.
The job `e2e-upgrade-from-release` starts from the tag in `CI_RELEASE_REF`.
The default tag is `v0.5.0`. This release already has encryption, so the job runs this check.
The job `e2e-upgrade-abort` ends at the rollback, before this check.

## Sealed backup archives

`hack/e2e-upgrade.sh` proves that the agent seals every backup archive with age.
Before the upgrade starts, the script does these steps.

1. It makes an admin identity with `bedrock backup keygen`. The script uses the CLI of the new version, because the old version can lack this command.
2. It sets the Setting `backup.archive-recipients` to the public key of this identity.

The old version can be a release that does not know this Setting.
The operator of such a release only sets `Ready=False` with the reason `UnknownKey` on the Setting.
It does not delete the Setting and does not change its value.
The operator of the new version sets `Ready=True` after the upgrade.

After the upgrade, the script checks these items.

| Check | Why |
| --- | --- |
| The newest `bedrock-<version A>-*` archive ends in `.tar.gz.age`, and no plain `bedrock-*.tar.gz` or `etcd-*.tar.gz` file stays in `/var/lib/bedrock/backups`. The script waits up to 10 minutes for this. | The agent seals older plain archives in its snapshot loop, so the seal is not instant. In `e2e-upgrade-from-release`, version A writes a plain archive, and the new agent must seal it. |
| The file starts with `age-encryption.org/v1`. | The file is an age file. |
| `bedrock backup decrypt` opens the file with the admin identity, and opens it with the host key. The two copies are equal. | The file is sealed to the recipients of the Setting and to the host key. |
| `tar -tzf` lists the opened copy, and the leak check of the section "Secrets encryption" runs on it. The script then deletes the copy. | The opened copy is a valid archive. |
| The script sets `backup.etcd-schedule` to `* * * * *` and waits up to 30 minutes for a new `etcd-*.tar.gz.age` file. It opens this file with the admin identity. It sets the schedule back to the default when the file exists. The restore of the section [Lean etcd snapshots](#lean-etcd-snapshots) brings the value `* * * * *` back, so the script sets the schedule back to the default again after the restore. | The new agent writes a sealed etcd snapshot, not only seals the old files. |

The job `e2e-upgrade-abort` checks that the archive that stays after the rollback ends in `.tar.gz.age`.
The agent of version A restores from this sealed archive with the host key.

## Lean etcd snapshots

A scheduled etcd snapshot (`etcd-*`) holds no image tarballs.
An upgrade backup (`bedrock-*`) keeps them.
`hack/e2e-upgrade.sh` checks a snapshot of the schedule `* * * * *`, and then restores it.

| Check | Why |
| --- | --- |
| The snapshot file is smaller than 512 MiB. The script prints the size in bytes and the seconds between its marker and the file. | The image tarballs are most of the size of a full k0s backup. The printed seconds show how long a snapshot takes. The script waits up to 30 minutes for the file. `k0s backup` still writes the image tarballs to a work directory before the agent removes them, so a snapshot takes several minutes. |
| The opened snapshot holds exactly one `k0s_backup_*.tar.gz` file. This k0s archive has no entry `images` and no entry under `images/`. | The agent drops the entries of the images from the k0s archive. |

Before the snapshot, the script makes the ConfigMap `default/e2e-etcd-restore` with the value `state=before-snapshot`.
It also starts the Pod `default/e2e-etcd-restore`.
The Pod uses the host network, the local pause image and `imagePullPolicy: IfNotPresent`.
It needs no CNI and no image pull.

After the checks of the upgrade, the script restores the snapshot.
It takes the sealed restore script from the section "Etcd snapshots" of `docs/backup.md`.
If the section has no such script, the job fails.
The script does these steps.

1. It saves the list of the files in `/var/lib/k0s/images`, with their sizes.
2. It deletes the ConfigMap and the Pod. It waits until containerd has no container of the Pod.
3. It runs the restore script of the documentation with the snapshot.
4. It waits up to 10 minutes for `/readyz`.
5. It sets `backup.etcd-schedule` to the default. The restored etcd holds `* * * * *` again. Without this step, the agent takes snapshots one after the other.

Then the script checks these items.

| Check | Why |
| --- | --- |
| The Lease of the node renews after the restore starts. | The kubelet works with the restored API server. |
| The Pod starts after the restore starts, and the ConfigMap has the value `before-snapshot`. | The restore brings back the objects of the snapshot. The kubelet starts the Pod again. |
| No Pod waits with the reason `ImagePullBackOff` or `ErrImagePull`. | The Pod starts from the images on the host. |
| Each file of the saved list is still in `/var/lib/k0s/images`, with the same size. | The restore script keeps the directory of the images when the snapshot has no images. |

The script then deletes the ConfigMap, the Pod, `/root/restore` and the directories `/var/lib/k0s/.pre-restore-*`.

## Adoption lab

The script `hack/lab-adopt.sh` rehearses the adoption of a Rook Ceph cluster on three Lima VMs.
It is not a job of the local runner. Run it by hand on a Linux host with about 46 GiB of free RAM.
Do not run it while a gate runs.
The runbook `docs/runbooks/adopt-ceph.md` describes the stages.

## Cleanup and teardown

Each job run writes markers into the directory `/run/bedrock-e2e`.
The markers name the loop device, the image file, the veth and the network namespace that the run created.
The cleanup removes only the objects that the markers name.
The scripts do not delete an image file that they did not create.
If the file at `E2E_OSD_IMAGE` exists before the run, the run stops.
`hack/e2e-init.sh` runs the cleanup when it fails.
`hack/e2e-tenant.sh` runs the cleanup when it ends.
To tear down by hand, run `source hack/e2e/lib.sh && e2e_cleanup` as root.

## Provider network

The ElasticIP checks need an external network.
Set `E2E_PROVIDER_NETWORK` to the name of the provider network, for example `public`.
Then `hack/e2e-init.sh` creates this underlay before it starts the cluster.

| Object | Value |
| --- | --- |
| veth pair | `bdrpub0` (host side) and `bdrpub0p` (peer side) |
| network namespace | `bdrext`, holds `bdrpub0p` with the address `192.0.2.250/24` |
| provider network | `192.0.2.0/24`, gateway `192.0.2.1`, interface `bdrpub0` |

A ping from `bdrext` to an ElasticIP proves that OVN answers ARP on the provider network and that the NAT reaches the guest.
The underlay does not prove gateway failover or BGP.
When `E2E_PROVIDER_NETWORK` is empty, the cluster config has no provider network.

## Fixtures

The cluster has no registry access.
`hack/e2e-tenant-images.sh` downloads the fixture images with `crane` into `dist/cache/e2e-tenant` and pins their digests.
The job script imports the tarballs into containerd with `k0s ctr`.

| Image | Use |
| --- | --- |
| `docker.io/chrislusf/seaweedfs:4.48` | The S3 target of the VM backup. |
| `docker.io/amazon/aws-cli:2.37.10` | The S3 client for the backup bucket and the object storage checks. It also checks `setfattr` in a Kata pod. |
| `docker.io/library/busybox:1.37.0` | The web server of the container checks. |

The VM image is the cirros containerDisk from the release bundle.
The Fabric image of the release is the pod image for network probes.
The tenant job does not need KVM inside the VM. KubeVirt runs in emulation there.

The catalog images are too large for the e2e cluster.
`hack/e2e-release-config.sh` removes the `catalog` component from the release config.
The tenant job builds the release with that config.

The files are in `test/e2e/tenant/fixtures`.

| File | Use |
| --- | --- |
| `seaweedfs.yaml` | SeaweedFS with an S3 identity in the namespace `backup-e2e`. |
| `bucket-job.yaml` | A Job that creates the bucket `bedrock-backups`. |
| `s3-roundtrip.yaml` | A Job that puts and gets one object. The script fills the variables with `envsubst`. |
| `s3-denied.yaml` | A Job that must get `AccessDenied` from another user. |

## The tenant job

`hack/e2e-tenant.sh` runs the steps in this order.
Each area has one file in `hack/e2e/tenant`.
If a name changes in an area, change it in that file only.

| Area file | Steps |
| --- | --- |
| `encryption.sh` | The fetch of the encryption file with a control-plane token. This step runs first. |
| `addons.sh` | Seeded addons. `kata` is blocked by the preflight when no KVM exists. `loki` is off, then on, then refuses wrong values. Grafana dashboards and the Alertmanager webhook. |
| `tenancy.sh` | The users and tokens. The tenant `acme` with its Network and owner role. The projects `web` and `data` with quota, the AAQ limit of five VMs, roles and policies. Workload and role checks. Quota edges. |
| `images.sh` | The ClusterImage `cirros` from the bundle. An image upload through the proxy. |
| `compute.sh` | An SSH key, a Volume, the SecurityGroup `web` and the VM `vm1`. Hot-plug and expand. A volume snapshot. The proxy actions and the console, which the Go test runs. |
| `network.sh` | The provider network and the Network status. The fabric-frr agent. Subnet rules. The security group on the VM. A VM with a pinned address, a Subnet MTU and Subnet DNS servers. An ElasticIP in L2 mode with the ARP and ping proof. An ElasticIP without a provider network. The ElasticIP quota. A LoadBalancer and a DNSZone. Isolation rules. |
| `container.sh` | The proof of the pod network: a hand-made Pod and a hand-made Deployment get the network metadata of a `Container`. Then the kind itself: the runtime gate, replicas, stop and start, a fixed address, autoscale, a Volume, a LoadBalancer target, logs, exec and restart through the proxy, and the cleanup. |
| `clusters.sh` | The proof of the control plane: a hand-made k0smotron `Cluster` with the fields of the `KubernetesCluster` kind. |
| `objectstorage.sh` | The object store, two users and one bucket. A put and a get from inside the cluster. A denied read by the other user. |
| `backup.sh` | The SeaweedFS target. A VM backup with one entry. A restore with `replace: true`. A restore of a missing backup. |
| `panel.sh` | The panel at `console.<host>`, the redirect from the apex, the API host and the Go test for the login and the watch stream. |
| `metering.sh` | `Project.status.usage` and `Tenant.status.usage`. The AAQ count of used VMs against the VMs in the project. The metering series in Prometheus with the labels `tenant` and `project`. The proxy route `/proxy/v1/metrics/range`, which returns points for the query `vm.cpu`. The serial console lines in Loki. |
| `cascade.sh` | Delete the projects and the tenant. Check that every underlying object is gone. |

The Go test binary `test/e2e/tenant` holds the checks that need a protocol client.
The script runs it with `-test.run` for each phase.

| Test | Checks |
| --- | --- |
| `TestProxyActions` | Start, stop, restart, migrate and snapshot through the proxy. A user without a role gets 403. |
| `TestConsoleSerial` | The serial console WebSocket, the origin check and the guest login prompt. |
| `TestPanelLoginAndStream` | The browserless login of the admin, the session, the access checks, the CSRF guard and the watch stream. |
| `TestContainerStreams` | The logs of a Container, merged with the pod name, the refusal of a viewer on exec and restart, the refusal of a stranger, a pod of another container giving 404, the origin check, a command through the exec WebSocket and the restart stamp. |
| `TestJoinFetchesEncryption` | The fetch of the encryption file with a control-plane join token. The fetched bytes equal the file on the node. |

The shell and the Go test do not repeat each other.
The shell checks objects with `kubectl`.
The Go test checks HTTP and WebSocket flows.

## Container network proof

The step `container_network_proof` in `hack/e2e/tenant/container.sh` proves the pod network that the `Container` kind needs.
The step runs after `network_isolation`.
A failed check stops the step.
The step makes the objects with the identity of `bedrock-operator`, because only the platform can set network annotations.

| Check | Proof |
| --- | --- |
| Tenant refusal | A Deployment of a tenant user with the network annotations in its pod template is refused with the text `only the platform may set network selection annotations`. |
| One interface | The pod has one interface that is not `lo`, and the pinned address `10.100.0.200`. |
| Fabric IP object | The Fabric `IP` object has `spec.podName` equal to the pod name and the Fabric Subnet label. |
| Pod port from a VM | A cirros VM on the same Subnet reads the page of the pod. The VM writes the result to the serial console. |
| Load balancer endpoint | A Fabric `SwitchLBRule` with the pod address as the endpoint forwards the traffic that the VM sends to the `Vip`. |
| Exec probe | A pod with an `exec` readiness probe becomes Ready, also as a Deployment with the strategy `Recreate`. |
| Probe limit | A pod with an `httpGet` readiness probe never becomes Ready, because the node cannot reach the pod network. |
| Kata | With KVM and a RuntimeClass `kata`, a pod runs, and `setxattr` works in an `emptyDir`. The step skips this check on hosts without KVM. |

The step uses a `SwitchLBRule` and not a `RouterLBRule`, because a `RouterLBRule` needs an external address.
Both kinds take the same endpoint list.

## Container steps

The e2e cluster has no KVM, so the kata RuntimeClass does not exist.
The steps of `hack/e2e/tenant/container.sh` use the preloaded busybox image.

| Step | Check |
| --- | --- |
| `container_runtime_gate` | With the default Setting `container.runtime-class` a Container has `Ready=False` with the reason `RuntimeUnavailable` and no Deployment. The step then sets the Setting to `none`. |
| `container_lifecycle` | A Container with two replicas is `Running` with two addresses and the strategy `RollingUpdate`. Replicas 0 gives `Stopped` with `Ready=True`, and it stays `Stopped`. Replicas 2 starts it again. A Container with `address` and one replica shows the address and uses `Recreate`. `autoscale` makes a HorizontalPodAutoscaler. Removing `autoscale` with `replicas: 0` stops the Container and removes the autoscaler. |
| `container_volume` | With the storage class `filesystem`, a Container mounts a Volume and the Volume shows `Container/stateful`. A second Container gets `VolumeInUse`. A restart keeps the data. Deleting the Container frees the Volume and keeps the claim. The step skips these checks when no `filesystem` class exists. |
| `container_load_balancer` | A LoadBalancer with `containerRef` is `Programmed`, and the Fabric load balancer holds the address of each ready pod. |
| `container_proxy` | The Go test `TestContainerStreams` for logs, exec and restart. |
| `container_cleanup` | Deleting the Containers removes every Deployment, autoscaler and pod. |

## Cluster proof

The step `clusters_proof` in `hack/e2e/tenant/clusters.sh` proves the control plane that the `KubernetesCluster` kind needs.
The step turns the addon `clusters` on and makes a k0smotron `v1beta2` `Cluster` by hand.
The `Cluster` has the fields that the controller renders: `patches` with the network annotations of a `Container`, `storage.type: kine`, a `pvc` for the data, both ports and `externalAddress`.
The step skips when the cluster has no `block` StorageClass.

| Check | Proof |
| --- | --- |
| First pod | The pod `kmc-cproof-0` has the fixed address from its first start. Its UID and restart count do not change, and the StatefulSet has no second revision. |
| Probes | The StatefulSet probes are exec probes of `k0s status`. No `httpGet` or `tcpSocket` probe exists. |
| Ports | The container ports are 6443 and 8132. The k0s config in the pod has `agentPort: 8132`, the `externalAddress` and the address in the API `sans`. The pod has one interface that is not `lo`. |
| Ready | The pod is Ready and `status.readyReplicas` of the `Cluster` is 1. |
| Kubeconfig | The Secret `cproof-kubeconfig` has the key `value`, and its server is `https://<address>:6443`. |
| Join token | A `JoinTokenRequest` gets `status.tokenID`. The Secret with the same name holds the key `token`. The step prints whether the deletion of the request invalidates the token. |

The e2e cluster has no internet, so a worker cannot install k0s there.
The worker join runs in `hack/lab-cluster.sh proof` on a host with KVM and internet.
The stage makes a security group, the `Cluster`, a `JoinTokenRequest`, the cloud-init Secret and the worker `VirtualMachine`.
It then waits for the Node to be Ready and runs `logs` and `exec` in the child through the konnectivity agent.
The stage `down` removes the objects.

## Network parity checks

The steps `network_underlay`, `network_frr_agent`, `network_guest_parity`, `network_elastic_ip_pool_choice` and `network_elastic_ip_quota` in `hack/e2e/tenant/network.sh` check the features of `docs/network.md`.

| Step | Check |
| --- | --- |
| `network_underlay` | The default Network of the tenant has `enableExternal: true` without a patch. The Fabric `Subnet` `provider-public` has the label `usability=public`. |
| `network_frr_agent` | The node has the label `fabric.cloudyfolks.io/external-gw=true`. The DaemonSet `fabric-frr` selects this label, has one ready pod, and the pod runs on the node. |
| `network_guest_parity` | The Subnet `parity` has `mtu: 1350` and two `dnsServers`. The Fabric `Subnet` has `spec.mtu` 1350 and the DHCP options `mtu=1350` and `dns_server=198.51.100.53;198.51.100.54`. A VM that pins the gateway address has `NetworkReady=False` with the reason `AddressReserved`, and it has no pod. A VM that pins `10.100.16.50` runs, and the VM status, the launcher pod annotations `ip_address` and `allocated`, and the guest agree. |
| `network_elastic_ip_pool_choice` | An ElasticIP with an empty `spec` is `Reserved`, has `status.providerNetwork` set to `public`, and has an address of the pool. |
| `network_elastic_ip_quota` | The script sets `quota.elasticIPs` to the number of ElasticIPs of the tenant plus one. The first new ElasticIP is `Reserved`. The second has `QuotaExceeded`, no `status.providerNetwork` and no Fabric `OvnEip`. When the first is deleted, the second becomes `Reserved`. The script then sets the quota to its old value. |

The test VM `vm2` has no SSH key.
Its user data is a shell script, and cirros runs it at boot.
The script writes `ip -o link show eth0`, the address of `eth0` and the `nameserver` lines of `/etc/resolv.conf` to the serial console, with the prefixes `PARITY-LINK`, `PARITY-ADDR` and `PARITY-DNS`.
The script reads these lines from the container `guest-console-log` of the launcher pod.
The NIC of the e2e host has MTU 1500, and Fabric gives the guest the MTU 1400 when the Subnet sets none.
The test Subnet therefore sets 1350, a value that the default cannot give, and the guest must report `mtu 1350`.
The script deletes `vm2`, `vm-bad`, their root disks and the Subnet `parity` at the end, because the backup step expects one VM.
It does this also when a check fails, after it prints the console log of `vm2`.
The quota step also restores the old quota and deletes its ElasticIPs when a check fails.

The guest has two possible DNS paths: the Fabric DHCP lease, or the `resolv.conf` of the launcher pod.
The step reads `/etc/resolv.conf` in the guest and expects the two servers of the Subnet.
If the guest reads other servers, the step fails with a message that says the guest does not take its DNS from Fabric DHCP.
The follow-up is then `dnsPolicy: None` with the same servers on the VM pod.

The step `network_frr_agent` proves that the agent runs.
It does not prove a BGP session.

## Read a failure

When a job fails, the script prints a dump.
The dump has all Bedrock kinds, the Fabric, KubeVirt, CDI, Velero and Rook kinds, the operator and proxy logs, the nmstate pods and logs, the launcher logs, the OVN NAT rows, the Velero and SeaweedFS logs and the events.
Read the first failing step in the output.
Then read the objects of that area in the dump.

## Limits

The tenant job does not check these items.

- The audit policy restart of the API server.
- Failover of the gateway chassis, because the cluster has one node.
- BGP mode of the ElasticIP. The e2e cluster runs in L2 mode, so no BGP Network, `BgpConf` or BGP session exists. Only the fabric-frr agent is checked.
- The migration of a VM to a second node.
- A full control-plane join of a second node. The tenant job checks only the fetch of the encryption file.
- The catalog download of the ClusterImage list.
