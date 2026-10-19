# Builds

## What the build area does

The build area builds container images for projects.
A user creates a Build in a project namespace.
The Build names a git revision, a build type and a registry image.
The operator makes one Job from the Build.
The Job clones the revision, builds the image and pushes it to the registry.
The operator writes the result to the `status` field of the Build.
The proxy gives the logs.

The project namespace must have the labels `bedrock.cloudyfolks.io/tenant` and `bedrock.cloudyfolks.io/project`.
Without these labels the operator creates nothing.
The condition `Ready` shows the reason `NotInProject`.

A Build is immutable.
You cannot change the `spec` after the create.
To build another revision, create a new Build.
To cancel a Build, delete it.

The area has no queue, no limit on the number of builds and no build minutes.
It does not poll an SCM and it does not deploy the image.
A product that sits on top of Bedrock does these jobs.

## Kinds

### Build

| Field | Meaning |
|---|---|
| `type` | `dockerfile` (the default) builds with BuildKit. `buildpacks` builds with Cloud Native Buildpacks. |
| `source.git.url` | The `https` URL of the repository. It has at most 512 characters and holds no user name. |
| `source.git.revision` | A commit, tag or branch. It has no space and at most 255 characters. |
| `source.git.secretRef` | The name of a Secret with the keys `username` and `token`. Optional. A public repository needs none. |
| `contextPath` | The directory of the build context in the repository. The default is `.`. It is relative and has no `..`. |
| `dockerfilePath` | The path of the Dockerfile in the repository. The default is `Dockerfile`. It is relative and has no `..`. Only the type `dockerfile` reads it. |
| `output.image` | The image to push, for example `registry.example.com/acme/shop:1`. |
| `output.pushSecretRef` | The name of a Secret of the type `kubernetes.io/dockerconfigjson` that can push the image. |
| `cache.enabled` | Keep a build cache in the registry, in the image `<repository>:cache`. |
| `size` | `s`, `m` (the default) or `l`. See the table of sizes. |
| `timeout` | The time limit. The default is the Setting `build.default-timeout`. It cannot be above `build.max-timeout`. |
| `network.subnetRef` | The Subnet of the build pod. Required. |

The name of a Build has at most 52 characters and is a DNS label.

Each size sets the same values for each container of the build pod.
A request is half of the limit.
The ephemeral storage request is equal to its limit.

| Size | CPU limit | Memory limit | Scratch disk |
|---|---|---|---|
| `s` | 1 | 2Gi | 5Gi |
| `m` | 2 | 4Gi | 10Gi |
| `l` | 4 | 8Gi | 20Gi |

The scratch disk size limits the volumes `workspace`, `layers` and `buildkit-state`.

## Status

| Field | Meaning |
|---|---|
| `phase` | `Pending`, `Running`, `Succeeded` or `Failed`. |
| `reason` | The reason of the condition `Ready`. See the table of reasons. |
| `jobName` | The name of the Job. It is the name of the Build. |
| `startedAt` | The time of the first start of the build pod. |
| `finishedAt` | The time of the end of the build. |
| `image` | The pushed image. It is set when the phase is `Succeeded`. |

A phase of `Succeeded` or `Failed` is final.
The operator never makes a second Job for a finished Build.
The `status` stays when the Job and the pod are gone.

## Credentials

The operator never makes a Secret.
Both Secrets are Secrets of the project, and the Build names them.

- The git Secret has the keys `username` and `token`. Only the `git-clone` container reads them, through environment variables. The other containers of the build pod cannot read them. Dockerfile code cannot read the token. The token is not in the clone URL.
- The push Secret has the type `kubernetes.io/dockerconfigjson`. It is mounted as `config.json` in the directory `/docker-config`. The variable `DOCKER_CONFIG` names the directory. The BuildKit container and the buildpacks phases that write or read the registry get it: `analyzer`, `exporter` and, with `cache.enabled`, `restorer`. The kubelet also uses it as the pull secret of the pod.

## Network

The build pod has one interface on the Subnet of `network.subnetRef`.
It uses the DNS servers of the Subnet.
The pod must reach the git server, the registry and the registries of the base images.
The project needs an internet gateway or an elastic IP for this.
Without it, the clone fails and the log shows the error.

