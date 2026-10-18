# Adopt an existing Ceph cluster

Use this runbook to move a Rook Ceph cluster from an old k0s cluster to a new Bedrock cluster.
The Ceph data stays on the OSD disks. The cluster keeps its fsid, its monitors, its OSDs and its data.

Adoption has these properties:

- The OS, `/var/lib/rook` and the OSD disks stay on the hosts.
- Bedrock writes the Rook identity (the Secret and the ConfigMap of the monitors) before it creates the CephCluster.
- The CephCluster uses the host network, as the old cluster does.
- Bedrock renames the pools, the CephFS file system and the RGW realm to its own names, once.
- Bedrock never wipes a disk.

This runbook does not move Kubernetes objects. The PVs, PVCs and VMs of the old cluster are not in the OSD disks.
The import of the old RBD images into Bedrock volumes is a later step.

Rehearse the whole procedure in the lab first. See "Rehearse in the lab" at the end.

## What changes

| Old name | Bedrock name |
| --- | --- |
| block pool (`adopt.pools.block`) | `bedrock-block` |
| CephFS (`adopt.pools.filesystem`) | `bedrock-fs` |
| CephFS metadata pool | `bedrock-fs-metadata` |
| CephFS data pools | `bedrock-fs-data0`, `bedrock-fs-data1`, and so on |
| RGW realm, zonegroup and zone (`adopt.pools.objectStore`) | `bedrock-rgw` |
| RGW pools `<old>.rgw.*` | `bedrock-rgw.rgw.*` |

Bedrock creates its own StorageClasses `block`, `block-retain`, `filesystem` and `bucket`.
It does not read the old StorageClasses.

## Requirements

- Every old Ceph daemon runs the same Ceph major release as the bundle, and at least v20.2.1.
  Read the release of the bundle in the `rook` component of `release/components.yaml`.
- The old cluster shows `HEALTH_OK` and all PGs are `active+clean`.
- Every adopted pool has a size that is not larger than `storage.replicas`.
  Adoption refuses the other case, because Rook would shrink the pool.
- The old cluster uses the host network. The monitor addresses are node addresses.
  The same node addresses must exist on the new hosts.
- Every host name is a lowercase DNS-1123 label (no dot). The host name must equal the node name of the old cluster.
- Each host has at least 100 GiB on the system disk. The mon store is on this disk.
  The bundle and its images use about 50 GiB during the install.
  Kubelet evicts pods when less than 10% of the disk is free, and Ceph raises `MON_DISK_LOW` when less than 30% is free on the disk of a monitor.
  A disk with 60 GiB showed both in the lab, and `StorageReady` stayed `False`.
- The new object store listens on port 80 of the host network on every host that runs a gateway. Port 80 is free on these hosts.
- You have root access on every host, a copy of the bundle, and a backup of the old k0s state.
- You have a maintenance window. The old cluster stops for the whole procedure.

## 1. Record the old cluster

Do these steps on the old cluster. They only read.
Run the `ceph` commands in the Rook toolbox. Define this function first:

```sh
ceph() { kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph "$@"; }
```

Save the output of every command in a safe place outside the hosts.
Some outputs hold keys. Do not put them in a ticket or in a log.

1. Read the Ceph versions. Every line must show the release of the bundle.

   ```sh
   ceph versions
   ```

2. Read the health, the pools and their sizes.

   ```sh
   ceph health detail
   ceph osd pool ls detail
   ```

3. Read the fsid, the monitor ids and the monitor addresses.

   ```sh
   ceph fsid
   kubectl -n rook-ceph get configmap rook-ceph-mon-endpoints -o jsonpath='{.data.data}{"\n"}{.data.mapping}{"\n"}'
   ```

4. Read the OSD tree. Note the host bucket names. They must equal the node names.

   ```sh
   ceph osd tree
   ```

5. Find out whether the OSDs are raw or LVM. On each host:

   ```sh
   lsblk -f
   ```

   A raw OSD disk shows `ceph_bluestore`. An LVM OSD disk shows `LVM2_member`.

6. Write down the stable path of every OSD disk, for every host.

   ```sh
   ls -l /dev/disk/by-id/
   lsblk -o NAME,SIZE,TYPE,WWN,SERIAL,FSTYPE
   ```

   Use a path under `/dev/disk/by-id/`. A reinstall can reorder `/dev/sdX`.

