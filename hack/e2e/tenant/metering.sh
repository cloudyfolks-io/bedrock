prometheus_query() {
  service_get monitoring kube-prometheus-stack-prometheus:9090 "/api/v1/query?query=$(urlencode "$1")"
}

series_count() {
  prometheus_query "$1" | jq '.data.result | length'
}

series_value() {
  prometheus_query "$1" | jq -r '.data.result[0].value[1]'
}

aaq_counts_vms() {
  local used
  used=$(kubectl -n "$NS" get applicationawareresourcequotas.aaq.kubevirt.io project-quota -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"]["used"]["count/virtualmachines.bedrock.cloudyfolks.io"])')
  [ "$used" = "$(kubectl -n "$NS" get virtualmachines.bedrock.cloudyfolks.io -o name | wc -l | tr -d ' ')" ]
}

metering_usage() {
  wait_jsonpath 300 1 get project "$PROJECT" -o jsonpath='{.status.usage.vms}'
  wait_not_empty 120 get project "$PROJECT" -o jsonpath='{.status.usage.cpu}'
  wait_not_empty 120 get project "$PROJECT" -o jsonpath='{.status.usage.storage}'
  wait_not_empty 120 get tenant "$TENANT" -o jsonpath='{.status.usage.cpu}'
  wait_not_empty 120 get tenant "$TENANT" -o jsonpath='{.status.usage.storage}'
  expect_equal 2 "$(kubectl get tenant "$TENANT" -o jsonpath='{.status.usage.projects}')" "tenant project usage"
  retry_until 300 aaq_counts_vms
}

rules_are_healthy() {
  service_get monitoring kube-prometheus-stack-prometheus:9090 /api/v1/rules |
    jq -e '[.data.groups[] | select(.name == "bedrock.metering") | .rules[].health] | length > 0 and all(. == "ok")'
}

metering_series() {
  retry_until 300 rules_are_healthy
  service_get monitoring kube-prometheus-stack-prometheus:9090 /api/v1/targets | jq -e '[.data.activeTargets[] | select(.scrapePool | test("kubevirt")) | select(.health == "up")] | length > 0'
  retry_until 600 metering_vm_series
  expect_equal 0 "$(series_count '{__name__=~"bedrock:project_.+", tenant=""}')" "series without a tenant label"
  expect_equal 0 "$(series_count '{__name__=~"bedrock:project_.+", project=""}')" "series without a project label"
  expect_equal 0 "$(series_count '{__name__=~"bedrock:project_.+", namespace="kube-system"}')" "series for a platform namespace"
}

proxy_range_has_points() {
  local now vmi
  now=$(date +%s)
  vmi=$(kubectl -n "$NS" get vmi -o jsonpath='{.items[0].metadata.name}')
  panel_curl -H "Authorization: Bearer $(cat "$TOKEN_DIR/admin")" "https://api.$HOST/proxy/v1/metrics/range?query=vm.cpu&namespace=$NS&name=$vmi&start=$((now - 900))&end=$now&step=60" |
    jq -e '.status == "success" and (.data.result | length > 0)'
}

metering_proxy_range() {
  expect_equal 9090 "$(kubectl -n monitoring get svc prometheus-operated -o jsonpath='{.spec.ports[?(@.name=="web")].port}')" "prometheus service port"
  retry_until 300 proxy_range_has_points
}

metering_vm_series() {
  [ "$(series_value "bedrock:project_vm_running:count{tenant=\"$TENANT\",project=\"$PROJECT\"}")" = 1 ]
}

metering_logs() {
  retry_until 300 loki_has_lines '{log_source="vm-serial-console"}'
}
