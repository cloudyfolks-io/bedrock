# Alerts

Each alert of Bedrock has a `runbook_url` that points to a section of this page. The section name is the alert name.

## Ceph

The rules are in the PrometheusRule `bedrock-ceph`. Bedrock turns on the monitoring of the CephCluster, so Rook creates the ServiceMonitor for the mgr. The OSD usage limit of 40 percent keeps the loss of one OSD survivable with two OSDs on each host: the data of the lost OSD goes to the other OSD of the host, which then has about twice the usage.

### CephMetricsAbsent

Check that the pod rook-ceph-mgr runs and that the ServiceMonitor rook-ceph-mgr exists in the namespace rook-ceph. Check the target in the Prometheus targets page.

### CephHealthError

Run `ceph health detail` in the rook-ceph toolbox or with `kubectl -n rook-ceph exec` in the operator pod. Fix the first listed check. The other Ceph alerts name the most common causes.

### CephHealthWarning

Run `ceph health detail` and fix the listed checks. Muted checks do not show here.

### CephMonQuorumAtRisk

Find the monitor with `ceph quorum_status`. Check the pod rook-ceph-mon-* and its host. Do not restart other monitors until the first one is back.

### CephMonQuorumLost

Bring back the monitor hosts first. If the hosts are lost, use the Rook disaster recovery guide for the monitor quorum.

### CephOSDDown

Check the pod rook-ceph-osd-* and the disk of the host. If the disk failed, replace it. Check the usage rule CephOSDUsageAboveOneLossLimit first: a full sibling OSD makes the recovery fail.

### CephOSDNearFull

Free space or add OSDs. Find the OSDs with `ceph osd df`. Do not wait for backfillfull.

### CephOSDBackfillFull

Free space at once or add OSDs. Find the OSDs with `ceph osd df`.

### CephOSDFull

Delete data or add OSDs. If you must write at once, raise the full ratio with `ceph osd set-full-ratio` by a small step and revert it after the cleanup.

### CephOSDUsageAboveOneLossLimit

Free space or add OSDs or hosts until all OSDs are below 40 percent. The limit assumes two OSDs for each host and the default ratios. With more OSDs for each host, the safe limit is higher.

### CephPoolNearQuota

Raise the quota with `ceph osd pool set-quota <pool> max_bytes <bytes>` or delete data. Find the owner of the pool in `ceph osd pool ls detail`.

### CephPGsNotClean

Run `ceph pg dump_stuck` and `ceph health detail`. Check for down OSDs, full OSDs and a recovery that is slow.

### CephPGsNotActive

Run `ceph pg dump_stuck inactive` and `ceph health detail`. Bring back the OSDs that hold the PGs. Check the nearfull and full alerts.

### CephSlowOps

Run `ceph health detail` to find the OSDs. Check the disk and the network of those hosts. Restart the OSD only after you find the cause.

## Bedrock operator and upgrades

The operator exposes metrics on port 8080. The PodMonitor `bedrock-operator` scrapes them. The operator also exports the conditions of Cluster, Host and NodeUpgrade as the series `bedrock_condition_status`, `bedrock_cluster_phase` and `bedrock_nodeupgrade_step_failed`.

### BedrockOperatorDown

Check `kubectl -n bedrock-system get pods` and the logs of the deployment bedrock-operator. Check the PodMonitor bedrock-operator in the namespace monitoring.

### BedrockOperatorNoLeader

Read the lease with `kubectl -n bedrock-system get lease bedrock-operator -o yaml`. Check the logs of the operator for errors from the leader election. Restart the pod when the holder does not exist.

### BedrockReconcileErrors

Read the logs of the deployment bedrock-operator and filter for the controller name. Fix the cause that the log names.

### BedrockUpgradeFailed

Read `kubectl get cluster cluster -o yaml` for status.upgrade.message and status.upgrade.failedPhase. Fix the cause. Then set spec.upgrade.action to resume or abort. See the upgrade guide.

### BedrockUpgradeStuck

Read status.upgrade.message of the Cluster and the NodeUpgrade objects. Check the agent logs on the host that the message names.

### BedrockUpgradeBlocked

Read the message of the condition. Fix the preflight failure that it names. The upgrade starts again by itself.

### BedrockNodeUpgradeStepFailed

Read the message of the step in `kubectl get nodeupgrade -o yaml`. Read the journal of bedrock-agent on the node. Fix the cause. The upgrade retries or you resume it.

### BedrockNodeUpgradeStepRetrying

Read the message of the step and the journal of bedrock-agent on the node. A step that retries is not healthy even if it later passes.

## Bedrock conditions

The operator exports one series for each condition. The series `TenantLegsGuarded` exists only when `spec.tenantLegs` is set.

### BedrockClusterConditionFalse

Read the message of the condition in `kubectl get cluster cluster -o yaml`. Fix the part that the type names.

### BedrockTenantLegsUnguarded

Read the message of the condition. It lists the hosts without the guard. Check the condition ManagementApplied on those hosts.

### BedrockNodePortsUnscoped

Read the message of the condition. Check the k0s configuration and the agent logs on the hosts.

### BedrockEncryptionRotationStalled

Read status.encryption of the Cluster. Do not delete the old key. See the secrets at rest guide.

### BedrockClusterDegraded

Read the message of the condition and status.components of the Cluster. Fix the component that is Degraded.

### BedrockHostConditionFalse

Read the message in `kubectl get host <name> -o yaml`. Read the journal of bedrock-agent on the host.

## Certificates

cert-manager exposes the certificate series. The alerts do not cover the CA of k0s and the client certificates of the API server. The stock rule `KubeClientCertificateExpiration` covers the API server.

### CertManagerCertificateNotReady

Run `kubectl describe certificate` and read the events of the CertificateRequest and the Order. Check DNS and the egress of the issuer.

