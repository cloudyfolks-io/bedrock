rgw_endpoint=http://rook-ceph-rgw-bedrock-rgw.rook-ceph.svc:80
s3_image=docker.io/amazon/aws-cli:2.37.10
applied_quota_annotation='{.metadata.annotations.bedrock\.cloudyfolks\.io/applied-quota}'

object_user_key() {
  kubectl -n "$NS" get secret "$1-s3" -o jsonpath="{.data.$2}" | base64 -d
}

s3_job() {
  local job=$1 user=$2 bucket=$3 versioning=$4
  ENDPOINT=$rgw_endpoint BUCKET=$bucket ACCESS_KEY=$(object_user_key "$user" AccessKey) SECRET_KEY=$(object_user_key "$user" SecretKey) \
    WANT_VERSIONING=$versioning envsubst '${ENDPOINT} ${BUCKET} ${ACCESS_KEY} ${SECRET_KEY} ${WANT_VERSIONING}' \
    <"test/e2e/tenant/fixtures/$job.yaml" | kubectl apply -f -
}

s3_aws() {
  local user=$1
  shift
  kubectl -n objectstorage-e2e run "s3-$RANDOM" --rm -i --restart=Never --image="$s3_image" --image-pull-policy=IfNotPresent \
    --env=AWS_ACCESS_KEY_ID="$(object_user_key "$user" AccessKey)" --env=AWS_SECRET_ACCESS_KEY="$(object_user_key "$user" SecretKey)" \
    --env=AWS_DEFAULT_REGION=us-east-1 --env=AWS_EC2_METADATA_DISABLED=true -- \
    --endpoint-url "$rgw_endpoint" "$@"
}

s3_put() {
  local user=$1 target=$2 bytes=${3:-16}
  head -c "$bytes" /dev/zero | s3_aws "$user" s3 cp - "$target"
}

s3_get() {
  s3_aws "$1" s3 cp "$2" -
}

s3_lists() {
  s3_aws "$1" s3 ls "$2"
}

object_user() {
  local name=$1 permission=$2 buckets=$3
  kubectl -n "$NS" apply -f - <<USER
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ObjectStorageUser
metadata:
  name: $name
spec:
  permission: $permission
  buckets: $buckets
USER
  wait_condition 300 objectstorageuser "$name" Ready True -n "$NS"
}

bucket_manifest() {
  local name=$1 extra=${2:-}
  cat <<BUCKET
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ObjectStorageBucket
metadata:
  name: $name
spec:
${extra:-  versioning: false}
BUCKET
}

object_bucket() {
  bucket_manifest "$2" "${3:-}" | kubectl -n "$1" apply -f -
}

bucket_ready() {
  wait_condition 300 objectstoragebucket "$2" Ready True -n "$1"
}

bucket_removed() {
  local namespace=$1 name=$2
  kubectl -n "$namespace" delete objectstoragebucket "$name" --wait=false
  wait_gone 300 objectstoragebucket "$name" -n "$namespace"
}

objectstorage_user_and_bucket() {
  local names user
  wait_jsonpath 600 Ready -n rook-ceph get cephobjectstore bedrock-rgw -o jsonpath='{.status.phase}'
  kubectl -n rook-ceph get svc rook-ceph-rgw-bedrock-rgw
  expect_equal rook-ceph.ceph.rook.io/bucket "$(kubectl get storageclass bucket -o jsonpath='{.provisioner}')" "bucket provisioner"
  kubectl create namespace objectstorage-e2e --dry-run=client -o yaml | kubectl apply -f -
  object_user reader read '[data]'
  object_user writer readWrite '[data]'
  object_user all readWrite '[]'
  for user in reader writer all; do
    expect_equal "$user-s3" "$(kubectl -n "$NS" get objectstorageuser "$user" -o jsonpath='{.status.credentialsSecret}')" "credentials secret of $user"
    expect_contains "$(kubectl -n "$NS" get objectstorageuser "$user" -o jsonpath='{.status.arn}')" "user/$NS.$user" "principal of $user"
    expect_equal "$NS" "$(kubectl -n rook-ceph get cephobjectstoreuser "$NS.$user" -o jsonpath='{.spec.accountRef.name}')" "account of $user"
  done
  expect_equal Ready "$(kubectl -n rook-ceph get cephobjectstoreaccount "$NS" -o jsonpath='{.status.phase}')" "account of the project"
  test "$(object_user_key reader AccessKey)" != "$(object_user_key writer AccessKey)"
  object_bucket "$NS" data $'  versioning: true'
  object_bucket "$NS" other
  bucket_ready "$NS" data
  bucket_ready "$NS" other
  expect_equal "$NS-data" "$(kubectl -n "$NS" get objectstoragebucket data -o jsonpath='{.status.bucketName}')" "bucket name"
  expect_equal "$rgw_endpoint" "$(kubectl -n "$NS" get objectstoragebucket data -o jsonpath='{.status.endpoint}')" "bucket endpoint"
  expect_equal no "$(as_user dana -n rook-ceph auth can-i get secrets)" "owner reads Rook secrets"
  expect_equal no "$(as_user dana -n rook-ceph auth can-i get cephobjectstoreaccounts.ceph.rook.io)" "owner reads accounts"
  names=$(kubectl -n "$NS" get secrets -o name)
  case "$names" in
  *root*)
    echo "a root key Secret is in the project namespace: $names" >&2
    return 1
    ;;
  esac
}