7. Read the CephFS and the RGW settings.

   ```sh
   ceph fs dump
   kubectl -n rook-ceph exec deploy/rook-ceph-tools -- radosgw-admin realm list
   kubectl -n rook-ceph exec deploy/rook-ceph-tools -- radosgw-admin zonegroup get --rgw-zonegroup=<objectstore name>
   kubectl -n rook-ceph exec deploy/rook-ceph-tools -- radosgw-admin zone get --rgw-zone=<objectstore name>
   ```

   The zone output holds the system key of the zone. Keep it secret.

8. Record the data. Section 7, step 4 compares the cluster with these records.

   ```sh
   ceph df
   kubectl -n rook-ceph exec deploy/rook-ceph-tools -- rbd ls -l <block pool>
   kubectl -n rook-ceph exec deploy/rook-ceph-tools -- radosgw-admin bucket list --rgw-zone=<objectstore name> --rgw-zonegroup=<objectstore name>
   kubectl -n rook-ceph exec deploy/rook-ceph-tools -- radosgw-admin bucket stats --rgw-zone=<objectstore name> --rgw-zonegroup=<objectstore name>
   ```

   Run `rbd ls -l` for every RBD pool. It lists the images and their sizes.
   The `bucket stats` output shows the object count and the size of each bucket.

   Record the CephFS tree on a mount host that is not an adopt host. Do not install packages on an adopt host.
   Make the secret file on a control-plane host of the old cluster and copy only this file to the mount host:

   ```sh
   umask 077
   ceph auth get-key client.admin > /root/admin.secret
   scp /root/admin.secret <mount host>:/root/admin.secret
   rm -f /root/admin.secret
   ```

   The mount host needs the Ceph kernel client and the `mount.ceph` helper. Run these commands on the mount host:

   ```sh
   apt-get install -y ceph-common
   mkdir -p /mnt/cephfs
   mount -t ceph <mon address>:6789:/ /mnt/cephfs -o name=admin,secretfile=/root/admin.secret,mds_namespace=<CephFS name>
   ls -lR /mnt/cephfs/<path>
   ```

   List a few paths that you know. Keep the mount until section 4, step 2.

9. Save the Kubernetes objects of the old cluster.

   ```sh
   kubectl get pv,pvc,volumesnapshotcontent -A -o yaml > pv-pvc.yaml
   kubectl -n rook-ceph get cephcluster -o yaml > cephcluster.yaml
   ```

10. Make the backup of the old k0s state on every controller.

    ```sh
    k0s backup --save-path /root/k0s-backup
    ```

    Copy the archive off the host. The rollback needs it.

11. Record the cephx users and their caps. Do not save the keys.

    ```sh
    ceph auth ls -f json | jq '.auth_dump |= map(del(.key))' > cephx-users.json
    jq -r '.auth_dump[] | select(any(.caps[]; test("pool=|fsname="))) | [.entity, (.caps | tostring)] | @tsv' cephx-users.json
    ```

    Run `jq` on the host that runs `kubectl`.
    The first command writes the users and their caps without the keys.
    The second command lists each user that has a cap with `pool=` or `fsname=`.
    Ceph matches these caps by name. After the rename, the old names match no pool and no file system.
    A cap with only `namespace=` or `path=` does not change.
    Keep the list. Section 7, step 5 changes these caps.

## 2. Export the Rook identity

### The key file

The key file holds the keys of the old cluster. It has plain (decoded) values.

1. Make the file with mode 0600 on a host that has `kubectl` access to the old cluster:

   ```sh
   umask 077
   kubectl -n rook-ceph get secret rook-ceph-mon -o go-template='{{range $k, $v := .data}}{{$k}}: {{$v | base64decode}}{{"\n"}}{{end}}' | grep -E '^(fsid|mon-secret|ceph-username|ceph-secret): ' > ceph-keys.yaml
   ```

   The `grep` keeps only `fsid`, `mon-secret`, `ceph-username` and `ceph-secret`.

2. Check the file. It must have four lines and mode 0600.

   ```sh
   wc -l ceph-keys.yaml
   stat -c '%a' ceph-keys.yaml
   ```

3. Copy the file to the first adopt host, for example to `/root/ceph-keys.yaml`.
   Keep mode 0600 and the owner root.

`bedrock init` refuses a key file with another mode, a missing key, or an fsid that differs from `storage.adopt.fsid`.
The error text never shows a key.

### The ConfigMap

Save the ConfigMap `rook-ceph-mon-endpoints` for the record.
Bedrock builds a new ConfigMap from `adopt.monitors`.

```sh
kubectl -n rook-ceph get configmap rook-ceph-mon-endpoints -o yaml > rook-ceph-mon-endpoints.yaml
```

## 3. Write the cluster configuration

Write one `cluster.yaml` and use the same file on every adopt host.
`bedrock init` and `bedrock join --adopt-config` both read it.

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ClusterConfig
metadata:
  name: cluster
