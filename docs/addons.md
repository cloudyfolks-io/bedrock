# Addons, audit and monitoring

An addon is an optional part of Bedrock. An admin turns an addon on or off
with an `Addon` object. This page describes the addons `kata` and `loki`,
the API server audit log, the monitoring stack and the metering series.

## Addons

The `Addon` kind has no namespace. The name of the object is the name of
the addon. Bedrock creates one `Addon` for each addon it knows: `kata` and
`loki`.

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

The Settings `kata.enabled` and `loki.enabled` set the first state of the
`Addon` objects when Bedrock creates them. After that, only the `Addon`
object counts. A change of the Setting does not change a live addon.
Bedrock never changes `spec.enabled` by itself.

Bedrock applies the addon objects in a namespace. `kata` uses
`kube-system`. `loki` uses `monitoring`. Bedrock records the objects of
each addon in the ConfigMap `bedrock-system/bedrock-addon-<name>-inventory`.
Do not edit this ConfigMap. Bedrock uses it to remove the objects.

### Values

An addon can have values. Put them in `spec.values` as flat keys.

| Addon | Key | Default | Rule |
|---|---|---|---|
| `kata` | none | | |
| `loki` | `persistence.size` | `20Gi` | A positive Kubernetes quantity. |
| `loki` | `persistence.storage-class` | `block` | A DNS-1123 subdomain. |

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

The `Addon` has the conditions `Ready` and, for `kata`, `Preflight`. The
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

When you turn kata off, Bedrock first deletes the DaemonSet. The kata pods
clean the node when they stop. This can take 10 minutes. Bedrock removes
the RBAC objects and the RuntimeClasses only after the last kata pod ends.
The condition `Ready` shows the reason `Pruning` during this time.

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

When you turn Loki off, Bedrock keeps the volume claim `storage-loki-0`.
The logs stay. To delete the logs, delete the claim:

```
kubectl -n monitoring delete pvc storage-loki-0
```

When Loki is on, the Grafana of the monitoring stack gets the data source
`Loki` from the ConfigMap `loki-datasource`.

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

Prometheus does not have an address outside the cluster. A client inside
the cluster reads the series through the federation endpoint:

```
http://kube-prometheus-stack-prometheus.monitoring.svc:9090/federate?match[]={__name__=~"bedrock:project_.*"}
```

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
