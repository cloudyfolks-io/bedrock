# Addons, audit and monitoring

An addon is an optional part of Bedrock. An admin turns an addon on or off
with an `Addon` object. This page describes the addons `kata`, `loki`, `clusters`, `cnpg`, `registry` and `percona-ps`,
the API server audit log, the monitoring stack and the metering series.

## Addons

The `Addon` kind has no namespace. The name of the object is the name of
the addon. Bedrock creates one `Addon` for each addon it knows: `kata`,
`loki`, `clusters`, `cnpg`, `registry` and `percona-ps`.

List the addons:

```
kubectl get addons
```

Turn an addon on:

```
kubectl patch addon loki --type merge -p '{"spec":{"enabled":true}}'
```

Turn an addon off:

```
kubectl patch addon loki --type merge -p '{"spec":{"enabled":false}}'
```

The Settings `kata.enabled`, `loki.enabled`, `clusters.enabled`, `cnpg.enabled`, `registry.enabled` and `percona-ps.enabled` set the first state of the
`Addon` objects when Bedrock creates them. After that, only the `Addon`
object counts. A change of the Setting does not change a live addon.
Bedrock never changes `spec.enabled` by itself.

Bedrock applies the addon objects in a namespace. `kata` uses
`kube-system`. `loki` uses `monitoring`. `cnpg` uses `cnpg`. `registry` uses `bedrock-registry`. `percona-ps` uses `ps-operator`. Bedrock records the objects of
each addon in the ConfigMap `bedrock-system/bedrock-addon-<name>-inventory`.
Do not edit this ConfigMap. Bedrock uses it to remove the objects.

### Values

An addon can have values. Put them in `spec.values` as flat keys.

| Addon | Key | Default | Rule |
|---|---|---|---|
| `kata` | none | | |
| `loki` | `persistence.size` | `20Gi` | A positive Kubernetes quantity. |
| `loki` | `persistence.storage-class` | `block` | A DNS-1123 subdomain. |
| `cnpg` | none | | |
| `registry` | none | | |
| `percona-ps` | none | | |

An unknown key is an error. The condition `Ready` then shows the reason
`InvalidValues`. Bedrock changes nothing until you fix the values.

Example:

```
kubectl patch addon loki --type merge \
  -p '{"spec":{"enabled":true,"values":{"persistence.size":"50Gi"}}}'
```

A running StatefulSet does not accept a new volume size. The condition
`Ready` shows the reason `ApplyFailed` in this case.

### Delete

`kubectl delete addon loki` removes the addon objects first. Then the
`Addon` object goes away. Bedrock creates the known `Addon` objects again
when the operator starts, with the first state from the Settings.

### Conditions

The `Addon` has the condition `Ready`. The addons `kata` and `registry` also have `Preflight`, and `registry` has `RuntimeReady`. The
field `status.observedGeneration` shows the last generation that Bedrock
read.

| Reason of `Ready` | Meaning |
|---|---|
| `Ready` | All workloads of the addon are ready. This is the only reason with status `True`. |
| `Progressing` | Bedrock applied the objects. A workload is not ready yet. |
| `Disabled` | The addon is off. Bedrock removed its objects. |
| `PreflightFailed` | A check failed. Bedrock removed the objects. |
| `PreflightPending` | A host did not report yet. Bedrock changes nothing. |
| `InvalidValues` | `spec.values` has an error. Bedrock changes nothing. |
| `UnknownAddon` | This release has no addon with this name. |
| `WaitingForCluster` | The cluster has no settled version, or an upgrade runs. |
| `ApplyFailed` | The API server refused an object. The message names it. |
| `MissingCRD` | A kind that the addon needs does not exist. |
| `RenderFailed` | Bedrock could not render the manifests. |
| `Pruning` | Bedrock waits for pods to end before it removes the rest. |