spec:
  version: <bundle version>
  api:
    vip: <free address in the management network>
  platform:
    host: <platform domain>
  network:
    managementInterface: <interface that carries the node address>
  storage:
    replicas: 3
    adopt:
      fsid: <fsid>
      keyFile: /root/ceph-keys.yaml
      monitors:
        - {id: a, node: node-1, address: <address of mon a>}
        - {id: b, node: node-2, address: <address of mon b>}
        - {id: c, node: node-3, address: <address of mon c>}
      hosts:
        - node: node-1
          devices: [/dev/disk/by-id/<disk>, /dev/disk/by-id/<disk>]
        - node: node-2
          devices: [/dev/disk/by-id/<disk>]
        - node: node-3
          devices: [/dev/disk/by-id/<disk>]
      pools:
        block: <old block pool>
        filesystem: <old CephFS>
        objectStore: <old object store>
      cephx:
        daemon:
          keyType: aes256k
          keyGeneration: 2
  roles: [control-plane, ceph-osd, fabric-gateway, workload]
```

Rules for the file:

- `monitors` lists 1, 3 or 5 monitors. The ids are lowercase letters. The addresses are IPv4 addresses.
- Every monitor node is in `hosts`.
- `hosts[].devices` lists every OSD disk of the host by `/dev/disk/by-id/` path.
  `storage.devices` must stay empty.
- `replicas` must be at least 3 when there are 3 or more hosts. It must not be smaller than any adopted pool.
- Set a name in `pools` only for a kind that exists. An empty name skips that kind.
- Copy the whole `security.cephx` block of the old CephCluster into `adopt.cephx`.
  Rook then keeps the daemon keys and does not rotate them again.

## 4. Stop the old cluster

This step stops all workloads of the old cluster.
Do the steps in this order.

1. Stop the clients of Ceph. Stop the VMs, unmap the RBD images, and stop every pod and host that mounts CephFS.

2. Record the checksums of the data while the clients are stopped.
   Choose a few RBD images and a few CephFS files that you know.

   ```sh
   kubectl -n rook-ceph exec deploy/rook-ceph-tools -- sh -c 'rbd export --no-progress <pool>/<image> - | sha256sum'
   sha256sum /mnt/cephfs/<path>/<file>
   umount /mnt/cephfs
   rm -f /root/admin.secret
   ```

   Run the first command for each chosen image and the second for each chosen file.
   Run the `umount` and `rm` commands on the host with the mount.

3. Set `noout` and stop Rook. The Ceph daemons stop cleanly.

   ```sh
   ceph osd set noout
   kubectl -n rook-ceph scale deployment/rook-ceph-operator --replicas=0
   kubectl -n rook-ceph scale deployment --all --replicas=0
   kubectl -n rook-ceph get pods --field-selector=status.phase=Running
   ```

   Wait until the last command lists no pod.
   A pod with the status `Terminating` is still listed.

4. Copy `/var/lib/rook` off every host. Do this before any other change to the host.
   The mon stores are on the system disk. A host rebuild wipes them.

   ```sh
   tar --numeric-owner --xattrs --acls -C / -cpf /root/rook-$(hostname).tar var/lib/rook
   sha256sum /root/rook-$(hostname).tar
   ```

   Copy the archive off the host, for example with `scp`. Check the copy:

   ```sh
   sha256sum rook-<host>.tar
   tar -tvf rook-<host>.tar | grep -E 'store\.db/CURRENT|keyring'
   ```

   The checksum must equal the checksum on the host.
   The listing must show the owner and mode of the mon store and of the keyrings.
   Every host that runs a monitor must show its `store.db`.

5. Stop k0s on every host. Do the workers first and the controllers last.
   `k0s reset` removes the Kubernetes state of the old cluster. It keeps `/var/lib/rook` and the disks.

   ```sh
   k0s stop
   k0s reset
   ```

   `k0s reset` can print the line `To ensure a full reset, a node reboot is recommended.`
   The lab restarted every host after `k0s reset`, because its `snapshot` stage stops and starts the VMs.
   The only lab run without a restart stopped at `RenameFailed`. The cause is fixed, but no run proved the adoption without a restart.
   Restart every host after `k0s reset`.

6. Check every host. The preflight of Bedrock refuses a busy port 3300 or 6789 on a fresh node.

   ```sh
   ss -ltn | grep -E ':(3300|6789)[[:space:]]'
   ps -eo comm= | grep -E '^(ceph-(mon|osd|mgr|mds)|radosgw)$'
   ls -d /var/lib/rook/mon-*/data/store.db
   ```

   The first two commands must print nothing. The third command must list the mon stores of a host that runs a monitor.
   Stop the old mon and OSD containers if a command shows them.
   Check that no process holds an OSD disk:

   ```sh
   find /proc/[0-9]*/fd -lname "$(readlink -f /dev/disk/by-id/<disk>)" -print -quit
   ```

7. Prepare every host.

   - If you rebuild the host OS, put the copy of `/var/lib/rook` back before `bedrock init` or `bedrock join`:

     ```sh
     tar --numeric-owner --xattrs --acls -C / -xpf rook-<host>.tar
     ```

   - Check that the host name equals the node name in `cluster.yaml`:

     ```sh
     hostname
     ```

   - Check that the node address of the management interface equals the monitor address of this host:

     ```sh
     ip -4 -o addr show dev <management interface>
     ```

   - Install `lvm2` on each host that has LVM-mode OSDs. The preflight of Bedrock needs it to read the label.

     ```sh
     apt-get install -y lvm2
     ```

## 5. Install Bedrock

1. On the first adopt host, run `bedrock init`:

   ```sh
   bedrock init -f cluster.yaml --bundle <bundle>
   ```

   The preflight checks the key file, the disk labels and the mon stores.
   It refuses a disk with a Ceph label of another fsid.
   It refuses a disk that has another signature and no Ceph label.
   Nothing wipes a disk in any case.

2. Make a join token. The roles must include `ceph-osd` for every adopt host.

   ```sh
   bedrock token create --roles ceph-osd,workload
   ```

3. On every other adopt host, run `bedrock join` with the same `cluster.yaml` as `bedrock init`:

   ```sh
   bedrock join --token <token> --bundle <bundle> --adopt-config cluster.yaml
   ```

   The Host objects of the joined nodes receive the devices of `adopt.hosts`.
   Bedrock does not render the CephCluster until every host of a monitor has joined.

## 6. Watch the adoption

The Cluster condition `StorageAdopted` shows the phase in its reason.
The condition `StorageReady` shows the same phase until the adoption ends.
In the lab (3 hosts, 6 OSDs), `Adopted` came 30 to 60 minutes after the last join.
`WaitingForHosts` can last several minutes after the last host has joined.
Act only when a reason does not change for 30 minutes.

```sh
export KUBECONFIG=/var/lib/k0s/pki/admin.conf
kubectl get cluster cluster -o jsonpath='{range .status.conditions[*]}{.type}{"\t"}{.status}{"\t"}{.reason}{"\t"}{.message}{"\n"}{end}'
```

| Reason | Meaning | What to do |
| --- | --- | --- |
| `WaitingForIdentity` | The Secret `rook-ceph/rook-ceph-mon` does not exist, or it has another fsid. | Check the key file and `storage.adopt.fsid`. Run `bedrock init` again with the right file. |
| `WaitingForHosts` | A host of a monitor has no `ceph-osd` role or no devices. | Join the host with `--roles ceph-osd,workload` and `--adopt-config`. Check `kubectl get host`. |
| `WaitingForQuorum` | The CephCluster exists. The monitors are not all in the quorum, or Bedrock cannot read the Ceph state. The message `Error initializing cluster client ... conf_read_file` is normal until the Rook operator writes its config file. | Check the mon pods and the mon stores. See "Monitors do not form a quorum". |
| `WaitingForOSDs` | Fewer OSDs are up than `adopt.hosts[].devices` lists. | Check the OSD pods and the logs of the `rook-ceph-osd-prepare` jobs. Check the disk paths. |
| `Renaming` | Bedrock ran the rename commands. The next check shows the result. | Wait. |
| `PoolSizeTooSmall` | An adopted pool is larger than `storage.replicas`. Bedrock stops before it runs a rename command. | Raise the setting: `kubectl patch setting storage.replicas --type=merge -p '{"spec":{"value":"3"}}'`. Use at least the pool size. Never lower a pool. |
| `RenameFailed` | A rename command failed, or a name was not found. The message names the command. | Fix the cause. See "Rename by hand". |
| `Adopted` | The pools, the CephFS and the RGW realm have the Bedrock names. Bedrock renders the pools and the classes. | Do "After the adoption". |

If you change `adopt.pools` after an error, edit the Cluster object: `kubectl edit cluster cluster`.

### Run Ceph commands in the new cluster

Bedrock has no Rook toolbox. The pod `rook-ceph-operator` holds `ceph`, `rbd` and `radosgw-admin`.
Define these functions on a control-plane host. They use bash function names:

```sh
ceph() { kubectl -n rook-ceph exec deploy/rook-ceph-operator -- ceph --conf=/var/lib/rook/rook-ceph/rook-ceph.config "$@"; }
rbd() { kubectl -n rook-ceph exec deploy/rook-ceph-operator -- rbd --conf=/var/lib/rook/rook-ceph/rook-ceph.config "$@"; }
radosgw-admin() { kubectl -n rook-ceph exec deploy/rook-ceph-operator -- radosgw-admin --conf=/var/lib/rook/rook-ceph/rook-ceph.config "$@"; }
```

### Monitors do not form a quorum

1. Check the mon pods and the host addresses.

   ```sh
   kubectl -n rook-ceph get pods -l app=rook-ceph-mon -o wide
   ```

2. Check the mon addresses in `adopt.monitors`. They must equal the node addresses.
3. Check that every mon store exists in `/var/lib/rook/mon-<id>/data/store.db` with the owner of the copy.
4. If one mon store is lost, keep the stores of the other monitors and use the Rook procedure `kubectl rook-ceph mons restore-quorum`.

### Rename by hand

Do this only after a `RenameFailed` that you cannot fix in another way.
Bedrock checks the state before each step, so it continues after your steps.

Use the functions of "Run Ceph commands in the new cluster". Read the state first:

```sh
ceph osd pool ls detail
ceph fs ls
radosgw-admin realm list
```

Block pool:

```sh
ceph osd pool rename <old block pool> bedrock-block
```

CephFS. Use the pool names that `ceph fs ls` shows:

```sh
ceph fs fail <old CephFS>
ceph fs set <old CephFS> refuse_client_session true
ceph fs rename <old CephFS> bedrock-fs --yes-i-really-mean-it
ceph osd pool rename <old metadata pool> bedrock-fs-metadata
ceph osd pool rename <old data pool> bedrock-fs-data0
ceph fs set bedrock-fs joinable true
ceph fs set bedrock-fs refuse_client_session false
```

RGW. Replace `<old>` with the old name of the realm, the zonegroup and the zone:

```sh
radosgw-admin realm rename --rgw-realm=<old> --realm-new-name=bedrock-rgw
radosgw-admin zonegroup rename --rgw-zonegroup=<old> --zonegroup-new-name=bedrock-rgw
radosgw-admin zone rename --rgw-zone=<old> --zone-new-name=bedrock-rgw --rgw-zonegroup=bedrock-rgw
```

Rewrite the pool names in the zone. The command keeps the zone output in the pod and prints nothing:

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-operator -- sh -c 'set -e; conf=--conf=/var/lib/rook/rook-ceph/rook-ceph.config; zone=$(radosgw-admin "$conf" zone get --rgw-zone=bedrock-rgw); printf "%s" "$zone" | sed "s/\"<old>\\.rgw\\./\"bedrock-rgw.rgw./g" | radosgw-admin "$conf" zone set --rgw-zone=bedrock-rgw --infile=/dev/stdin >/dev/null'
```

