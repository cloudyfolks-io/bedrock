# Stop the Rook operator on a host

Use this runbook before maintenance on one storage host, for example a reboot
or a disk swap, when the Rook operator must not act while the host is away.

Drain the node first. Stop the operator second. The Rook operator rewrites the
PodDisruptionBudget `rook-ceph-osd` for the node that drains. If the operator is
already stopped, the PodDisruptionBudget stays in its old state. The drain then
evicts one OSD pod and waits forever for the next one.

Do the steps for one host at a time. Start the next host only when Ceph is idle.

## Before you start

Set the names. Bedrock has no Rook toolbox, so run `ceph` in the operator pod:

```
NODE=<node name>
ceph() { kubectl -n rook-ceph exec deploy/rook-ceph-operator -- ceph --conf=/var/lib/rook/rook-ceph/rook-ceph.config "$@"; }
```

Check that the cluster is healthy and idle:

```
ceph -s
kubectl get nodes
kubectl -n rook-ceph get pdb
```

`ceph -s` must show `HEALTH_OK`, every PG `active+clean` and no recovery. All
nodes must be `Ready`. The PodDisruptionBudgets of the monitors and the OSDs
must allow at least one disruption.

Set `noout`. Do not set `norecover` or `norebalance`:

```
ceph osd set noout
```

## 1. Cordon and drain the node

Check the eviction strategy of your VMs. With `LiveMigrate`, the drain
live-migrates each VM and waits for a VM that cannot migrate. With
`LiveMigrateIfPossible`, the drain shuts down a VM that cannot migrate. See
[Node drains](../compute.md#node-drains).

```
kubectl cordon $NODE
kubectl drain $NODE --delete-emptydir-data --ignore-daemonsets --timeout=10m
```

The Rook monitor and OSD pods stay on this node. They have no other node to
move to. The drain handles them through the PodDisruptionBudgets that the
running operator keeps up to date.

If a VM stays in the node and the drain waits, find it:

```
kubectl get vmi -A -o wide | grep $NODE
kubectl get vmi -A -o json | jq -r '.items[] | select(.status.conditions[]? | select(.type=="LiveMigratable" and .status=="False")) | "\(.metadata.namespace)/\(.metadata.name)"'
```

## 2. Stop the operator

Do this only after the drain ends:

```
kubectl -n rook-ceph scale deploy/rook-ceph-operator --replicas=0
kubectl -n rook-ceph wait --for=delete pod -l app=rook-ceph-operator --timeout=60s
```

Now the operator does not replace the monitor of the node while the node is
away. The Bedrock operator reads the state of Ceph through the Rook operator
pod. It reports a storage problem while the pod does not run. This is expected.

## 3. Do the maintenance

Do the work on the host. When you reboot, first check that no RBD device stays
mapped. A reboot with a mapped device makes Ceph block the host address:

```
ssh <node> ls /sys/bus/rbd/devices/
```

The command must print nothing.

## 4. Return the node

Wait for the node, uncordon it, then start the operator and clear `noout`.
Uncordon must come before the operator, because the Rook monitor and OSD pods
are pinned to the node and cannot start on a cordoned node:

```
kubectl wait --for=condition=Ready node/$NODE --timeout=10m
kubectl uncordon $NODE
kubectl -n rook-ceph scale deploy/rook-ceph-operator --replicas=1
kubectl -n rook-ceph rollout status deploy/rook-ceph-operator --timeout=120s
ceph osd unset noout
```

If the host address is on the Ceph blocklist, remove it before you unset
`noout`:

```
ceph osd blocklist ls
ceph osd blocklist rm <address>
```

## 5. Wait until Ceph is idle

`HEALTH_OK` alone is not enough. Recovery and backfill can still run. Wait
until `ceph -s` shows no `recovery:` line, every PG `active+clean` and
`HEALTH_OK` for at least 60 seconds. Then repeat the runbook for the next host.