| Reason of `Preflight` | Status | Meaning |
|---|---|---|
| `Passed` | `True` | A workload host has `/dev/kvm`. |
| `NoKVM` | `False` | No workload host has `/dev/kvm`. |
| `WaitingForHosts` | `Unknown` | A workload host did not report yet. |
| `HostNotSet` | `Unknown` | The `registry` addon waits for the Setting `registry.host`. |
| `CNPGAddonOff` | `Unknown` | The `registry` addon waits for the addon `cnpg`. |
| `DatabaseDisabled` | `Unknown` | The `registry` addon waits for the Setting `database.enabled`. |
| `CertManagerNotReady` | `Unknown` | The `registry` addon waits for cert-manager. |

## Kata

Kata runs a pod in its own light virtual machine. Use it for workloads
that need a stronger wall than a normal container.

Kata needs KVM. The agent on each host reports the fact in
`Host.status.checks.kvm`. The file `/dev/kvm` exists on bare metal and with
nested virtualization. Bedrock puts the label `bedrock.cloudyfolks.io/kvm`
with the value `true` on each node that has KVM. The kata DaemonSet runs
only on these nodes.

The preflight looks at the hosts with the role `workload`:

- One host with KVM: the preflight passes.
- No host with KVM and one host that did not report: the preflight waits.
- No host with KVM: the preflight fails. Bedrock removes the kata objects
  and shows the reason `PreflightFailed`.

`spec.enabled` stays `true` when the preflight fails. When a host with KVM
joins later, Bedrock applies kata again. You do not edit anything.

Bedrock creates the RuntimeClass `kata`. Use it in a pod:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: kata-example
spec:
  runtimeClassName: kata
  containers:
    - name: shell
      image: docker.io/library/busybox:1.37
      command: ["sleep", "3600"]
