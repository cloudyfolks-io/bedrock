# Restore the control plane after an aborted upgrade

Use this runbook when `bedrock upgrade abort` during the ControlPlane phase
stops with phase `Failed` and reason `RestoreManual`. This occurs when:

- the cluster has more than one node (the automatic restore runs only on a
  single-node cluster),
- the automatic restore on a single node failed, or
- the API does not answer after `bedrock upgrade abort` in the ControlPlane
  phase. A failed automatic restore on a single node can leave k0s
  stopped, so the operator cannot report `RestoreManual`.

Every change to the cluster after the backup is lost. The restore also
brings back the Nodes and Hosts that you removed after the backup. Delete
them again with `kubectl delete node <name>` and
`kubectl delete host <name>`.

### The agent restored the backup, but the abort does not end

The file `/var/lib/bedrock/restore.json` on the backup node shows an
automatic restore. The abort does not end within 15 minutes, or
`journalctl -u bedrock-agent` shows `restart` failures. An agent of an
older version restores without the workload restarts.

In this case, do not do steps 1 to 3. Do steps 4 and 5 only. For step 5,
use the `bedrock` binary of the new version:
`/var/lib/bedrock/depot/<version>/<arch>/bedrock restart-workloads`.

## Before you start

- Get root access on every node.
- Get the bundle of the version that ran before the upgrade, for each node
  architecture.
- Read the backup location while the API is available:

  ```sh
  kubectl get cluster cluster -o jsonpath='{.status.upgrade.backup}{"\n"}'
  ```

  The value is `host:<node>:<path>`. `<node>` is the backup node. `<path>`
  is the backup archive on that node.
- When the API does not answer, find the path another way:
  - Look for the line `restoring host:<node>:<path>` that `bedrock upgrade
    abort` printed.
  - Or, on the controller, take the newest archive under
    `/var/lib/bedrock/backups`.
- The agent may have already moved the k0s state directories into
  `/var/lib/k0s/.pre-restore-<time>` on that node. Check there first.

## 1. Stop k0s on every controller

On each controller node:

```sh
systemctl stop k0scontroller
```

## 2. Restore the backup node

On the backup node, run this script in a child shell, with `<path>` set to
the backup archive path. A step failure then stops the script and leaves
your root shell open.

```sh
BACKUP=<path> sh -eu <<'EOF'
install -m 0755 /var/lib/bedrock/previous/k0s /usr/local/bin/k0s
mkdir -p /root/restore
tar -xzf "$BACKUP" -C /root/restore
HOLD=/var/lib/k0s/.pre-restore-$(date -u +%Y%m%dT%H%M%SZ)
mkdir -p "$HOLD"
for dir in etcd pki manifests images helmhome; do
  if [ -e "/var/lib/k0s/$dir" ]; then mv "/var/lib/k0s/$dir" "$HOLD/"; fi
done
k0s restore --config-out /root/restore/k0s.yaml /root/restore/k0s_backup_*.tar.gz
printf '{"backup":"%s","completedAt":"%s"}\n' "$BACKUP" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > /var/lib/bedrock/restore.json
systemctl start k0scontroller
EOF
```

The file `/var/lib/bedrock/restore.json` is the same marker that the agent
writes after an automatic restore. The agent reports it in
`Host.status.restore`. The operator then ends the upgrade: it removes the
staged files and the `/var/lib/k0s/.pre-restore-*` directories, sets
`desiredVersion` back and sets reason `Aborted`. Do not skip this step.
Without the marker, the restored operator continues the upgrade from the
Backup phase.

The archive also contains the OVN databases `ovnnb_db.db` and `ovnsb_db.db`.
This procedure does not use them.

## 3. Join the other controllers again

Do this step only when the cluster has more than one controller.

On each other controller, remove its state:

```sh
k0s reset
reboot
```

On the backup node, make a join token:

```sh
bedrock token create --roles control-plane
```

On each other controller, join with the token and the bundle of the
previous version:

```sh
bedrock join --token <token> --bundle <bundle of the previous version>
```

## 4. Restart the kubelet on every worker

The kubelet on a worker kept its cache while etcd returned to the backup.
It does not see the pods that the restore brought back. On each node that
is not a controller:

```sh
systemctl restart k0sworker
```

The pods on the node continue to run.

## 5. Restart the platform workloads

The pods kept their caches while etcd returned to the backup. For example,
the CNI does not see a restored pod, so that pod gets no network. On the
backup node:

```sh
bedrock restart-workloads
```

The command waits for the API. Then it restarts the Deployments,
DaemonSets and StatefulSets in the platform namespaces. The platform
namespaces are `kube-system` and the namespaces with the label
`bedrock.cloudyfolks.io/component`. The command does not restart:

- the workloads in other namespaces, for example tenant workloads,
- the `OnDelete` sets, for example the OVS data plane,
- the Ceph daemons (label `ceph_daemon_type`),
- `ovs-ovn`, `ovs-ovn-dpdk`, `ovn-central` and `kube-vip`.

The automatic restore does the same. The restarts cause short effects:

- Traefik closes long connections.
- The nmstate handler applies its policies again.
- A VM migration that runs at this time fails. Running VMs continue.

When the command shows a failure, run it again.

## 6. Check the result

```sh
kubectl get nodes
kubectl get cluster cluster
kubectl get cluster cluster -o jsonpath='{.status.conditions[?(@.type=="Progressing")].reason}{"\n"}'
```

All nodes are `Ready`. The cluster phase is `Idle` at the previous version,
and the `Progressing` reason is `Aborted`. On each controller,
`k0s version` shows the previous k0s version.
