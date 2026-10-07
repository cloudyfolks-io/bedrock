# Identity and access

`bedrock-authn` gives every user one identity for the whole cluster.
Sign in once at `https://sso.<platform.host>/login/`. Use the token
from sign-in with `kubectl`. Other Dadehat services also accept this
token.

## Platform domain

Every cluster needs a platform domain. Set it in `cluster.yaml` before
`bedrock init`:

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ClusterConfig
metadata:
  name: lab
spec:
  platform:
    host: cloud.example.com
```

The domain is required. Bedrock does not choose a domain for you. The
domain must be a valid DNS name with at least two labels. It can be
unregistered. For example, `e2e.bedrock.test` is valid.

Bedrock serves these names under the domain: `sso.`, `api.`, `console.`
and `upload.`. The domain itself is also served. Point all of them at the
cluster VIP. Use your own DNS, or add lines to `/etc/hosts` on each
client.

## Users and groups

A `User` object holds a username, a display name and an email. It also
lists the sign-in methods the user may use. It also lists the Bedrock
groups the user belongs to. A username is a DNS-1123 subdomain, such as
`alice`, or an email address.

`bedrock init` creates the first user, named `admin`. `bedrock init`
puts `admin` in the group `bedrock-admins`. A `ClusterRoleBinding` maps
the group `bedrock-admins` to the role `cluster-admin`.

A `Group` object can add members beyond a `User`'s own `spec.groups`
field. A `Group` carries a description. A `Group` can require a second
sign-in factor for every member: set `spec.requireSecondFactor: true`
on the `Group`.

Kubernetes sees every Bedrock group with the prefix `bedrock:`. For
example, Kubernetes sees the group `ops` as `bedrock:ops`. Name a
`RoleBinding` or `ClusterRoleBinding` subject the same way:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ops-edit
  namespace: team-a
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: edit
subjects:
  - kind: Group
    name: bedrock:ops
    apiGroup: rbac.authorization.k8s.io
```

## Local passwords, TOTP and recovery codes

`bedrock-authn` checks a local user's password with argon2id. The
Setting `authn.lockout-threshold` sets how many wrong attempts lock the
account. The default is 5 wrong attempts inside fifteen minutes. After
that many wrong attempts, the account locks for fifteen minutes. Each
further lock doubles the lock time, up to four hours.

During a lockout, sign-in still answers `invalid_credentials`. This is
the same answer a wrong password gets. The answer never says the
account is locked. An administrator sees the lock in the `User`'s own
status field, `status.lockedUntil`. Clear a lock with
`bedrock authn reset-password`.

`bedrock-authn` also rate-limits credential checks. Each replica allows
10 attempts per minute for each pair of client address and username.
Each replica also runs at most 4 credential checks at once. This limit
covers two login requests: the login step answer, and the login
challenge request. This limit also covers three account requests: an
account password change, an account TOTP check and account
recovery-code generation. A sign-in allows at most 5 wrong TOTP or
recovery codes. After the fifth wrong code, start the sign-in again.

Each replica allows 600 sign-in starts per minute for each client
address, as Traefik sees it. A sign-in start is a request to `/oauth/v2/authorize` or to
`/oauth/v2/device_authorization`. When an LDAP provider is on, each
replica allows 30 username steps per minute for each client address.
Each replica keeps at most 5000 open sign-ins and 1000 open device
sign-ins. Past that number, a new sign-in gets the answer
`temporarily_unavailable`. Past any other limit, the answer is
`rate_limited` with HTTP status 429. This answer never names the
account or the check that was slow.

TOTP follows RFC 6238. A TOTP code has six digits and changes every
thirty seconds. `bedrock-authn` accepts one step of clock skew, earlier
or later. `bedrock-authn` accepts each code only once.

Enrollment shows a QR code and a base32 secret. Enrollment then asks
for one valid code. Only a valid code finishes the enrollment. A QR
code shown but never confirmed enrolls nothing.

A finished enrollment then shows ten recovery codes, once. A recovery
code is a second-factor answer, not an enrollment step. Use a recovery
code in place of a TOTP code at sign-in. A recovery code cannot enroll
or re-enroll TOTP. Each recovery code works once. Enrolling TOTP again
replaces all old recovery codes.

Two more Settings control session and token lifetime. `authn.session-ttl`
sets the login session cookie lifetime. The default is `12h`.
`authn.refresh-ttl` sets the refresh token lifetime. The default is
`720h`.

