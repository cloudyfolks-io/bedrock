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
  retry_until 300 aaq_counts_vms || {
    kubectl -n "$NS" get applicationawareresourcequotas.aaq.kubevirt.io project-quota -o jsonpath='aaq used={.status.used} hard={.spec.hard}{"\n"}' >&2 || true
    echo "virtual machines: $(kubectl -n "$NS" get virtualmachines.bedrock.cloudyfolks.io -o name 2>/dev/null | wc -l | tr -d ' ')" >&2
    return 1
  }
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
  metering_every_series
}

proxy_range_request() {
  local now vmi
  now=$(date +%s)
  vmi=$(kubectl -n "$NS" get vmi -o jsonpath='{.items[0].metadata.name}')
  panel_curl -S -w '\n%{http_code}' -H "Authorization: Bearer $(cat "$TOKEN_DIR/admin")" "https://api.$HOST/proxy/v1/metrics/range?query=vm.cpu&namespace=$NS&name=$vmi&start=$((now - 900))&end=$now&step=60"
}

proxy_range_has_points() {
  proxy_range_request | sed '$d' | jq -e '.status == "success" and (.data.result | length > 0)'
}

metering_proxy_range() {
  local answer body
  expect_equal 9090 "$(kubectl -n monitoring get svc prometheus-operated -o jsonpath='{.spec.ports[?(@.name=="http-web")].port}')" "prometheus service port"
  retry_until 300 proxy_range_has_points || {
    answer=$(proxy_range_request || true)
    body=${answer%$'\n'*}
    echo "proxy answered ${answer##*$'\n'}: ${body:0:500}" >&2
    return 1
  }
}