Rename the RGW pools:

```sh
for pool in $(ceph osd pool ls | grep '^<old>\.rgw\.'); do ceph osd pool rename "$pool" "bedrock-rgw.rgw.${pool#<old>.rgw.}"; done
```

Commit the period last, after the last pool rename.
The commit opens the zone pools under their new names.
It creates an empty pool for each pool that still has the old name, and the rename of that pool then fails.

```sh
kubectl -n rook-ceph exec deploy/rook-ceph-operator -- sh -c 'radosgw-admin --conf=/var/lib/rook/rook-ceph/rook-ceph.config period update --commit --rgw-realm=bedrock-rgw >/dev/null'
```

## 7. After the adoption

1. Delete the key file when the cluster shows `StorageAdopted=True`.
   Delete every other copy of the file.
   Do not delete the key file while the reason is `RenameFailed` or any reason before `Adopted`.
   The command deletes the file only when the wait succeeds.

   ```sh
   kubectl wait --for=condition=StorageAdopted cluster/cluster --timeout=60m && rm -f /root/ceph-keys.yaml
   ```

2. Unset `noout`. All OSDs are up. The flag keeps the health at `HEALTH_WARN`.

   ```sh
   ceph osd unset noout
   ```

   Use the `ceph` function of "Run Ceph commands in the new cluster".

