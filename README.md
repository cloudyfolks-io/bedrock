# sCloud Bedrock

A private cloud platform that installs on your existing GNU/Linux hosts.
Virtual machines, tenant networks, distributed storage and identity on
Kubernetes, with one binary and one upgrade path.

Status: pre-release. Follow the milestones in the GitHub project.

## Build

    make build
    make test

## Platform domain

`bedrock init` needs a platform domain in `cluster.yaml`:

    spec:
      platform:
        host: cloud.example.com

The domain is required. It does not need to be registered. It must
follow these rules:

- It uses only lowercase letters, digits and hyphens, with dots between
  the labels.
- It has at least two labels.
- Each label has 1 to 63 characters. A label does not start or end with
  a hyphen.
- It has at most 253 characters.
- It is not an IP address.

See `docs/authn.md` for the names Bedrock serves under it.

## Identity

Read `docs/authn.md` for users, groups, LDAP and OIDC, API tokens,
`bedrock login` and token exchange.
