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
