# Secrets at rest

Bedrock encrypts Kubernetes Secrets in etcd. The API server encrypts each
Secret before it writes the Secret to etcd. Other objects are not encrypted.
A new cluster encrypts from its first start. The default provider is AES-GCM
with a local key. You can choose KMS v2 at install.

## Where the key is

The key is in this file on each control-plane node:

```
/etc/bedrock/encryption/encryption.yaml
```

The file has mode `0600`. The directory has mode `0700`. The owner of both
is `kube-apiserver`. The file is an `EncryptionConfiguration` with one
`aesgcm` key, named `key1`. A [rotation](#rotate-the-key) names each new key
`key2`, `key3` and so on. With KMS v2, it has one `kms` provider, named
`bedrock-kms`, and no key.

`bedrock init` also stores a copy of the file in the Secret
`kube-system/bedrock-secrets-encryption`. The API server encrypts this
Secret too. Etcd data alone does not give the key.

`bedrock init` makes a key only on a fresh node. A fresh node has no running
k0s and no `<data-dir>/etcd`. On an installed node, `bedrock init` never
changes the encryption. The API server argument `encryption-provider-config`
in `/etc/k0s/k0s.yaml` shows if the cluster is encrypted. It must name
`/etc/bedrock/encryption/encryption.yaml`. On an installed node,
`bedrock init` stops in these cases:

- `k0s.yaml` names another path than `encryption.yaml`.
- `k0s.yaml` names `encryption.yaml`, and `encryption.yaml` is missing.
- `encryption.yaml` exists, but `k0s.yaml` does not name it.

If the node ever ran encrypted, add `encryption-provider-config` back to
the API server `extraArgs` in `k0s.yaml`. Remove `encryption.yaml` only if
the node never ran encrypted. A removed key makes every encrypted Secret
unreadable.

To restore a missing `encryption.yaml`, copy it from another control-plane
node or from your separate key copy. On a cluster with more control-plane
nodes, you can also read the Secret through another control-plane node. On a
one-node cluster, the API server cannot start without the file, so nobody
can read the Secret.

After you restore the file by hand, set the mode to `0600` and the owner to
`kube-apiserver`. Set the directory to mode `0700` and the same owner. Or run
`bedrock init` again on that node. It sets them.

## Backups do not hold the key

The etcd snapshots and the backups of `bedrock upgrade` do not hold the key.
This is on purpose. `k0s backup` archives `/var/lib/k0s/pki`, and Bedrock
keeps the key outside of that directory. A backup alone does not show the
Secrets.

Each archive is also sealed. Only the host key and the admin recipients open
it. See [Sealed archives](backup.md#sealed-archives).

Keep a copy of the key apart from the backups, for example in a secrets
vault. Replace the copy after each [key rotation](#rotate-the-key). Without
a current copy, you cannot read the Secrets of a restored cluster.

Keep the admin identity apart from the backups too. The host key is lost
with the host. Without the admin identity, you cannot open an archive after
the loss of all control-plane nodes.

To restore after the loss of all control-plane nodes:

1. Put the key at `/etc/bedrock/encryption/encryption.yaml`. Set the file to
   mode `0600` and the directory to mode `0700`. The owner of both is
   `kube-apiserver`. The user exists after `k0s install controller`. If the
   archive is older than a key rotation, use the key of that time. See
   [Restore an older snapshot](#restore-an-older-snapshot).
2. Open the backup archive with the admin identity:

   ```sh
   bedrock backup decrypt --identity admin.key \
     --in <archive> --out backup.tar.gz
   ```

   Unpack `backup.tar.gz`. It holds the file `k0s_backup_*.tar.gz`.
3. Put the images back. A scheduled etcd snapshot holds no images. On a
   rebuilt host, extract the images of the bundle before you restore. The
   section [Etcd snapshots](backup.md#etcd-snapshots) gives the command and
   the restore script.
4. Run `k0s restore` with the file `k0s_backup_*.tar.gz`.
5. Start k0s.

With KMS v2, the KMS service holds the key. The file holds only the
endpoint. Back up the keys of the KMS service separately. A restore cannot
decrypt the Secrets without them.

## Join a control-plane node

The key never goes in the join token. `bedrock join` gets the key in these
steps before it starts k0s:

1. It calls `GET https://<vip>:9443/v1beta1/ca` on the k0s join API. The join
   token gives it access. The answer holds the cluster CA.
2. It signs a client certificate with the CA. The certificate is valid for 10
   minutes. Its name is `bedrock-join` and its group is `system:masters`.
3. It reads the Secret through `https://<vip>:6443` and writes the file.

An expired or deleted token gets nothing, and the join stops. A worker join
does not get the key. A token from a cluster without encryption joins as
before.

## Rotate the key

Rotate the AES-GCM key when a person who knew it leaves, or after 200,000
writes. Rotation works only with the default AES-GCM key. With KMS v2, rotate
the key in your KMS service.

Start a rotation:

```sh
kubectl patch cluster cluster --type merge \
  -p '{"spec":{"encryption":{"action":"rotate"}}}'
```

The operator clears `spec.encryption.action` when it takes the action. Read
the progress in `status.encryption`:

```sh
kubectl get cluster cluster -o jsonpath='{.status.encryption}{"\n"}'
```

The rotation has four phases. In each phase, every control-plane node must
have the same file before the next phase starts. The first key writes. The
other keys only read.

| Phase | Keys in the file | Work |
|---|---|---|
| `AddingKey` | old, new | Each node adds the new key as the second key and restarts its API server. |
| `PromotingKey` | new, old | Each node makes the new key first and restarts its API server. |
| `RewritingSecrets` | new, old | The operator writes every Secret again, so that etcd holds each Secret under the new key. |
| `RemovingKey` | new | Each node removes the old key and restarts its API server. |

When the last phase ends, `status.encryption.phase` is empty and
`status.encryption.lastRotatedAt` shows the time.

The operator changes one control-plane node at a time. It starts the next
node only when all of these are true:

- The earlier nodes have the new file and a running API server.
- Etcd is healthy on every control-plane node.

During a restart, a cluster with one control-plane node has no API for a short
time. A cluster with three control-plane nodes keeps its API.

The operator gives a node its turn with the annotation
`bedrock.cloudyfolks.io/encryption-apply` on the Host. The agent reads the
new content with the admin kubeconfig of the node, writes the file, restarts
`k0scontroller` and waits for `/readyz`. A rotation does not start, and it
does not go on, during an upgrade.

### If a rotation stops

`status.encryption.message` tells the reason. The rotation goes on by itself
when the reason is gone, also after a restart of the operator or of an agent.

If a node cannot start its API server with the new file, the agent puts the
old file back and restarts again. It writes the failed hash in
`/etc/bedrock/encryption/.apply-failed` and does not try that content again.
The operator waits and gives no other node its turn. Read
`journalctl -u bedrock-agent` on the node, fix the reason, and then revert the
rotation.

To undo a rotation, set the action `revert`:

```sh
kubectl patch cluster cluster --type merge \
  -p '{"spec":{"encryption":{"action":"revert"}}}'
```

- In `AddingKey`, the operator removes the new key. No Secret used it.
- In `PromotingKey` or `RewritingSecrets`, the roles of the keys swap. The
  rotation goes on to the end with the old key as the new key, and it
  rewrites all Secrets.
- In `RemovingKey`, the revert is refused. Some nodes already lost the old
  key.

### After a rotation

1. Replace your copy of the key (see [Backups do not hold the
   key](#backups-do-not-hold-the-key)). Replace the copy in a vault, in
   OpenBao and in your offline storage.
2. Keep the retired files. See the next section.

### Retired files

When a node removes a key, the agent first keeps the old file in:

```
/etc/bedrock/encryption/retired/<UTC stamp>.yaml
```

The file has mode `0600`, and the directory has mode `0700`. The stamp is the
time of the removal.

The agent deletes a retired file when no archive in
`/var/lib/bedrock/backups` has a stamp at or before the stamp of the retired
file. If you copied archives off the host, keep the retired files for as
long as you keep those copies.

### Restore an older snapshot

A snapshot holds Secrets that are encrypted with the keys of its time. After
a rotation, the live file does not hold the old key. To restore a snapshot:

1. Find the stamp of the archive in its name.
2. On each control-plane node, find the retired file with the first stamp at
   or after the stamp of the archive. If no retired file has such a stamp,
   use the live file.
3. Copy that file to `/etc/bedrock/encryption/encryption.yaml`. Set the file
   to mode `0600` and the directory to mode `0700`. The owner of both is
   `kube-apiserver`.
4. Restore the archive as in the steps above.

## Choose KMS v2

Add this to your cluster configuration before `bedrock init`:

```yaml
spec:
  encryption:
    kms:
      endpoint: unix:///var/run/kms/kms.sock
```

Obey these rules:

- The endpoint has the form `unix://` and the absolute path of the socket.
  Example: `unix:///var/run/kms/kms.sock`.
- Bedrock does not run the KMS plugin. Run the plugin on every control-plane
  node before `bedrock init` and `bedrock join`.
- The socket must accept connections from the user `kube-apiserver`.
- `bedrock init` and a control-plane `bedrock join` stop when the socket is
  missing.

Bedrock copies the endpoint to `Cluster.spec.encryption.kms`. This field
cannot change after install.

## Limits

- A cluster installed before 0.5 is not encrypted. An upgrade does not
  enable encryption. `bedrock init` on such a node does not enable it.
- You cannot switch between AES-GCM and KMS v2 after install.
- Rotation works only with AES-GCM. A KMS v2 cluster rotates the key in its
  KMS service.
- Bedrock does not read etcd to prove that no Secret uses the old key. It
  writes every Secret again, and it shows the number in
  `status.encryption.rewrittenSecrets`.
- A rotation in the phase `RemovingKey` cannot revert.
