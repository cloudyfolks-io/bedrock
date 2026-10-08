prometheus_query() {
  service_get monitoring kube-prometheus-stack-prometheus:9090 "/api/v1/query?query=$(urlencode "$1")"
}

series_count() {
  prometheus_query "$1" | jq '.data.result | length'
}

series_value() {
  prometheus_query "$1" | jq -r '.data.result[0].value[1]'
}

metering_usage() {
  wait_jsonpath 300 1 get project "$PROJECT" -o jsonpath='{.status.usage.vms}'
  wait_not_empty 120 get project "$PROJECT" -o jsonpath='{.status.usage.cpu}'
  wait_not_empty 120 get project "$PROJECT" -o jsonpath='{.status.usage.storage}'
  wait_not_empty 120 get tenant "$TENANT" -o jsonpath='{.status.usage.cpu}'
  wait_not_empty 120 get tenant "$TENANT" -o jsonpath='{.status.usage.storage}'
  expect_equal 2 "$(kubectl get tenant "$TENANT" -o jsonpath='{.status.usage.projects}')" "tenant project usage"
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

metering_vm_series() {
  [ "$(series_value "bedrock:project_vm_running:count{tenant=\"$TENANT\",project=\"$PROJECT\"}")" = 1 ]
}

metering_logs() {
  retry_until 300 loki_has_lines '{log_source="vm-serial-console"}'
}
