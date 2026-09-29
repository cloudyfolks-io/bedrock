# Check a read-only bind against the platform LDAP

Use this runbook to prove that `bedrock-authn` can bind to the platform
LDAP and search it, before staff sign in with an LDAP account on a real
cluster. The check runs from a disposable `IdentityProvider` in a test
cluster and makes no write to LDAP.

## Before you start

- A test Bedrock cluster with `bedrock-authn` running.
- A read-only LDAP bind account and its password from the LDAP
  administrator. Do not use a personal account for this bind.
- The platform LDAP host, its base DN and, if it serves LDAPS with a
  private CA, that CA certificate.
- `kubectl` access to the test cluster with `admin.conf` or an admin
  session.
- The username of one real LDAP user who agreed to be the known test
  case.

## 1. Store the bind password and create the IdentityProvider

```sh
kubectl -n bedrock-system create secret generic ldap-check-bind \
  --from-literal=bindPassword='<the read-only bind password>'
kubectl -n bedrock-system label secret ldap-check-bind \
  bedrock.cloudyfolks.io/authn=true
```

Apply an `IdentityProvider`, with every `<...>` replaced by the
platform's own values:

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: IdentityProvider
metadata:
  name: ldap-check
  namespace: bedrock-system
spec:
  type: ldap
  displayName: Platform LDAP check
  secretRef: ldap-check-bind
  caBundle: "<PEM, only if the platform LDAP serves LDAPS with a private CA>"
  ldap:
    url: ldaps://<platform LDAP host>:636
    bindDN: <the read-only bind DN>
    userSearch:
      baseDN: <the people base DN>
      usernameAttribute: uid
    groupSearch:
      baseDN: <the groups base DN>
      memberAttribute: member
      nameAttribute: cn
```

This step writes only the `IdentityProvider` and its `Secret` in the test
cluster; it makes no LDAP write.

## 2. Bind and search from inside the test cluster

```sh
password=$(kubectl -n bedrock-system get secret ldap-check-bind -o jsonpath='{.data.bindPassword}' | base64 -d)
kubectl run ldap-check --rm -i --restart=Never --image=docker.io/osixia/openldap:1.5.0 --command -- \
  ldapsearch -H ldaps://<platform LDAP host>:636 -D '<the read-only bind DN>' -w "$password" \
  -b '<the people base DN>' '(uid=<the known username>)' -o ldif-wrap=no
unset password
```

A `dn:` line for the known user proves the bind and the search both
worked. An empty answer or a bind error means the account, the host or
the network path is wrong. Either way nothing in the platform LDAP
changed, because a search makes no write.

Warning: an optional last step signs in with the known username and a
wrong password. This performs a real bind as that user against the real
platform LDAP, and it counts against the platform LDAP's own lockout
policy, not against any Bedrock lockout. Use a test account that can
tolerate a lockout, or skip this step, and run it at most once. Step 2's
`ldapsearch` alone is already proof enough.

Optionally, sign in at `https://sso.<test cluster host>/login/` with the
known username and a wrong password. Every answer looks the same,
`invalid_credentials`, whether the bind failed, the search found no
entry or more than one, or the password was simply wrong. This is by
design: the login answer never tells you which of these happened, and
never tells you whether the username exists. Step 2's `ldapsearch` is
what actually proves the bind and the search; this step only shows the
screen a real user would see.

## 3. Remove the provider

```sh
kubectl -n bedrock-system delete identityprovider ldap-check
kubectl -n bedrock-system delete secret ldap-check-bind
```

## 4. Record the result

Add one line to this plan's ledger, "Decisions recorded for this plan" in
`2026-09-28-bedrock-08-authn.md`, with the date, the LDAP host, the
result (bind and search worked, or what failed) and the operator who ran
it. The plan is not done until this line exists.
