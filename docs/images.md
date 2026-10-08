# Images

An image is a disk that a virtual machine can start from.
Bedrock imports the disk one time.
Then it keeps a golden copy on Ceph.
Each virtual machine gets a clone of the golden copy.

## Image and ClusterImage

An `Image` lives in a project namespace.
Only the users of that project can see it.

A `ClusterImage` is visible to all users.
Only a cluster administrator can create it.
Its objects live in the namespace `bedrock-images`.
Every authenticated user can read the list of `ClusterImage` objects.

Both kinds use the same spec and the same status.

## Sources

Set exactly one source in `spec.source`.

### Registry

Use a registry source for a containerDisk image.
Without `pullSecretRef`, the node pulls the image.
This works offline when the image is in the node cache.

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Image
metadata:
  name: ubuntu
  namespace: acme-web
spec:
  source:
    registry:
      image: quay.io/containerdisks/ubuntu:24.04
  sizeGi: 10
```

Set `refresh.schedule` to import the image again on a schedule.
Bedrock keeps the last `refresh.keep` versions.
The default for `keep` is 3.
A schedule needs a registry that the cluster can reach.
It does not work offline.
Only a registry source can have `refresh`.

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Image
metadata:
  name: ubuntu-fresh
  namespace: acme-web
spec:
  source:
    registry:
      image: quay.io/containerdisks/ubuntu:24.04
  refresh:
    schedule: "0 3 * * *"
    keep: 2
  sizeGi: 10
```

### HTTP

Use an http source for a disk file.
Add a `checksum` to verify the file.
Bedrock uses the checksum as the digest of the image.

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Image
metadata:
  name: rocky
  namespace: acme-web
spec:
  source:
    http:
      url: https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base-9.8-20260525.0.x86_64.qcow2
      checksum: sha256:92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8
  sizeGi: 10
```

### Upload

Use an upload source for a file on your computer.
See the section "Upload".

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Image
metadata:
  name: custom
  namespace: acme-web
spec:
  source:
    upload: {}
  sizeGi: 20
```

### Volume

Use a volume source to make an image from a disk that exists.
The name is the name of a volume in the same namespace.
Bedrock clones the claim of the volume.

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Image
metadata:
  name: golden
  namespace: acme-web
spec:
  source:
    volumeRef:
      name: web-root
```

If you change the source, Bedrock imports the disk again.
The old golden copy stays as an older version.

## Phases and status fields

| Phase | Meaning |
| --- | --- |
| `Pending` | The import did not start. |
| `WaitingForUpload` | The image waits for your file. |
| `Importing` | Bedrock imports the disk or makes the golden copy. |
| `Ready` | A virtual machine can use the image. |
| `Failed` | The import failed. Read the condition message. |

| Field | Meaning |
| --- | --- |
| `status.digest` | The registry digest, the checksum of an http source, or `uid:` and the claim UID. |
| `status.sizeBytes` | The size of the golden copy. |
| `status.dataSource` | The name of the DataSource that virtual machines clone. |
| `status.uploadURL` | The upload address. It is set while the phase is `WaitingForUpload`. |
| `status.progress` | The import progress in percent. Upload has no progress. |
| `status.versions` | The kept versions, newest first. |

The condition `Ready` is true only in the phase `Ready`.

## Upload

1. Create the Image with `source.upload`.
2. Wait for the phase `WaitingForUpload`.
3. Send `POST /apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/<namespace>/images/<name>/upload` to the API proxy.
4. Read `url` and `token` from the answer.
5. Send the file to the address:

```
curl -H "Authorization: Bearer <token>" --data-binary @disk.qcow2 <url>
```

The token is valid for 5 minutes.
Call `upload` again to get a new token.
You can upload only while the phase is `WaitingForUpload`.
A `ClusterImage` cannot take an upload.

## ISO images

Set `spec.type` to `iso` for an installation disk.
Bedrock imports an ISO in the same way as a disk.
The golden copy and the DataSource carry the annotation `bedrock.cloudyfolks.io/attach: read-only`.
A virtual machine attaches a clone of an ISO as a read-only CD-ROM.

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Image
metadata:
  name: installer
  namespace: acme-web
spec:
  type: iso
  source:
    http:
      url: https://example.com/installer.iso
      checksum: sha256:0000000000000000000000000000000000000000000000000000000000000000
  sizeGi: 2
```