3. Wait for the storage gate. The condition `StorageReady` must become `True`.

   ```sh
   kubectl wait --for=condition=StorageReady cluster/cluster --timeout=60m
   ```

4. Check your data. Compare each item with the records of section 1, step 8 and section 4, step 2.

   | Comparison | Command | It must show |
   | --- | --- | --- |
   | fsid | `ceph fsid` | The recorded fsid. |
   | OSD ids | `ceph osd tree` | The same OSD ids on the same hosts. |
   | Pools | `ceph osd pool ls detail` | The Bedrock pool names, with the recorded sizes. |
   | Stored data | `ceph df` | At least the recorded stored data in each pool. |
   | RBD images | `rbd ls -l bedrock-block` | The same images with the same sizes as `rbd ls -l` of the old pool. |
   | RBD checksums | the `rbd export` command below | The recorded checksum of each chosen image. |
   | CephFS tree and checksums | the CephFS mount below | The recorded `ls -lR` output and the recorded checksum of each chosen file. |
   | Buckets | `radosgw-admin bucket list` and `bucket stats` | The same buckets with the same object counts and sizes. |

   ```sh
   ceph fsid
   ceph osd tree
   ceph osd pool ls detail
   ceph df
   rbd ls -l bedrock-block
   kubectl -n rook-ceph exec deploy/rook-ceph-operator -- sh -c 'rbd --conf=/var/lib/rook/rook-ceph/rook-ceph.config export --no-progress bedrock-block/<image> - | sha256sum'
   radosgw-admin bucket list --rgw-zone=bedrock-rgw --rgw-zonegroup=bedrock-rgw
   radosgw-admin bucket stats --rgw-zone=bedrock-rgw --rgw-zonegroup=bedrock-rgw
   ```

   Run `rbd ls -l` for every RBD pool and the `rbd export` command for each chosen image.
   For the CephFS comparison, use the mount host of section 1, step 8. That host has no Bedrock kubeconfig.
   Make the secret file on a control-plane host. Use the functions of "Run Ceph commands in the new cluster". Copy only the secret file to the mount host:

   ```sh
   umask 077
   ceph auth get-key client.admin > /root/admin.secret
   scp /root/admin.secret <mount host>:/root/admin.secret
   rm -f /root/admin.secret
   ```

   Then run these commands on the mount host:

   ```sh
   mount -t ceph <mon address>:6789:/ /mnt/cephfs -o name=admin,secretfile=/root/admin.secret,mds_namespace=bedrock-fs
   ls -lR /mnt/cephfs/<path>
   sha256sum /mnt/cephfs/<path>/<file>
   umount /mnt/cephfs
   rm -f /root/admin.secret
   ```

   A difference is an error. Do not go on to the volume import until you know its cause.

