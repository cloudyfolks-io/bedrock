# Identity and access

`bedrock-authn` gives every user one identity across the cluster: sign in
once at `https://sso.<platform.host>/login/`, use `kubectl` with the
token that follows, or call another Dadehat service with a token it
accepts.

## Users and groups

A `User` holds a username, a display name, an email, the methods it may
use to sign in, and the Bedrock groups it belongs to. A username is
either a DNS-1123 subdomain, such as `alice` or `t.farahani`, or an
email address. `bedrock init` creates the first user, `admin`, in the
group `bedrock-admins`, which a `ClusterRoleBinding` maps to
`cluster-admin`.

A `Group` adds members beyond a `User`'s own `spec.groups`, carries a
description, and can require a second sign-in factor for every member
with `spec.requireSecondFactor`. Kubernetes sees every Bedrock group as
`bedrock:<name>`, so a `RoleBinding` or `ClusterRoleBinding` names it the
same way:

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

A local user's password is checked with argon2id. After
`authn.lockout-threshold` wrong attempts in fifteen minutes (default 5)
the account locks for fifteen minutes. Each further lock doubles this
time, up to four hours. During a lockout the sign-in answer still says
`invalid_credentials`, the same answer a wrong password gets. The answer
never says the account is locked. An administrator sees the lock in the
`User`'s own status (`status.lockedUntil`) and clears it with
`bedrock authn reset-password`.

`bedrock-authn` also rate-limits credential checks. Each replica allows
10 attempts per minute per client IP. Each replica also runs at most 4
credential checks at once. This gate covers the login step answer and
the login challenge request, plus the account password change, the
account TOTP verify, and account recovery-code generation. Past either
limit the answer is `rate_limited`. This answer never hints at which
account or which check was slow.

TOTP follows RFC 6238: six digits, thirty-second steps, one step of
clock skew either way, each code accepted once. Enrollment shows a QR
code and a base32 secret and asks for one valid code. Only that valid
code finishes the enrollment. A QR code shown but never confirmed
enrolls nothing. A successful enrollment then shows ten recovery codes
once. A recovery code is a second-factor answer, not an enrollment step.
It stands in for a TOTP code at sign-in. It cannot enroll or re-enroll
TOTP itself. Each recovery code works once, until it is used or replaced
by enrolling TOTP again.

Two more Settings control session and token lifetime:
`authn.session-ttl` sets the login session cookie lifetime (default
`12h`), and `authn.refresh-ttl` sets the refresh token lifetime (default
`720h`).

## Second-factor policy

A second factor is required when any of these is true: the Setting
`authn.require-second-factor` is `true`; the user is in a `Group` with
`spec.requireSecondFactor: true`; or the `OAuthClient` used to sign in
has `spec.requireSecondFactor: true`. A user who is required to add a
factor but has none enrolled gets the enrollment screen inside the same
sign-in, not a separate step later. For a user federated through an
upstream OIDC provider, an upstream `amr` claim of `mfa`, `otp` or `hwk`
also satisfies the requirement; a user federated through LDAP always
uses Bedrock TOTP for the second factor, because LDAP itself carries no
`amr`.

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

The `Secret` named by `secretRef` holds the bind password under the key
`bindPassword`. `bedrock-authn` binds with the service account, searches
for the user with an escaped filter, then binds again as the user with
the password given at sign-in; a search that returns no entry or more
than one fails the same way as a wrong password. Groups come from
`groupSearch` or, when `memberOfAttribute` is set instead, from that
attribute on the user's own entry. `groupMapping` turns an external
group name into a Bedrock group; an LDAP group not listed there is
ignored, so it grants no Bedrock membership. See
`docs/runbooks/ldap-check.md` before wiring in the platform LDAP.

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