## The Kata rule

A BuildKit container is root and privileged.
The only isolation is the Kata microVM.
The operator makes a build pod only when the RuntimeClass `kata` exists and the Addon `kata` is `Ready`.
If not, the condition `Ready` shows the reason `KataUnavailable` and the operator makes no Job.
The operator never runs a build without Kata.
The Setting `container.runtime-class` does not change this.
A cluster with no KVM cannot run builds.

## Registry mirrors

The Setting `build.registry-mirrors` lists mirrors for the `FROM` lines of a Dockerfile.
The operator writes the list to a ConfigMap, owned by the Build, and BuildKit reads it as `buildkitd.toml`.
A mirror is for the whole cluster. It is not a field of the Build.
Buildpacks builds do not use it.

## Logs

The proxy gives the logs of a build at `builds/logs`.
The role of the user must allow `get` on `builds/logs`.
All three project roles do.

| Query | Meaning |
|---|---|
| `follow` | Keep the connection open and move to the next container of the pod until the build ends. |
| `tail` | The number of lines of each container. The default is 2000. The range is 1 to 10000. |
| `container` | Return the raw log of one container of the pod. |

The stream shows the clone first.
Then it shows each phase of the build.
A line `==> <container> <==` starts the log of each container.

The logs are available while the pod exists.
The Setting `build.job-ttl` sets how long the pod stays after the build.
After that, a request returns the code 410 with the reason `LogsExpired`.
Save the log before this time if you need it later.
A Pending Build with no pod gives the code 404, or waits when `follow` is set.

## Settings

These Settings control the Build kind.
A change applies to the next Build.
A build that runs keeps the values it started with.

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

## Reasons

The condition `Ready` of a Build shows one of these reasons.
A reason that comes from a missing dependency is not final.
The operator tries again every 30 seconds.

| Reason | Cause | Action |
|---|---|---|
| `NotInProject` | The namespace has no tenant and project labels. | Create the Build in a project namespace. |
| `InvalidSpec` | The timeout is below 1 second or above `build.max-timeout`, or a build Setting is not valid. | Create a new Build with a valid timeout, or ask an administrator to change the Setting. |
| `SubnetNotFound` | The Subnet of `network.subnetRef` does not exist. | Create the Subnet or use the name of another one. |
| `SubnetNotReady` | The Subnet or its network is not ready. | Wait, or read the condition of the Subnet. |
| `WaitingForDefaultSecurityGroup` | The default security group of the project does not exist yet. | Wait. The network area makes it. |
| `SecretNotFound` | The git Secret or the push Secret does not exist in the namespace. | Create the Secret. The operator starts the build when it exists. |
| `SecretTypeInvalid` | The push Secret is not of the type `kubernetes.io/dockerconfigjson`, or the git Secret has no `username` or no `token`. | Correct the Secret. |
| `KataUnavailable` | The RuntimeClass `kata` does not exist, or the Addon `kata` is not `Ready`. | Run builds on a cluster with KVM and an enabled `kata` Addon. |
| `ApplyFailed` | The cluster refused the Job or the ConfigMap. | Ask an administrator to read the operator log. |
| `NameConflict` | A Job or a ConfigMap of the same name exists and the Build does not own it. | Use another name for the Build. |
| `Starting` | The Job exists and the pod has not started. | Wait. |
| `Running` | The build pod runs. | Read the logs. |
| `Succeeded` | The image is pushed. | Use the image in `status.image`. |
| `Failed` | A container of the build exited with an error. | Read the logs. |
| `Timeout` | The build ran longer than its time limit. | Create a new Build with a longer `timeout`, or make the build faster. |
| `ImagePullFailed` | A pod image stayed unavailable for 2 minutes. The operator deletes the Job. | Read the message. Ask an administrator to check the build image Settings and the registry. |
| `ContainerConfigError` | The pod could not start a container for 2 minutes, for example a Secret key is missing. The operator deletes the Job. | Read the message and correct the Secret. |
| `JobMissing` | The Job was deleted for more than one minute before the build ended. | Create a new Build. |

## Delete a Build

Delete the Build to cancel it.
The Job, the pod and the ConfigMap belong to the Build, so Kubernetes deletes them with it.
No Secret is left, because the operator makes none.