```

The RuntimeClass adds an overhead of 512Mi memory and 250m CPU to each pod.
The quota of a project counts this overhead.
The chart default is 320Mi.
The values file `release/values/kata.yaml` sets the larger value through the chart value `shims.qemu.runtimeClass.overhead`.

The same values file sets two options of the QEMU shim.
The chart value `shims.qemu.dropIn` holds a Kata TOML drop-in.
The chart puts it in the ConfigMap `kata-deploy-custom-configs`, and kata-deploy applies it to the `qemu` configuration as `50-user-overrides.toml`.

| Option | Value | Reason |
| --- | --- | --- |
| `default_memory` | 512 | The guest starts with 512 MiB. The shipped default is 2048. |
| `virtio_fs_extra_args` | `["--thread-pool-size=1", "--announce-submounts", "--xattr"]` | The drop-in replaces the list, so it repeats the two stock values. `--xattr` makes extended attributes work in a volume that the guest shares with the host, for example an `emptyDir`. Tools that set an attribute, such as an image builder, need this. |

The Setting `container.runtime-class` has the default `kata`.
The controller of the `Container` kind uses its value as the `runtimeClassName` of each pod.
An empty value keeps the default.
The value `none` sets no runtime class.
Use `none` only on a cluster with no KVM, for example the e2e cluster.
A customer pod then shares the kernel of the node.

When you turn kata off, Bedrock first deletes the DaemonSet. The kata pods
clean the node when they stop. This can take 10 minutes. Bedrock removes
the RBAC objects and the RuntimeClasses only after the last kata pod ends.
The condition `Ready` shows the reason `Pruning` during this time.

## CloudNativePG

The addon `cnpg` installs the CloudNativePG operator and the Barman Cloud
plugin in the namespace `cnpg`. The managed PostgreSQL databases of the
`Database` kind need it. The addon is off by default. The Setting
`cnpg.enabled` sets the first state of the `Addon` object.

- The chart `cloudnative-pg` version 0.29.1 installs the operator.
- The Barman Cloud plugin v0.13.0 stores backups and write-ahead logs in
  the object store. It needs cert-manager for its certificates. Bedrock
  always installs cert-manager. If the CRDs of cert-manager are missing,
  the condition `Ready` shows the reason `MissingCRD`.
- The CRDs of both parts are part of the base install. They stay when you
  turn the addon off, so no database is lost.
- The release holds the seed `DatabaseVersion` objects `postgresql-16`,
  `postgresql-17` and `postgresql-18`. The images are pinned by digest.

Turn the addon on:

```
kubectl patch addon cnpg --type merge -p '{"spec":{"enabled":true}}'
```

## Registry

The addon `registry` installs one shared Harbor in the namespace
`bedrock-registry`. The kinds `Registry` and `RegistryCredential` use it.
The addon is off by default. The Setting `registry.enabled` sets the first
state of the `Addon` object.

The addon needs four things. The `Preflight` condition names each missing
one with a reason, and Bedrock changes nothing until all four are there:

- The Setting `registry.host` has a DNS name (`HostNotSet`).
- The addon `cnpg` is on (`CNPGAddonOff`).
- The Setting `database.enabled` is `true` (`DatabaseDisabled`).
- cert-manager is ready (`CertManagerNotReady`).

Bedrock does not turn on `cnpg` or `database.enabled` for you.

Turn the addon on:

```
kubectl patch setting registry.host --type merge -p '{"spec":{"value":"registry.example.com"}}'
kubectl patch setting database.enabled --type merge -p '{"spec":{"value":"true"}}'
kubectl patch addon cnpg --type merge -p '{"spec":{"enabled":true}}'
kubectl patch addon registry --type merge -p '{"spec":{"enabled":true}}'
```

The chart `harbor` version 1.18.4 installs Harbor with the core, registry,
jobservice, portal and exporter services, an internal Redis, no scanner and
an external database. A controller of Bedrock makes the other objects in
`bedrock-registry`:

- The namespace. Turning the addon off keeps it, so no data is lost.
- The Secrets that the chart reads: `harbor-admin`, `harbor-core-keys`,
  `harbor-jobservice-keys`, `harbor-registry-keys`,
  `harbor-registry-credentials` and `harbor-db-credentials`. Bedrock makes a
  Secret only when it does not exist. A Secret that you made before stays as
  it is.
- The CloudNativePG cluster `harbor-db`, with the instances from
  `registry.db-instances`. A daily backup goes to the bucket of
  `database.backup.bucket` under the prefix `registry/`.
- The object store user `bedrock-registry` and the bucket of
  `registry.bucket` for the image data.
- The certificate `harbor-token`, from the issuer `bedrock-selfsigned`.
- A certificate and Traefik routes for `registry.host` and each name of
  `registry.aliases`. Harbor makes the token address from one URL. Each alias
  route rewrites the header `WWW-Authenticate` so that a client asks the
  alias for its token.
- A route for the Harbor portal only when `registry.admin-host` and
  `registry.admin-allow-cidrs` are both set. Only the listed networks can
  open it.

The condition `RuntimeReady` shows the state of these objects.

| Reason of `RuntimeReady` | Meaning |
|---|---|
| `Ready` | All objects are applied. |
| `InvalidSettings` | A Setting of the registry has a wrong value. |
| `StorePending` | A user of the object store is not ready yet. |
| `BucketNotOwned` | The bucket exists and another identity owns it. Bedrock never changes the owner. An administrator links the bucket to the user `bedrock-registry` with `radosgw-admin bucket link`. |
| `MissingCRD` | The kind of cert-manager, Traefik or CloudNativePG is not in the cluster. |
| the reasons of `Preflight` | A prerequisite is missing. |

| Setting | Default | Meaning |
|---|---|---|
| `registry.enabled` | `false` | First state of the addon. |
| `registry.host` | empty | DNS name of the registry. |
| `registry.aliases` | empty | Comma list of extra DNS names. |
| `registry.admin-host` | empty | DNS name of the Harbor portal. |
| `registry.admin-allow-cidrs` | empty | Networks that can open the portal. |
| `registry.replicas` | `2` | Replicas of core, registry and jobservice (1 to 5). |
| `registry.db-instances` | `2` | Instances of `harbor-db` (1 to 3). |
| `registry.storage-class` | `block` | StorageClass of the database and Redis. |
| `registry.bucket` | `bedrock-registry` | Bucket of the image data. |
| `registry.http-proxy` | empty | Proxy for the pull-through caches. |
| `registry.robot-prefix` | `robot$` | Prefix of each robot login. |
| `registry.gc-cron` | `0 0 3 * * *` | Garbage collection schedule. |
| `registry.proxy-caches` | empty | JSON list of pull-through caches. |
| `registry.webhook-blocked-cidrs` | private, shared, link-local and loopback ranges | Networks that the jobservice cannot reach, so a webhook cannot call into the cluster. |
| `database.enabled` | `false` | Turns on the database platform. |
| `database.backup.bucket` | `bedrock-database-backups` | Bucket of the platform database backups. |

## Percona Server for MySQL

The addon `percona-ps` installs the Percona Operator for MySQL in the
namespace `ps-operator`. The managed MySQL databases of the `Database` kind
need it. The addon is off by default. The Setting `percona-ps.enabled` sets
the first state of the `Addon` object.

- The chart `ps-operator` version 1.2.0 installs the operator. The operator
  image is pinned by digest. The operator watches only the namespace
  `bedrock-dbaas`.
- The CRDs are part of the base install. They stay when you turn the addon
  off, so no database is lost.
- The release holds the seed `DatabaseVersion` objects `mysql-8.4` and
  `mysql-9.7`. The server image and the companion images `backup`,
  `haproxy`, `init` and `toolkit` are pinned by digest.
- Valkey needs no addon. It works when the Setting `database.enabled` is
  true.

Turn the addon on:

```
kubectl patch addon percona-ps --type merge -p '{"spec":{"enabled":true}}'
```

## Loki

Loki and Alloy keep logs. Loki is off by default.

Turn Loki on with its storage values:

```
kubectl patch addon loki --type merge \
  -p '{"spec":{"enabled":true,"values":{"persistence.size":"50Gi","persistence.storage-class":"block"}}}'
