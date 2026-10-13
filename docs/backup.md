# Backup and restore

Bedrock backs up VMs with Velero. An admin sets one backup target. A tenant
backs up a VM with `VirtualMachineBackup`. A tenant restores a VM with
`VirtualMachineRestore`. The controller hosts also take etcd snapshots.

## What is backed up

A backup of a VM contains these objects:

- The Bedrock `Volume` objects that the VM uses.
- The KubeVirt VM and its related objects.
- The Secret `<vm>-cloudinit`. Bedrock creates it from the cloud-init data of
  the VM.
- The data of all disks.

A backup also holds a copy of the Bedrock `VirtualMachine` spec. Velero cannot
back up the Bedrock `VirtualMachine` object. The Velero plugin for KubeVirt
changes every object named `VirtualMachine` and removes the fields that it
does not know. So Bedrock excludes the object from the backup. Bedrock writes
the copy to the annotation `bedrock.cloudyfolks.io/virtual-machine` of the
Velero backup. Velero stores the Velero backup in the backup target. The copy
stays with the backup.

The copy holds these fields:

- The name and the namespace of the VM.
- The `spec` of the VM.
- The labels and the annotations that Bedrock owns. These are the keys with
  the prefix `bedrock.cloudyfolks.io/` and the label
  `velero.io/exclude-from-backup`.

The copy does not hold the status, the `managedFields`, the uid, the resource
version, the owner references or the annotation `restore-hold`.

The Secret that `spec.cloudInit.secretRef` names is not part of the backup.
Keep that Secret. If it is missing after a restore, the VM reports
`InvalidUserData` until you create it again.

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

## Backups stay when the VM is deleted

A backup outlives its VM. If you delete the Bedrock `VirtualMachine`, Bedrock
keeps its `VirtualMachineBackup` objects and its Velero backups. Bedrock
deletes a `VirtualMachineBackup` in only two cases:

- You delete the `VirtualMachineBackup`.
- You delete the project namespace.

While the VM does not exist, these rules apply:

- `Ready` is `False` with the reason `VirtualMachineNotFound`.
- Bedrock pauses the Velero schedule. No new backup starts.
- Bedrock does not remove old backups.
- The list `status.backups` keeps its entries.

Bedrock also pauses the schedule and does not remove backups while a restore
holds the VM. Bedrock starts the schedule again when the VM exists and the
hold is gone.

A schedule keeps the copy of the VM spec current. When the VM changes,
Bedrock writes the new copy to the schedule. Each backup that the schedule
makes gets the copy that was current at that time.

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

When the Bedrock `VirtualMachine` does not exist, Bedrock does these steps:

1. It reads the copy of the VM spec from the Velero backup. If the copy is
   missing, the restore gets the phase `Failed` and the reason
   `VirtualMachineSpecMissing`. Backups from older releases have no copy.
   Bedrock creates nothing in this case. Create the VM again by hand. Then
   restore with `replace: true`.
2. It creates the Bedrock `VirtualMachine` from the copy. The new object has
   the annotation `bedrock.cloudyfolks.io/restore-hold`. The compute
   controllers do not change the underlying objects while the annotation is
   present.
3. It waits until the old KubeVirt VM, data volumes and claims of the VM are
   gone. The reason is `Preparing`. Bedrock does not delete them. The
   garbage collector removes them after you delete the VM.
4. It asks Velero to restore the KubeVirt VM, the data volumes, the claims
   and the Secret `<vm>-cloudinit`.
5. When the restore is complete, it removes the annotation. The compute
   controllers adopt the restored objects.

If the copy is not valid, the restore gets the phase `Failed` and the reason
`VirtualMachineSpecInvalid`. Bedrock creates nothing in this case.

A restore creates the Bedrock `VirtualMachine` only one time. If the
controller restarts in the middle of a restore, it continues with the object
that exists.

The restored VM starts at once when the restored spec says `running: true`.
The spec is the spec that the VM had at the time of the backup. To stop the
VM, use the normal stop action after the restore.

If the restore fails, Bedrock keeps the Bedrock `VirtualMachine` that it
created and removes the annotation. The compute controllers then make
the missing objects from the spec. These objects are new and empty. To try
again, create a restore with `replace: true`.

While a restore runs, the VM and its volumes carry the annotation
`bedrock.cloudyfolks.io/restore-hold`. Bedrock removes the annotation when
the restore ends, fails or is deleted. If you delete a restore before it
ends, Bedrock keeps the Bedrock `VirtualMachine` that it created.

Remember these limits:

- A restore with `replace` does not revert the Bedrock `VirtualMachine` spec.
- A restore of a deleted VM needs a backup that has the copy of the VM spec.
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
