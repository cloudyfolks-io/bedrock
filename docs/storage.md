# Storage keys

Bedrock runs Ceph with Rook. Ceph uses cephx keys to identify its daemons and its clients.
The setting `storage.cephx-cipher` selects the key type.

| Value | Meaning |
|-------|---------|
| `aes` | The key type of Rook 1.20. The CephCluster has no `allowedCiphers`. The `AUTH_*` health warnings are muted. This is the default of a cluster that has no setting. |
| `aes256k` | Only `aes256k` keys are allowed. The `AUTH_*` health warnings are not muted. |

## Kernel rule

The CSI drivers mount RBD and CephFS volumes with the kernel clients.
Rook says that `aes256k` CSI keys need Linux kernel 7.0 or newer.
Keep `aes` until every host runs kernel 7.0 or newer.
Bedrock checks the rule in these places:

- `bedrock init` chooses `aes256k` only when the kernel of the first host is 7.0 or newer. Otherwise it chooses `aes`.
  Set `spec.storage.cephxCipher` in the cluster config to choose it yourself.
- An adopted cluster keeps its live mode. The mode is `aes256k` when `adopt.cephx.csi.keyType` is `aes256k`.
- `bedrock join` refuses a host with a kernel older than 7.0 when the cluster uses `aes256k`.
- The upgrade preflight refuses to start when a host kernel is older than 7.0 and the cluster uses `aes256k`.
- When you change the setting from `aes` to `aes256k` and one host reports an older kernel, Bedrock keeps `aes`.
  The condition `StorageCephxRestricted` names the host.

Rook v1.21 also requires Ceph msgr2 by default. This needs kernel 5.11 or newer.

## Change from aes to aes256k

Rook must rotate every key before it restricts the ciphers.
If a core daemon key is still `aes` when the ciphers are restricted, Ceph refuses the connection and Rook cannot update the cluster.
Bedrock does the change in two stages:

1. Stage A: the daemon generation is 2, the CSI generation is 3, the `rbdMirrorPeer` generation is 2, `keepPriorKeyCountMax` is 1, and the key type is `aes256k`.
   There is no `allowedCiphers`. The `AUTH_*` warnings stay muted. The condition `StorageCephxRestricted` is `False` with the reason `RotatingKeys`.
2. Stage B: Bedrock adds `allowedCiphers: [aes256k]` and stops muting the `AUTH_*` warnings.
   It does this only when `status.cephx` of the CephCluster shows every key at the target generation with key type `aes256k`, and `ceph auth dump-keys` lists no `aes` key.

If Bedrock cannot prove this, it stays in stage A. The message of the condition tells why.

A new cluster and an adopted cluster whose `adopt.cephx` has `allowedCiphers` go to stage B at once.
A key generation cannot go down in Rook. Do not set `aes` again after stage A started.

## Upgrade from Rook 1.20

A cluster with no `storage.cephx-cipher` setting keeps `aes`. The upgrade to Rook 1.21 does not rotate keys.
Change the setting when every host runs kernel 7.0 or newer:

```sh
kubectl patch setting storage.cephx-cipher --type=merge -p '{"spec":{"value":"aes256k"}}'
```

Watch the stages with `kubectl get cluster bedrock -o jsonpath='{.status.conditions[?(@.type=="StorageCephxRestricted")]}'`.

## Resources of the Ceph pods

Bedrock sets requests and limits on the Ceph pods in the CephCluster, CephFilesystem and CephObjectStore specs. An adopted CephCluster gets the same values.
The OSD request is equal to its limit for memory and CPU. This gives the OSD pods the Guaranteed QoS class, so the kernel kills them last.

| Setting | Default | Use |
|---|---|---|
| `storage.osd-memory` | `5Gi` | Request and limit of each OSD. |
| `storage.osd-memory-target` | `4Gi` | `osd_memory_target` of each OSD. It must be lower than `storage.osd-memory`. |
| `storage.osd-cpu` | `2` | CPU request and limit of each OSD. |
| `storage.mon-memory` | `2Gi` | Request of each mon. The limit is 1.5 times the request. |
| `storage.mgr-memory` | `1Gi` | Request of each mgr. The limit is twice the request. |
| `storage.mds-memory` | `2Gi` | Request of each metadata server. The limit is twice the request. |
| `storage.rgw-memory` | `2Gi` | Request of each object gateway. The limit is twice the request. |

The OSD memory target of 4 GiB is the value of the old ir-central-1 cluster. The limit is 25 percent higher, because an OSD can use more than its target for a short time.
The CPU requests are 500m for mon, mgr and mds, and 1 CPU (limit 2) for rgw. The old cluster used 1 CPU for mgr and 2 CPU for each OSD.
Two OSDs, one mon, one mgr, one mds and one rgw on a host request 16 GiB of memory. Size the memory of the hosts with this value and with the kubelet reservations in `docs/upgrade.md`. A lab with small nodes must lower these settings before the first install.

Bedrock also carries these settings of the old cluster:

- `rbd_blocklist_on_break_lock=false`, `rbd_blocklist_expire_seconds=60` and `mon_osd_blocklist_default_expire=60`, so a host that comes back after a crash is not blocked for an hour.
- The object gateway options `rgw_gc_max_concurrent_io=20`, `rgw_override_bucket_index_max_shards=101`, `rgw_bucket_quota_ttl=60`, `rgw_user_quota_bucket_sync_interval=60` and `rgw_user_quota_sync_interval=60`.
- The object gateway APIs `s3`, `admin` and `iam`.

Bedrock does not carry `osd_pool_default_pg_autoscale_mode=on` (this is the Ceph default), the crash collector retention (the crash collector is off), the pool option `bulk` and `enableRBDStats` (adopted pools keep them), and the dashboard.