```

Alloy runs on every node. It sends four sources to Loki:

| Source | Labels | Example query |
|---|---|---|
| Container logs | `log_source="container"`, `namespace`, `pod`, `container`, and for project namespaces `tenant`, `project` | `{tenant="acme", project="web"}` |
| Journald | `job="journald"`, `unit`, `node` | `{job="journald", unit="k0scontroller.service"}` |
| VM serial consoles | `log_source="vm-serial-console"`, `vm`, and for project namespaces `tenant`, `project` | `{tenant="acme", project="web", vm="web-1", log_source="vm-serial-console"}` |
| API server audit | `job="kube-apiserver-audit"`, `node` | `{job="kube-apiserver-audit"} \|= "objectRef"` |

Alloy copies the namespace labels `bedrock.cloudyfolks.io/tenant` and
`bedrock.cloudyfolks.io/project` to the stream labels `tenant` and
`project`. Only the operator can set these labels on a namespace. A
namespace that is not a project has neither label, so no query with
`tenant` and `project` reaches the logs of `kube-system`, the journal or the
audit log.

Serial console logs need the flag `logSerialConsole` on the VM instance.
The compute controller sets it.

Alloy runs as root with only the capability `DAC_READ_SEARCH`. The API
server writes its audit file with mode `0600`. Alloy needs this capability
to read it. Alloy keeps its read positions in a temporary volume. After a restart of
an Alloy pod, it reads the audit file again. Loki drops the duplicate lines.
Neither Loki nor Alloy sends usage data outside the cluster.

The Setting `loki.retention-days` sets how long Loki keeps logs. The
default is `14`. A change of the Setting rolls the Loki pod.

Loki has no address outside the cluster. A platform service reads it
through the API service proxy, as the section Metering shows.

When Loki is on, Prometheus scrapes it through the ServiceMonitor `loki`.
The PrometheusRule `loki` holds the alert `LokiDiscardsLogs` with the
severity `warning`. It fires when `loki_discarded_samples_total` rises in 10
minutes. Loki drops lines when a stream is over a limit, and without this
alert the loss is silent. The label `reason` names the limit. The addon
applies these two objects with the others. When the monitoring CRDs are
missing, the condition `Ready` shows `MissingCRD`.

When you turn Loki off, Bedrock keeps the volume claim `storage-loki-0`.
The logs stay. To delete the logs, delete the claim:

```
kubectl -n monitoring delete pvc storage-loki-0
```

When Loki is on, the Grafana of the monitoring stack gets the data source
`Loki` from the ConfigMap `loki-datasource`.

## Clusters

The addon `clusters` runs the k0smotron operator, version `v2.1.0`.
k0smotron makes the control plane of a `KubernetesCluster` as a pod.
The addon is off by default. The installer for a region turns it on.

The operator runs in the namespace `k0smotron`. Its webhooks use a
certificate of cert-manager, which Bedrock always installs.

The CustomResourceDefinitions of k0smotron are part of the CRD group of
the release, not of the addon. They stay when you turn the addon off, so
no cluster object is lost. Without the addon, no operator reads them.
A `KubernetesCluster` then shows the reason `AddonDisabled`.

The operator account `system:serviceaccount:k0smotron:k0smotron-controller-manager`
may make StatefulSets with network annotations. A tenant user may not.
The control plane pod of a cluster has a fixed address, and the annotations
carry it.

Two Settings belong to the addon:

| Setting | Default | Meaning |
|---|---|---|
| `clusters.enabled` | `false` | First state of the `Addon` object. |
| `clusters.versions` | `v1.36.3+k0s.1` | Allowed k0s versions, separated by spaces or new lines. The value `any` allows every version of the form `v<major>.<minor>.<patch>+k0s.<n>`. |

The k0s image of the default version is in the offline bundle. Another
version needs the registry mirror.

## Audit

The API server writes an audit log on each control plane host. Bedrock
sets the policy from the Setting `audit.level`.

| Request | Level |
|---|---|
| Health URLs, `events`, `leases` | Not logged |
| Reads by nodes | Not logged |
| Secrets, ConfigMaps, TokenReviews | `Metadata` |
| The kinds `apitokens`, `authcodes`, `authrequests`, `credentials`, `devicerequests`, `refreshtokens`, `sessions`, `signingkeys` and all `*/status` of the Bedrock group | `Metadata` |
| All requests on Settings | `Metadata`, because a Setting can hold a webhook URL with a token |
| Writes on other Bedrock kinds | `RequestResponse` |
| Other reads | The Setting `audit.level` |
| Everything else | `Metadata` |

The first rule that matches wins. `audit.level` accepts `Metadata` and
`RequestResponse`. The default is `Metadata`.

The files:

| Path | Content |
|---|---|
| `/etc/bedrock/audit/policy.yaml` | The policy. The agent writes it from the ConfigMap `bedrock-system/bedrock-audit-policy`. |
| `/var/log/bedrock/audit/audit.log` | The audit log. |

The API server rotates the log at 100 MB and keeps 10 old files.

`bedrock init` writes the policy and the log directory before k0s starts.
A new cluster does not restart its API server. On other clusters the agent
adds the flags to `/etc/k0s/k0s.yaml` only when the policy file and the log
directory exist. The agent never changes a flag that you set.

The API server reads the policy only when it starts. After a change of
`audit.level`, the agent writes the new file and sets
`Host.status.authn.webhookRestartPending`. The restart then waits for the
restart grant of the operator, as for the authn files. The API server is
down for about one minute during the restart.

A `RequestResponse` body can hold cloud-init data and other secrets. Keep
the audit file and the access to Loki under control when you use this
level.

## Monitoring

Bedrock installs `kube-prometheus-stack` in the namespace `monitoring`.

- Prometheus keeps data for 15 days on a volume claim of `50Gi` with the
  class `block`.
- Alertmanager reads its configuration from the Secret
  `monitoring/bedrock-alertmanager-config`.
- Grafana is not exposed outside the cluster.

Read the Grafana admin password:

```
kubectl -n monitoring get secret bedrock-grafana-admin \
  -o jsonpath='{.data.admin-password}' | base64 -d
