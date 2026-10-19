# Builds

## Settings

These Settings control the Build kind.
Each value that an operator changes applies to the next Build.
A Build that runs keeps the values it started with.

| Setting | Default | Meaning |
|---|---|---|
| `build.git-image` | `alpine/git:v2.54.0` | The image of the git-clone step. It must hold `git` and `sh` and run as user 1001. It is in the offline bundle. |
| `build.buildkit-image` | `moby/buildkit:v0.31.2` | The image that runs BuildKit for the type `dockerfile`. Use the image without the `-rootless` suffix. It is in the offline bundle. |
| `build.builder-image` | `paketobuildpacks/builder-jammy-base:0.4.640` | The builder image of the type `buildpacks`. It is not in the offline bundle. |
| `build.run-image` | `paketobuildpacks/run-jammy-base:0.1.275` | The run image of the type `buildpacks`. It is not in the offline bundle. |
| `build.default-timeout` | `30m` | The time limit of a Build that has no `spec.timeout`. From 1m to 24h. |
| `build.max-timeout` | `2h` | The largest `spec.timeout`. A Build above it gets the reason `InvalidSpec`. From 1m to 24h. |
| `build.job-ttl` | `1h` | The time that the Job and the pod of a finished Build stay on the cluster. From 1m to 24h. |
| `build.registry-mirrors` | empty | A comma list of `<upstream>=<host>/<path>` entries. BuildKit reads the `FROM` lines of the upstream from the mirror. |

The builder image and the run image are about 1 GB each.
On a cluster with no access to the internet, put them on a registry mirror and set the two Settings to the mirror names.
