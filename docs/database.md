# Databases

## What the database area does

The database area gives a project managed PostgreSQL, MySQL and Valkey databases.
A user creates a `Database` in a project namespace.
The operator makes the engine in the hidden namespace `bedrock-dbaas`.
The user connects from the project subnet to a proxy address.
The operator also makes backups, restores, users and logical databases.

PostgreSQL runs with CloudNativePG (addon `cnpg`), MySQL with the Percona operator (addon `percona-ps`) and Valkey as one Deployment without an addon.

The project namespace must have the labels `bedrock.cloudyfolks.io/tenant` and `bedrock.cloudyfolks.io/project`.
Without these labels the operator creates nothing.
The condition `Ready` shows the reason `NotInProject`.

## Turn the area on

The area is off by default. Do these steps as an administrator.

1. Turn the addon `cnpg` on: `kubectl patch addon cnpg --type merge -p '{"spec":{"enabled":true}}'`.
   The addon installs the CloudNativePG operator and the Barman Cloud plugin. See `docs/addons.md`.
2. Set `database.vip-range` to an IPv4 network of the services plane, for example `100.66.16.0/21`.
3. Make at least one `DatabaseClass`.
4. Set `database.enabled` to `true`.

With `database.enabled` set to `false`, a `Database` shows `Ready=False` with the reason `DatabaseDisabled`.
With the addon of the engine off, it shows `EngineNotAvailable`.
The operator makes the namespace, the quota and the network policy only when `database.enabled` is `true`.
It never removes them when you set the Setting back to `false`.

## Kinds

### DatabaseVersion and DatabaseClass

Both kinds have no namespace. They are the catalog.
Every authenticated user can read them. Only the group `bedrock:bedrock-admins` can write them.

`DatabaseVersion` has the name `<engine>-<major>`, for example `postgresql-17`.

| Field | Meaning |
|---|---|
| `engine` | `postgresql`, `mysql` or `valkey`. |
| `major` | The major version. |
| `image` | The engine image. It must have a digest (`@sha256:`). |
| `port` | The port of the engine. |
| `enabled` | A disabled version is refused for a new `Database`. A running `Database` is not changed. |
| `eolDate` | An optional end-of-life date. |
| `upgradeTo` | The majors of the same engine that this version can move to. A major must differ from `major`. |
| `companions` | A map of extra images. Each image needs a digest. Plan 26c uses it. |

The addon `cnpg` ships the seed versions `postgresql-16`, `postgresql-17` and `postgresql-18`.
The ansible role of the region adds the others.

`DatabaseClass` has these fields: `cpu`, `memory`, `minStorageGi`, `maxStorageGi`, `highAvailability` and `enabled`.
A class with `highAvailability: true` makes 3 instances. Other classes make 1 instance.
`maxStorageGi` must not be below `minStorageGi`.

### Database

| Field | Meaning |
|---|---|
| `engine` | `postgresql`, `mysql` or `valkey`. It cannot change. |
| `version` | A major that names an enabled `DatabaseVersion`. It changes only to an enabled `upgradeTo` entry of the current version. See "Changes to a running database". |
| `class` | The name of a `DatabaseClass`. A change must keep `highAvailability`. |
| `storageGi` | The storage in GiB. It is between the class minimum and maximum. It can only grow. |
| `subnetRef` | A `Subnet` of the project. The default is `default`. It cannot change. |
| `allowedCIDRs` | The networks that can connect. The default is the CIDR of the subnet. |
| `backup.enabled` | The default is `true`. A Valkey database must set `false`. |
| `restore` | `sourceUID` and an optional `targetTime`. It cannot change after the create. |
| `suspended` | Hibernates the engine. |

The name has at most 52 characters and is a DNS label.

The `status` shows `phase`, `conditions`, `addresses` (`subnet`, `ip` and `port`), `credentialsSecret`, `instances`, `backup` and `stats`.