```

Bedrock creates this Secret once with a random password. It never changes
the password afterwards. Open Grafana:

```
kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80
```

Then open `http://127.0.0.1:3000` and sign in as `admin`.

### Who can read Prometheus and Loki

The NetworkPolicy `monitoring/bedrock-metrics-readers` selects the pods with
the label `app.kubernetes.io/name` set to `prometheus` or `loki`. It
allows ingress from these sources only:

- Pods in the namespaces `monitoring` and `bedrock-system`. Grafana,
  Alertmanager, the Prometheus operator, Alloy and the Bedrock proxy use
  this path.
- The join subnet `100.64.0.0/16`. The API server and the kubelet reach a
  pod from the address of their node in this subnet. The API service proxy
  and the readiness probe of Prometheus use this path. The policies of
  `bedrock-authn` and `bedrock-proxy` use the same range.

Egress stays open. A pod of a project cannot connect to Prometheus or Loki,
because a project namespace is in neither source. Loki has no
authentication, and the series name every tenant, so this policy is the
only access control for both. When Loki is off, the second name selects no
pod.

Two dashboards come with Bedrock:

- `Bedrock / Virtual machines` (`bedrock-vms`): VM state, CPU, memory,
  network and storage. Select the project with the `project` variable.
- `Bedrock / Tenants and projects` (`bedrock-tenants`): totals and a table
  for each project, from the metering series.

