# Proxy

## What the proxy does

The proxy is the one door to the Kubernetes API for the panel and for scripts.
It runs as the Deployment `bedrock-proxy` in the namespace `bedrock-system`.
It uses the same image as the operator.
Start it with `bedrock proxy serve`.

The proxy does these jobs:

- It signs browsers in through `bedrock-authn`.
- It serves the panel files.
- It sends each Kubernetes request to the API server with the identity of the user.
- It mounts the subresource handlers of other areas.
- It answers batches of access checks.
- It streams watches over one WebSocket.
- It sends named range queries to Prometheus.

The proxy never uses its own identity for a user request.
The API server decides what a user may do.

## Names

The proxy answers on three host names.
Replace `<host>` with the Setting `platform.host`.

| Host | Use |
|---|---|
| `console.<host>` | The panel and the browser API. |
| `api.<host>` | The Kubernetes API for scripts. Bearer tokens only. |
| `<host>` | Answers `307` and sends the client to `console.<host>`. |

Any other host name gets `421`.
When `platform.host` is empty, the proxy answers `503` with the code `platform_host_not_set`.
The paths `/healthz` and `/readyz` answer on every host.

Public DNS must point the three names to the cluster address.
In a lab, add the names to `/etc/hosts`.

## Sign in and sign out

A browser starts at `/oauth/login`.
The proxy sends the browser to `bedrock-authn` with the code flow and PKCE.
After the sign-in, authn sends the browser to `/oauth/callback`.
The proxy checks the state, takes the tokens, and sets the session cookie.
Then it sends the browser to the page that the user asked for.

The query value `returnTo` must be a local path.
The proxy replaces any other value with `/`.

The sign-out is `POST /oauth/logout`.
It needs the header `X-CSRF-Token`.
It revokes the refresh token, deletes the session record, and clears the cookie.
It does not end the single sign-on session of authn.
The user can sign in again at the next visit without a password while that session lives.

## Cookies and session records

The session cookie is `__Host-bedrock_console`.
It is `HttpOnly`, `Secure` and `SameSite=Lax`.
It has no `Domain`.
The login cookie is `__Host-bedrock_oauth`.
It lives for 10 minutes.

The proxy keeps one record for each session in a Secret of the namespace `bedrock-proxy`.
The Secret name is `ps-` and the SHA-256 hash of the cookie value.
A login record has the name `pl-` and the hash of the state.
Run this command to see the records:

```
kubectl -n bedrock-proxy get secrets -l bedrock.cloudyfolks.io/kind=ProxySession
```

Delete a Secret to end that session.

The proxy checks the authn `Session` and the `User` of each record every 15 seconds.
A deleted session or a disabled user ends the proxy session at once.
The proxy keeps the access token in the record.
It runs the refresh grant only when less than 60 seconds remain.
One replica holds a claim on the record while it refreshes.
The other replicas wait and read the new record.
A sweeper deletes expired records every minute.

## Use the API from scripts

Send a bearer token to `api.<host>`.
The token is an access token or an API token that starts with `brk_`.
Run `bedrock login` to get a token.

`GET /kubeconfig` returns a kubeconfig file for the cluster.
`GET /ca.crt` returns the public CA of the platform.
Both paths need no sign-in.
The CA path answers `404` when the platform uses a public certificate.

The API host sets no CORS headers.
A browser page on another origin cannot read the answers.

## Subresources

Other areas register handlers for paths like these:

```
/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/<ns>/<plural>/<name>/<sub>
```

The proxy takes `<plural>` from the path.
It looks up the handler with `<plural>` and `<sub>`.
`<sub>` can hold one slash, for example `console/vnc`.
A path with no handler goes to the API server.

Before a handler runs, the proxy asks the API server if the caller may do the action.
The verb comes from the method:

| Request | Verb |
|---|---|
| `GET` | `get` |
| `POST`, `PUT`, `PATCH` | `update` |
| `DELETE` | `delete` |

These pairs are different:

- `start`, `stop`, `restart`, `migrate` and `snapshot` with `POST` on `virtualmachines` use `update`.
- `snapshot` with `GET`, `console/vnc` and `console/serial` on `virtualmachines` use `get`.
- `upload` on `images` uses `create`.

The roles of the projects must grant these verbs.
The handler gets the client of the proxy service account.
The handler does no access check of its own.
A handler that needs more rights ships a ClusterRole with the label `bedrock.cloudyfolks.io/aggregate-to-proxy: "true"`.
The proxy role collects every ClusterRole with this label.

## Access checks and visible projects

`POST /proxy/v1/authz` takes a list of checks.
Each check has a verb, a resource, and optional group, subresource, namespace and name.
The list holds at most 100 checks.
The answer lists one result for each check in the same order.