### CertManagerCertificateExpiringSoon

Find why cert-manager does not renew. Read the events of the Certificate. Renew by hand with `cmctl renew` when the cause is fixed.

### CertManagerCertificateExpiresInThreeDays

Renew it at once with `cmctl renew` or replace the Secret by hand. Then fix the renewal.

## Backup

The agent on each controller host writes the file `/var/lib/node_exporter/textfile_collector/bedrock_etcd_snapshot.prom`. The node exporter reads it with the textfile collector.

### BedrockEtcdSnapshotFailing

Read the journal of bedrock-agent on the host for the line `agent: etcd snapshot`. Check the free disk space of /var/lib/bedrock/backups and the state of k0s.

### BedrockEtcdSnapshotStale

Read the journal of bedrock-agent. Check the Setting backup.etcd-schedule. Take a snapshot by hand when the cause is fixed.

### BedrockEtcdSnapshotMissing

Read the journal of bedrock-agent. Check the Setting backup.etcd-schedule and the directory /var/lib/bedrock/backups.

### BedrockEtcdOffClusterUploadStale

The newest etcd snapshot that reached the backup target is older than 26 hours. The series `bedrock_backup_last_upload_timestamp_seconds` exists only when the Setting `backup.target` is set. Read the journal of `bedrock-agent` for upload errors. Check the credentials and the network to the target.

### BedrockEtcdOffClusterCopyFailing

The condition `EtcdOffClusterCopy` of a Host is False for 15 minutes. Read the message of the condition and the journal of `bedrock-agent` on the host. Check the Setting `backup.target`.

### BedrockDatabaseOffClusterCopyStale

The CronJob `database-offsite` has no successful run for more than 3 hours. The operator makes the CronJob only when the Setting `backup.target` is set. Read the logs of the newest Job in the namespace `bedrock-dbaas`. Check the Secret `database-offsite-s3` and the network to the target.

### BedrockEtcdSnapshotMetricsAbsent

Check that the file /var/lib/node_exporter/textfile_collector/bedrock_etcd_snapshot.prom exists on the controller hosts. The upload metric is in the file /var/lib/node_exporter/textfile_collector/bedrock_backup.prom. Check that the node exporter has the textfile collector and the mount.

## etcd and tenant network

### EtcdDatabaseNearQuota

The etcd database is larger than 70 percent of its quota. See [The alert EtcdDatabaseNearQuota](maintenance.md#the-alert-etcddatabasenearquota). Defragment the members by hand when the size stays high. Find the objects that fill etcd when a defragmentation does not help.

### TenantPodNetworkAudit

A tenant pod request breaks the pod network policy in audit mode. Find the pods in the audit log of the API server, in the annotation `validation.policy.admission.k8s.io/validation_failure`. Fix the pod builder. Then set `tenancy.pod-network-admission` to `deny`.

### TenantPodNetworkDenied

The pod network policy rejects tenant pods. Customer workloads can fail to start. Find the service that builds the pod without the network annotations.

### TenantPodNetworkError

The pod network policy fails to evaluate. A CEL expression does not compile or breaks on some pods. The policy fails closed.

## Fabric

The Fabric chart creates these rules. Bedrock turns on its ServiceMonitors and its PrometheusRule.

### KubeOvnControllerDown

A fabric-controller target fails to scrape for 5 minutes. Check the pod `fabric-controller` in `kube-system`. New pods and subnets are not wired while it is down.

### KubeOvnControllerAbsent

Prometheus has no fabric-controller target. Check that the pods and the Service exist and that the ServiceMonitor `fabric-controller` exists.

### KubeOvnSubnetIPExhaustion

A subnet has almost no free IP addresses. Free addresses or add a subnet. For the public pool, see [Network](network.md).

### KubeOvnOvsDown

The OVS daemon on a node is down. Check the pod `ovs-ovn` on the node. Workloads on the node lose the network.

### KubeOvnOvnControllerDown

The OVN controller on a node is down. Check the pod `ovs-ovn` on the node and its logs.

### KubeOvnApiserverUnhealthy

The CNI cannot reach the Kubernetes API server. Check the API server address and the network of the node.

### KubeOvnInternalDNSUnhealthy

The pinger cannot resolve an internal name. Check CoreDNS and the DNS Service address.

### KubeOvnOvsdbSlowRequest

OVSDB requests are slow. Check the load and the disk of the hosts that run ovn-central. Check the size of the database.

## Addons

### LokiDiscardsLogs

Loki discards log lines. Read the label `reason` of `loki_discarded_samples_total`. Raise the limits or the volume size, or reduce the log rate.

### ManagedDatabaseReplicationLag

A PostgreSQL replica is more than 30 seconds behind. Check the load, the network and the disk of the replica pod. See [Database](database.md).

### ManagedDatabaseBackupStale

A PostgreSQL cluster has no backup newer than 36 hours. Check the backup object store and the Cluster backup status. See [Database](database.md).

### ManagedDatabaseWalArchiveFailing

WAL archiving to the object store fails. The disk can fill. Check the object store and the credentials. See [Database](database.md).

### ManagedDatabaseDiskNearlyFull

A database volume is nearly full. Grow the volume or delete data. See [Database](database.md).

### ManagedDatabaseDiskCritical

A database volume is almost full. Grow the volume now. See [Database](database.md).

### ManagedDatabasePodNotReady

A database pod is not ready. Run `kubectl describe pod` in the project namespace and read the events. See [Database](database.md).

### ManagedDatabasePodRestarting

A database pod restarts again and again. Read the previous container log. See [Database](database.md).

### ManagedDatabaseValkeyMemoryHigh

A Valkey instance uses most of its memory. Raise the memory or reduce the data. See [Database](database.md).