5. Change the cephx caps that name an old pool or an old file system.
   Do this before a client of the old cluster connects again.
   Ceph matches `pool=` and `fsname=` in a cap by name. The rename of the pools and of the file system breaks these caps.

   1. List the users with these caps. Use the `ceph` function of "Run Ceph commands in the new cluster".
      Compare the list with `cephx-users.json` of section 1, step 11.

      ```sh
      ceph auth ls -f json | jq -r '.auth_dump[] | select(any(.caps[]; test("pool=|fsname="))) | [.entity, (.caps | tostring)] | @tsv'
      ```

   2. Change each user that names an old pool or an old file system. Use the Bedrock names of the section "What changes".
      The command `ceph auth caps` replaces all caps of the user. Give every cap type that the record shows:

      ```sh
      ceph auth caps <entity> mon '<mon caps>' osd '<osd caps with the new pool names>' mds '<mds caps with the new file system name>'
      ```

      For example, `osd 'allow rw pool=<old block pool>'` becomes `osd 'allow rw pool=bedrock-block'`.
      The cap `fsname=<old CephFS>` becomes `fsname=bedrock-fs`.

   3. Run the list command of sub-step 1 again. No cap must show an old name.
      Do not use `ceph auth get` for this check. It prints the key.

### Checks of the storage gate

After `Adopted`, the condition `StorageReady` runs read-only checks in each reconcile.
A failed check keeps `StorageReady` at `False`, with the reason `VerificationFailed`.
The message names every failed check. Read it with the command of section 6.