objectstorage_roundtrip() {
  local bucket
  bucket=$(kubectl -n "$NS" get objectstoragebucket data -o jsonpath='{.status.bucketName}')
  s3_job s3-roundtrip writer "$bucket" true
  kubectl -n objectstorage-e2e wait --for=condition=complete job/s3-roundtrip --timeout=300s
  s3_job s3-denied reader "$bucket" false
  kubectl -n objectstorage-e2e wait --for=condition=complete job/s3-denied --timeout=300s
}

objectstorage_permissions() {
  retry_until 120 s3_put writer "s3://$NS-data/hello.txt"
  s3_get reader "s3://$NS-data/hello.txt" >/dev/null
  expect_refused AccessDenied s3_put reader "s3://$NS-data/denied.txt"
  expect_refused AccessDenied s3_lists reader "s3://$NS-other"
  expect_refused AccessDenied s3_lists writer "s3://$NS-other"
  retry_until 120 s3_lists all "s3://$NS-other"
  bucket_removed "$NS" other
}

objectstorage_names() {
  object_bucket acme-data data
  bucket_ready acme-data data
  expect_equal acme-data-data "$(kubectl -n acme-data get objectstoragebucket data -o jsonpath='{.status.bucketName}')" "same object name in a second project"
  object_bucket acme-data clash "  bucketName: $NS-data"
  wait_reason 300 objectstoragebucket clash Ready NameTaken -n acme-data
  object_bucket acme-data chosen '  bucketName: acme-chosen-name'
  bucket_ready acme-data chosen
  expect_equal acme-chosen-name "$(kubectl -n acme-data get objectstoragebucket chosen -o jsonpath='{.status.bucketName}')" "chosen bucket name"
  bucket_manifest reserved '  bucketName: admin' | expect_refused "only an administrator may use a reserved bucket name" as_user alice -n acme-data apply -f -
  object_bucket acme-data reserved '  bucketName: admin'
  wait_reason 300 objectstoragebucket reserved Ready NameReserved -n acme-data
  for name in clash chosen reserved data; do
    bucket_removed acme-data "$name"
  done
}

objectstorage_quota() {
  local over_quota=2097152
  kubectl -n "$NS" patch objectstoragebucket data --type merge -p '{"spec":{"quota":{"bytes":"1Mi"}}}'
  wait_jsonpath 300 1048576/0 -n "$NS" get objectstoragebucket data -o jsonpath="$applied_quota_annotation"
  wait_condition 300 objectstoragebucket data QuotaApplied True -n "$NS"
  expect_refused QuotaExceeded s3_put writer "s3://$NS-data/big.bin" "$over_quota"
  kubectl -n "$NS" patch objectstoragebucket data --type merge -p '{"spec":{"quota":null}}'
  wait_jsonpath 300 "" -n "$NS" get objectstoragebucket data -o jsonpath="$applied_quota_annotation"
  retry_until 120 s3_put writer "s3://$NS-data/big.bin" "$over_quota"
}

objectstorage_suspend() {
  kubectl -n "$NS" patch objectstorageuser writer --type merge -p '{"spec":{"suspended":true}}'
  wait_jsonpath 300 '[]' -n rook-ceph get cephobjectstoreuser "$NS.writer" -o jsonpath='{.spec.opMask}'
  retry_until 180 objectstorage_scope_blocked writer
  kubectl -n "$NS" patch objectstorageuser writer --type merge -p '{"spec":{"suspended":false}}'
  wait_jsonpath 300 "" -n rook-ceph get cephobjectstoreuser "$NS.writer" -o jsonpath='{.spec.opMask}'
  retry_until 180 s3_get writer "s3://$NS-data/hello.txt"
}

objectstorage_scope_blocked() {
  ! s3_get "$1" "s3://$NS-data/hello.txt"
}

objectstorage_force_destroy() {
  object_bucket "$NS" scratch $'  forceDestroy: true'
  object_bucket "$NS" guarded
  bucket_ready "$NS" scratch
  bucket_ready "$NS" guarded
  retry_until 120 s3_put all "s3://$NS-scratch/one.txt"
  retry_until 120 s3_put all "s3://$NS-guarded/one.txt"
  bucket_removed "$NS" scratch
  expect_refused NoSuchBucket s3_lists all "s3://$NS-scratch"
  kubectl -n "$NS" delete objectstoragebucket guarded --wait=false
  wait_reason 300 objectstoragebucket guarded Ready NotEmpty -n "$NS"
  kubectl -n "$NS" patch objectstoragebucket guarded --type merge -p '{"spec":{"forceDestroy":true}}'
  wait_gone 300 objectstoragebucket guarded -n "$NS"
}

objectstorage_public_host() {
  kubectl patch setting objectstorage.public-host --type merge -p "{\"spec\":{\"value\":\"s3.$HOST\"}}"
  wait_jsonpath 300 "https://s3.$HOST" -n "$NS" get objectstoragebucket data -o jsonpath='{.status.endpoint}'
  wait_not_empty 300 -n rook-ceph get certificate objectstorage-public -o jsonpath='{.metadata.name}'
  wait_not_empty 300 -n rook-ceph get ingressroute objectstorage-public -o jsonpath='{.metadata.name}'
  expect_contains "$(kubectl -n rook-ceph get cephobjectstore bedrock-rgw -o jsonpath='{.spec.hosting.dnsNames}')" "s3.$HOST" "store host names"
  kubectl patch setting objectstorage.public-host --type merge -p '{"spec":{"value":""}}'
  wait_jsonpath 300 "$rgw_endpoint" -n "$NS" get objectstoragebucket data -o jsonpath='{.status.endpoint}'
  wait_gone 300 certificate objectstorage-public -n rook-ceph
  wait_jsonpath 900 Ready -n rook-ceph get cephobjectstore bedrock-rgw -o jsonpath='{.status.phase}'
}