The stack gives the host dashboards.

### Alertmanager webhook

Set the Setting `alertmanager.webhook-url` to send notifications to a
webhook:

```
kubectl patch setting alertmanager.webhook-url --type merge \
  -p '{"spec":{"value":"https://hooks.example.test/alert"}}'
```

The URL must use `http` or `https` and must have a host. An empty value
sends all alerts to the receiver `null`. The alerts `Watchdog` and
`InfoInhibitor` always go to `null`. A `critical` alert mutes a `warning`
alert with the same `alertname` and `namespace`.

A webhook URL can hold a token. Bedrock puts it in a Secret, but the
Setting stays readable for everyone who can read Settings. Do not put
secrets in the Setting when other people can read it.

## Metering

Bedrock records nine series for each project. Each series has the labels
`tenant` and `project`. The rules join every source series with the
labels of the project namespaces. They never match a namespace name.

| Series | Meaning | Source |
|---|---|---|
| `bedrock:project_vm_running:count` | VMs in phase running | `kubevirt_vmi_info` |
| `bedrock:project_vm_vcpu:sum` | vCPUs of the running VMs | `kubevirt_vmi_vcpu_seconds_total` |
| `bedrock:project_vm_memory_bytes:sum` | Guest memory of the running VMs | `kubevirt_vmi_memory_domain_bytes` |
| `bedrock:project_volume_requested_bytes:sum` | Requested bytes of all volume claims | `kube_persistentvolumeclaim_resource_requests_storage_bytes` |
| `bedrock:project_vm_cpu_usage_cores:rate5m` | Used CPU cores, 5 minute rate | `kubevirt_vmi_cpu_usage_seconds_total` |
| `bedrock:project_vm_network_receive_bytes:rate5m` | Received bytes per second, 5 minute rate | `kubevirt_vmi_network_receive_bytes_total` |
| `bedrock:project_vm_network_transmit_bytes:rate5m` | Sent bytes per second, 5 minute rate | `kubevirt_vmi_network_transmit_bytes_total` |
| `bedrock:project_vm_network_receive_bytes:increase1h` | Received bytes in the last hour, summed after the increase of each VM series | `kubevirt_vmi_network_receive_bytes_total` |
| `bedrock:project_vm_network_transmit_bytes:increase1h` | Sent bytes in the last hour, summed after the increase of each VM series | `kubevirt_vmi_network_transmit_bytes_total` |

### Labels

Each series has the labels `tenant` and `project`. They come from the
namespace labels `bedrock.cloudyfolks.io/tenant` and
`bedrock.cloudyfolks.io/project`. Only the operator can set these labels.
The name of the namespace is `<tenant>-<project>`. A namespace with no
`project` label has no series. Series also exist for platform projects, like
for all other projects. The consumer must drop the pairs that it does not
know.

