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
2. Set `database.vip-range` to an IPv4 network inside the services provider network, for example `100.66.16.0/21`. The services plane is not configured if the range is outside it. The ACL of the plane does not drop the traffic to this range.
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
| `endpoint.address` | An optional IPv4 address for the customer endpoint. For a direct endpoint, the proxy pod gets this address from the project subnet. The operator rejects network, broadcast, gateway, excluded and in-use addresses. Without this field the operator picks the lowest free address in the subnet. |
| `endpoint.loadBalancerRef`, `endpoint.port` | A Fabric frontend name and TCP port for an administrator-approved migration. Set both fields with `endpoint.address` to keep a legacy IPv4 address in the load balancer plane of the project Network. |
| `allowedCIDRs` | The networks that can connect. The default is the CIDR of the subnet. |
| `backup.enabled` | The default is `true`. A Valkey database must set `false`. |
| `backup.retentionDays` | Optional, 1 to 365. The days of backups and WAL that this database keeps. It must be inside `database.backup.retention-days-min` and `-max`, or the reason is `RetentionOutOfRange` and the controller changes nothing. Without it the Setting `database.backup.retention-days` applies. It needs `backup.enabled`. A lower value removes the backups that are older than the new value. |
| `restore` | `sourceUID` and an optional `targetTime`. It cannot change after the create. |
| `suspended` | Hibernates the engine. |

The name has at most 52 characters and is a DNS label.

The `status` shows `phase`, `conditions`, `addresses` (`subnet`, `ip` and `port`), `credentialsSecret`, `instances`, `backup` (with `request`) and `stats`. For an imported frontend, `addresses` gives the customer IPv4 address and TCP port. `proxyAddress` gives the private proxy NIC address.

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
| `EndpointReady` | The proxy Deployment in the subnet is ready. For an imported frontend, the controller also checks the Fabric SecurityGroup generation, VIP, frontend and backend endpoints. |
| `BackupHealthy` | The last completed backup is not older than 36 hours. |

### Keep a legacy database frontend

Create the `Database` in its project namespace with `spec.suspended` set to `true`. Set the three endpoint fields at creation. These fields cannot change. Then create the immutable ConfigMap `database-endpoint-<loadBalancerRef>` in `bedrock-dbaas` with these keys.

| Keys | Values |
|---|---|
| `namespace`, `database`, `uid` | The target Database namespace, name and Kubernetes UID. |
| `address`, `port` | The exact legacy IPv4 address and TCP port. |
| `loadBalancerID`, `projectID`, `environment` | The source network service IDs and environment. |
| `legacyVPC`, `legacyNamespace` | The source Fabric VPC and backend namespace. An existing rule must match both values. |
| `subnetUID`, `network` | The target Subnet Kubernetes UID and its `spec.networkRef`. |
| `jobCIDR` | The IPv4 network of the migration job. The controller uses only this range for customer access during verification. |

Follow the migration procedure to freeze the source before you activate the Database. The migration command records start approval in the immutable ConfigMap `database-endpoint-<loadBalancerRef>-start` in `bedrock-dbaas`. The operator reserves the legacy VIP in the load balancer plane and sets the approved Fabric frontend to use the private proxy NIC. Verify the data and connect at the legacy address and port before you open access to customers. The migration command records open approval in `database-endpoint-<loadBalancerRef>-open`. The controller accepts both approval records only when they match the database, subnet, network and endpoint claim UIDs and names.

An imported frontend needs the two-interface managed Pod network. The proxy listens on its private project NIC and connects to the internal gate Service on its managed network. The gate NetworkPolicy accepts only the controller-managed proxy identity for this Database.

Use the paired Fabric release and CRD that report the current SecurityGroup generation. Bedrock waits for Fabric to program ingress and egress for the selected rules before it creates or retargets the imported frontend. Bedrock also checks this status before it reports the endpoint ready.

### DatabaseUser