| Message starts with | Meaning | What to do |
| --- | --- | --- |
| `ceph versions: not on release` | A daemon runs another Ceph major release than the bundle. | Wait for Rook to finish the daemon roll. Watch `kubectl -n rook-ceph get pods` and run `ceph versions`. If a daemon does not restart, read the log of the operator. Do not go on while a daemon has the old release. |
| `ceph pool <name> size` | A Bedrock pool has a size that differs from `storage.replicas`. | Find out why. Check `kubectl -n rook-ceph get cephblockpool,cephfilesystem,cephobjectstore`. If the pool is larger than `storage.replicas`, raise `storage.replicas` to the pool size. Never lower the size of a pool that holds data, and never run `ceph osd pool set <name> size` with a smaller number. If the pool is smaller, wait for Rook to reconcile it. |
| `ceph osd tree: host buckets without a ceph-osd node` | A CRUSH host bucket has no Host with the role `ceph-osd` and the same name. Rook would move data to a new bucket. | Make the host name equal the bucket name. If the bucket has the wrong name, rename it with `ceph osd crush rename-bucket <bucket> <node name>`. Do this before more data moves. |
| `ceph: health is` | Ceph is not `HEALTH_OK`. | Run `ceph health detail`. Fix the cause. A `noout` flag: run `ceph osd unset noout`. A clock skew: fix NTP on the host. |
| `ceph: no OSD is up` | No OSD runs. | Check the OSD pods, the by-id paths and the logs of the `rook-ceph-osd-prepare` jobs. |
| `ceph: PGs are not active+clean` | PGs peer or recover. | Wait. Run `ceph pg stat`. If it does not end in 30 minutes, run `ceph pg dump_stuck`. |
| `ceph: misplaced objects remain` | Ceph moves data. | Wait. If the move started at the adoption, check `ceph osd tree` and the CRUSH rules for the cause, because a new bucket or a new pool size moves data. |
| `cannot read` | Bedrock could not run a `ceph` command in the operator pod. | Check that the pod `rook-ceph-operator` runs and that the monitors have a quorum. Run `ceph status` with the function above. |

## Rollback

Decide the case by facts. Do not decide by memory.
Run these commands on a control-plane host of Bedrock:

```sh
kubectl -n rook-ceph get cephcluster
kubectl -n rook-ceph get deployment -l app=rook-ceph-mon
```

Use case A only when both commands list nothing.
If the API does not answer, use case B.
A host without a running `ceph-mon` process proves nothing, because a stopped Bedrock cluster whose monitors ran shows no process.
In any other case, or when you are in doubt, use case B.
The two cases differ.

### Case A: the Ceph daemons of Bedrock never ran

Use this case only when the commands above show that no Bedrock mon ever started.
`/var/lib/rook` and the disks are as you left them.
Put the copy of `/var/lib/rook` back only in this case.

1. Stop and reset the Bedrock k0s on every host. Do the workers first.

   ```sh
   k0s stop
   k0s reset
   ```

2. If a host rebuild wiped `/var/lib/rook`, restore the copy of that host:

   ```sh
   tar --numeric-owner --xattrs --acls -C / -xpf rook-<host>.tar
   ```

3. Start the old k0s cluster. Restore the k0s backup of section 1, step 10 on the first controller, or run the code that built the old cluster.
   If you run that code, create the Secret `rook-ceph-mon` and the ConfigMap `rook-ceph-mon-endpoints` from the exports before the CephCluster exists.

   ```sh
   k0s restore /root/k0s-backup/<archive>
   ```

   A restore brings back the Deployments with the replica counts of the backup time.

4. Wait until the mon, mgr and OSD pods run. Then unset `noout` with the toolbox function of section 1.

   ```sh
   ceph osd unset noout
   ceph health detail
   ```

### Case B: the Ceph daemons of Bedrock ran, or you are in doubt

The mon stores and the OSDs moved on together. The copy of `/var/lib/rook` is older.
Do not put the old copy over the live stores. Old mon stores with newer OSDs can leave PGs incomplete.
The old Rook creates an empty pool when a pool of its name is missing. So rename the pools back first.
Stop Ceph in the same clean way as in "Stop the old cluster". Stop k0s only at the end.

1. Stop the clients of Ceph. Stop the VMs and unmap the RBD images.

2. Stop both operators. Rook and Bedrock then change nothing.

   ```sh
   kubectl -n bedrock-system scale deployment/bedrock-operator --replicas=0
   kubectl -n rook-ceph scale deployment/rook-ceph-operator --replicas=0
   ```

3. Start the Rook toolbox. It does not need the operator.
   Download the file `deploy/examples/toolbox.yaml` of the Rook release first when the cluster has no internet access.

   ```sh
   kubectl apply -f toolbox.yaml
   kubectl -n rook-ceph set image deployment/rook-ceph-tools rook-ceph-tools=$(kubectl -n rook-ceph get cephcluster rook-ceph -o jsonpath='{.spec.cephVersion.image}')
   kubectl -n rook-ceph rollout status deployment/rook-ceph-tools
   ```

4. Define the toolbox functions. Set `noout`.

   ```sh
   ceph() { kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph "$@"; }
   radosgw-admin() { kubectl -n rook-ceph exec deploy/rook-ceph-tools -- radosgw-admin "$@"; }
   ceph osd set noout
   ```