Loki streams of project namespaces have the same two labels, as the
section Loki shows. A reader always selects `{tenant="<t>", project="<p>"}`.

### Hourly queries

A reader runs these queries as instant queries, one for each closed hour.
The parameter `time` is the end of the hour.

| Unit | Query |
|---|---|
| CPU-hours | `sum_over_time(bedrock:project_vm_vcpu:sum[1h]) / 60` |
| Memory GB-hours | `sum_over_time(bedrock:project_vm_memory_bytes:sum[1h]) / 60 / 1073741824` |
| Network GB, in | `bedrock:project_vm_network_receive_bytes:increase1h / 1073741824` |
| Network GB, out | `bedrock:project_vm_network_transmit_bytes:increase1h / 1073741824` |

The rule interval is 1 minute. This value is part of the contract. The
vCPU and memory series have one sample each minute while a VM runs, and no
sample while no VM runs. The division by 60 turns the samples into hours,
so a VM that runs 6 minutes of an hour counts 6 minutes. Do not use
`avg_over_time` for these two units: it counts a full hour for a VM that
runs 6 minutes.

The network series take the increase of each VM series first and add the
results second. A VM that ends in the middle of the hour keeps its bytes,
and a live migration counts the bytes of both series. The error is about
one scrape interval of traffic for each series and hour. The test
`TestMeteringQueriesPassPromtool` runs these queries with `promtool`.

### Read path

Prometheus and Loki have no address outside the cluster. A service of the
platform reads them through the API service proxy:

```
/api/v1/namespaces/monitoring/services/kube-prometheus-stack-prometheus:9090/proxy/api/v1/query
/api/v1/namespaces/monitoring/services/loki:3100/proxy/loki/api/v1/query_range
```

The names `kube-prometheus-stack-prometheus:9090` and `loki:3100` are part
of the contract. The reader needs the verb `get` on `services/proxy` for
these two names. Bedrock does not ship this role. The IaC of the consumer
writes it:

```
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: metering-reader
rules:
  - apiGroups: [""]
    resources: [services/proxy]
    verbs: [get]
    resourceNames: [kube-prometheus-stack-prometheus:9090, loki:3100]
```

Bind the role with a RoleBinding in the namespace `monitoring`. This role
reads the series and the logs of every tenant. Give it to platform
services only.

The NetworkPolicy of the section "Who can read Prometheus and Loki" keeps
the pods of the projects away from both services. The API service proxy
still works.

### Gaps

Prometheus keeps series for 15 days, so a reader can fill a missed hour
for 15 days. The rules run each minute while Prometheus runs. When
Prometheus is down for part of an hour, the CPU-hours and the memory
GB-hours lose the minutes of the outage, and no more. The counters keep
the traffic of the outage, so the network total of the hour stays complete
when Prometheus runs at the end of the hour. When Prometheus is down at the
end of the hour, the query answers with a sample of the last 5 minutes or
with no value. The reader treats a missing value as a gap.

Loki keeps logs for the number of days in the Setting
`loki.retention-days`.

## Troubleshooting

- `kubectl get addon <name> -o yaml` shows the conditions and the message.
- `Ready` is `False` with `ApplyFailed`: read the message. It names the
  object. A new `persistence.size` on a running StatefulSet gives this
  error.
- `Ready` is `False` with `MissingCRD`: the kind of the addon is not in the
  cluster. Check that the release has installed all groups.
- `Preflight` is `False` with `NoKVM`: check `kubectl get hosts -o
  custom-columns=NAME:.metadata.name,KVM:.status.checks.kvm`.
- `Ready` stays at `Pruning`: a pod of the addon does not end. List the
  pods with `kubectl get pods -A`.
- The audit log has no new lines after a change of `audit.level`: check
  `kubectl get host <name> -o jsonpath='{.status.authn.webhookRestartPending}'`.
  The value `true` means the API server waits for the restart grant.
- `Host.status.authn.message` starts with `audit: `: the agent did not find
  the ConfigMap `bedrock-audit-policy`, or it could not write a file.
