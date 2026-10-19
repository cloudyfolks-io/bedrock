spike_endpoint=http://rook-ceph-rgw-bedrock-rgw.rook-ceph.svc:80
spike_namespace=objectstorage-e2e
spike_store=bedrock-rgw
spike_aws_image=docker.io/amazon/aws-cli:2.37.10
spike_buckets=(spike-allowed spike-denied spike-foreign spike-adopted)

spike_aws() {
  local access=$1 secret=$2
  shift 2
  kubectl -n "$spike_namespace" run "spike-$RANDOM" --rm -i --restart=Never --image="$spike_aws_image" --image-pull-policy=IfNotPresent \
    --env=AWS_ACCESS_KEY_ID="$access" --env=AWS_SECRET_ACCESS_KEY="$secret" --env=AWS_DEFAULT_REGION=us-east-1 --env=AWS_EC2_METADATA_DISABLED=true -- \
    --endpoint-url "$spike_endpoint" "$@"
}

spike_secret_field() {
  kubectl -n rook-ceph get secret "$1" -o jsonpath="{.data.$2}" | base64 -d
}

spike_user_secret() {
  echo "rook-ceph-object-user-$spike_store-$1"
}

spike_root_secret() {
  kubectl -n rook-ceph get cephobjectstoreaccount "$1" -o jsonpath='{.status.rootAccountSecretName}'
}

spike_root_aws() {
  local account=$1 secret
  shift
  secret=$(spike_root_secret "$account")
  spike_aws "$(spike_secret_field "$secret" AccessKey)" "$(spike_secret_field "$secret" SecretKey)" "$@"
}

spike_user_aws() {
  local user=$1 secret
  shift
  secret=$(spike_user_secret "$user")
  spike_aws "$(spike_secret_field "$secret" AccessKey)" "$(spike_secret_field "$secret" SecretKey)" "$@"
}

spike_apply_account() {
  local name=$1 id=${2:-}
  kubectl apply -f - <<ACCOUNT
apiVersion: ceph.rook.io/v1
kind: CephObjectStoreAccount
metadata:
  name: $name
  namespace: rook-ceph
spec:
  store: $spike_store
${id:+  accountID: $id}
ACCOUNT
}

spike_apply_user() {
  local name=$1 account=$2 mask=${3:-}
  kubectl apply -f - <<USER
apiVersion: ceph.rook.io/v1
kind: CephObjectStoreUser
metadata:
  name: $name
  namespace: rook-ceph
spec:
  store: $spike_store
  displayName: $name
${account:+  accountRef:
    name: $account}
${mask:+  opMask: $mask}
USER
}

