# Object storage

## What you get

Bedrock runs one shared Ceph object store. Its name is `bedrock-rgw`.
The store speaks the S3 protocol and the IAM protocol.

Each project has one account in the store.
Bedrock makes the account when the first user or bucket of the project appears.
Bedrock keeps the root key of the account in the `rook-ceph` namespace.
A project member cannot read this key.

Each `ObjectStorageUser` is one IAM user of the account of its project.
A user has one key pair and one inline policy.
Each `ObjectStorageBucket` is one S3 bucket of the account.
A bucket name is unique in the whole store.

## Create a user

Create the user in a project namespace.

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ObjectStorageUser
metadata:
  name: app
  namespace: acme-prod
spec:
  displayName: App
  permission: readWrite
  buckets: [data, logs]
```

`permission` is required. It is `read` or `readWrite`.
`buckets` lists names of `ObjectStorageBucket` objects in the same namespace.
An empty list, or no list, gives access to every bucket of the project.
The user sees a new bucket after Bedrock rebuilds its policy.
The user loses a bucket when the bucket goes away.
Set `suspended: true` to block the keys of the user. Set it to `false` to restore them.

When the user is ready, `status.credentialsSecret` holds the name of a Secret.
The name is `<user name>-s3`. The Secret is in the same namespace.
It has the keys `AccessKey`, `SecretKey` and `Endpoint`.

```sh
kubectl -n acme-prod get secret app-s3 -o jsonpath='{.data.AccessKey}' | base64 -d
kubectl -n acme-prod get secret app-s3 -o jsonpath='{.data.SecretKey}' | base64 -d
```

The store user id is `<namespace>.<name>`. Two projects can use the same user name.

### The principal

`status.arn` holds the IAM principal of the user.
It has the form `arn:aws:iam::<account id>:user/<namespace>.<name>`.
Use it in a bucket policy of a bucket in the same account.

### The policy of a user

Bedrock writes one inline policy named `access`.
The first statement allows `s3:ListAllMyBuckets`.
The second statement allows the read actions, or the read and write actions, on the buckets in scope.
You cannot edit this policy. Bedrock replaces it when it differs from the spec.

## Create a bucket

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ObjectStorageBucket
metadata:
  name: data
  namespace: acme-prod
spec:
  bucketName: acme-data
  versioning: true
  corsOrigins: ["https://app.example.com"]
  forceDestroy: false
  quota:
    bytes: 512Mi
    objects: 100000
```

`bucketName` is optional and you cannot change it.
It has 3 to 63 characters: lowercase letters, digits and hyphens.
It starts and ends with a letter or a digit.
Without `bucketName`, the bucket name is `<namespace>-<name>`, cut to 63 characters.
When the bucket is ready, `status.bucketName` holds the name and `status.endpoint` holds the S3 endpoint.

A bucket that exists in your account is adopted. Bedrock does not make it again.

Bedrock refuses a bucket name in these cases.

| Reason | Meaning |
|---|---|
| `NameInvalid` | The name does not follow the S3 rules. |
| `NameReserved` | The name is reserved. See the Settings below. |
| `NameTaken` | Another project, or another bucket of this project, has the name. |
| `BucketLimit` | The project already holds `objectstorage.max-buckets` buckets. |

The quota applies after create and when you change it.
The condition `QuotaApplied` shows `Applied` when the store has the values.
`status.usage` shows `bytes`, `objects` and `sampledAt`. Bedrock refreshes it every 5 minutes.

`corsOrigins` and the Setting `objectstorage.cors-origins` are merged into the CORS rules of the bucket.

You can add a bucket `policy`. It must be a JSON document with a `Statement` list.
It must not be larger than 20480 bytes.
If the policy is not valid, the bucket shows the reason `PolicyInvalid`.
If the store refuses the policy, the bucket shows the reason `PolicyRejected`.

## Settings

| Setting | Default | Use |
|---|---|---|
| `objectstorage.reserved-bucket-names` | `admin` | Names that tenants cannot use. |
| `objectstorage.reserved-bucket-prefixes` | empty | Prefixes that tenants cannot use. |
| `objectstorage.cors-origins` | empty | Web origins that every bucket allows. |
| `objectstorage.max-buckets` | `100` | Buckets for each project. |
| `objectstorage.public-host` | empty | DNS name of the public HTTPS endpoint. |
| `objectstorage.public-aliases` | empty | More DNS names for the public endpoint. |

A user of the group `bedrock:bedrock-admins` can make a bucket with a reserved name.
The administrator sets the annotation `bedrock.cloudyfolks.io/reserved-name-approved: "true"` on the bucket.

## Use the bucket

Use path-style addressing and the region `us-east-1`.

```sh
export AWS_ACCESS_KEY_ID=<AccessKey>
export AWS_SECRET_ACCESS_KEY=<SecretKey>
export AWS_DEFAULT_REGION=us-east-1
aws configure set default.s3.addressing_style path
aws --endpoint-url <Endpoint> s3 cp ./file.txt s3://<bucketName>/file.txt
```

## Public endpoint

Without `objectstorage.public-host`, the endpoint is `http://rook-ceph-rgw-bedrock-rgw.rook-ceph.svc:80`.
It works inside the cluster only.

With `objectstorage.public-host`, Bedrock makes a certificate and a Traefik route for the host and its aliases.
The endpoint in `status.endpoint` is `https://<public-host>`.
With aliases, the route also accepts `<bucket>.<host>` for virtual-host addressing.
Bedrock never exposes the admin API.

## Delete

Delete a user to remove its keys and its policy.
A project keeps its account until you delete the project.

A bucket with objects stays in `Terminating`.
Its condition `Ready` shows the reason `NotEmpty` and the number of objects.
Set `forceDestroy: true` to delete the objects and the bucket.
Deleting a project deletes all of its buckets with their objects.
This cannot be undone.

## Objects of an older release

An older release made users and buckets with a different model.
These objects show `Ready=False` with the reason `LegacyUser` or `LegacyBucket`.
Bedrock does not delete their data.
Delete the object and create it again to use the new model.

## Limits

You cannot change the name of a bucket. Create a new bucket.
Versioning cannot go back to never-versioned. Setting `versioning: false` suspends it.
A tenant VM runs in a private network. It cannot reach the in-cluster endpoint.
The store uses replication. Erasure coding is not available.
Lifecycle rules and notifications are not available.
All buckets share one failure domain.

## Operations

```sh
kubectl -n rook-ceph get cephobjectstore bedrock-rgw
kubectl -n rook-ceph get pods -l app=rook-ceph-rgw
kubectl -n rook-ceph get cephobjectstoreaccount
kubectl -n rook-ceph get cephobjectstoreuser
```