`GET /proxy/v1/projects` returns the projects that the user can see.
A user sees a project when the user can list projects in the cluster.
A user also sees a project when the user can list virtual machines in the namespace of the project.
The proxy reads the projects in pages of 500 and checks every project.
It keeps each access answer for 30 seconds.

`GET /proxy/v1/session` returns the user, the groups, the CSRF token, the end time and the platform host.
It never returns a token.

## Live updates

`GET /proxy/v1/stream` opens a WebSocket with the subprotocol `bedrock.stream.v1`.
The browser must send the header `Origin` with the value `https://console.<host>`.
Each text frame holds one JSON object.

The client sends these frames:

- `subscribe` with an `id`, `group`, `version`, `resource`, optional `namespace` and selectors, and a `resourceVersion`.
- `unsubscribe` with the `id`.
- `ping`.

The server sends these frames:

- `hello` with the limits and the end time of the session.
- `subscribed`.
- `event` with `ADDED`, `MODIFIED` or `DELETED` and the full object.
- `bookmark` with a `resourceVersion`.
- `relist` when the API server says the version is too old.
- `error` with a code.
- `pong`.

The proxy streams only the API group `bedrock.cloudyfolks.io`.
Secrets never travel over the stream.
The proxy opens each watch with the token of the user.
It never shares a watch between users.
When the API server ends a watch, the proxy opens it again from the last version.

The server closes the socket with these codes:

| Code | Meaning |
|---|---|
| `4401` | The session ended. Sign in again. |
| `4400` | The client broke the protocol. |
| `1013` | The client reads too slowly. Connect again and resume. |
| `1001` | The proxy shuts down. |

## Metrics

`GET /proxy/v1/metrics/range` sends a named query to Prometheus.
The query parameters are `query`, `namespace`, `name`, `start`, `end` and `step`.
The proxy never sends a free query string.
It checks the access of the caller on the object that the query names.

| Query | Needs |
|---|---|
| `vm.cpu` | `namespace`, `name` |
| `vm.memory` | `namespace`, `name` |
| `vm.network.rx` | `namespace`, `name` |
| `vm.network.tx` | `namespace`, `name` |
| `vm.disk.read` | `namespace`, `name` |
| `vm.disk.write` | `namespace`, `name` |
| `volume.used` | `namespace`, `name` |
| `volume.capacity` | `namespace`, `name` |
| `host.cpu` | `name` |
| `host.memory` | `name` |
| `host.network.rx` | `name` |
| `host.network.tx` | `name` |
| `ceph.used` | nothing |
| `ceph.capacity` | nothing |

Set the Prometheus address with the flag `--prometheus-url`.
The default is `http://prometheus-operated.monitoring.svc:9090`.
When Prometheus does not answer, the proxy returns `503` with the code `metrics_unavailable`.
Check the series names against the real exporters when Prometheus runs.
The table is in `internal/proxy/metrics.go`.

## Limits

| Item | Limit |
|---|---|
| Connections for each user | 8 |
| Subscriptions for each connection | 64 |
| Client frame | 16 KiB |
| Frames in the send queue | 1024 |
| Checks in one batch | 100 |
| Reviews in parallel | 8 |
| Cached access answers | 20000, 30 seconds each |
| Stream connections in total | 2000 |
| Queued stream bytes for each connection | 8 MiB |
| Query range | 31 days |
| Points in one query | 1500 |
| Login starts and callbacks for each client address | 30 for each minute |

## Security rules

- A request with the cookie that changes state needs the header `X-CSRF-Token`.
- The proxy refuses a `Sec-Fetch-Site: cross-site` request that changes state.
- A WebSocket upgrade and a state change need `Origin` equal to `https://console.<host>`.
- A cookie request ignores any `Authorization` header.
- A console request loses all `Impersonate-*` headers.
- The proxy never sends its own service account token to the API server for a user request.
- The client address in the audit log is the last hop of `X-Forwarded-For`.
- No token is in a URL or in a log line.
- The cookie uses the `__Host-` prefix.
- The panel answers with a content security policy and no framing.

## Troubleshooting

Check the condition `ProxyReady` on the Cluster:

```
kubectl get cluster cluster -o jsonpath='{.status.conditions[?(@.type=="ProxyReady")]}'
```

| Reason | Action |
|---|---|
| `ProxyNotInstalled` | Wait for the Deployment `bedrock-proxy`. |
| `PlatformHostNotSet` | Set the Setting `platform.host`. |
| `WaitingForPlatformCA` | Wait for the platform CA Secret. |
| `Progressing` | Wait for the pods to become ready. |
| `Ready` | No action. |

A `421` answer means the host name is wrong.
Use one of the three names.

Read the logs of the proxy:

```
kubectl -n bedrock-system logs deploy/bedrock-proxy
```
