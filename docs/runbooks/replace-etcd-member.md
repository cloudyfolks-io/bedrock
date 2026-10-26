# Replace an etcd member

Use this runbook when one control-plane host has a broken etcd database and the
other controllers keep the quorum. A hard power-off can break the database. The
symptoms are:

- `k0scontroller` restarts etcd in a loop and the journal shows
  `panic: freepages: failed to get all reachable pages`.
- The host has no local API server and its Node is `NotReady`.

Do not change the data on the healthy members. Replace only the broken member.
If fewer than a quorum of controllers are healthy, use
[Restore the control plane](restore-control-plane.md) instead.

## Before you start

- Get root access on the broken host and on one healthy controller.
- Make sure that at least two other controllers are healthy. Check from a
  healthy controller:

  ```
  sudo k0s etcd member-list
  kubectl get nodes
  ```

- Take a fresh etcd snapshot on a healthy controller. See
  [Etcd snapshots](../backup.md#etcd-snapshots).

Set the names:

```
NODE=<name of the broken host>
HEALTHY=<ssh name of a healthy controller>
```

## 1. Stop k0s on the broken host

```
ssh $NODE sudo systemctl stop k0scontroller
```

## 2. Remove the member from the etcd cluster

Run this from a healthy controller. The command removes the member from the
etcd member list:

```
kubectl patch etcdmembers.etcd.k0sproject.io $NODE --type merge -p '{"spec":{"leave":true}}'
kubectl wait etcdmembers.etcd.k0sproject.io/$NODE --for=condition=Joined=False --timeout=300s
```

If the object is not available, use the address of the broken member. Find it
with `sudo k0s etcd member-list`:

```
ssh $HEALTHY sudo k0s etcd leave --peer-address <peer address of the broken member>
```

Check that the list has one member less:

```
ssh $HEALTHY sudo k0s etcd member-list
```

## 3. Wipe the etcd data on the broken host

Remove only the etcd directory. Keep the `pki` directory and the other
directories:

```
ssh $NODE sudo rm -rf /var/lib/k0s/etcd
```

## 4. Join the host again

Create a token with the control-plane role on a healthy controller. Give the
token the same roles as the broken host. Find them in `spec.roles` of the Host
object:

```
kubectl get host $NODE -o jsonpath='{.spec.roles}{"\n"}'
TOKEN=$(ssh $HEALTHY sudo bedrock token create --roles control-plane,<other roles> --expiry 30m)
```

Run `bedrock join` on the broken host with this token. k0s is stopped, so the
command writes the unit again and starts it. The new member gets a new etcd
identity and copies the data from the others:

```
ssh $NODE sudo bedrock join --token "$TOKEN"
```

If your host keeps its bundle in a file, add `--bundle <path>` to the command.

## 5. Check the result

```
ssh $NODE sudo k0s kc get --raw /livez
kubectl get nodes
sudo k0s etcd member-list
```

The last command, run on any controller, must list every controller again. Wait
for the Host status. `status.checks.etcdHealthy` must be `true`, and
`status.checks.etcdMembers` must be equal to the number of controllers:

```
kubectl get host -o custom-columns=NAME:.metadata.name,ETCD:.status.checks.etcdHealthy,MEMBERS:.status.checks.etcdMembers
```

The Bedrock operator does not start a node move while a controller reports a
wrong member count. Wait until all controllers report the same count.