5. Rename the pools back, if they have Bedrock names. Use the commands of "Rename by hand" with the old and the new name swapped.
   The `sh -c` commands of that section use the operator pod and its `--conf` flag.
   In the toolbox, use the deployment `rook-ceph-tools` and leave out the `--conf` flag.

   - `bedrock-block` to the old block pool.
   - `bedrock-fs` to the old CephFS, and its pools `bedrock-fs-metadata` and `bedrock-fs-data0` to the old pool names.
   - `bedrock-rgw` to the old realm, zonegroup and zone, and the pools `bedrock-rgw.rgw.*` to `<old>.rgw.*`.
     Keep the order of the RGW steps: rename the identity, rewrite the zone, rename the pools, and run `period update --commit` last.

6. Check the names with `ceph osd pool ls detail`, `ceph fs ls` and `radosgw-admin realm list`.

7. Stop the Ceph daemons. Scale every Deployment except the monitors to 0 first.

   ```sh
   kubectl -n rook-ceph scale deployment -l app!=rook-ceph-mon --replicas=0
   kubectl -n rook-ceph get pods --field-selector=status.phase=Running
   ```

   Wait until the last command lists only mon pods.
   A pod with the status `Terminating` is still listed.
   Then scale the monitors to 0.

   ```sh
   kubectl -n rook-ceph scale deployment -l app=rook-ceph-mon --replicas=0
   kubectl -n rook-ceph get pods --field-selector=status.phase=Running
   ```

   Wait until the last command lists no pod.
   A pod with the status `Terminating` is still listed.

8. Stop and reset the Bedrock k0s on every host. Do the workers first. Keep `/var/lib/rook` and the disks.

   ```sh
   k0s stop
   k0s reset
   ```

9. Start the old k0s cluster as in case A, steps 3 and 4. The old Rook finds the pools with the old names and uses them.
10. If a mon store is lost, keep the stores of the other monitors and use `kubectl rook-ceph mons restore-quorum`.

## Rehearse in the lab

The script `hack/lab-adopt.sh` rehearses the whole procedure on three Lima VMs.
It is not a job of `hack/local-ci.sh`. Run it by hand on a Linux host.

The host needs about 46 GiB of free RAM, KVM, Lima, Docker, Go, Helm and `make`.
Do not run it while a gate runs.

```sh
hack/lab-adopt.sh up
hack/lab-adopt.sh old
hack/lab-adopt.sh export
hack/lab-adopt.sh stop
hack/lab-adopt.sh snapshot
hack/lab-adopt.sh adopt
hack/lab-adopt.sh check
hack/lab-adopt.sh down
```

`hack/lab-adopt.sh all` runs the stages from `up` to `check`.
Run `snapshot` after `stop` when you want to repeat `adopt`. Run `rewind` to go back to that point.

| Stage | What it does |
| --- | --- |
| `up` | Builds the bundle from the worktree into `dist/`. Starts three Ubuntu 24.04 VMs (4 vCPU, 14 GiB, 100 GiB root disk) on one Lima user-v2 network, and gives each VM two 20 GiB OSD disks with serial numbers. |
| `old` | Installs k0s and Rook with the old values in `hack/lab/old-rook-values.yaml`. Writes two RBD images, a CephFS file, and a bucket with two S3 objects. |
| `export` | Writes the key file and the cluster configuration from `hack/lab/adopt.yaml.tmpl`. Records the fsid, the monitor ids, the OSD ids and the pool sizes. |
| `stop` | Does section 4 of this runbook: `noout`, Rook scale down, copy of `/var/lib/rook`, `k0s reset`. |
| `snapshot` | Stops the VMs, snapshots every disk as `after-stop`, and starts the VMs. |
| `rewind` | Restores the snapshot of every disk and starts the VMs. The disks are as `stop` left them. |
| `adopt` | Runs `bedrock init` and `bedrock join --adopt-config` and waits for `Adopted`. Deletes the key file, unsets `noout` and waits for `StorageReady`. |
| `check` | Prints `PASS` or `FAIL` for each assertion. The exit status is not zero if one fails. |
| `down` | Removes the lab VMs, their disks and the lab secrets. It does not touch other VMs. |

The lab files are in `dist/lab/`. They hold lab keys.
The `down` stage deletes the key file, the exports, the state file and the copies of `/var/lib/rook`.
The lab uses raw OSDs. It does not run the rollback and it does not mount CephFS with the kernel client.
The lab does not cover these cases. Do them in the rehearsal on the real hardware:

- OSDs in LVM mode.
- A rename command that fails, and the retry after it.
- The change of the cephx caps (section 7, step 5).
