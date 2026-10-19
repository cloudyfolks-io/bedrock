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

## Documentation

- [Authentication](docs/authn.md): users, groups, tokens and login.
- [Tenancy](docs/tenancy.md): tenants, projects, roles and quota.
- [Images](docs/images.md): images, uploads and the catalog.
- [Compute](docs/compute.md): virtual machines, volumes, snapshots, SSH keys and consoles.
- [Network](docs/network.md): Fabric networks, subnets, elastic IPs, load balancers, DNS zones and security groups.
- [Backup](docs/backup.md): back up and restore VMs, etcd snapshots.
- [Secrets at rest](docs/secrets-at-rest.md): Secret encryption, the key file and KMS v2.
- [Object storage](docs/object-storage.md): S3 users and buckets on the shared Ceph object store.
- [Databases](docs/database.md): managed PostgreSQL with users, logical databases, backups and restore.
- [Proxy and panel API](docs/proxy.md): the edge service for the panel and the API.
- [Panel](docs/panel.md): the web console.
- [Addons, audit and monitoring](docs/addons.md): optional addons, audit log and metering.
- [End-to-end jobs](docs/e2e.md): how the e2e jobs run and what they prove.
