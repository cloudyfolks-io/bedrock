# Maintenance

This page describes the work that Bedrock does on the control-plane hosts by
itself and the runbooks for work that an admin does by hand.

## Etcd defragmentation

Etcd keeps freed pages in its database file. The file grows, but the data
does not. A file near the quota of 2 GiB makes etcd refuse writes. A large
file also slows the leader election and the Helm installs.

The agent on each control-plane host defragments the local etcd member. The
rules are:

- The run is weekly, on Sunday, in UTC.
- The start time is 03:00 plus 15 minutes times the controller index modulo 4.
  The controller index is the position of the host name in the sorted list of
  the control-plane hosts. The first host runs at 03:00, the second at 03:15,
  the third at 03:30, the fourth at 03:45, the fifth at 03:00 again. The members
  never defragment together when the cluster has up to four controllers. A
  cluster with more than four controllers has members with the same slot.
- Each run handles one member. The agent defragments only the member on its own
  host, through `https://127.0.0.1:2379`. It uses the files
  `apiserver-etcd-client.crt`, `apiserver-etcd-client.key` and `etcd/ca.crt`
  under `/var/lib/k0s/pki`.
- The agent runs only inside the slot of its own member, in the current week.
  The slot lasts 15 minutes. A missed slot does not run later. It waits for the
  next week. A change of the clock never gives a run outside the slot.
- The agent checks that the member is healthy before the defragmentation. When
  the member is not healthy, the agent writes the error to the journal and skips
  this week. It does not try again in the same slot. The agent checks the
  health again after the defragmentation and reports an error when the member
  is not healthy.
- The agent skips the run when the database file is less than 268435456 bytes
  (256 MiB) larger than the data in it.
- The agent saves the start of the last slot it handled in
  `/var/lib/bedrock/etcd-defrag`. A run, a skip and a failed health check all
  count as handled.
- On a control plane with one member, the defragmentation blocks the API for
  its duration, because no other member serves requests. Plan the Sunday slot
  for a quiet time.

The agent writes `etcd member defragmented` to the journal of `bedrock-agent`
after a defragmentation. Find the line on a controller:

```
sudo journalctl -u bedrock-agent --since "7 days ago" | grep defrag
```

Child clusters have their own defragmentation in k0smotron. Bedrock does not
change it.

### The alert EtcdDatabaseNearQuota

The PrometheusRule `bedrock-etcd` in the namespace `monitoring` holds the alert
`EtcdDatabaseNearQuota`. It fires when the database is larger than 1503238554
bytes, which is 70 percent of the 2 GiB quota, for 15 minutes. Bedrock does not
set a quota, so etcd uses its default of 2147483648 bytes. The k0s etcd listens
only on `127.0.0.1`, so Prometheus reads the size from the API server series
`apiserver_storage_size_bytes`. The alert also accepts the etcd series
`etcd_mvcc_db_total_size_in_bytes`.

When the alert fires, find the cause. A defragmentation does not help when the
data itself is large. The agent skips a member when the free space in the file
is less than 256 MiB. In that case, find the objects that fill etcd.

## Descheduler

The addon `descheduler` moves VMs from a busy host to a quiet host. See
[Descheduler](addons.md#descheduler). The addon is off by default. An adopted
cluster turns it on with `spec.addons.descheduler: true` in the cluster
configuration. The addon needs the KubeVirt eviction strategy `LiveMigrate` or
`LiveMigrateIfPossible`, as the Setting `virt.eviction-strategy` describes in
[Node drains](compute.md#node-drains).

## Runbooks

- [Stop the Rook operator on a host](runbooks/stop-rook-operator.md)
- [Replace an etcd member](runbooks/replace-etcd-member.md)
- [Restore the control plane after an aborted upgrade](runbooks/restore-control-plane.md)
