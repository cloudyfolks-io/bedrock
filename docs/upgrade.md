# Upgrade

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
| Agent start and each agent loop | It links into `/var/lib/k0s/images` each archived tarball that containerd does not hold. It waits until k0s imported it. It removes the link. |
| After an import | It sets the label `io.cri-containerd.pinned=pinned` on each image. It removes the label `io.k0sproject.ocibundle-paths`. The garbage collection of the kubelet then keeps these images. |
| First start with a release that has no archive | It moves the tarballs of `/var/lib/k0s/images` into the archive. It does not download them again. |
| Upgrade, step Prune | It keeps the archive of the target version. It links the tarballs that the old version shares into it. It removes the other versions. |
| Upgrade, step Cleanup (abort) | It keeps the archive of the old version. It removes the archive of the target version. |
| Abort or restore on a host with no containerd store | It links the tarballs of the old version into `/var/lib/k0s/images` before k0s starts. |

The agent never removes the last copy of a tarball.
If the archive of the version to keep does not exist, it removes nothing.

The restore scripts of [Backup](backup.md#etcd-snapshots) do the same link step on a host with no containerd store.
The link names have the form `<version>-<file>`.
