build_push_secret=build-push
build_git_secret=build-git

build_manifest() {
  local name=$1 type=$2 push=$3
  cat <<BUILD
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Build
metadata:
  name: $name
spec:
  type: $type
  source:
    git:
      url: https://git.example.test/acme/shop.git
      revision: 0123456789abcdef0123456789abcdef01234567
      secretRef: $build_git_secret
  output:
    image: registry.example.test/acme/shop:$name
    pushSecretRef: $push
  network:
    subnetRef: default
BUILD
}

build_kata_ready() {
  [ "$(kubectl get addon kata -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ]
}

build_dns_setting() {
  kubectl patch setting network.dns-servers --type merge -p "{\"spec\":{\"value\":\"$1\"}}"
}

build_prepare_secrets() {
  build_dns_setting 198.51.100.53
  kubectl -n "$NS" create secret docker-registry "$build_push_secret" --docker-server=registry.example.test --docker-username=robot --docker-password=pushword --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n "$NS" create secret generic "$build_git_secret" --from-literal=username=robot --from-literal=token=build-e2e-token --dry-run=client -o yaml | kubectl apply -f -
}

build_roles() {
  expect_equal yes "$(as_user carol -n "$NS" auth can-i get builds)" "viewer reads builds"
  expect_equal yes "$(as_user carol -n "$NS" auth can-i get builds --subresource=logs)" "viewer reads build logs"
  expect_equal no "$(as_user carol -n "$NS" auth can-i create builds || true)" "viewer creates builds"
  expect_equal yes "$(as_user dana -n "$NS" auth can-i create builds)" "owner creates builds"
  expect_equal yes "$(as_user dana -n "$NS" auth can-i get builds --subresource=logs)" "owner reads build logs"
  expect_equal no "$(as_user erin -n "$NS" auth can-i get builds --subresource=logs || true)" "stranger reads build logs"
}

build_missing_secret() {
  build_prepare_secrets
  build_manifest no-secret dockerfile absent-push | kubectl -n "$NS" apply -f -
  wait_reason 120 build no-secret Ready SecretNotFound -n "$NS"
  expect_equal Pending "$(kubectl -n "$NS" get build no-secret -o jsonpath='{.status.phase}')" "phase of a build with no push secret"
  expect_equal "" "$(kubectl -n "$NS" get job no-secret -o name 2>/dev/null || true)" "no job for a build with no push secret"
  kubectl -n "$NS" create secret generic build-wrong-type --from-literal=a=b --dry-run=client -o yaml | kubectl apply -f -
  build_manifest wrong-type dockerfile build-wrong-type | kubectl -n "$NS" apply -f -
  wait_reason 120 build wrong-type Ready SecretTypeInvalid -n "$NS"
  expect_equal "" "$(kubectl -n "$NS" get job wrong-type -o name 2>/dev/null || true)" "no job for a push secret of the wrong type"
}

build_kata_gate() {
  if build_kata_ready; then
    echo "the kata addon is ready: skipping the gate check"
    return 0
  fi
  build_manifest gated dockerfile "$build_push_secret" | kubectl -n "$NS" apply -f -
  wait_reason 120 build gated Ready KataUnavailable -n "$NS"
  expect_equal Pending "$(kubectl -n "$NS" get build gated -o jsonpath='{.status.phase}')" "phase of a gated build"
  expect_equal "" "$(kubectl -n "$NS" get job gated -o name 2>/dev/null || true)" "no job without kata"
  build_manifest gated-pack buildpacks "$build_push_secret" | kubectl -n "$NS" apply -f -
  wait_reason 120 build gated-pack Ready KataUnavailable -n "$NS"
  expect_equal "" "$(kubectl -n "$NS" get job gated-pack -o name 2>/dev/null || true)" "no job without kata for buildpacks"
}

build_logs_url() {
  echo "https://api.$HOST/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/$NS/builds/$1/logs"
}

build_logs_through_proxy() {
  curl -s --cacert "$BEDROCK_CA" -m 30 -H "Authorization: Bearer $(cat "$TOKEN_DIR/dana")" "$(build_logs_url "$1")"
}

build_logs_contain() {
  [[ "$(build_logs_through_proxy "$1")" == *"$2"* ]]
}

build_logs() {
  if build_kata_ready; then
    build_manifest logs-probe dockerfile "$build_push_secret" | kubectl -n "$NS" apply -f -
    wait_not_empty 600 -n "$NS" get pods -l "bedrock.cloudyfolks.io/kind=Build,bedrock.cloudyfolks.io/name=logs-probe" -o jsonpath='{.items[0].metadata.name}'
    retry_until 300 build_logs_contain logs-probe "==> git-clone <=="
    return 0
  fi
  retry_until 60 build_logs_contain gated "the build has no pod yet"
}

build_immutable() {
  expect_refused "a build is immutable" kubectl -n "$NS" patch build no-secret --type merge -p '{"spec":{"source":{"git":{"revision":"main"}}}}'
}

build_cleanup() {
  kubectl -n "$NS" delete build --all
  wait_empty 120 -n "$NS" get builds.bedrock.cloudyfolks.io
  wait_empty 120 -n "$NS" get jobs,configmaps,pods -l bedrock.cloudyfolks.io/kind=Build
  expect_equal "" "$(kubectl -n "$NS" get secrets -l bedrock.cloudyfolks.io/kind=Build -o name)" "no secret made for a build"
  kubectl -n "$NS" get secret "$build_push_secret" "$build_git_secret" build-wrong-type
  kubectl -n "$NS" delete secret "$build_push_secret" "$build_git_secret" build-wrong-type
  build_dns_setting ""
}