The `Secret` holds the client secret under the key `clientSecret`. The
redirect URI to register with the upstream provider is always
`https://sso.<platform.host>/api/v1/login/providers/<name>/callback`.
Bedrock keeps the upstream `sub` in `User.status.upstreamSubject` and
takes the username from the configured claim (`preferred_username` by
default). Upstream groups reach Bedrock the same way LDAP groups do:
only through this `IdentityProvider`'s own `groupMapping`. This applies
to both providers. An upstream group not listed in `groupMapping` grants
no Bedrock membership, whether it comes from LDAP or from an OIDC
provider's own groups claim.

## Network exposure

The `bedrock-authn` pods carry a `NetworkPolicy`. It accepts HTTPS
traffic only from two sources: the Traefik pods, which carry the public
`sso.<platform.host>` route, and nodes in the cluster's own join CIDR,
which run kube-apiserver's webhook calls for token review. Nothing else
in the cluster, and nothing outside it, can reach `bedrock-authn`
directly. The public route itself excludes `/healthz` and `/readyz`.
Those health endpoints answer only inside the cluster, never on the
public route.

## API tokens

A user creates an API token from the account page, `/login/account`. Its
format is `brk_` followed by 40 base62 characters; only its SHA-256 is
stored, so a lost token cannot be recovered, only replaced. A token can
carry an expiry or none, and `kubectl` uses it exactly like any other
bearer token:

```sh
kubectl --token=brk_... --server=https://api.<platform.host>:6443 get pods
```

Disabling a user (`spec.disabled: true`) refuses that user's new logins,
refresh-token requests, userinfo requests and new API tokens, all at
once. An access token already issued keeps working until its own hour
is up, because a JWT access token is verified only by its own signature
and expiry, not by asking Bedrock whether the user is still enabled.

## `bedrock login` and kubeconfig

```sh
bedrock login --server https://sso.<platform.host>
```

By default this runs the device flow: it prints a URL and a short code,
you open the URL on any device, sign in and enter the code. `--browser`
instead opens a local browser and completes an authorization-code
exchange on a loopback port. Either way, `bedrock login` caches the
tokens under `~/.config/bedrock/tokens/` and refreshes them until they
are revoked.

```sh
bedrock login --server https://sso.<platform.host> --write-kubeconfig
```

writes a context named `bedrock` into `~/.kube/config` (another file with
`--write-kubeconfig=<path>`) whose user runs
`bedrock login --server <issuer> --exec-credential` for every `kubectl`
call (plus `--ca-file <path>` when the login used one). This way, a
fresh access token is always used. Nothing long-lived sits in the
kubeconfig file itself. `--write-kubeconfig=false` skips this write. It
is also the default when you leave the flag out. When the target path
is a symlink, `bedrock login` follows it. It writes the merged
kubeconfig at the symlink's target, not over the symlink itself.

## Token exchange for other services

A Dadehat service that wants to trust a Bedrock sign-in without asking
the user to sign in twice registers an `OAuthClient` with the grant type
`urn:ietf:params:oauth:grant-type:token-exchange` and its own audiences:

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

The service then sends a user's Bedrock access token as `subject_token`
with its own client credentials to `/oauth/v2/token`, and gets back a
new access token for its own audience, with the same subject and groups
and an `act` claim naming the calling client. The new token lives no
longer than the original and carries no refresh token.

## `bedrock authn reset-password`

On a controller, with `admin.conf` available:

```sh
bedrock authn reset-password <username>
```

prints a new password once, clears any lock, and revokes that user's
refresh tokens and sessions. It refuses LDAP and OIDC users, because
their password lives upstream, not in Bedrock.

## Changing `platform.host`

kube-apiserver reloads `authentication.yaml` on every change, so a new
`platform.host` takes effect for token verification at once. It reads
`webhook.kubeconfig` only when it starts, so a change there needs a
rolling restart of `k0scontroller`; until that restart,
`Host.status.authn.webhookRestartPending` is `true` on the affected
controllers, and `brk_` API tokens on them keep using the previous
webhook URL until the restart happens. JWT-based access tokens are not
affected, because their issuer check follows the reloaded file.

Known limit: the webhook bearer token itself never rotates after
`bedrock init` creates it. Changing `platform.host` changes only the
webhook URL, not the bearer. Rotating the bearer token itself is not yet
supported.
