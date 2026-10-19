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
| `hack/lab/lib.sh` | The VM, network, disk and bundle code of the two labs. |
| `hack/e2e/lib.sh` | Host pins, dump helpers, wait helpers, the etcd read and encryption check, the OSD and underlay functions, and the reader of the restore script in docs/backup.md. |
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

## Multi-node lab

The script `hack/e2e-multinode.sh` builds a cluster of three Lima VMs.
Node 1 runs `bedrock init`. Nodes 2 and 3 run `bedrock join`. All three nodes carry the four roles.
Ceph has 3 replicas and 6 OSDs.
It is not a job of the local runner. Run it by hand, or from a nightly job, on a Linux host with KVM and about 46 GiB of free RAM.
Do not run it while a gate or the adoption lab runs. Both labs use the same network and the same VIP, and the script stops when it finds a VM of the other lab.
The VM code is in `hack/lab/lib.sh`. The adoption lab uses the same file.
The checks are in `hack/e2e/multinode/`.

```
hack/e2e-multinode.sh all
hack/e2e-multinode.sh down
```

| Stage | What it does |
| --- | --- |
| `up` | Builds the bundle, starts the three VMs, checks the network and the disks. |
| `install` | Runs `bedrock init` and two `bedrock join`, sets the OSD devices of nodes 2 and 3, waits for the storage. |
| `tenant` | Creates the tenant `acme`, the projects `web` and `data`, and the cirros image. |
| `check` | Runs every check and writes `dist/lab-multinode/result.txt`. A failed check also writes `diag-<n>.log` in the same directory. |
| `snapshot`, `rewind` | Snapshot the VM disks after `tenant`, and go back to them. |
| `down` | Removes the VMs and their disks. |
| `all` | Runs `up`, `install`, `tenant` and `check`. The VMs stay. |

The checks run in this order.

| Check | What it proves |
| --- | --- |
| Three control-plane nodes | Three Nodes are Ready, three Hosts have the role `control-plane`, etcd has three members, and kube-vip runs on three nodes. |
| Ceph topology and pool size | Three monitors, six OSDs with two on each host, `HEALTH_OK`, and every replicated pool has size 3. |
| Block Volume access mode | The PVC of a Volume on the `block` class is `ReadWriteMany` with the mode `Block`. |
| Live migration keeps IP and MAC | The VM status and the guest show the same IP and MAC after a migration, and the guest does not restart. |
| Large transfer between hosts | A guest reads 10 MiB from a guest on another host. The guest MTU plus 100 bytes is not more than the MTU of the node NIC. |
| Pinned address after a migration | A VM with a pinned address keeps it after a migration, and the client reads 10 MiB from it again. |
| Hot-plugged volume | A Volume that is added to `spec.disks` of a running VM shows in the guest as a SCSI disk, with no restart. The guest writes and reads it. A migration with the disk attached works. |
| SMBIOS UUID | The UUID in the guest equals the firmware UUID of the VMI. The script records whether one VM name in two namespaces gives one UUID. |
| Subnet MTU 1500 | The script records whether the 10 MiB read stalls with a Subnet MTU of 1500 on an underlay of 1500. |
| Drain | `kubectl drain` moves a `LiveMigrateIfPossible` VM and a `LiveMigrate` VM to another node with no guest restart. |
| Etcd restore with running VMs | The restore follows `docs/runbooks/restore-control-plane.md` with a snapshot of the schedule. Then the cluster checks run again. |
| Secrets key rotation | Runs only when the Cluster CRD has `spec.encryption.action`. The script creates 2000 Secrets, sets the action `rotate`, and crashes node 3 during `RewritingSecrets`. The rotation must end. The sampled Secrets in etcd must hold one new key name, no `encryption.yaml` may hold the old key, and an archive made before `bedrock backup rotate-key` must still decrypt. |

A check that answers an open question prints lines that start with `info:`. Read them in `result.txt`:

- `Q2`, `Q3`, `Q5` and `Q7` are the open questions of the compute research.
- `8b` is the MTU rule of the migration program.
- `restore:` lines list each VM, and tell if its guest restarted. The script samples twice: after the nodes renew their Leases, and again after the taint eviction window of 380 seconds. Read the final sample. The last line gives the number of `TaintManagerEviction` events.

A guest is a cirros VM. Its boot script prints `LAB-` lines to the serial console every 5 seconds. The checks read them from the container `guest-console-log` of the launcher pod, so no guest needs an SSH route.

The variable `LAB_EMULATION` sets the virtualization of the guests: `kvm`, `tcg` or `auto`. The default is `auto`. It probes `/dev/kvm` and the CPU flags inside node 1, and it falls back to `tcg` with an `info:` line when the probe fails. A guest in `tcg` is much slower than a guest with KVM.
A check that fails writes its full output to `dist/lab-multinode/check-<name>.log`. The script prints the path, and the `info:` lines of the failed check.

The run takes about 2.5 hours. The restore waits for an etcd snapshot, which takes about 11 minutes, and then for Ceph to settle.

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
| `docker.io/amazon/aws-cli:2.37.10` | The S3 client for the backup bucket and the object storage checks. |

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
| `TestJoinFetchesEncryption` | The fetch of the encryption file with a control-plane join token. The fetched bytes equal the file on the node. |

The shell and the Go test do not repeat each other.
The shell checks objects with `kubectl`.
The Go test checks HTTP and WebSocket flows.

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
- The migration of a VM to a second node, and a full control-plane join of a second node. The [multi-node lab](#multi-node-lab) checks both.
- The catalog download of the ClusterImage list.