You cannot change the type after you create the image.

## Catalog

The release has a catalog of `ClusterImage` objects.

| Name | Type | Source |
| --- | --- | --- |
| `ubuntu-24.04`, `ubuntu-26.04` | disk | registry, `quay.io/containerdisks/ubuntu` |
| `debian-12`, `debian-13` | disk | registry, `quay.io/containerdisks/debian` |
| `almalinux-9`, `almalinux-10` | disk | registry, `quay.io/containerdisks/almalinux` |
| `fedora-43` | disk | registry, `quay.io/containerdisks/fedora` |
| `rocky-9`, `rocky-10` | disk | http, `dl.rockylinux.org`, with SHA-256 |
| `virtio-win` | iso | registry, `quay.io/kubevirt/virtio-container-disk` |

Windows disk images are never part of the catalog.
The `virtio-win` ISO holds the Windows drivers.

Rocky has no containerDisk upstream.
The Rocky images use an http source.
They are for amd64 only.
They are not available offline.
If Rocky removes the file, the import fails.

Each catalog image has the annotation `bedrock.cloudyfolks.io/target-image`.
It holds the future name, for example `ghcr.io/cloudyfolks-io/images/ubuntu:24.04`.
Today the source points to the upstream image.
When the registry `ghcr.io/cloudyfolks-io/images` exists, only `source.registry.image` changes.

The release build pins each registry image to a digest and lists it in `images.txt`.
The e2e release does not contain the catalog.

## How a virtual machine uses an image

A virtual machine clones the DataSource that `status.dataSource` names.
The DataSource of an `Image` is in the namespace of the Image.
The DataSource of a `ClusterImage` is in `bedrock-images`.

The operator creates the clone.
A project user never creates a clone.
The Role `bedrock-image-cloner` in `bedrock-images` lets the operator clone from the catalog namespace.
The operator has no clone permission in other namespaces.

## Private sources

- Registry: set `pullSecretRef` to a Secret in the namespace of the Image. The Secret holds the keys `accessKeyId` and `secretKey`. Bedrock then pulls the image over the network, not through the node.
- HTTP: set `http.secretRef` to a Secret with the keys `accessKeyId` and `secretKey`.
- HTTP with a private CA: set `http.caConfigMap` to a ConfigMap with the key `ca.crt`.
- Platform CA: when the Setting `platform.additional-ca` is not empty, Bedrock copies it to the ConfigMap `platform-additional-ca` in each namespace that has an Image. An http source without `caConfigMap` uses it.

## Troubleshooting

| Symptom | Cause | Command |
| --- | --- | --- |
| `Pending` with the reason `SnapshotFormatUnavailable` | The StorageProfile has no snapshot format. Ceph or the snapshot class is missing. | `kubectl describe image <name>` |
| `WaitingForUpload` and `uploadURL` is empty | The Setting `platform.host` is not set. | `kubectl describe image <name>` |
| `Failed` with the reason `ImportFailed` | The importer could not read the source. | `kubectl get datavolume -A` |
| Upload answers `409` | The image is not in the phase `WaitingForUpload`, or it has no upload source. | `kubectl describe image <name>` |
| `CatalogNamespaceMissing` | The namespace `bedrock-images` does not exist. | `kubectl get namespace bedrock-images` |

To see why an upload to `upload.<host>` fails, read the logs of the upload proxy:

```
kubectl -n cdi logs deploy/cdi-uploadproxy
```
