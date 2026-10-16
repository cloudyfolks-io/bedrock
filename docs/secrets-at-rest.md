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
k0s and no `<data-dir>/etcd`. On an installed node, `bedrock init` does not
change the encryption. The file `/etc/k0s/k0s.yaml` decides. If it names the
file and the file is missing, `bedrock init` stops. Restore the file from the
Secret or from a k0s backup.

## Backups hold the key

`k0s backup` archives `/var/lib/k0s/pki`, so the archive holds the key. The
etcd snapshots of `backup.etcd-schedule` contain a k0s backup. They hold the
key and the encrypted data together. Protect each backup like the cluster CA.
With KMS v2, the key stays in the KMS service.

## Join a control-plane node

The key never goes in the join token. `bedrock join` gets the key in these
steps before it starts k0s:

1. It calls `GET https://<vip>:9443/v1beta1/ca` on the k0s join API. The join
   token authorizes the call. The answer holds the cluster CA.
2. It signs a client certificate with the CA. The certificate has the name
   `bedrock-join`, the group `system:masters` and a life of 10 minutes.
3. It reads the Secret through `https://<vip>:6443` and writes the file.

An expired or deleted token gets nothing, and the join stops. A worker join
does not get the key. A token from a cluster without encryption joins as
before.

## Choose KMS v2

Set the endpoint in the cluster configuration before `bedrock init`:

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ClusterConfig
metadata:
  name: lab
spec:
  encryption:
    kms:
      endpoint: unix:///var/run/kms/kms.sock
```

Obey these rules:

- The endpoint is `unix:///` and an absolute socket path.
- Bedrock does not run the KMS plugin. Run the plugin on every control-plane
  node before `bedrock init` and `bedrock join`.
- The socket must accept connections from the user `kube-apiserver`.
- `bedrock init` and a control-plane `bedrock join` stop when the socket is
  missing.

`Cluster.spec.encryption.kms` copies the endpoint. It cannot change after
install.

## Limits

- A cluster installed before 0.5 is not encrypted. An upgrade does not
  enable encryption. `bedrock init` on such a node does not enable it.
- You cannot switch between AES-GCM and KMS v2 after install.
- The AES-GCM key has no rotation yet. Kubernetes advises a new key after
  200,000 writes.