| Phase | Meaning |
|---|---|
| `Provisioning` | The database is new and not ready yet. |
| `Restoring` | A restore is new and not ready yet. |
| `Ready` | The database accepts connections. |
| `Unhealthy` | The database was ready and a part of it is not ready now. |
| `Suspended` | `spec.suspended` is `true`. |
| `Failed` | The database was not ready after `database.provision-timeout`. The controller keeps trying. |
| `Deleting` | The database is being deleted. |

| Condition | Meaning |
|---|---|
| `Ready` | The phase is `Ready`. |
| `EngineReady` | The engine reports a healthy cluster and the first setup ran. |
| `GateReady` | The gate Deployment is ready. |
| `EndpointReady` | The proxy Deployment in the subnet is ready. |
| `BackupHealthy` | The last completed backup is not older than 36 hours. |

### DatabaseUser

`DatabaseUser` has these fields: `databaseRef`, `username` and `grants`.
A grant has a `database` (`app` or the name of a ready `DatabaseSchema`) and a `level` (`readonly`, `readwrite` or `all`).
The user name is `spec.username`, or the name of the object when you set no `username`.
It must match `^[a-z][a-z0-9_]{1,62}$`.
These names are reserved: `postgres`, `dbadmin`, `streaming_replica`, `cnpg_pooler_pgbouncer`, names that start with `pg_` and names that end with `_ro`, `_rw` or `_all`.

The operator makes the Secret `<name>-db` with the keys `username`, `password`, `host`, `port` and `uri`.
Set the annotation `bedrock.cloudyfolks.io/reset-password` to a new value to make a new password.
When you delete the object, the operator moves the objects of the role to `dbadmin` and drops the role.
If the role still owns objects that cannot move, the reason is `UserOwnsObjects` and the object stays.

### DatabaseSchema

`DatabaseSchema` makes a logical database in the engine.
It has these fields: `databaseRef`, `name` and `deletionPolicy`.
`name` is the name of the object when you set no `name`. It must match `^[a-z][a-z0-9_]{1,62}$`.
The names `app`, `postgres`, `template0` and `template1` are reserved.
The database `app` exists from the first setup and has no object.
`deletionPolicy` is `Retain` (the default) or `Delete`.
`Delete` drops the database when you delete the object. `Retain` removes only the object.
The operator makes three group roles for each database: `<name>_ro`, `<name>_rw` and `<name>_all`.

## The admin account

The admin user is `dbadmin` and the first database is `app`.
The operator copies the credentials to the Secret `<name>-admin` in the project namespace.
The Secret has the keys `username`, `password`, `host`, `port`, `database` and `uri`.
The operator overwrites a change that you make to this Secret.
Set the annotation `bedrock.cloudyfolks.io/reset-password` on the `Database` to a new value to reset the admin password.

## Network path

The engine pods are in `bedrock-dbaas`. A pod in a tenant subnet cannot reach them directly.
The operator builds a path of three hops.

1. A proxy pod in the project subnet. It is a Deployment named `<name>-endpoint` with one replica, a pinned address and the Kata runtime class (the Setting `container.runtime-class`). A SecurityGroup named `database-<first 8 characters of the UID>` allows the engine port from `allowedCIDRs`.
2. A mutual TLS link from the proxy to a kube-vip address. The address comes from `database.vip-range`. The Service is `<uid>-vip` with the annotation `kube-vip.io/loadbalancerIPs`.
3. A gate pod in `bedrock-dbaas`. It ends the TLS and sends plain TCP to the engine. The gate accepts only a client certificate with the common name equal to the UID of its database.

The CA of this path is the ClusterIssuer `bedrock-database-ca`. Only the addon `cnpg` makes it.
A tenant cannot make a cert-manager object.

Limits:

- The customer connects to the address in `status.addresses` with plain TCP. The proxy does not offer TLS to the customer.
- A change of the allowlist or a renewed certificate restarts the proxy or the gate. The connection stops for a short time.

## Backups and restore

