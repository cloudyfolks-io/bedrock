# Secrets at rest

Bedrock encrypts Kubernetes Secrets in etcd. The API server encrypts each
Secret before it writes the Secret to etcd. Other objects are not encrypted.
A new cluster encrypts from its first start. The default provider is AES-GCM
with a local key. You can choose KMS v2 at install.

## Where the key is

The key is in this file on each control-plane node:

```
/var/lib/k0s/pki/encryption.yaml
```

The file has mode `0600`. The owner is `kube-apiserver`. It is an
`EncryptionConfiguration` with one `aesgcm` key, named `key1`. With KMS v2,
it has one `kms` provider, named `bedrock-kms`, and no key.

`bedrock init` also stores a copy of the file in the Secret
`kube-system/bedrock-secrets-encryption`. The API server encrypts this
Secret too. Etcd data alone does not give the key.

`bedrock init` makes a key only on a fresh node. A fresh node has no running
k0s and no `<data-dir>/etcd`. On an installed node, `bedrock init` never
changes the encryption. The API server argument `encryption-provider-config`
in `/etc/k0s/k0s.yaml` shows if the cluster is encrypted. On an installed
node, `bedrock init` stops in these cases:

- `k0s.yaml` names `encryption.yaml`, and `encryption.yaml` is missing.
- `encryption.yaml` exists, but `k0s.yaml` does not name it.

To restore a missing `encryption.yaml`, use a k0s backup. The API server
encrypts the Secret with the same key. On a one-node cluster, nobody can
read the Secret without the key. On a cluster with more control-plane nodes,
you can also read the Secret through another control-plane node.

## Backups hold the key

`k0s backup` includes `/var/lib/k0s/pki`, so the archive holds the key. The
etcd snapshots of `backup.etcd-schedule` contain a k0s backup. The backups of
`bedrock upgrade` use `k0s backup` too. Each of them holds the key and the
encrypted data together. Protect each backup like the cluster CA.

With KMS v2, the file holds only the endpoint. Back up the keys of the KMS
service separately. A restore cannot decrypt the Secrets without them.

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