## Second-factor policy

Bedrock requires a second sign-in factor in three cases. The Setting
`authn.require-second-factor` is `true`. Or the user is in a `Group`
with `spec.requireSecondFactor: true`. Or the `OAuthClient` used to
sign in has `spec.requireSecondFactor: true`.

A user may need a second factor but have none enrolled. In this case,
sign-in shows the enrollment screen at once. The user does not get a
separate step later.

An upstream OIDC provider can also satisfy this requirement: Bedrock
accepts an upstream `amr` claim of `mfa`, `otp` or `hwk`. A user who
signs in through LDAP always uses Bedrock TOTP instead, because LDAP
itself carries no `amr` claim.

## IdentityProviders

### LDAP

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: IdentityProvider
metadata:
  name: corp-ldap
  namespace: bedrock-system
spec:
  type: ldap
  displayName: Corp LDAP
  secretRef: corp-ldap-bind
  ldap:
    url: ldaps://ldap.example.com:636
    bindDN: cn=reader,dc=example,dc=com
    userSearch:
      baseDN: ou=people,dc=example,dc=com
      usernameAttribute: uid
    groupSearch:
      baseDN: ou=groups,dc=example,dc=com
      memberAttribute: member
      nameAttribute: cn
  groupMapping:
    - external: platform-ops
      group: ops
```

`bedrock-authn` reads the bind password from the key `bindPassword` in
the `Secret` named by `secretRef`. `bedrock-authn` binds with the
service account first. It then searches for the user with an escaped
filter. It then binds again as the user, with the password from
sign-in. A search that finds no entry, or more than one, fails the
same way as a wrong password.

Groups come from `groupSearch`. `bedrock-authn` binds as the service
account again before it searches for groups, so the user does not need
read access to the group entries. When `memberOfAttribute` is set
instead, groups come from that attribute on the user's own entry.
`groupMapping` turns an external group name into a Bedrock group. An
LDAP group missing from `groupMapping` grants no Bedrock membership.
Read `docs/runbooks/ldap-check.md` before you wire in the platform
LDAP.

### OIDC

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: IdentityProvider
metadata:
  name: corp-oidc
  namespace: bedrock-system
spec:
  type: oidc
  displayName: Corp SSO
  secretRef: corp-oidc-client
  oidc:
    issuer: https://sso.example.com
    clientID: bedrock
  groupMapping:
    - external: platform-ops
      group: ops
```

`bedrock-authn` reads the client secret from the key `clientSecret` in
the `Secret`. Register this redirect URI with the upstream provider:
`https://sso.<platform.host>/api/v1/login/providers/<name>/callback`.

Bedrock keeps the upstream `sub` claim in `User.status.upstreamSubject`.
Bedrock takes the username from a configured claim. The default is
`preferred_username`. Upstream groups reach Bedrock the same way LDAP
groups do: only `groupMapping` can turn an upstream group into a
Bedrock group. An upstream group missing from `groupMapping` grants no
Bedrock membership, for LDAP and for OIDC alike.

## Network exposure

The `bedrock-authn` pods carry a `NetworkPolicy`. This policy allows
HTTPS traffic from two sources only. The first source is the Traefik
pods, which carry the public `sso.<platform.host>` route. The second
source is nodes in the cluster's own join CIDR: control-plane nodes use
this path for the token-review webhook call. Nothing else can reach
`bedrock-authn` directly.

The public route also excludes `/healthz` and `/readyz`. These health
endpoints answer only inside the cluster, never on the public route.

## API tokens

Create an API token from the account page, `/login/account`. A token
has the form `brk_` plus 40 base62 characters. `bedrock-authn` stores
only the token's SHA-256 hash. You cannot recover a lost token. Create
a new one instead. A token can carry an expiry, or none. Use a token
with `kubectl` like any other bearer token:

```sh
kubectl --token=brk_... --server=https://api.<platform.host>:6443 get pods
```

Set `spec.disabled: true` on a `User` to disable it. This refuses new
logins, refresh-token requests, userinfo requests and new API tokens,
all at once. An access token already issued keeps working until it
expires, in one hour. A JWT access token carries its own signature and
expiry. kube-apiserver checks only that signature and expiry. It never
reads the `User` object to check `disabled`.

## `bedrock login` and kubeconfig

