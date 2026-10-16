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
`aesgcm` key, named `key1`. With KMS v2, it has one `kms` provider, named
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

Keep a copy of the key apart from the backups, for example in a secrets
vault. The key never changes, so you do not update the copy. Without the
copy, you cannot read the Secrets of a restored cluster.

To restore after the loss of all control-plane nodes:

1. Put the key at `/etc/bedrock/encryption/encryption.yaml`. Set the file to
   mode `0600` and the directory to mode `0700`. The owner of both is
   `kube-apiserver`. The user exists after `k0s install controller`.
2. Run `k0s restore` with the backup archive.
3. Start k0s.

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
- The AES-GCM key has no rotation yet. Kubernetes says to use a new key after
  200,000 writes.
