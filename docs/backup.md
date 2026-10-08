# Backup and restore

Bedrock backs up VMs with Velero. An admin sets one backup target. A tenant
backs up a VM with `VirtualMachineBackup`. A tenant restores a VM with
`VirtualMachineRestore`. The controller hosts also take etcd snapshots.

## What is backed up

A backup of a VM contains these objects:

- The Bedrock `VirtualMachine` object and the Bedrock `Volume` objects
  that the VM uses.
- The KubeVirt VM and its related objects.
- The data of all disks.

Velero copies the disk data with CSI snapshots. The node agent of Velero
moves the data to the backup target.

A backup does not contain network objects. It does not contain objects that
have no namespace.

A snapshot of a running VM disk is crash-consistent. Bedrock does not freeze
the guest file system.

## Set the backup target

Velero writes backups to S3-compatible object storage. Set the target in the
Setting `backup.target`. Use this form:

```
s3://<endpoint>[:<port>]/<bucket>[/<prefix>][?region=<region>][&insecure=true]
```

These examples are valid:

```
s3://s3.eu-west-1.amazonaws.com/my-bucket/bedrock/prod?region=eu-west-1
s3://storage.example.com/bucket-1
s3://seaweedfs.backup-e2e.svc:8333/bedrock-backups?insecure=true
```

The connection uses HTTPS. Add `insecure=true` to use plain HTTP. The default
region is `us-east-1`. Do not put credentials in the URL. Bedrock refuses
a URL with credentials.

A bucket name has 3 to 63 characters. It uses lowercase letters, digits,
dots and hyphens.

NFS targets are not supported. Velero has no NFS store. To use an NFS
export, put an S3 gateway in front of it. Then use the `s3://` address of the
gateway.

Put the credentials in a Secret in the namespace `bedrock-system`. The Secret
has these keys:

| Key | Required | Meaning |
|---|---|---|
| `access-key-id` | yes | S3 access key |
| `secret-access-key` | yes | S3 secret key |
| `repository-password` | yes | Password of the backup repository |
| `ca.crt` | no | PEM certificate of a private CA |

Keep the value of `repository-password`. A restore on a new cluster needs
it. Without the password, nobody can read the backups.

Create the Secret and set the two Settings:

```
kubectl -n bedrock-system create secret generic backup-credentials \
  --from-literal=access-key-id=<key> \
  --from-literal=secret-access-key=<secret> \
  --from-literal=repository-password=<password>
kubectl patch setting backup.credentials-secret --type merge \
  -p '{"spec":{"value":"backup-credentials"}}'
kubectl patch setting backup.target --type merge \
  -p '{"spec":{"value":"s3://storage.example.com/bucket-1"}}'
```

The controller creates the Velero backup location `default`. Read the result:

```
kubectl get setting backup.target -o jsonpath='{.status.conditions}'
kubectl get events -A --field-selector involvedObject.name=backup.target
kubectl -n velero get backupstoragelocation default
```

If the URL or the Secret is not valid, the controller changes nothing. It
raises a warning event on the Setting. The event never shows secret values.

If you empty `backup.target`, the controller removes the backup location and
the credential Secrets that it created.

## Back up a VM

Create a `VirtualMachineBackup` in the project namespace. This example makes
one backup:

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachineBackup
metadata:
  name: web-once
  namespace: acme-prod
spec:
  vmRef: web
  target: default
```

This example makes a backup every day at 02:00 UTC. It keeps the newest 7:

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachineBackup
metadata:
  name: web-daily
  namespace: acme-prod
spec:
  vmRef: web
  target: default
  schedule: "0 2 * * *"
  keep: 7
```

The rules are:

- `target` is `default`. Only the target in the Settings exists.
- `schedule` has five cron fields in UTC. Descriptors such as `@daily` are
  not allowed.
- A schedule fires at most once per hour. `* * * * *` and `*/5 * * * *` give
  `Ready=False` with the reason `ScheduleTooFrequent`.
- `keep` is a count. The default is 7. The maximum is 365.
- A one-shot backup is created once. If it fails, Bedrock does not retry it.
  Delete the object and create it again.
- A one-shot backup is kept for 30 days.

Read the status:

```
kubectl -n acme-prod get virtualmachinebackup web-daily -o yaml
```

The field `status.phase` is `Pending`, `InProgress`, `Completed` or
`Failed`. The field `status.lastBackup` is the time of the newest completed
backup. The field `status.backups` lists the newest completed backups. Each
entry has an `id`, a `time` and `sizeBytes`.

When you delete a `VirtualMachineBackup`, Bedrock deletes its schedule and
its stored backups.

## Restore a VM

A `VirtualMachineRestore` names a `VirtualMachineBackup` in the same project
and one backup `id` from its status.

This example replaces a VM with a backup:

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachineRestore
metadata:
  name: web-again
  namespace: acme-prod
spec:
  backupRef: web-daily
  backupID: <id from status.backups>
  replace: true
```

Bedrock deletes the KubeVirt VM, the data volumes and the claims of the VM.
Then Velero restores them from the backup. The Bedrock `VirtualMachine`
object stays as it is.

This example restores a VM after you deleted it:

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachineRestore
metadata:
  name: web-recover
  namespace: acme-prod
spec:
  backupRef: web-daily
  backupID: <id from status.backups>
  targetName: web
```

While a restore runs, the VM and its volumes carry the annotation
`bedrock.cloudyfolks.io/restore-hold`. The compute controllers do not change
the underlying objects while the annotation is present. Bedrock removes the
annotation when the restore ends, fails or is deleted.

Remember these limits:

- A restore does not revert the Bedrock `VirtualMachine` spec.
- `targetName` must be the name of the VM that you backed up. A different name
  gives the phase `Failed` and the reason `RenameNotSupported`.
- `backupID` must be in the list `status.backups` of the named backup.
  Otherwise the reason is `BackupNotFound` and Velero gets no request.

## Etcd snapshots

Every control-plane host takes etcd snapshots. The Setting
`backup.etcd-schedule` sets the time. The default is `0 2 * * *`. The format
is the same as the schedule of a VM backup, but a short interval is allowed.

Each snapshot is a file on the host:

```
/var/lib/bedrock/backups/etcd-<stamp>.tar.gz
```

The file contains the k0s backup and the OVN databases. The host keeps the
newest 7 snapshots. Backups of an upgrade (`bedrock-*.tar.gz`) are not
touched.

Snapshots stay on the host. They are not copied to the backup target. To
protect them from the loss of a disk, copy the files off the host with your
own tools. To restore a snapshot, use `k0s restore` with the archive.

## Limits

- The only backup engine for VMs is Velero.
- Only S3-compatible targets work.
- Only one target exists. Its name is `default`.
- A backup is crash-consistent.
- A restore cannot rename a VM.
- A schedule fires at most once per hour.
- Etcd snapshots stay on the host.

## Troubleshooting

Look at the Velero objects:

```
kubectl -n velero get backupstoragelocation,backups,restores,datauploads
```

Look at the node agent pods and the Velero log:

```
kubectl -n velero get pods
kubectl -n velero logs deployment/velero
```

If a condition says `VeleroNotInstalled`, the Velero component is not in the
release yet. Wait for the upgrade to finish.