spike_policy() {
  cat <<POLICY
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListAllMyBuckets"],"Resource":["arn:aws:s3:::*"]},{"Effect":"Allow","Action":["s3:GetObject","s3:ListBucket","s3:GetBucketLocation"],"Resource":["arn:aws:s3:::$1","arn:aws:s3:::$1/*"]}]}
POLICY
}

spike_rgw_admin() {
  kubectl -n rook-ceph exec deploy/rook-ceph-operator -- radosgw-admin \
    --cluster=rook-ceph --conf=/var/lib/rook/rook-ceph/rook-ceph.config --keyring=/var/lib/rook/rook-ceph/client.admin.keyring \
    --rgw-realm="$spike_store" --rgw-zonegroup="$spike_store" --rgw-zone="$spike_store" "$@"
}

spike_ready() {
  wait_jsonpath 300 Ready -n rook-ceph get "$1" "$2" -o jsonpath='{.status.phase}'
}

spike_account_and_users() {
  spike_apply_account spike-account
  spike_apply_account spike-foreign
  spike_ready cephobjectstoreaccount spike-account
  spike_ready cephobjectstoreaccount spike-foreign
  spike_apply_user spike-user spike-account
  spike_ready cephobjectstoreuser spike-user
  kubectl -n rook-ceph get secret "$(spike_root_secret spike-account)" >/dev/null
  kubectl -n rook-ceph get secret "$(spike_user_secret spike-user)" >/dev/null
}

spike_iam_policy() {
  local iam_names
  spike_root_aws spike-account s3 mb s3://spike-allowed
  spike_root_aws spike-account s3 mb s3://spike-denied
  spike_root_aws spike-foreign s3 mb s3://spike-foreign
  iam_names=$(spike_root_aws spike-account iam list-users --query 'Users[].UserName' --output text)
  echo "iam users: $iam_names"
  spike_root_aws spike-account iam put-user-policy --user-name spike-user --policy-name access --policy-document "$(spike_policy spike-allowed)"
  spike_root_aws spike-account iam get-user-policy --user-name spike-user --policy-name access >/dev/null
  retry_until 120 spike_user_aws spike-user s3 ls s3://spike-allowed
  spike_expect_denied spike-user s3 ls s3://spike-denied
  spike_expect_denied spike-user s3 ls s3://spike-foreign
}

spike_suspend() {
  spike_apply_user spike-user spike-account '[]'
  retry_until 120 spike_user_blocked
  spike_apply_user spike-user spike-account
  retry_until 120 spike_user_aws spike-user s3 ls s3://spike-allowed
}

spike_user_blocked() {
  ! spike_user_aws spike-user s3 ls s3://spike-allowed
}

spike_expect_denied() {
  if spike_user_aws "$@"; then
    echo "call was allowed: $*" >&2
    return 1
  fi
}

spike_adopt_account() {
  local id=RGW31415926535897932 access=spikeadoptaccess secret=spikeadoptsecret listing
  spike_rgw_admin account create --account-id="$id" --account-name=spike-adopt
  spike_rgw_admin user create --uid=spike-adopt-root --display-name=spike-adopt-root --account-id="$id" --account-root --access-key="$access" --secret="$secret"
  spike_aws "$access" "$secret" s3 mb s3://spike-adopted
  spike_apply_account spike-adopt "$id"
  spike_ready cephobjectstoreaccount spike-adopt
  expect_equal "$id" "$(kubectl -n rook-ceph get cephobjectstoreaccount spike-adopt -o jsonpath='{.status.accountID}')" "adopted account id"
  listing=$(spike_aws "$access" "$secret" s3 ls)
  expect_contains "$listing" spike-adopted "adopted bucket"
}

spike_keys_from_secret() {
  local access=spikekeyaccess secret=spikekeysecret
  kubectl -n rook-ceph create secret generic spike-keys --from-literal=access="$access" --from-literal=secret="$secret"
  kubectl apply -f - <<USER
apiVersion: ceph.rook.io/v1
kind: CephObjectStoreUser
metadata:
  name: spike-keyed
  namespace: rook-ceph
spec:
  store: $spike_store
  displayName: spike-keyed
  keys:
    - accessKeyRef:
        name: spike-keys
        key: access
      secretKeyRef:
        name: spike-keys
        key: secret
USER
  spike_ready cephobjectstoreuser spike-keyed
  expect_equal "$access" "$(spike_secret_field "$(spike_user_secret spike-keyed)" AccessKey)" "key kept"
}

spike_clean() {
  local bucket
  for bucket in "${spike_buckets[@]}"; do
    spike_root_aws spike-account s3 rb "s3://$bucket" --force >/dev/null 2>&1 || true
  done
  kubectl -n rook-ceph delete cephobjectstoreuser spike-user spike-keyed --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl -n rook-ceph delete cephobjectstoreaccount spike-account spike-foreign spike-adopt --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl -n rook-ceph delete secret spike-keys --ignore-not-found >/dev/null 2>&1 || true
}

spike_check() {
  local name=$1 check=$2 out status
  set +e
  out=$(set -e; "$check" 2>&1)
  status=$?
  set -e
  if [ "$status" -eq 0 ]; then
    echo "SPIKE $name ok"
    return 0
  fi
  echo "SPIKE $name failed $(printf '%s' "$out" | tail -n 1 | tr '\n' ' ')"
  return 1
}

objectstorage_iam_spike() {
  local failed=0
  kubectl create namespace "$spike_namespace" --dry-run=client -o yaml | kubectl apply -f -
  spike_check account-and-users spike_account_and_users || failed=1
  spike_check iam-policy spike_iam_policy || failed=1
  spike_check op-mask spike_suspend || failed=1
  spike_check account-id-adoption spike_adopt_account || failed=1
  spike_check user-keys-from-secret spike_keys_from_secret || failed=1
  spike_clean
  return "$failed"
}