```sh
bedrock login --server https://sso.<platform.host>
```

By default, this runs the device flow. It prints a URL and a short
code. Open the URL on any device, sign in and enter the code. Add
`--browser` to open a local browser instead and complete an
authorization-code exchange on a loopback port. Either way,
`bedrock login` caches the tokens under `~/.config/bedrock/tokens/` and
refreshes them until you revoke them.

```sh
bedrock login --server https://sso.<platform.host> --write-kubeconfig
```

This command writes a context named `bedrock` into `~/.kube/config`.
Use `--write-kubeconfig=<path>` to write another file instead. The
written user config runs
`bedrock login --server <issuer> --exec-credential` for every `kubectl`
call, plus `--ca-file <path>` when the login used one. This way,
`kubectl` always uses a fresh access token, and nothing long-lived sits
in the kubeconfig file itself.

`--write-kubeconfig=false` skips this write. This is also the default
when you leave the flag out. When the target path is a symlink,
`bedrock login` follows it and writes the merged kubeconfig at the
symlink's target, not over the symlink itself.

## Token exchange for other services

A Dadehat service may want to trust a Bedrock sign-in so the user does
not sign in twice. Register an `OAuthClient` with the grant type
`urn:ietf:params:oauth:grant-type:token-exchange` and the client's own
audiences:

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: OAuthClient
metadata:
  name: dadehat-service
  namespace: bedrock-system
spec:
  clientID: dadehat-service
  public: false
  redirectURIs: []
  grantTypes:
    - urn:ietf:params:oauth:grant-type:token-exchange
  secretRef: dadehat-service-client
  tokenExchange:
    audiences:
      - dadehat-service
```

The service sends a user's Bedrock access token as `subject_token`,
with its own client credentials, to `/oauth/v2/token`. Bedrock returns
a new access token for the service's own audience. The new token
carries the same subject and groups, plus an `act` claim naming the
calling client. The new token lives no longer than the original and
carries no refresh token.

## `bedrock authn reset-password`

Run this on a controller, with `admin.conf` available:

```sh
bedrock authn reset-password <username>
```

This command prints a new password once, clears any lock on the user,
and revokes the user's refresh tokens and sessions. It refuses LDAP and
OIDC users, because their password lives upstream, not in Bedrock.

## Upgrade from an earlier release

A cluster from a release without Bedrock authn has no `admin` user after
the upgrade. The operator creates the platform CA and the webhook token.
The agent on each controller then adds the kube-apiserver flags. Then
the agent waits for a restart grant from the operator. The operator
gives one grant at a time, so only one `k0scontroller` restarts at a
time. A grant is the annotation `bedrock.cloudyfolks.io/authn-restart` on
the `Host`. It holds the grant time and lasts 15 minutes. The operator
removes it when the restart is done or the time is over.

1. Wait until the upgrade is complete.
2. On one controller, run this command once:

   ```sh
   bedrock authn create-admin
   ```

3. Keep the password. The command prints it only one time.
4. Sign in as `admin` with `bedrock login --server https://sso.<host>`.

If `admin` already has a password, the command prints `admin exists` and
changes nothing. To get a new password, use
`bedrock authn reset-password admin`.

## Changing `platform.host`

Change the value with this command:

```sh
kubectl patch setting platform.host --type merge -p '{"spec":{"value":"<domain>"}}'
```

A cluster that was installed before the domain became required can have
an empty `platform.host`. For such a cluster, the platform and authn
components report `PlatformHostNotSet` and render nothing. Run the
command above to fix it. `bedrock upgrade` refuses to start until
`platform.host` is set.

kube-apiserver reloads `authentication.yaml` on every change, so a new
`platform.host` value takes effect for token verification at once.
kube-apiserver reads `webhook.kubeconfig` only when it starts. A change
there needs a restart of `k0scontroller`. The operator grants the
restarts one at a time, and the agent on each controller restarts
`k0scontroller` when it holds a grant. Until that restart,
`Host.status.authn.webhookRestartPending` is `true` on the affected
controllers, and `brk_` API tokens on those controllers keep using the
previous webhook URL. JWT access tokens are not affected, because their
issuer check follows the reloaded file at once.

Known limit: the webhook bearer token never rotates after `bedrock init`
creates it. Changing `platform.host` changes only the webhook URL, not
the bearer. Bedrock does not yet support rotating the bearer token
itself.
