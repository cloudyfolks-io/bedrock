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
  is the backup archive on that node. The archive is sealed, and `<path>`
  ends in `.tar.gz.age`. An archive of an older release is not sealed, and
  `<path>` ends in `.tar.gz`.
- When the API does not answer, find the path another way:
  - Look for the line `restoring host:<node>:<path>` that `bedrock upgrade
    abort` printed.
  - Or, on the controller, take the newest `bedrock-*` archive under
    `/var/lib/bedrock/backups`. The upgrade writes the `bedrock-*`
    archives. Do not take an `etcd-*` file. It is a scheduled snapshot. It
    holds no images, and it can be newer than the upgrade backup.
- If you rotated the Secrets key after the time of the archive, the restored
  etcd holds Secrets that are encrypted with the old key. Before step 2, on
  each controller, put the key of that time at
  `/etc/bedrock/encryption/encryption.yaml`. See [Restore an older
  snapshot](../secrets-at-rest.md#restore-an-older-snapshot).
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

Use the script that fits the archive. A sealed archive ends in
`.tar.gz.age`. A plain archive of an older release ends in `.tar.gz`.

For a sealed archive, use this script. It opens the archive with the host key
`/etc/bedrock/backup/host.key` and the retired host keys. If the host key is
missing, copy the admin
identity to the node. Then add `--identity <key file>` to the `bedrock backup
decrypt` line.

```sh
BACKUP=<path> sh -eu <<'EOF'
install -m 0755 /var/lib/bedrock/previous/k0s /usr/local/bin/k0s
mkdir -p /root/restore
bedrock backup decrypt --in "$BACKUP" --out /root/restore/backup.tar.gz
tar -xzf /root/restore/backup.tar.gz -C /root/restore
set -- /root/restore/k0s_backup_*.tar.gz
[ "$#" -eq 1 ] && [ -e "$1" ] || { echo "error: one k0s archive must be in /root/restore" >&2; exit 1; }
ENTRIES=$(tar -tzf "$1")
DIRS="etcd pki manifests helmhome"
if printf '%s\n' "$ENTRIES" | grep -Eq '^(\./)?images(/|$)'; then DIRS="$DIRS images"; fi
HOLD=/var/lib/k0s/.pre-restore-$(date -u +%Y%m%dT%H%M%SZ)
mkdir -p "$HOLD"
for dir in $DIRS; do
  if [ -e "/var/lib/k0s/$dir" ]; then mv "/var/lib/k0s/$dir" "$HOLD/"; fi
done
k0s restore --config-out /root/restore/k0s.yaml "$1"
printf '{"backup":"%s","completedAt":"%s"}\n' "$BACKUP" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > /var/lib/bedrock/restore.json
if [ ! -e /var/lib/k0s/containerd ]; then
  mkdir -p /var/lib/k0s/images
  for FILE in /var/lib/bedrock/images/*/*/*.tar; do
    [ -e "$FILE" ] || continue
    VERSION=$(basename "$(dirname "$(dirname "$FILE")")")
    ln -f "$FILE" "/var/lib/k0s/images/$VERSION-$(basename "$FILE")" || cp "$FILE" "/var/lib/k0s/images/$VERSION-$(basename "$FILE")"
  done
fi
systemctl start k0scontroller
EOF
```

For a plain archive, use this script. It has no decrypt line.

```sh
BACKUP=<path> sh -eu <<'EOF'
install -m 0755 /var/lib/bedrock/previous/k0s /usr/local/bin/k0s
mkdir -p /root/restore
tar -xzf "$BACKUP" -C /root/restore
set -- /root/restore/k0s_backup_*.tar.gz
[ "$#" -eq 1 ] && [ -e "$1" ] || { echo "error: one k0s archive must be in /root/restore" >&2; exit 1; }
ENTRIES=$(tar -tzf "$1")
DIRS="etcd pki manifests helmhome"
if printf '%s\n' "$ENTRIES" | grep -Eq '^(\./)?images(/|$)'; then DIRS="$DIRS images"; fi
HOLD=/var/lib/k0s/.pre-restore-$(date -u +%Y%m%dT%H%M%SZ)
mkdir -p "$HOLD"
for dir in $DIRS; do
  if [ -e "/var/lib/k0s/$dir" ]; then mv "/var/lib/k0s/$dir" "$HOLD/"; fi
done
k0s restore --config-out /root/restore/k0s.yaml "$1"
printf '{"backup":"%s","completedAt":"%s"}\n' "$BACKUP" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > /var/lib/bedrock/restore.json
if [ ! -e /var/lib/k0s/containerd ]; then
  mkdir -p /var/lib/k0s/images
  for FILE in /var/lib/bedrock/images/*/*/*.tar; do
    [ -e "$FILE" ] || continue
    VERSION=$(basename "$(dirname "$(dirname "$FILE")")")
    ln -f "$FILE" "/var/lib/k0s/images/$VERSION-$(basename "$FILE")" || cp "$FILE" "/var/lib/k0s/images/$VERSION-$(basename "$FILE")"
  done
fi
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

After a restore, Kubernetes can delete and start again some pods on the
node, also VM pods. The restored Node object has the state of the snapshot
time, and the taint manager can evict the pods that do not tolerate a
`NoExecute` taint. Pods of a Deployment or a StatefulSet start again. A VM
that cannot migrate restarts. A bare pod with no controller is gone. Plan a
restore as an outage of the workloads on the node.

The archive also contains the OVN databases `ovnnb_db.db` and `ovnsb_db.db`.
This procedure does not use them.

A scheduled etcd snapshot (`etcd-*`) holds no image tarballs. The script
keeps the directory `/var/lib/k0s/images` on the host. On a rebuilt host,
put the images of the bundle back before you run the script. See
[Etcd snapshots](../backup.md#etcd-snapshots).

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

Then delete the plain files. The directory `/root/restore` holds the keys of
the cluster in plain text. If you copied an admin identity to a node, delete
that copy too.

```sh
rm -rf /root/restore
```
