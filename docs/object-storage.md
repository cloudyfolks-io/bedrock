# Object storage

## What you get

Bedrock runs one shared Ceph object store. Its name is `bedrock-rgw`.
Each `ObjectStorageUser` is one user in this store.
Each `ObjectStorageBucket` is one S3 bucket in this store.
The store speaks the S3 protocol.

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
  quota:
    bytes: 1Gi
    buckets: 5
```

The `quota` fields are optional. A value of zero, or no value, means no limit.
A negative value is refused. The user shows the reason `QuotaInvalid`.

When the user is ready, `status.credentialsSecret` holds the name of a Secret.
The Secret is in the same namespace.
It has the keys `AccessKey`, `SecretKey` and `Endpoint`.

```sh
kubectl -n acme-prod get objectstorageuser app -o jsonpath='{.status.credentialsSecret}'
kubectl -n acme-prod get secret rook-ceph-object-user-bedrock-rgw-acme-prod.app -o jsonpath='{.data.AccessKey}' | base64 -d
kubectl -n acme-prod get secret rook-ceph-object-user-bedrock-rgw-acme-prod.app -o jsonpath='{.data.SecretKey}' | base64 -d
```

The store user id is `<namespace>.<name>`. Two projects can use the same user name. They get two different store users.

## Create a bucket

A bucket belongs to one user of the same namespace.

```yaml
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ObjectStorageBucket
metadata:
  name: data
  namespace: acme-prod
spec:
  userRef: app
  versioning: true
  quota:
    bytes: 512Mi
    objects: 100000
  policy: |
    {
      "Version": "2012-10-17",
      "Statement": [
        {
          "Effect": "Allow",
          "Principal": {"AWS": ["arn:aws:iam:::user/acme-prod.other"]},
          "Action": ["s3:GetObject", "s3:ListBucket"],
          "Resource": ["arn:aws:s3:::*", "arn:aws:s3:::*/*"]
        }
      ]
    }
```

You do not choose the bucket name. The store makes a unique name from the object name.
When the bucket is ready, `status.bucketName` holds the name.
Also, `status.endpoint` holds the S3 endpoint.

The policy must be a JSON document with a `Statement` list. It must not be larger than 20480 bytes.
The principal of another Bedrock user has the form `arn:aws:iam:::user/<namespace>.<user>`.
If the policy is not valid, the bucket shows the reason `PolicyInvalid`.
If the store refuses the policy, the bucket shows the reason `PolicyRejected`.

## Use the bucket

Use path-style addressing and the region `us-east-1`.

```sh
export AWS_ACCESS_KEY_ID=<AccessKey>
export AWS_SECRET_ACCESS_KEY=<SecretKey>
export AWS_DEFAULT_REGION=us-east-1
aws configure set default.s3.addressing_style path
aws --endpoint-url http://rook-ceph-rgw-bedrock-rgw.rook-ceph.svc:80 s3 cp ./file.txt s3://<bucketName>/file.txt
```

## Delete

Delete a bucket to delete its objects. This cannot be undone.
A user that owns buckets stays until you delete all of its buckets.
During this time the user shows the reason `InUse`.

## Limits

The bucket quota is applied when the bucket is created. A later change is not applied.
The condition `QuotaApplied` shows `ChangeNotSupported`. Delete the bucket and create it again to change the quota.
You cannot change the user of a bucket. Create a new bucket.
Versioning cannot go back to never-versioned. Setting `versioning: false` suspends it.
The endpoint is in the cluster only. It uses HTTP on port 80.
A tenant VM runs in a private network. It cannot reach this endpoint.
The store uses replication. Erasure coding is not available.
Lifecycle rules, notifications and IAM are not available.
All buckets share one failure domain.

## Operations

```sh
kubectl -n rook-ceph get cephobjectstore bedrock-rgw
kubectl -n rook-ceph get pods -l app=rook-ceph-rgw
kubectl get cephobjectstoreuser -A
kubectl get objectbucketclaim -A
```