The operator makes one bucket for all databases and one prefix `<uid>/` for each database.
The bucket name is the Setting `database.backup.bucket`.
An RGW user `database-backups-s3` owns it. Its keys are in the Secret `database-backups-s3` in `bedrock-dbaas`.
A tenant cannot read this Secret.

- The schedule is `0 0 2 * * *` with the Barman Cloud plugin.
- `database.backup.retention-days` is the number of days of backups and WAL (1 to 365, default 7).
- `status.backup` shows `items`, `lastAt`, `pitrFrom`, `pitrTo` and `sizeBytes`. The size is read every 10 minutes.

To restore, set `spec.restore.sourceUID` to the UID of the source database. Add `targetTime` for a point in time.

- The source does not need to be a live object. An old UID works when the prefix `<uid>/` has at least one object.
- A live source must have the same engine and version. Its storage must not be larger than `storageGi`. It must be in the same project.
- The restored database gets a new admin password.

When you delete a database, the operator leaves the ConfigMap `tombstone-<uid>` in `bedrock-dbaas`.
The annotation `bedrock.cloudyfolks.io/delete-after` holds the time.
The sweeper deletes the prefix and the ConfigMap after `database.backup.retention-after-delete`.

## Quota and capacity

- The key `count/databases.bedrock.cloudyfolks.io` of the project quota limits the number of `Database` objects. The field is `Project.spec.quota.databases`.
- The quota `databases` of `bedrock-dbaas` limits the platform. It follows `database.capacity.cpu`, `database.capacity.memory` and `database.capacity.pods`.
- A new database is refused with the reason `CapacityExceeded` when the used requests plus the new requests are above `database.max-utilization` of the quota.
  A quota without a status gives `CapacityUnknown`. The operator creates nothing before this check.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `database.enabled` | `false` | Turns the area on. |
| `database.capacity.cpu` | `32` | CPU requests of the platform namespace. |
| `database.capacity.memory` | `128Gi` | Memory requests of the platform namespace. |
| `database.capacity.pods` | `150` | Pods of the platform namespace. |
| `database.max-utilization` | `0.9` | The share of the capacity that a create can use. |
| `database.sweeper-dry-run` | `true` | The orphan sweeper only logs. Set `false` after you read the log. |
| `database.provision-timeout` | `30m` | The time after which a database that is not ready is `Failed`. |
| `database.exec-timeout` | `120s` | The time limit of one SQL script. |
| `database.vip-range` | empty | The network of the gate addresses. Empty gives `VIPRangeMissing`. |
| `database.backup.bucket` | `bedrock-database-backups` | The bucket of the backups. |
| `database.backup.retention-days` | `7` | Days of backups. |
| `database.backup.retention-after-delete` | `168h` | The time that the backups of a deleted database stay. |
| `database.upgrade.backup-max-age` | `26h` | The age limit of the backup that an upgrade needs. An older backup makes a new pre-upgrade backup. |

The engine pods run in the namespace `bedrock-dbaas`. Tenants cannot read this namespace.

## MySQL

The addon `percona-ps` must be on. The Setting `database.enabled` must be `true`.
Without the addon, the condition `Ready` shows the reason `EngineNotAvailable`.

- A class without high availability gives one instance. A class with high availability gives three instances and three HAProxy pods.
- Backups go to `<bucket>/<uid>/` every day at 02:00. The Setting `database.backup.retention-days` sets how many days stay.
- The server image and the companion images `backup`, `haproxy`, `init` and `toolkit` come from the `DatabaseVersion`. The seed versions are `mysql-8.4` and `mysql-9.7`.
- The names `app`, `mysql`, `sys`, `information_schema` and `performance_schema` are reserved: a `DatabaseSchema` with one of them shows `InvalidSpec`, and no script drops them.
- A restore uses a full backup. A MySQL restore has no target time: `restore.targetTime` shows the reason `RestoreTimeUnsupported`.
- If the source `Database` does not exist, the controller reads the newest backup directory under `<bucket>/<sourceUID>/`. If there is none, the reason is `RestoreSourceEmpty`.