`DatabaseUser` has these fields: `databaseRef`, `username`, `grants` and `passwordSecretRef`.
A grant has a `database` (`app` or the name of a ready `DatabaseSchema`) and a `level` (`readonly`, `readwrite` or `all`).
The user name is `spec.username`, or the name of the object when you set no `username`.
It must match `^[a-z][a-z0-9_]{1,62}$`.
These names are reserved: `postgres`, `dbadmin`, `streaming_replica`, `cnpg_pooler_pgbouncer`, names that start with `pg_` and names that end with `_ro`, `_rw` or `_all`.

The operator makes the Secret `<name>-db` with the keys `username`, `password`, `host`, `port` and `uri`.
Set the annotation `bedrock.cloudyfolks.io/reset-password` to a new value to make a new password.
`passwordSecretRef.name` names a Secret in the project with the key `password`. The operator sets this password and does not make one.
The password has 16 to 128 characters: letters, digits and `!#$%&()*+,-./:;<=>?@[]^_{|}~`. A change of the Secret changes the password within one minute. The operator never logs the password and never sends it as plain text to the engine. When `passwordSecretRef` is set, the reset-password annotation has no effect. When you remove it, the user keeps the last password.
If the role already exists in the engine (PostgreSQL, MySQL and Valkey), the reason is `NameTaken`. An administrator can set the annotation `bedrock.cloudyfolks.io/adopt` to `true`. The operator then adopts the role and sets its grants. It keeps the password of the role, unless `passwordSecretRef` is set. When it keeps the password, the key `password` of the Secret `<name>-db` is empty and the `uri` has no password. If the operator stops after it made a role and before it saved the status, the status keeps `roleCreateIntent` with the UID of the object. The next pass then knows that the operator made the role, sets the password of the Secret on it and finishes. Only the operator can write the status, so a role without this fact is never changed without the adopt annotation, whatever a project member writes to the Secret.
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
If the database already exists in the engine, the reason is `NameTaken`. An administrator can set the annotation `bedrock.cloudyfolks.io/adopt` to `true` to adopt it (PostgreSQL makes the group roles then).

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
2. A mutual TLS link from the proxy to a kube-vip address. The address comes from `database.vip-range`. The Service is `<uid>-vip` with the annotation `kube-vip.io/loadbalancerIPs`. When `database.vip-interface` is set, the Service also has the annotation `kube-vip.io/serviceInterface` with this value, and kube-vip announces the address on this interface. kube-vip reads this annotation only when it adds a Service, so the operator deletes and creates the Service again when the Setting changes.
3. A gate pod in `bedrock-dbaas`. It ends the TLS and sends plain TCP to the engine. The gate accepts only a client certificate with the common name equal to the UID of its database.

The CA of this path is the ClusterIssuer `bedrock-database-ca`. Only the addon `cnpg` makes it.
A tenant cannot make a cert-manager object.

Limits:

- The customer connects to the address in `status.addresses`. The proxy passes the connection through, and the engine offers TLS with its own certificate. A client with `sslmode=disable` connects with plain TCP.
- A change of the allowlist or a renewed certificate restarts the proxy or the gate. The connection stops for a short time.

## Backups and restore

The operator makes one bucket for all databases and one prefix `<uid>/` for each database.
The bucket name is the Setting `database.backup.bucket`.
An RGW user `database-backups-s3` owns it. Its keys are in the Secret `database-backups-s3` in `bedrock-dbaas`.
A tenant cannot read this Secret.

- The schedule is `0 0 2 * * *` with the Barman Cloud plugin.
- `database.backup.retention-days` is the number of days of backups and WAL (1 to 365, default 7).
- `status.backup` shows `items`, `lastAt` and `sizeBytes`. The operator reads the size every 10 minutes. PostgreSQL can also show a point-in-time recovery window in `pitrFrom` and `pitrTo`. MySQL restores use full backups.

