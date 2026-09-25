# Restore the control plane after an aborted upgrade

Use this runbook when `bedrock upgrade abort` during the ControlPlane phase
stops with phase `Failed` and reason `RestoreManual`. This occurs when:

- the cluster has more than one controller, or
- the automatic restore on a single controller failed.

Every change to the cluster after the backup is lost.

## Before you start

- Get root access on every controller node.
- Get the bundle of the version that ran before the upgrade, for each node
  architecture.
- Read the backup location while the API is available:

  ```sh
  kubectl get cluster cluster -o jsonpath='{.status.upgrade.backup}{"\n"}'
  ```

  The value is `host:<node>:<path>`. `<node>` is the backup node. `<path>`
  is the backup archive on that node.

## 1. Stop k0s on every controller

On each controller node:

```sh
systemctl stop k0scontroller
```

## 2. Restore the backup node

On the backup node, set the archive path and do these steps:

```sh
BACKUP=<path>
install -m 0755 /var/lib/bedrock/previous/k0s /usr/local/bin/k0s
mkdir -p /root/restore
tar -xzf "$BACKUP" -C /root/restore
mkdir -p /var/lib/k0s/.pre-restore
for dir in etcd pki manifests images helmhome; do
  [ -e "/var/lib/k0s/$dir" ] && mv "/var/lib/k0s/$dir" /var/lib/k0s/.pre-restore/
done
k0s restore --config-out /root/restore/k0s.yaml /root/restore/k0s_backup_*.tar.gz
printf '{"backup":"%s","completedAt":"%s"}\n' "$BACKUP" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > /var/lib/bedrock/restore.json
systemctl start k0scontroller
```

The file `/var/lib/bedrock/restore.json` is the same marker that the agent
writes after an automatic restore. The agent reports it in
`Host.status.restore`. The operator then ends the upgrade: it removes the
staged files, sets `desiredVersion` back and sets reason `Aborted`. Do not
skip this step. Without the marker, the restored operator continues the
upgrade from the Backup phase.

The archive also contains the OVN databases `ovnnb_db.db` and `ovnsb_db.db`.
This procedure does not use them.

## 3. Join the other controllers again

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

## 4. Check the result

```sh
kubectl get nodes
kubectl get cluster cluster
kubectl get cluster cluster -o jsonpath='{.status.conditions[?(@.type=="Progressing")].reason}{"\n"}'
```

All nodes are `Ready`. The cluster phase is `Idle` at the previous version,
and the `Progressing` reason is `Aborted`. On each controller,
`k0s version` shows the previous k0s version.
