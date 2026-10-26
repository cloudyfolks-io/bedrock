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
