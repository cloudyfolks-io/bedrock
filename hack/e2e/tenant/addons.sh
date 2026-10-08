service_get() {
  local namespace=$1 service=$2 path=$3
  kubectl get --raw "/api/v1/namespaces/$namespace/services/$service/proxy$path"
}

urlencode() {
  python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$1"
}

loki_lines() {
  local query=$1 end start
  end=$(date +%s)
  start=$((end - 3600))
  service_get monitoring loki:3100 "/loki/api/v1/query_range?limit=20&start=${start}000000000&end=${end}000000000&query=$(urlencode "$query")" |
    jq -r '[.data.result[].values[]] | length'
}

loki_has_lines() {
  [ "$(loki_lines "$1")" -gt 0 ]
}

addons_seeded() {
  kubectl get addon kata loki
  expect_equal true "$(kubectl get addon kata -o jsonpath='{.spec.enabled}')" "kata is enabled by default"
  expect_equal false "$(kubectl get addon loki -o jsonpath='{.spec.enabled}')" "loki is disabled by default"
  expect_equal true "$(kubectl get setting kata.enabled -o jsonpath='{.spec.value}')" "setting kata.enabled"
  expect_equal false "$(kubectl get setting loki.enabled -o jsonpath='{.spec.value}')" "setting loki.enabled"
}

addons_kata() {
  local node
  node=$(hostname | tr '[:upper:]' '[:lower:]')
  if [ -c /dev/kvm ]; then
    wait_reason 180 addon kata Preflight Passed
    return
  fi
  wait_reason 180 addon kata Preflight NoKVM
  wait_reason 60 addon kata Ready PreflightFailed
  kubectl -n kube-system get daemonset kata-deploy 2>&1 | grep -q NotFound
  kubectl get runtimeclass kata 2>&1 | grep -q NotFound
  expect_equal false "$(kubectl get host "$node" -o jsonpath='{.status.checks.kvm}')" "host check kvm"
  expect_equal "" "$(kubectl get node "$node" -o jsonpath='{.metadata.labels.bedrock\.cloudyfolks\.io/kvm}')" "node kvm label"
  expect_equal true "$(kubectl get addon kata -o jsonpath='{.spec.enabled}')" "kata stays enabled"
}

addons_loki_off() {
  wait_reason 120 addon loki Ready Disabled
  kubectl -n monitoring get statefulset loki 2>&1 | grep -q NotFound
  kubectl -n monitoring get daemonset alloy 2>&1 | grep -q NotFound
}

addons_loki_on() {
  kubectl patch addon loki --type merge -p '{"spec":{"enabled":true,"values":{"persistence.size":"2Gi","persistence.storage-class":"block"}}}'
  wait_condition 300 addon loki Ready True
  kubectl -n monitoring rollout status statefulset/loki --timeout=300s
  kubectl -n monitoring rollout status daemonset/alloy --timeout=300s
  retry_until 180 loki_has_lines '{namespace="kube-system"}'
  retry_until 180 loki_has_lines '{job="journald", unit="k0scontroller.service"}'
}

addons_invalid_values() {
  kubectl patch addon loki --type merge -p '{"spec":{"values":{"persistence.nothing":"x"}}}'
  wait_reason 120 addon loki Ready InvalidValues
  kubectl -n monitoring get statefulset loki
  kubectl patch addon loki --type merge -p '{"spec":{"values":{"persistence.size":"2Gi","persistence.storage-class":"block"}}}'
  wait_condition 300 addon loki Ready True
}

addons_grafana_and_alerts() {
  local password
  kubectl -n monitoring get secret bedrock-grafana-admin >/dev/null
  password=$(kubectl -n monitoring get secret bedrock-grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d)
  GRAFANA_PASSWORD=$password retry_until 300 grafana_has_dashboards
  kubectl patch setting alertmanager.webhook-url --type merge -p '{"spec":{"value":"http://webhook.e2e.test:9000/alerts"}}'
  retry_until 180 alertmanager_has_webhook
  kubectl patch setting alertmanager.webhook-url --type merge -p '{"spec":{"value":""}}'
}

grafana_has_dashboards() {
  local found address
  address=$(kubectl -n monitoring get svc kube-prometheus-stack-grafana -o jsonpath='{.spec.clusterIP}')
  found=$(curl -sf -m 10 -u "admin:$GRAFANA_PASSWORD" "http://$address/api/search?query=Bedrock" | jq -r '[.[].uid] | sort | join(",")')
  expect_contains "$found" bedrock-tenants "grafana dashboards" && expect_contains "$found" bedrock-vms "grafana dashboards"
}

alertmanager_has_webhook() {
  kubectl -n monitoring get secret bedrock-alertmanager-config -o json | jq -r '.data | map(@base64d) | join("\n")' | grep -q 'http://webhook.e2e.test:9000/alerts'
}
