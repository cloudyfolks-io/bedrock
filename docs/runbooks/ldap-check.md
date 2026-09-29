# Check a read-only bind against the platform LDAP

Use this runbook before staff sign in with an LDAP account on a real
cluster. It proves that `bedrock-authn` can bind to the platform LDAP
and search it. The check runs from a disposable `IdentityProvider` in a
test cluster. It makes no write to LDAP.

## Before you start

- Get a test Bedrock cluster with `bedrock-authn` running.
- Get a read-only LDAP bind account and its password from the LDAP
  administrator. Do not use a personal account for this bind.
- Get the platform LDAP host and its base DN.
- If the platform LDAP serves LDAPS with a private CA, get that CA
  certificate too.
- Get `kubectl` access to the test cluster, with `admin.conf` or an
  admin session.
- Get the username of one real LDAP user who agreed to be the known
  test case.

## 1. Store the bind password and create the IdentityProvider

```sh
kubectl -n bedrock-system create secret generic ldap-check-bind \
  --from-literal=bindPassword='<the read-only bind password>'
kubectl -n bedrock-system label secret ldap-check-bind \
  bedrock.cloudyfolks.io/authn=true
```

Apply an `IdentityProvider`. Replace every `<...>` with the platform's
own values:

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

This step writes only the `IdentityProvider` and its `Secret`, in the
test cluster. It makes no LDAP write.

## 2. Bind and search from inside the test cluster

```sh
password=$(kubectl -n bedrock-system get secret ldap-check-bind -o jsonpath='{.data.bindPassword}' | base64 -d)
kubectl run ldap-check --rm -i --restart=Never --image=docker.io/osixia/openldap:1.5.0 --command -- \
  ldapsearch -H ldaps://<platform LDAP host>:636 -D '<the read-only bind DN>' -w "$password" \
  -b '<the people base DN>' '(uid=<the known username>)' -o ldif-wrap=no
unset password
```

A `dn:` line for the known user proves the bind and the search both
worked. An empty answer, or a bind error, means the account, the host
or the network path is wrong. Either way, nothing in the platform LDAP
changed, because a search makes no write.

This step alone is proof enough. The step below is optional.

### Optional: watch a real sign-in

Read this warning before you try this step.

This step signs in with the known username and a wrong password. It
performs a real bind as that user, against the real platform LDAP. It
counts against the platform LDAP's own lockout policy, not against any
Bedrock lockout. Use a test account that can tolerate a lockout, or
skip this step. Run this step only once, so you do not lock the
account.

Sign in at `https://sso.<test cluster host>/login/`. Use the known
username and a wrong password. Every answer looks the same,
`invalid_credentials`. This is true whether the bind failed, the search
found no entry or more than one, or the password was simply wrong. This
is by design. The login answer never tells you which of these happened.
The login answer never tells you whether the username exists.

## 3. Remove the provider

```sh
kubectl -n bedrock-system delete identityprovider ldap-check
kubectl -n bedrock-system delete secret ldap-check-bind
```

## 4. Record the result

Add one line to this plan's ledger, "Decisions recorded for this plan"
in `2026-09-28-bedrock-08-authn.md`. Record the date, the LDAP host, the
result, and the operator who ran it. State the result as "bind and
search worked" or state what failed. The plan is not done until this
line exists.
