# Registry

## What you get

Bedrock can run one shared Harbor for the whole cluster.
Harbor is an image registry. It speaks the OCI registry protocol and has its own API.
The `registry` addon installs it. The addon is off by default.
See `docs/addons.md` for the addon, its Settings and its conditions.

A project uses two kinds:

- `Registry` is one Harbor project. It has a name, a storage quota, a retention policy and a webhook.
- `RegistryCredential` is one Harbor robot account. Bedrock puts its login in a Secret.

Bedrock does not wrap the content of a registry.
It has no kind for repositories, artifacts, tags or retention runs.
Use the OCI protocol and the Harbor API with a robot account for them.

Harbor runs in the namespace `bedrock-registry`.
A project member cannot read this namespace.
The member cannot read the Harbor admin password, the registry htpasswd, the S3 key or the robot secret of another project.

## Create a registry

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Registry
metadata:
  name: images
  namespace: acme-prod
spec:
  name: acme-images
  quotaGB: 10
  retention:
    keepLastN: 10
    retainDays: 30
    tagPattern: "v*"
    retainUntagged: false
    schedule: daily
  webhook:
    url: https://hooks.example.com/registry
    authHeaderSecretRef:
      name: registry-hook
      key: token
```

| Field | Meaning |
|---|---|
| `name` | The name of the Harbor project. Optional. The default is `metadata.name`. It has 3 to 30 letters (lowercase), digits and dashes. It starts and ends with a letter or a digit. You cannot change it. |
| `quotaGB` | The storage quota in GB. `0` means no limit. The default is the Setting `registry.default-quota-gb`, which is `5`. |
| `retention.keepLastN` | Keep the newest N tags. From 1 to 1000. |
| `retention.retainDays` | Keep the tags that were pushed in the last N days. From 1 to 3650. |
| `retention.tagPattern` | The tags that the rules apply to. The default is every tag. |
| `retention.retainUntagged` | Apply the rules to untagged artifacts. |
| `retention.schedule` | `off`, `hourly` or `daily`. |
| `webhook.url` | Harbor sends the events `PUSH_ARTIFACT` and `DELETE_ARTIFACT` to this URL. The policy is named `platform-events`. |
| `webhook.authHeaderSecretRef` | A Secret and a key in the same namespace. The value is the `Authorization` header of the call. |
| `forceDestroy` | Delete the images with the object. The default is `false`. |

The retention policy keeps the tags that one of the rules selects.
If the spec has no `keepLastN` and no `retainDays`, Bedrock clears the policy of the project.
If you remove `webhook`, Bedrock deletes the policy `platform-events`.

The webhook cannot target a service in the cluster.
Harbor sends the call from the jobservice pods in `bedrock-registry`.
A NetworkPolicy named `harbor-jobservice-egress` limits their egress.
They can reach DNS, the Harbor pods in the namespace and addresses outside the blocked ranges.
The Setting `registry.webhook-blocked-cidrs` lists the blocked ranges.
The default has the private ranges, the shared range `100.64.0.0/10`, the link-local range and the loopback range.
This covers the default pod and service networks.
If your pod network or service network is outside these ranges, add it to the Setting.
Bedrock also blocks the address of every node.
Use a public URL for the webhook.

`status` shows these fields:

| Field | Meaning |
|---|---|
| `host` | The registry host. Use it in image names: `<host>/<name>/<repository>:<tag>`. |
| `projectName` | The Harbor project that the object owns. |
| `projectID` | The Harbor id of the project. |
| `usedBytes`, `hardBytes` | The storage used, and the limit. `-1` means no limit. |
| `repositoryCount` | The number of repositories. |

Bedrock reads the usage from Harbor every 5 minutes.
If the read fails, the old values stay.

### Names

Harbor project names are unique in the whole cluster.
If the name is in use, the condition `Ready` shows the reason `NameTaken`.
Bedrock never takes a project that it did not make.

These names are reserved: the Setting `registry.reserved-names` (default `library`, `admin`, `registry`, `api`, `docs`, `system`, `internal`, `platform` and `builders`).
The prefixes in `registry.reserved-prefixes` (default `mirror-`) are reserved too.
Only the group `bedrock:bedrock-admins` can use them.
A project owner who tries gets an admission error.
An administrator needs the annotation `bedrock.cloudyfolks.io/reserved-name-approved: "true"` on the object.
Without it the controller shows the reason `NameReserved`.

A name that was in use is free again after the delete.
Bedrock does not keep a deleted name for ever.

### Adopt an existing project

An administrator can adopt a Harbor project that exists already.
For example, a Bedrock install can run next to an older Harbor.
Set the annotation `bedrock.cloudyfolks.io/adopt: "true"` on the `Registry`.
The object then takes the project with the same name.
Only `bedrock:bedrock-admins` can set this annotation, because an unclaimed project holds the images of someone else.
Without the annotation, an existing name gives `NameTaken`.

### Delete

A registry with repositories and `forceDestroy: false` stays in `Terminating`.
The condition `Ready` shows the reason `NotEmpty` and `status.repositoryCount` shows the number.
Set `forceDestroy: true` to continue.
Bedrock then deletes the robots of the project, then every repository, then the project.
It deletes 100 repositories in each round, at most 100 rounds in one pass.
If the project still has repositories after that, the reason is `PurgeIncomplete`, and the next pass continues.

When the namespace of the object is deleting, Bedrock acts as if `forceDestroy` were `true`.
A project delete is a delete of everything in it.

If the addon is off, a delete waits with the reason `RegistryNotAvailable`.

## Create a credential

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: RegistryCredential
metadata:
  name: ci
  namespace: acme-prod
spec:
  registryRef:
    name: images
  permission: pushPull
```

