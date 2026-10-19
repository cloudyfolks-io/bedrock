# Databases

This page describes the managed MySQL and Valkey engines of the `Database` kind and the operations on every engine.
The engine pods run in the namespace `bedrock-dbaas`. Tenants cannot read this namespace.

## MySQL

The addon `percona-ps` must be on. The Setting `database.enabled` must be `true`.
Without the addon, the condition `Ready` shows the reason `EngineNotAvailable`.

- A class without high availability gives one instance. A class with high availability gives three instances and three HAProxy pods.
- Backups go to `<bucket>/<uid>/` every day at 02:00. The Setting `database.backup.retention-days` sets how many days stay.
- The server image and the companion images `backup`, `haproxy`, `init` and `toolkit` come from the `DatabaseVersion`. The seed versions are `mysql-8.4` and `mysql-9.7`.
- A restore uses a full backup. A MySQL restore has no target time: `restore.targetTime` shows the reason `RestoreTimeUnsupported`.
- If the source `Database` does not exist, the controller reads the newest backup directory under `<bucket>/<sourceUID>/`. If there is none, the reason is `RestoreSourceEmpty`.

## Valkey

Valkey needs no addon. It works when `database.enabled` is `true`.

- One instance only. A class with high availability shows the reason `InstancesUnsupported`.
- No backup and no restore. `backup.enabled: true` and `restore` are refused. A `DatabaseSchema` shows the reason `UnsupportedForEngine`.
- A `DatabaseUser` takes exactly one grant on the database `app`. `readonly`, `readwrite` and `all` are the levels. The optional `keyPrefix` limits keys and channels to the prefix. A prefix has at most 64 characters.
- A metrics sidecar runs as the user `_exporter`. This user cannot run a write command. A `PodMonitor` scrapes it. The seed versions are `valkey-8.1`, `valkey-9.0` and `valkey-9.1`.

## Image rule

The controller writes the digest that it applied to `status.image`. It uses `status.image` for every later reconcile.
A change of a `DatabaseVersion` never restarts a running database.
To apply the current digest, set the annotation `bedrock.cloudyfolks.io/refresh-image` to `"true"` and change the annotation `bedrock.cloudyfolks.io/restart`.

## Operations

| Change | What happens | Reasons |
| --- | --- | --- |
| `spec.class` | Resources and memory parameters change. | `ClassChangeInvalid` when the new class changes `highAvailability`. |
| `spec.storageGi` grows | The volume grows. | `StorageOutOfRange` outside the class limits. `ExpansionNotSupported` when the storage class cannot expand. |
| `spec.version` | The version upgrade starts when the new version is in `upgradeTo` of the current one. | `VersionNotAllowed` otherwise. |
| `bedrock.cloudyfolks.io/restart` changes | The engine restarts. | |
| `spec.suspended` | The engine, the gate and the proxy scale to zero, or back. The Secrets and the address stay. | |

A PostgreSQL or MySQL upgrade needs a completed backup younger than the Setting `database.upgrade.backup-max-age` (default `26h`).
If there is none, the controller makes one `Backup` named `<uid>-pre-<unix>` once. `Ready` shows `BackupRequired` until it completes.
A Valkey upgrade needs no backup.
`status.applied` records the class, version, storage, suspend state and restart token that the controller applied.

## Statistics and logs

`status.stats` holds the used disk, the connections and, for PostgreSQL, the replication lag. The controller reads it every 5 minutes.
A failed read keeps the old values and the old `sampledAt`.

Read the engine log through the proxy:

```
GET /apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/<project>/databases/<name>/logs?tail=200
```

`tail` is 200 by default and 500 at most. The output has a limit of 1 MiB. Project owner, editor and viewer may read it.
The call answers 409 when the database has no ready pod.

## Alerts

The alerts `DbaasDiskNearlyFull`, `DbaasDiskCritical`, `DbaasPodNotReady`, `DbaasPodRestarting` and `DbaasValkeyMemoryHigh` ship with the base. The alerts `DbaasReplicationLag`, `DbaasBackupStale` and `DbaasWalArchiveFailing` ship with the `cnpg` addon.
Each alert names the database with its UID and its name. A suspended database has no pod, so no alert fires.