## Valkey

Valkey needs no addon. It works when `database.enabled` is `true`.

- One instance only. A class with high availability shows the reason `InstancesUnsupported`.
- No backup and no restore. `backup.enabled: true` and `restore` are refused. A `DatabaseSchema` shows the reason `UnsupportedForEngine`.
- A `DatabaseUser` takes exactly one grant on the database `app`. Set `spec.keyPrefix` for the prefix; the other engines refuse it. `readonly`, `readwrite` and `all` are the levels. The optional `keyPrefix` limits keys and channels to the prefix. A prefix has at most 64 characters. The ACL does not filter the output of `SCAN` and `KEYS`: a user can list key names outside its prefix in the same database. It cannot read or write their values.
- A metrics sidecar runs as the user `_exporter`. This user cannot run a write command. A `PodMonitor` scrapes it. The seed versions are `valkey-8.1`, `valkey-9.0` and `valkey-9.1`.

## Image rule

The controller writes the digest that it applied to `status.image`. It uses `status.image` for every later reconcile.
A change of a `DatabaseVersion` never restarts a running database.
To apply the current digest, set the annotation `bedrock.cloudyfolks.io/refresh-image` to `"true"` and change the annotation `bedrock.cloudyfolks.io/restart`.

## Changes to a running database

| Change | What happens | Reasons |
| --- | --- | --- |
| `spec.class` | Resources and memory parameters change. The address does not change. | `ClassChangeNotSupported` when the new class changes `highAvailability`. The check compares the instance count of the running engine, from the first render on. |
| `spec.storageGi` grows | The volume grows. | `StorageOutOfRange` outside the class limits. `ExpansionNotSupported` when the storage class cannot expand. |
| `spec.version` | The version upgrade starts when the new version is in `upgradeTo` of the current one. | `VersionNotAllowed` otherwise. |
| `bedrock.cloudyfolks.io/restart` changes | The engine restarts. | |
| `spec.suspended` | The engine, the gate and the proxy scale to zero, or back. The Secrets and the address stay. | |

An upgrade of PostgreSQL or MySQL needs a completed backup younger than the Setting `database.upgrade.backup-max-age` (default `26h`).
If there is none, the controller makes one `Backup` named `<uid>-pre-<unix>` once. The `Backup` records the target version and the request time. Only a pre-upgrade `Backup` for the same target, requested less than `database.upgrade.backup-max-age` ago, counts. A database with `backup.enabled` set to `false` cannot upgrade (`BackupsDisabled`). `Ready` shows `BackupRequired` and the phase is `Upgrading` until the backup completes.
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

## Reasons

The condition `Ready` shows one of these reasons.