To start one backup at once, set the annotation `bedrock.cloudyfolks.io/backup-requested` on the `Database` to an RFC 3339 time, for example the current time. Project editors and administrators can set it.
The operator starts one backup when the time is newer than `status.backup.request.at`. The same or an older time starts nothing. The backup is a CNPG `Backup` (PostgreSQL) or a Percona backup (MySQL) in `bedrock-dbaas`.
`status.backup.request` shows `at`, `phase` (`Pending`, `Running`, `Completed`, `Failed` or `Refused`), `backupID`, `reason` and `message`. `Pending` lasts until the engine is ready.
The request is `Refused` with the reason `BackupNotSupported` for Valkey, `BackupsDisabled` when `backup.enabled` is `false`, `InvalidBackupRequest` when the value is not an RFC 3339 time, `BackupRequestInFuture` when the time is more than 5 minutes ahead, and `BackupInProgress` when a backup of an earlier request is not finished.

To restore, set `spec.restore.sourceUID` to the UID of the source database. Add `targetTime` for a point in time.

- The source does not need to be a live object. An old UID needs a backup prefix and a tombstone that records the same project namespace. A prefix without a project ownership record is refused with `RestoreSourceMismatch`.
- A live source must have the same engine and version. Its storage must not be larger than `storageGi`. It must be in the same project.
- The restored database gets a new admin password.

When you delete a database, the operator leaves the ConfigMap `tombstone-<uid>` in `bedrock-dbaas`.
The annotation `bedrock.cloudyfolks.io/delete-after` holds the time.
For MySQL it also keeps the Secret `tombstone-<uid>` with the system passwords of the database, because a MySQL restore must start with the passwords of its source.
A MySQL restore whose source has neither a live superuser Secret nor this Secret is refused with `RestoreSourceEmpty`.
The sweeper deletes the prefix, the ConfigMap and the Secret after `database.backup.retention-after-delete`.

## Quota and capacity

- The key `count/databases.bedrock.cloudyfolks.io` of the project quota limits the number of `Database` objects. The field is `Project.spec.quota.databases`.
- The quota `databases` of `bedrock-dbaas` limits the platform. It follows `database.capacity.cpu`, `database.capacity.memory` and `database.capacity.pods`.
- A new database is refused with the reason `CapacityExceeded` when the used requests plus the new requests are above `database.max-utilization` of the quota.
  A quota without a status gives `CapacityUnknown`. The operator creates nothing before this check.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `database.enabled` | `false` | Turns the area on. |
| `database.mysql.runtime-class` | empty | RuntimeClass for new MySQL server pods. Empty uses the default runtime. |
| `database.capacity.cpu` | `32` | CPU requests of the platform namespace. |
| `database.capacity.memory` | `128Gi` | Memory requests of the platform namespace. |
| `database.capacity.pods` | `150` | Pods of the platform namespace. |
| `database.max-utilization` | `0.9` | The share of the capacity that a create can use. |
| `database.sweeper-dry-run` | `true` | The orphan sweeper only logs. Set `false` after you read the log. |
| `database.provision-timeout` | `30m` | The time after which a database that is not ready is `Failed`. |
| `database.exec-timeout` | `120s` | The time limit of one SQL script. |
| `database.vip-range` | empty | The network of the gate addresses. Empty gives `VIPRangeMissing`. |
| `database.vip-interface` | empty | The host interface on the services plane where kube-vip announces the gate addresses, for example `br-public.20`. It must exist on the control-plane hosts with an address in the services provider network. Empty announces them on the management interface. |
| `database.backup.bucket` | `bedrock-database-backups` | The bucket of the backups. |
| `database.backup.offsite-schedule` | `0 * * * *` | Cron schedule of the copy of the backup bucket to `backup.target`. See [Backup](backup.md). |
| `database.backup.retention-days` | `7` | Days of backups. |
| `database.backup.retention-days-min` | `1` | Smallest `backup.retentionDays` of a database. |
| `database.backup.retention-days-max` | `365` | Largest `backup.retentionDays` of a database. |
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

### MySQL server runtime

Set `database.mysql.runtime-class` to an installed RuntimeClass to select it for new MySQL clusters. A missing class stops provisioning with the reason `RuntimeUnavailable`. Each existing cluster keeps the runtime in its Percona object. Changing or clearing the setting does not restart those clusters. Keep their RuntimeClasses installed so that replacement server pods can start. While any cluster uses this setting, keep a Bedrock version that preserves the runtime.

