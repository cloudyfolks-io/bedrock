panel_curl() {
  curl -s --cacert "$BEDROCK_CA" -m 20 "$@"
}

panel_served() {
  local body entry headers
  kubectl -n bedrock-system rollout status deployment/bedrock-proxy --timeout=180s
  expect_equal 2 "$(kubectl -n bedrock-system get deployment bedrock-proxy -o jsonpath='{.status.readyReplicas}')" "proxy replicas"
  wait_condition 120 cluster cluster ProxyReady True
  expect_equal 32 "$(kubectl -n bedrock-system get secret bedrock-console-client -o jsonpath='{.data.clientSecret}' | base64 -d | wc -c | tr -d ' ')" "console client secret length"
  expect_equal "307 https://console.$HOST/x?y=1" "$(panel_curl -o /dev/null -w '%{http_code} %{redirect_url}' "https://$HOST/x?y=1")" "apex redirect"
  body=$(panel_curl "https://console.$HOST/")
  expect_contains "$body" '<div id="root">' "panel shell"
  expect_contains "$body" 'type="module"' "panel script"
  entry=$(echo "$body" | grep -o 'src="/assets/[^"]*"' | head -1 | sed 's/^src="//;s/"$//')
  test -n "$entry"
  headers=$(panel_curl -D - -o /dev/null "https://console.$HOST$entry")
  expect_contains "$headers" 'immutable' "asset cache header"
  expect_equal "$body" "$(panel_curl "https://console.$HOST/p/web/vms")" "client route fallback"
  expect_equal 404 "$(panel_curl -o /dev/null -w '%{http_code}' "https://console.$HOST/assets/missing.js")" "missing asset"
  expect_equal 401 "$(panel_curl -o /dev/null -w '%{http_code}' "https://console.$HOST/apis/x")" "anonymous API call"
  expect_equal 404 "$(panel_curl -o /dev/null -w '%{http_code}' "https://console.$HOST/healthz")" "health path stays off the public route"
  expect_contains "$(panel_curl -D - -o /dev/null "https://console.$HOST/")" "connect-src 'self' wss://console.$HOST https://upload.$HOST" "content security policy"
  expect_equal 200 "$(panel_curl -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat "$TOKEN_DIR/admin")" "https://api.$HOST/apis/bedrock.cloudyfolks.io/v1alpha1/hosts")" "api pass-through"
  expect_equal 401 "$(panel_curl -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer junk' "https://api.$HOST/apis/bedrock.cloudyfolks.io/v1alpha1/hosts")" "api with a bad token"
  expect_contains "$(panel_curl "https://api.$HOST/kubeconfig")" "server: https://api.$HOST:6443" "kubeconfig"
  expect_contains "$(panel_curl "https://api.$HOST/ca.crt")" "BEGIN CERTIFICATE" "platform CA download"
}

panel_login_and_stream() {
  BEDROCK_HOST=$HOST ADMIN_PASSWORD=$(cat "$admin_password_file") TOKEN_ADMIN=$(cat "$TOKEN_DIR/admin") \
    "$test_bin" -test.count=1 -test.v -test.timeout 10m -test.run '^TestPanelLoginAndStream$'
}
