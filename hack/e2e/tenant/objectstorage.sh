rgw_endpoint=http://rook-ceph-rgw-bedrock-rgw.rook-ceph.svc:80

object_user_secret() {
  echo "rook-ceph-object-user-bedrock-rgw-$NS.$1"
}

object_user_key() {
  kubectl -n "$NS" get secret "$(object_user_secret "$1")" -o jsonpath="{.data.$2}" | base64 -d
}

s3_job() {
  local job=$1 user=$2 bucket=$3 versioning=$4
  ENDPOINT=$rgw_endpoint BUCKET=$bucket ACCESS_KEY=$(object_user_key "$user" AccessKey) SECRET_KEY=$(object_user_key "$user" SecretKey) \
    WANT_VERSIONING=$versioning envsubst '${ENDPOINT} ${BUCKET} ${ACCESS_KEY} ${SECRET_KEY} ${WANT_VERSIONING}' \
    <"test/e2e/tenant/fixtures/$job.yaml" | kubectl apply -f -
}

objectstorage_user_and_bucket() {
  local user
  wait_jsonpath 600 Ready -n rook-ceph get cephobjectstore bedrock-rgw -o jsonpath='{.status.phase}'
  kubectl -n rook-ceph get svc rook-ceph-rgw-bedrock-rgw
  expect_equal rook-ceph.ceph.rook.io/bucket "$(kubectl get storageclass bucket -o jsonpath='{.provisioner}')" "bucket provisioner"
  for user in app other; do
    kubectl -n "$NS" apply -f - <<USER
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ObjectStorageUser
metadata:
  name: $user
spec:
  displayName: $user
  quota:
    bytes: 1Gi
    buckets: 5
USER
    wait_condition 300 objectstorageuser "$user" Ready True -n "$NS"
    expect_equal "$(object_user_secret "$user")" "$(kubectl -n "$NS" get objectstorageuser "$user" -o jsonpath='{.status.credentialsSecret}')" "credentials secret of $user"
  done
  expect_equal 5 "$(kubectl -n "$NS" get cephobjectstoreuser "$NS.app" -o jsonpath='{.spec.quotas.maxBuckets}')" "user bucket quota"
  test "$(object_user_key app AccessKey)" != "$(object_user_key other AccessKey)"
  kubectl -n "$NS" apply -f - <<BUCKET
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ObjectStorageBucket
metadata:
  name: data
spec:
  userRef: app
  versioning: true
  quota:
    bytes: 512Mi
BUCKET
  wait_condition 300 objectstoragebucket data Ready True -n "$NS"
  expect_contains "$(kubectl -n "$NS" get objectstoragebucket data -o jsonpath='{.status.bucketName}')" data- "bucket name"
  expect_equal "$rgw_endpoint" "$(kubectl -n "$NS" get objectstoragebucket data -o jsonpath='{.status.endpoint}')" "bucket endpoint"
  expect_equal "$NS.app" "$(kubectl -n "$NS" get objectbucketclaim data -o jsonpath='{.spec.additionalConfig.bucketOwner}')" "bucket owner"
}

objectstorage_roundtrip() {
  local bucket
  bucket=$(kubectl -n "$NS" get objectstoragebucket data -o jsonpath='{.status.bucketName}')
  kubectl apply -f - <<NAMESPACE
apiVersion: v1
kind: Namespace
metadata:
  name: objectstorage-e2e
NAMESPACE
  s3_job s3-roundtrip app "$bucket" true
  kubectl -n objectstorage-e2e wait --for=condition=complete job/s3-roundtrip --timeout=300s
  s3_job s3-denied other "$bucket" false
  kubectl -n objectstorage-e2e wait --for=condition=complete job/s3-denied --timeout=300s
}