The class applies to all containers in each MySQL server pod, including the backup sidecar. The HAProxy pods, tenant proxy, database gate, separate backup and restore Jobs, PostgreSQL and Valkey keep their runtime settings.

Before enabling the setting in a region, use a test cluster with real KVM access on the worker nodes. For the release `kata` class, check the eligible node labels and available capacity for every replica. Allow an additional 250 millicores and 512 MiB per MySQL server pod for this class. Confirm that the Percona cluster, StatefulSet and running server pods all specify the class and that the nodes start the configured runtime handler.

Test both MySQL 8.4 and 9.7. For each version, test access through the tenant endpoint, write a row, restart a server pod and read that row from its persistent disk. For a three-instance cluster, check that all replicas can schedule on eligible nodes and that the group remains healthy after a restart. Then run a full backup to object storage, restore to a new cluster and verify the row through the restored endpoint. Check that the sidecar and the separate backup and restore Jobs complete their work.

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

The alerts `ManagedDatabaseDiskNearlyFull`, `ManagedDatabaseDiskCritical`, `ManagedDatabasePodNotReady`, `ManagedDatabasePodRestarting` and `ManagedDatabaseValkeyMemoryHigh` ship with the base. The alerts `ManagedDatabaseReplicationLag`, `ManagedDatabaseBackupStale` and `ManagedDatabaseWalArchiveFailing` ship with the `cnpg` addon.
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
| `NameTaken` | A Secret, SecurityGroup or Service that the operator needs exists and the object does not own it. A role or logical database of that name exists in the engine and the object has no adopt annotation. |
| `UpgradeNotSupported` | The version of a running database changed. The operator keeps the old render. |
| `ClassChangeNotSupported` | The new class has another `highAvailability`. |
| `VersionNotAvailable` | The `DatabaseVersion` does not exist, or it is disabled for a new database. |
| `ClassNotAvailable` | The `DatabaseClass` does not exist, or it is disabled for a new database. |
| `StorageOutOfRange` | `storageGi` is outside the range of the class. |
| `SubnetNotFound` | The subnet does not exist. |
| `SubnetNotReady` | The subnet is not ready. |
| `EndpointImageMissing` | The release holds no HAProxy image. |
| `RuntimeUnavailable` | The RuntimeClass set for the tenant endpoint by `container.runtime-class` or for a new MySQL cluster by `database.mysql.runtime-class` does not exist. |
| `DNSMissing` | The Setting `network.dns-servers` and the subnet have no DNS server. |
| `VIPRangeMissing` | `database.vip-range` is empty. |
| `BackupStorePending` | The identity of the backup store is not ready. |
| `BackupStoreFailed` | The object store refused the backup bucket or does not answer. |
| `ApplyFailed` | The API server refused an object. |
| `RetentionOutOfRange` | `backup.retentionDays` is outside the Settings `database.backup.retention-days-min` and `-max`. |
| `BackupNotSupported` | A backup was requested for a Valkey database. |
| `InvalidBackupRequest` | The annotation `bedrock.cloudyfolks.io/backup-requested` is not an RFC 3339 time. |
| `BackupRequestInFuture` | The time of the annotation is more than 5 minutes ahead. |
| `BackupInProgress` | A backup of an earlier request is not finished. |
| `PasswordSecretNotFound` | The Secret of `passwordSecretRef` does not exist in the project. |
| `AddressUnavailable` | The subnet or the gate range has no free address. |
| `EndpointAddressInvalid` | `spec.endpoint.address` is outside the subnet, or it is the gateway, the network or broadcast address, or an excluded address. |
| `EndpointAddressTaken` | Another database, VM or container holds `spec.endpoint.address`. |
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
Before the restore, an administrator must create the ConfigMap `tombstone-<old UUID>` in `bedrock-dbaas` with `data.namespace` set to the target project namespace and `data.name` set to the old database name. This record binds the imported backup to its project. Do not set a deletion deadline until the import is complete.
This is a generic feature. The product has no field for the import only.
The move of data is a one-time job outside the product.