metering_vm_series() {
  [ "$(series_value "bedrock:project_vm_running:count{tenant=\"$TENANT\",project=\"$PROJECT\"}")" = 1 ]
}

metering_series_names() {
  printf '%s\n' \
    bedrock:project_vm_running:count \
    bedrock:project_vm_vcpu:sum \
    bedrock:project_vm_memory_bytes:sum \
    bedrock:project_volume_requested_bytes:sum \
    bedrock:project_vm_cpu_usage_cores:rate5m \
    bedrock:project_vm_network_receive_bytes:rate5m \
    bedrock:project_vm_network_transmit_bytes:rate5m \
    bedrock:project_vm_network_receive_bytes:increase1h \
    bedrock:project_vm_network_transmit_bytes:increase1h
}

metering_source_metric_names() {
  printf '%s\n' \
    kubevirt_vmi_info \
    kubevirt_vmi_vcpu_seconds_total \
    kubevirt_vmi_memory_domain_bytes \
    kubevirt_vmi_cpu_usage_seconds_total \
    kubevirt_vmi_network_receive_bytes_total \
    kubevirt_vmi_network_transmit_bytes_total
}

project_series_exists() {
  [ "$(series_count "$1{tenant=\"$TENANT\",project=\"$PROJECT\"}")" -ge 1 ]
}

source_metric_has_vm_labels() {
  [ "$(series_count "{__name__=\"$1\",namespace=\"$NS\",name!=\"\"}")" -ge 1 ]
}

source_metric_has_namespace_label() {
  [ "$(series_count "{__name__=\"$1\",namespace=\"$NS\"}")" -ge 1 ]
}

metering_every_series() {
  local name
  for name in $(metering_source_metric_names); do
    retry_until 300 source_metric_has_vm_labels "$name" || {
      echo "source metric $name has no series with the labels namespace and name for $NS" >&2
      return 1
    }
  done
  for name in kube_persistentvolumeclaim_resource_requests_storage_bytes kube_namespace_labels; do
    retry_until 300 source_metric_has_namespace_label "$name" || {
      echo "source metric $name has no series with the label namespace for $NS" >&2
      return 1
    }
  done
  for name in $(metering_series_names); do
    retry_until 600 project_series_exists "$name" || {
      echo "series $name has no value for $TENANT/$PROJECT" >&2
      return 1
    }
  done
}

metering_transfer_bytes=67108864
metering_transfer_port=8000
metering_transfer_server=10.100.18.10

network_increase() {
  local value
  value=$(series_value "bedrock:project_vm_network_$1_bytes:increase1h{tenant=\"$TENANT\",project=\"$PROJECT\"}")
  echo "${value/null/0}"
}

network_increase_rose_by() {
  local direction=$1 before=$2 minimum=$3
  awk -v now="$(network_increase "$direction")" -v before="$before" -v minimum="$minimum" 'BEGIN { exit !(now - before >= minimum) }'
}

transfer_subnet_manifest() {
  cat <<SUBNET
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Subnet
metadata:
  name: metering
spec:
  networkRef: $TENANT
  cidr: 10.100.18.0/24
SUBNET
}

transfer_server_script() {
  cat <<SCRIPT
#!/bin/sh
while true; do
  dd if=/dev/zero bs=1048576 count=$((metering_transfer_bytes / 1048576)) 2>/dev/null | nc -l -p $metering_transfer_port
done &
SCRIPT
}

transfer_client_script() {
  cat <<SCRIPT
#!/bin/sh
until [ "\$(nc $metering_transfer_server $metering_transfer_port | wc -c)" -eq $metering_transfer_bytes ]; do
  sleep 2
done
echo "METER-DONE" > /dev/console
SCRIPT
}

transfer_vm_manifest() {
  cat <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: $1
spec:
  instanceType: u1.nano
  preference: cirros
  image:
    name: cirros
    scope: cluster
  rootDisk:
    sizeGi: 2
  networks:
    - subnetRef: metering
      address: $2
  cloudInit:
    userData: |
$($3 | sed 's/^/      /')
  running: true
VM
}

transfer_has_finished() {
  [[ "$(guest_console_log meter-client)" == *METER-DONE* ]]
}

transfer_cleanup() {
  if [ "$1" -ne 0 ]; then
    guest_console_log meter-client >&2 || true
    guest_console_log meter-server >&2 || true
  fi
  kubectl -n "$NS" delete virtualmachine meter-client meter-server --ignore-not-found
  wait_gone 180 virtualmachine meter-client -n "$NS"
  wait_gone 180 virtualmachine meter-server -n "$NS"
  wait_empty 180 -n "$NS" get pods -l 'kubevirt.io/domain in (meter-client,meter-server)'
  wait_gone 180 datavolume meter-client-root -n "$NS"
  wait_gone 180 datavolume meter-server-root -n "$NS"
  kubectl -n "$NS" delete subnet metering --ignore-not-found
  wait_gone 180 subnet metering -n "$NS"
}

transfer_checks() {
  local received=$1 transmitted=$2 minimum=$((metering_transfer_bytes * 9 / 10))
  transfer_subnet_manifest | kubectl -n "$NS" apply -f -
  wait_reason 120 subnet metering Ready Applied -n "$NS"
  transfer_vm_manifest meter-server "$metering_transfer_server" transfer_server_script | kubectl -n "$NS" apply -f -
  transfer_vm_manifest meter-client 10.100.18.11 transfer_client_script | kubectl -n "$NS" apply -f -
  wait_jsonpath 600 Running -n "$NS" get virtualmachine meter-server -o jsonpath='{.status.phase}'
  wait_jsonpath 600 Running -n "$NS" get virtualmachine meter-client -o jsonpath='{.status.phase}'
  retry_until 600 transfer_has_finished
  retry_until 300 network_increase_rose_by receive "$received" "$minimum" || {
    echo "receive increase1h was $received, now $(network_increase receive), want a rise of $minimum" >&2
    return 1
  }
  retry_until 300 network_increase_rose_by transmit "$transmitted" "$minimum" || {
    echo "transmit increase1h was $transmitted, now $(network_increase transmit), want a rise of $minimum" >&2
    return 1
  }
}

metering_network_totals() {
  local received transmitted
  received=$(network_increase receive)
  transmitted=$(network_increase transmit)
  (
    trap 'transfer_cleanup "$?"' EXIT
    transfer_checks "$received" "$transmitted"
  )
}

metering_logs() {
  retry_until 300 loki_has_lines '{log_source="vm-serial-console"}'
  retry_until 300 loki_has_lines "{tenant=\"$TENANT\",project=\"$PROJECT\",log_source=\"vm-serial-console\"}"
  retry_until 300 loki_has_lines "{tenant=\"$TENANT\",project=\"$PROJECT\",log_source=\"container\"}"
  expect_equal 0 "$(loki_lines '{namespace="kube-system", tenant!=""}')" "kube-system lines under a tenant label"
  expect_equal 0 "$(loki_lines '{job="journald", tenant!=""}')" "journald lines under a tenant label"
  expect_equal 0 "$(loki_lines '{job="kube-apiserver-audit", tenant!=""}')" "audit lines under a tenant label"
}

reader_service_names() {
  printf '%s\n' kube-prometheus-stack-prometheus:9090 loki:3100
}

reader_role_manifest() {
  cat <<ROLE
apiVersion: v1
kind: ServiceAccount
metadata:
  name: metering-reader
  namespace: monitoring
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: metering-reader
  namespace: monitoring
rules:
  - apiGroups: [""]
    resources: [services/proxy]
    verbs: [get]
    resourceNames: [kube-prometheus-stack-prometheus:9090, loki:3100]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: metering-reader
  namespace: monitoring
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: metering-reader
subjects:
  - kind: ServiceAccount
    name: metering-reader
    namespace: monitoring
ROLE
}

reader_ready_path() {
  case "$1" in
  loki:*) echo /ready ;;
  *) echo /-/ready ;;
  esac
}

reader_get() {
  local token=$1 service=$2 path=$3
  kubectl --kubeconfig=/dev/null --server="$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')" \
    --certificate-authority=/var/lib/k0s/pki/ca.crt --token="$token" \
    get --raw "/api/v1/namespaces/monitoring/services/$service/proxy$path"
}

reader_probe_manifest() {
  cat <<POD
apiVersion: v1
kind: Pod
metadata:
  name: reader-probe
spec:
  restartPolicy: Never
  containers:
    - name: probe
      image: docker.io/amazon/aws-cli:2.37.10
      imagePullPolicy: IfNotPresent
      command:
        - bash
        - -c
        - |
          for target in loki:3100 kube-prometheus-stack-prometheus:9090 kube-prometheus-stack-grafana:80; do
            if timeout 5 bash -c "exec 3<>/dev/tcp/\${target%%:*}.monitoring.svc/\${target##*:}"; then
              echo "PROBE \${target%%:*} open"
            else
              echo "PROBE \${target%%:*} blocked"
            fi
          done
POD
}

reader_probe_state() {
  kubectl -n "$NS" logs reader-probe | sed -n "s/^PROBE $1 //p"
}

reader_probe_finished() {
  [ "$(kubectl -n "$NS" get pod reader-probe -o jsonpath='{.status.phase}')" = Succeeded ]
}

reader_cleanup() {
  kubectl -n "$NS" delete pod reader-probe --ignore-not-found
  kubectl -n monitoring delete rolebinding,role,serviceaccount metering-reader --ignore-not-found
}

reader_checks() {
  local token service
  reader_probe_manifest | kubectl -n "$NS" apply -f -
  retry_until 300 reader_probe_finished
  expect_equal blocked "$(reader_probe_state loki)" "tenant pod reaching loki"
  expect_equal blocked "$(reader_probe_state kube-prometheus-stack-prometheus)" "tenant pod reaching prometheus"
  expect_equal open "$(reader_probe_state kube-prometheus-stack-grafana)" "tenant pod reaching grafana, the control that proves the pod has a network"
  for service in $(reader_service_names); do
    service_get monitoring "$service" "$(reader_ready_path "$service")"
  done
  reader_role_manifest | kubectl apply -f -
  token=$(kubectl -n monitoring create token metering-reader)
  for service in $(reader_service_names); do
    reader_get "$token" "$service" "$(reader_ready_path "$service")"
  done
  expect_refused Forbidden reader_get "$token" kube-prometheus-stack-grafana:80 /
  kubectl -n monitoring rollout status statefulset/prometheus-kube-prometheus-stack-prometheus --timeout=300s
}

metering_reader_policy() {
  (
    trap reader_cleanup EXIT
    reader_checks
  )
}
