# Upgrade

This page describes how the agent keeps images on a host, what an upgrade changes in cluster DNS, and what `bedrock upgrade abort` leaves behind.
The steps of an upgrade to this release are in the section "Cluster DNS" of `docs/network.md`.

## Image archive

A bundle holds one tarball for each image and one tarball of the k0s images (airgap).
The agent keeps these tarballs in the archive `/var/lib/bedrock/images/<version>/<arch>/`.
k0s does not read the archive.

k0s imports each tarball of `/var/lib/k0s/images` again at every start, before the kubelet starts.
A host with many tarballs then has no kubelet for minutes after each k0s restart.
So in steady state `/var/lib/k0s/images` is empty, and the kubelet starts at once.

| Event | What the agent does |
| --- | --- |
| Upgrade, step Preload | It downloads the tarballs that containerd does not hold. It imports them with `ctr`. It keeps them in the archive of the target version. |
| Agent start and each agent loop | It links into `/var/lib/k0s/images` each archived tarball that containerd does not hold, and records the link in `/var/lib/bedrock/image-links`. In a later loop it pins the images and removes the link. It never waits in the loop. |
| After an import | It sets the label `io.cri-containerd.pinned=pinned` on each image. It removes the label `io.k0sproject.ocibundle-paths`. The garbage collection of the kubelet then keeps these images. |
| A tarball of an older release in `/var/lib/k0s/images` | It hard links the tarball into the archive of each version whose release image list names it. It removes the original only when the archived file has the same content. A tarball that no list names stays where it is. |
| Upgrade, step Prune | It keeps the files that the release image list of the target version names, and its airgap tarball. It links the shared tarballs of the old version into it. It removes the other versions. It removes the pin of images that only a removed tarball named. |
| Upgrade, step Cleanup (abort) | It keeps the archive of the old version. It removes the archive of the target version. |
| Abort or restore on a host with no containerd store | It links the tarballs of the old version into `/var/lib/k0s/images` before k0s starts. |

An image is done when it is pinned and has no k0s source label. The agent removes a link only when the image is done, or when the archive of the link is gone.
If k0s does not import a link within the k0s timeout, the agent removes the link and tries again later, with a backoff from one minute to one hour. The host then reports the condition `ImagesImported` with the status `False`.
Every `k0s ctr` call has a time limit of 30 seconds.

The agent never removes the last copy of a tarball.
If the archive of the version to keep does not exist, it removes nothing.

The restore scripts of [Backup](backup.md#etcd-snapshots) do the same link step on a host with no containerd store.
The link names have the form `<version>-<file>`.

## CoreDNS after `bedrock upgrade abort`

An upgrade moves cluster DNS from the CoreDNS of k0s to the CoreDNS of the release.
The `K0sUpdate` step of the new agent disables the k0s CoreDNS in the k0s unit of each control plane node.
On an upgrade from a release whose agent does not do this, the old agent runs `K0sUpdate` in the ControlPlane phase and the unit keeps the k0s CoreDNS. The Workers phase then runs the step `K0sReconfigure` on each control plane node after `AgentUpdate`. The new agent changes the unit and restarts k0s only when the unit changed. So k0s restarts twice per control plane node on such an upgrade, one node at a time, and each restart waits for `/readyz`. The k0s CoreDNS runs until the second restart.
The Components phase then takes over the objects of k0s, and the `Prune` step removes the old k0s manifest directory.
`bedrock upgrade abort` does not change the unit file. The flag `--disable-components` keeps `coredns`.
Cluster DNS keeps working in every case below, because an object is never deleted and the Service keeps its address.

| Phase of the abort | State after the abort |
|---|---|
| `ControlPlane` | The control plane is restored from the backup of the upgrade. The restored cluster holds the CoreDNS objects of k0s with the stack label. The restore also puts back the k0s manifest directory. The k0s unit still disables the k0s CoreDNS, so nobody updates these objects. They keep running. |
| `Components` or later | The abort is refused. Finish the upgrade or restore from the backup. |

Check the state with these commands:

```
kubectl -n kube-system get deployment,service,configmap,poddisruptionbudget -l k8s-app=kube-dns
kubectl get cluster cluster -o jsonpath='{.metadata.annotations}'
grep disable-components /etc/systemd/system/k0scontroller.service
```

The annotation `bedrock.cloudyfolks.io/k0s-coredns-takeover` shows that the take over ran.
Run the upgrade again to give the CoreDNS to the operator of the release. The take over is safe to repeat.

To let k0s manage CoreDNS again, do these steps on each control plane node:

1. Remove `coredns` from the list of `--disable-components` in `/etc/systemd/system/k0scontroller.service`.
2. Run `systemctl daemon-reload` and `systemctl restart k0scontroller`.

k0s then writes its own CoreDNS objects over the existing ones. Do this only on a cluster that runs the old release.

## The Prune step finds the k0s stack label again

The `Prune` step keeps the old k0s manifest directory `/var/lib/k0s/manifests/coredns` while an object of the old k0s stack has the label `k0s.k0sproject.io/stack=coredns`.
k0s puts the label back when a new leader applies a stack file that differs from the live objects.
With the directory removed, k0s would delete every labelled object, and cluster DNS would stop.
The step then fails and its message names the object and the command to repair it, for example:

```
kubectl -n kube-system label deployment coredns k0s.k0sproject.io/stack-
```

Find every labelled object, remove the label from each one, and run the `Prune` step again:

```
kubectl get deployments,services,configmaps,serviceaccounts,poddisruptionbudgets -n kube-system -l k0s.k0sproject.io/stack=coredns
kubectl get clusterroles,clusterrolebindings -l k0s.k0sproject.io/stack=coredns
```