| Permission | Access |
|---|---|
| `pull` | Pull images. |
| `pushPull` | Pull and push images. |
| `manage` | Pull and push images, list and delete repositories, artifacts and tags, and manage the retention policy of the project. |

The spec cannot change after the create. To change the permission, make a new credential.
The robot has no expiry.
The name `platform-` as a prefix is for administrators.

Bedrock writes the Secret `<name>-docker` in the same namespace.
It has the type `kubernetes.io/dockerconfigjson`.
`status.secretName` and `status.username` show the name of the Secret and the login.

| Key | Content |
|---|---|
| `.dockerconfigjson` | The Docker client configuration for the registry host. |
| `host` | The registry host. |
| `username` | The login of the robot. It has the form `<prefix><project>+<name>`. The prefix is the Setting `registry.robot-prefix`. |
| `password` | The robot secret. |

Use the Secret as an image pull Secret:

```yaml
spec:
  imagePullSecrets:
    - name: ci-docker
```

Harbor shows a robot secret only at the create.
So the Secret in the namespace is the only copy.
If the Secret is missing or broken, Bedrock sets a new secret on the robot and writes the Secret again.
To get a new login on purpose, change the annotation `bedrock.cloudyfolks.io/reset-secret` to a new value.

Deleting the credential deletes the robot.

The registry must be ready.
Until then the credential shows the reason `RegistryNotFound` or `RegistryNotReady`.
A robot with the same name that exists already gives `NameTaken`.
An administrator can adopt it with the annotation `bedrock.cloudyfolks.io/adopt: "true"`.
Bedrock then takes the robot over and sets a new secret.

## Conditions

Both kinds report the condition `Ready`.

| Reason | Kind | Meaning |
|---|---|---|
| `Ready` | both | The object is applied. This is the only reason with status `True`. |
| `RegistryNotAvailable` | both | The addon is off, the Secret `harbor-admin` is missing, or Harbor does not answer. |
| `InvalidSettings` | `Registry` | A Setting of the registry has a wrong value. |
| `NameInvalid` | `Registry` | The project name does not follow the rules. |
| `NameReserved` | `Registry` | The name or its prefix is reserved and the object has no approval. |
| `NameTaken` | both | The project or the robot exists and the object does not own it. |
| `WebhookSecretMissing` | `Registry` | The Secret or the key of `webhook.authHeaderSecretRef` does not exist. |
| `NotEmpty` | `Registry` | The delete waits because the project has repositories and `forceDestroy` is `false`. |
| `PurgeIncomplete` | `Registry` | The purge of a forced delete is not finished. The next pass continues. |
| `HarborError` | both | Harbor refused a call. The message has the answer of Harbor. |
| `RegistryNotFound` | `RegistryCredential` | The `Registry` of `registryRef` does not exist. |
| `RegistryNotReady` | `RegistryCredential` | The `Registry` is not ready. |

## Pull-through caches

The Setting `registry.proxy-caches` lists caches for base images.
Each entry has a `name` that starts with `mirror-`, a `type` and an `https` `url`.
The `type` is a Harbor adapter, for example `docker-hub` or `docker-registry`.

```json
[{"name":"mirror-docker","type":"docker-hub","url":"https://hub.docker.com"}]
```

A cache is a Harbor registry endpoint and a public project that uses it.
Bedrock makes both and does not change them afterwards.
To use another URL, use a new name.
If an endpoint with the same name has another type, the condition `HarborConfigured` of the addon shows the reason `ProxyCacheTypeMismatch`.
The prefix `mirror-` is reserved, so a tenant cannot make a project with such a name.

## Project quota

A project can limit its registries: `spec.quota.registries` of the `Project` is the maximum count.
The quota key is `count/registries.bedrock.cloudyfolks.io`.

## Limits

- Bedrock has no kind for the content of a registry. Use the OCI protocol and the Harbor API with a `manage` credential.
- Harbor projects are not Kubernetes objects. A Harbor project that no `Registry` claims stays in Harbor. An administrator removes it.
- A project name is global, and a deleted name is not reserved.
- Bedrock does not scan images. The scanner of Harbor is off.
- The pin tags of deployed images belong to the work on the Container kind.

## The data of an older Harbor

The image bucket of an older Harbor can survive a move to Bedrock.
The addon waits with the reason `BucketNotOwned` while the bucket belongs to another store user.
An administrator links the bucket to the user `bedrock-registry` with `radosgw-admin bucket link`.
The addon never changes the owner.

Bedrock makes the Secret `harbor-admin` only if it does not exist.
An administrator who moves an older Harbor database writes this Secret with the old admin password before the addon starts.
Then Bedrock uses that password.
Harbor projects and robots come back with the database.
Adopt them with the annotation `bedrock.cloudyfolks.io/adopt: "true"`.