| Reason | Meaning |
|---|---|
| `DatabaseDisabled` | `database.enabled` is `false`. |
| `EngineNotAvailable` | The engine has no running operator addon. |
| `NotInProject` | The namespace is not a project namespace. |
| `CapacityExceeded` | The platform has no room for the database. |
| `CapacityUnknown` | The quota of the platform has no status yet. |
| `PlatformPending` | The platform namespace does not exist yet. |
| `Deleting` | The database is being deleted. |
| `RestoreSourceMismatch` | The live source has another engine, version or project, or more storage. |
| `RestoreSourceEmpty` | The prefix of the source has no object. |
| `RestoreTargetInFuture` | `restore.targetTime` is in the future. |
| `DatabaseNotFound` | The `databaseRef` names no database. |
| `DatabaseNotReady` | The engine of the database is not ready. |
| `InvalidSpec` | A name does not follow the rules. |
| `GrantDatabaseNotFound` | A grant names a database that is not `app` and not a ready `DatabaseSchema`. |
| `EndpointPending` | The database has no address yet. |
| `UserOwnsObjects` | The role still owns objects that cannot move. |
| `NameConflict` | Another object holds the name that the operator needs. |
| `EngineUnavailable` | The engine did not answer a command. |
| `EngineRejected` | The engine refused a command. |
| `Ready` | The object is as the spec says. |
| `NameTaken` | A Secret, SecurityGroup or Service that the operator needs exists and the object does not own it. |
| `UpgradeNotSupported` | The version of a running database changed. The operator keeps the old render. |
| `ClassChangeNotSupported` | The new class has another `highAvailability`. |
| `VersionNotAvailable` | The `DatabaseVersion` does not exist, or it is disabled for a new database. |
| `ClassNotAvailable` | The `DatabaseClass` does not exist, or it is disabled for a new database. |
| `StorageOutOfRange` | `storageGi` is outside the range of the class. |
| `SubnetNotFound` | The subnet does not exist. |
| `SubnetNotReady` | The subnet is not ready. |
| `EndpointImageMissing` | The release holds no HAProxy image. |
| `RuntimeUnavailable` | The RuntimeClass of the Setting `container.runtime-class` does not exist. |
| `DNSMissing` | The Setting `network.dns-servers` and the subnet have no DNS server. |
| `VIPRangeMissing` | `database.vip-range` is empty. |
| `BackupStorePending` | The identity of the backup store is not ready. |
| `BackupStoreFailed` | The object store refused the backup bucket or does not answer. |
| `ApplyFailed` | The API server refused an object. |
| `AddressUnavailable` | The subnet or the gate range has no free address. |
| `Provisioning` | The database is new and not ready yet. |
| `Restoring` | A restore is not ready yet. |
| `Suspended` | `spec.suspended` is `true`. |
| `Unhealthy` | The database was ready and a part is not ready now. |
| `EngineNotReady` | The cluster of the engine is not healthy. |
| `BootstrapPending` | The first setup of the engine did not run yet. |
| `GateNotReady` | The gate Deployment is not ready. |
| `EndpointNotReady` | The proxy Deployment is not ready. |
| `InstancesUnsupported` | The class asks for more instances than the engine runs. |
| `BackupUnsupported` | The engine has no backup. |
| `RestoreUnsupported` | The engine has no restore. |
| `RestoreTimeUnsupported` | The engine has no restore target time. |
| `UnsupportedForEngine` | The object does not apply to the engine. |
| `BackupsDisabled` | The upgrade of PostgreSQL or MySQL needs a backup, and `backup.enabled` is `false`. Enable backups to upgrade. The old version keeps running. |
| `UpgradeBackupFailed` | The backup before the upgrade failed. The operator waits one hour, deletes the failed `Backup` and makes one new `Backup`. |
| `ExpansionNotSupported` | The storage class cannot expand a volume. |
| `VersionNotAllowed` | The version is not an enabled upgrade target of the current one. |
| `BackupRequired` | An upgrade waits for a backup younger than `database.upgrade.backup-max-age`. |
| `Upgrading` | The upgrade runs until the engine reports the new image and is healthy. |

## Delete

The finalizer `bedrock.cloudyfolks.io/database` deletes every engine object and waits until none is left.
Deleting the project namespace does the same for each database.
Only the tombstone stays, until it expires.

## Running the operator

- The orphan sweeper runs every 10 minutes. It removes engine objects whose `Database` is gone. It is in dry-run mode until you set `database.sweeper-dry-run` to `false`.
- The operator runs SQL with `pods/exec` in `bedrock-dbaas`. A script holds a SCRAM verifier and never a password. The command line is fixed.
- Look at the objects of one database: `kubectl -n bedrock-dbaas get all,clusters.postgresql.cnpg.io -l app.kubernetes.io/instance=<uid>`.

## Import boundary

A database that existed before Bedrock can be restored from its backups.
Set `spec.restore.sourceUID` to the old UUID and set `database.backup.bucket` to the bucket that holds its prefix.
This is a generic feature. The product has no field for the import only.
The move of data is a one-time job outside the product.
