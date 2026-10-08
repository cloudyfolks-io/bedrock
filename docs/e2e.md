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
| `e2e-upgrade`, `e2e-upgrade-abort`, `e2e-upgrade-from-main` | The cluster upgrade and its rollback. |

## Run a job

Use the local runner on a Linux host. The host needs a writable `/dev/kvm`, Lima, Docker, Go, Helm and kind.

```
hack/local-ci.sh e2e-tenant
```

The runner builds the release and the bundle, starts a Lima VM, runs `hack/e2e-init.sh` and then the job script.
The runner deletes the VM at the end.

GitHub runs the same jobs from `.github/workflows/ci.yml`.
The `make e2e-tenant` target runs the tenant job on a host that you prepared.

## Shared files

| File | Purpose |
| --- | --- |
| `hack/e2e-init.sh` | Creates the cluster and checks the platform. |
| `hack/e2e/lib.sh` | Host pins, dump helpers, wait helpers and the OSD and underlay functions. |
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
| `blank` | The first whole disk without a file system and without partitions. The local runner adds a second 60 GiB disk to the Lima VM for this. |
| `loop` | A loop device on a sparse file. GitHub uses this value. `E2E_OSD_LOOP_GIB` sets the size, 40 GiB by default. |
| a path | That block device. |

When a device exists, `hack/e2e-init.sh` waits for `StorageReady` to be true.
It then checks the StorageClasses `block`, `block-retain` and `filesystem` and the VolumeSnapshotClass `block`.

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
| `addons.sh` | Seeded addons. `kata` is blocked by the preflight when no KVM exists. `loki` is off, then on, then refuses wrong values. Grafana dashboards and the Alertmanager webhook. |
| `tenancy.sh` | The users and tokens. The tenant `acme` with its Network and owner role. The projects `web` and `data` with quota, roles and policies. Workload and role checks. Quota edges. |
| `images.sh` | The ClusterImage `cirros` from the bundle. An image upload through the proxy. |
| `compute.sh` | An SSH key, a Volume, the SecurityGroup `web` and the VM `vm1`. Hot-plug and expand. A volume snapshot. The proxy actions and the console, which the Go test runs. |
| `network.sh` | The provider network and the Network status. Subnet rules. The security group on the VM. An ElasticIP in L2 mode with the ARP and ping proof. A LoadBalancer and a DNSZone. Isolation rules. |
| `objectstorage.sh` | The object store, two users and one bucket. A put and a get from inside the cluster. A denied read by the other user. |
| `backup.sh` | The SeaweedFS target. A VM backup with one entry. A restore with `replace: true`. A restore of a missing backup. |
| `panel.sh` | The panel at `console.<host>`, the redirect from the apex, the API host and the Go test for the login and the watch stream. |
| `metering.sh` | `Project.status.usage` and `Tenant.status.usage`. The metering series in Prometheus with the labels `tenant` and `project`. The serial console lines in Loki. |
| `cascade.sh` | Delete the projects and the tenant. Check that every underlying object is gone. |

The Go test binary `test/e2e/tenant` holds the checks that need a protocol client.
The script runs it with `-test.run` for each phase.

| Test | Checks |
| --- | --- |
| `TestProxyActions` | Start, stop, restart, migrate and snapshot through the proxy. A user without a role gets 403. |
| `TestConsoleSerial` | The serial console WebSocket, the origin check and the guest login prompt. |
| `TestPanelLoginAndStream` | The browserless login of the admin, the session, the access checks, the CSRF guard and the watch stream. |

The shell and the Go test do not repeat each other.
The shell checks objects with `kubectl`.
The Go test checks HTTP and WebSocket flows.

## Read a failure

When a job fails, the script prints a dump.
The dump has all Bedrock kinds, the Fabric, KubeVirt, CDI, Velero and Rook kinds, the operator and proxy logs, the launcher logs, the OVN NAT rows, the Velero and SeaweedFS logs and the events.
Read the first failing step in the output.
Then read the objects of that area in the dump.

## Limits

The tenant job does not check these items.

- The audit policy restart of the API server.
- Failover of the gateway chassis, because the cluster has one node.
- BGP mode of the ElasticIP.
- The migration of a VM to a second node.
- The catalog download of the ClusterImage list.
