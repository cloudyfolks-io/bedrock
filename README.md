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

The domain is required. It does not need to be registered. See
`docs/authn.md` for the names Bedrock serves under it.

## Identity

Read `docs/authn.md` for users, groups, LDAP and OIDC, API tokens,
`bedrock login` and token exchange.
