database_import_legacy_frontend() {
  kubectl apply -f - <<FRONTEND
apiVersion: fabric.cloudyfolks.io/v1
kind: LoadBalancer
metadata:
  name: $frontend
  labels:
    app.kubernetes.io/managed-by: network-api
    app.kubernetes.io/instance: $legacy_id
    bedrock.cloudyfolks.com/project-id: $project_id
    bedrock.cloudyfolks.com/environment: e2e
spec:
  vpc: $vpc
  namespace: $NS
  frontend:
    vip: $address
  endpoints: [$legacy_backend]
  ports:
    - name: database
      port: $port
      targetPort: $proxy_port
      protocol: TCP
FRONTEND
}

database_import_claim() {
  kubectl -n "$database_namespace" create configmap "$claim" --dry-run=client -o json \
    --from-literal="namespace=$NS" --from-literal="database=$database" --from-literal="uid=$uid" \
    --from-literal="address=$address" --from-literal="port=$port" \
    --from-literal="loadBalancerID=$legacy_id" --from-literal="projectID=$project_id" --from-literal=environment=e2e \
    --from-literal="legacyVPC=$vpc" --from-literal="legacyNamespace=$NS" \
    --from-literal="subnetUID=$subnet_uid" --from-literal="network=$network" --from-literal="jobCIDR=$job_address/32" |
    jq '.immutable = true' | kubectl -n "$database_namespace" create -f -
}

database_import_approve() {
  kubectl -n "$database_namespace" get configmap "$claim" -o json |
    jq --arg phase "$1" '. as $claim | {apiVersion, kind, metadata: {name: (.metadata.name + "-" + $phase), namespace: .metadata.namespace},
      immutable: true, data: (.data | {namespace, database, uid, subnetUID, network} + {claimUID: $claim.metadata.uid})}' |
    kubectl -n "$database_namespace" create -f -
}

database_import_material() {
  openssl req -x509 -sha256 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 \
    -subj /CN=legacy-database-fixture-ca -keyout "$certificate_dir/ca.key" -out "$certificate_dir/ca.crt"
  openssl req -new -sha256 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
    -subj /CN=legacy-database-fixture -keyout "$certificate_dir/tls.key" -out "$certificate_dir/tls.csr"
  printf 'subjectAltName=IP:%s\nextendedKeyUsage=serverAuth\n' "$address" >"$certificate_dir/extensions"
  openssl x509 -req -sha256 -days 1 -in "$certificate_dir/tls.csr" \
    -CA "$certificate_dir/ca.crt" -CAkey "$certificate_dir/ca.key" -CAcreateserial \
    -extfile "$certificate_dir/extensions" -out "$certificate_dir/tls.crt"
  openssl rand -hex 24 | tr -d '\n' >"$certificate_dir/password"
  {
    kubectl -n "$database_namespace" create secret generic "$certificate_secret" --type=kubernetes.io/tls \
      --dry-run=client -o json --from-file="ca.crt=$certificate_dir/ca.crt" \
      --from-file="tls.crt=$certificate_dir/tls.crt" --from-file="tls.key=$certificate_dir/tls.key"
    kubectl -n "$database_namespace" create secret generic "$admin_secret" \
      --dry-run=client -o json --from-file="password=$certificate_dir/password"
  } | jq -s --arg project "$NS" --arg database "$database" --arg uid "$uid" '{apiVersion:"v1", kind:"List", items: map(.metadata.annotations = {
    "dadehat.cloudyfolks.com/source": ("databases/" + $database),
    "bedrock.cloudyfolks.io/import-project": $project,
    "bedrock.cloudyfolks.io/import-target": $database,
    "bedrock.cloudyfolks.io/import-target-uid": $uid
  })}' | kubectl -n "$database_namespace" create -f -
}

database_import_customer_pod() {
  local client_image
  client_image=$(kubectl get databaseversion postgresql-17 -o jsonpath='{.spec.image}')
  test -n "$client_image"
  container_proof_address="$customer_address" container_proof_pod "$customer_pod" '' |
    kubectl -n "$NS" create --dry-run=client -f - -o json |
    jq --arg image "$client_image" '.spec.containers += [{name:"postgres-client", image:$image, imagePullPolicy:"IfNotPresent", command:["sleep", "7200"]}]' |
    as_operator -n "$NS" create -f -
}

database_import_customer_sql() {
  local query=$1
  {
    cat "$certificate_dir/password"
    printf '\n'
    cat "$certificate_dir/ca.crt"
  } | kubectl -n "$NS" exec -i "$customer_pod" -c postgres-client -- sh -ec '
    IFS= read -r PGPASSWORD
    root_ca=$(mktemp)
    trap '\''rm -f "$root_ca"'\'' EXIT
    cat >"$root_ca"
    export PGPASSWORD PGSSLMODE=verify-full PGSSLROOTCERT="$root_ca" PGCONNECT_TIMEOUT=5
    psql -XAtqw -v ON_ERROR_STOP=1 -h "$1" -p "$2" -U dbadmin -d app -c "$3"
  ' database-query "$address" "$port" "$query"
}

database_import_backup_acknowledged_write() {
  local result
  result=$(database_import_customer_sql "BEGIN; SET LOCAL synchronous_commit=on; INSERT INTO cutover_proof VALUES ('after-open'); COMMIT; SELECT value FROM cutover_proof WHERE value = 'after-open';") || return 1
  expect_equal after-open "$result" "acknowledged write over the original endpoint"
  database_backup_manifest "$recovery_backup" "$database" | kubectl create -f -
  expect_equal "db-$uid" "$(kubectl -n "$database_namespace" get backups.postgresql.cnpg.io "$recovery_backup" -o jsonpath='{.spec.cluster.name}')" "owner of the new backup"
  wait_jsonpath 900 completed -n "$database_namespace" get backups.postgresql.cnpg.io "$recovery_backup" -o jsonpath='{.status.phase}'
  wait_not_empty 120 get backups.postgresql.cnpg.io "$recovery_backup" -n "$database_namespace" -o jsonpath='{.status.stoppedAt}'
}

database_import_restore_acknowledged_write() {
  local restored_uid rows
  database_apply "$restored" postgresql 17 false 5 "  subnetRef: default
  restore:
    sourceUID: $uid"
  database_wait_ready "$restored" 1800
  restored_uid=$(database_uid "$restored")
  test -n "$restored_uid"
  test "$restored_uid" != "$uid"
  rows=$(database_sql "$restored" -c "SELECT string_agg(value, ',' ORDER BY value) FROM cutover_proof;") || return 1
  expect_equal 'after-open,baseline' "$rows" "baseline and acknowledged customer rows in the recovery database"
  database_import_reply_is "$customer_pod" ''
}

database_import_approval_ids() {
  kubectl -n "$database_namespace" get configmap "$claim-start" "$claim-open" -o json |
    jq -er '
      .items | sort_by(.metadata.name)
      | select(length == 2 and all(.[]; .immutable == true and ((.metadata.uid // "") | length > 0)))
      | map(.metadata.uid) | join(" ")
    '
}

database_import_engine_claims() {
  kubectl -n "$database_namespace" get persistentvolumeclaims -l "cnpg.io/cluster=db-$uid" -o json |
    jq -cer '
      .items | sort_by(.metadata.name)
      | select(length > 0 and all(.[];
          .status.phase == "Bound"
          and ((.spec.volumeName // "") | length > 0)
          and ((.metadata.uid // "") | length > 0)))
      | map({name: .metadata.name, uid: .metadata.uid, volume: .spec.volumeName})
    '
}

database_import_resume_checkpoint() {
  local approved=$1 reservation_uid=$2 engine_claims=$3 rows
  expect_equal "$reservation_uid $address" "$(kubectl get vips.fabric.cloudyfolks.io "db-$uid-frontend-vip" -o jsonpath='{.metadata.uid} {.status.v4ip}')" "VIP reservation during suspension"
  expect_equal "$approved" "$(database_import_approval_ids)" "retained approvals during suspension"
  as_user bob -n "$NS" patch database "$database" --type merge -p '{"spec":{"suspended":false}}'
  database_wait_ready "$database"
  retry_until 90 database_import_reply_is "$customer_pod" S
  expect_equal "$uid" "$(database_uid "$database")" "Database UID after customer resume"
  expect_equal "$reservation_uid $address" "$(kubectl get vips.fabric.cloudyfolks.io "db-$uid-frontend-vip" -o jsonpath='{.metadata.uid} {.status.v4ip}')" "VIP reservation after customer resume"
  expect_equal "$approved" "$(database_import_approval_ids)" "retained approvals after customer resume"
  expect_equal "$engine_claims" "$(database_import_engine_claims)" "original volume bindings after customer resume"
  rows=$(database_import_customer_sql "SELECT string_agg(value, ',' ORDER BY value) FROM cutover_proof;") || return 1
  expect_equal 'after-open,baseline' "$rows" "customer data over the original TLS endpoint after resume"
  database_import_reply_is "$job_pod" ''
}

database_import_tls_session() {
  exec kubectl -n "$NS" exec -i "$customer_pod" -c postgres-client -- sh -ec '
    IFS= read -r PGPASSWORD
    root_ca=$(mktemp)
    trap '\''rm -f "$root_ca"'\'' EXIT
    while IFS= read -r line; do
      printf "%s\n" "$line"
      if [ "$line" = "-----END CERTIFICATE-----" ]; then
        break
      fi
    done >"$root_ca"
    export PGPASSWORD PGSSLMODE=verify-full PGSSLROOTCERT="$root_ca" PGCONNECT_TIMEOUT=5
    result=0
    psql -XAtqw -v ON_ERROR_STOP=1 "host=$1 port=$2 user=dbadmin dbname=app tcp_user_timeout=10000" || result=$?
    printf "session-exit:%s\n" "$result"
    exit "$result"
  ' database-session "$address" "$port"
}

database_import_reply() {
  kubectl -n "$NS" exec "$1" -c app -- sh -c '
    printf "\000\000\000\010\004\322\026\057" |
      timeout 7 nc -w 5 "$1" "$2" | od -An -c | tr -d "[:space:]"
  ' database-probe "$2" "$3"
}

database_import_reply_is() {
  local got probe_address=${3:-$address} probe_port=${4:-$port}
  got=$(database_import_reply "$1" "$probe_address" "$probe_port") || return 1
  expect_equal "$2" "$got" "PostgreSQL SSLRequest from $1 to $probe_address:$probe_port"
}

database_import_ovn_withdrawn() {
  local frontends
  frontends=$(kubectl -n kube-system exec deployment/ovn-central -- ovn-nbctl list Load_Balancer) || return 1
  [[ "$frontends" != *"$address:$port"* ]]
}

database_import_proxy_stopped() {
  kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=db-$uid,app.kubernetes.io/component=endpoint" -o json |
    jq -e '
      def stopped($containers; $statuses):
        ($containers | map(.name) | sort) == ($statuses | map(.name) | sort) and
        all($statuses[];
          .ready == false and .state.running == null and .state.waiting == null and
          (.state.terminated.finishedAt | type == "string" and length > 0) and
          .state.terminated.reason != "ContainerStatusUnknown");
      (.items | type == "array") and all(.items[];
        (.status.phase == "Succeeded" or .status.phase == "Failed") and
        all(.status.conditions[]?; (.type != "Ready" and .type != "ContainersReady") or .status != "True") and
        (.spec.containers | length > 0) and
        stopped(.spec.containers; (.status.containerStatuses // [])) and
        stopped((.spec.initContainers // []); (.status.initContainerStatuses // [])) and
        stopped((.spec.ephemeralContainers // []); (.status.ephemeralContainerStatuses // [])))
    ' >/dev/null
}

database_import_wait_withdrawn() {
  wait_gone 180 loadbalancers.fabric.cloudyfolks.io "$frontend"
  wait_gone 180 switch-lb-rules.fabric.cloudyfolks.io "flb-$frontend"
  wait_gone 180 service "slr-flb-$frontend" -n "$NS"
  wait_gone 180 endpoints "slr-flb-$frontend" -n "$NS"
  retry_until 180 database_import_proxy_stopped
  retry_until 120 database_import_ovn_withdrawn
}

database_import_cleanup() {
  local status=$1 cleanup_failed=0
  if [ -n "$tls_session_input" ]; then
    exec {tls_session_input}>&-
  fi
  if [ -n "$tls_session_pid" ]; then
    kill "$tls_session_pid" 2>/dev/null || true
    wait "$tls_session_pid" 2>/dev/null || true
  fi
  if [ "$status" -ne 0 ]; then
    if [ -f "$certificate_dir/session.log" ]; then
      cat "$certificate_dir/session.log" >&2
    fi
    kubectl -n "$NS" get database "$database" -o yaml >&2 || true
    kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o yaml >&2 || true
    kubectl -n "$NS" get pods "$job_pod" "$customer_pod" -o wide >&2 || true
  fi
  if [ -n "$restored" ]; then
    kubectl -n "$NS" delete database "$restored" --ignore-not-found --wait=false || cleanup_failed=1
    wait_gone 300 database "$restored" -n "$NS" || cleanup_failed=1
  fi
  if [ -n "$recovery_backup" ]; then
    kubectl -n "$database_namespace" delete backups.postgresql.cnpg.io "$recovery_backup" --ignore-not-found --wait=false || cleanup_failed=1
  fi
  kubectl -n "$NS" delete database "$database" --ignore-not-found --wait=false || cleanup_failed=1
  wait_gone 300 database "$database" -n "$NS" || cleanup_failed=1
  kubectl -n "$NS" delete pod "$job_pod" "$customer_pod" --ignore-not-found --wait=false || cleanup_failed=1
  kubectl delete loadbalancers.fabric.cloudyfolks.io "$frontend" --ignore-not-found --wait=false || cleanup_failed=1
  wait_gone 120 loadbalancers.fabric.cloudyfolks.io "$frontend" || cleanup_failed=1
  kubectl -n "$database_namespace" delete configmap "$claim" "$claim-start" "$claim-open" --ignore-not-found || cleanup_failed=1
  if [ -n "$certificate_secret" ]; then
    kubectl -n "$database_namespace" delete secret "$certificate_secret" "$admin_secret" --ignore-not-found || cleanup_failed=1
  fi
  rm -r "$certificate_dir" || cleanup_failed=1
  kubectl patch setting network.tenant-management.enabled --type merge -p "$(jq -c '{spec: {value: .spec.value}}' <<<"$managed_before")" || cleanup_failed=1
  wait_jsonpath 120 "$managed_applied" get setting network.tenant-management.enabled -o jsonpath='{.status.applied}' || cleanup_failed=1
  if [ -z "$management_before" ]; then
    kubectl patch cluster cluster --type merge -p '{"spec":{"managementCIDR":null}}' || cleanup_failed=1
  fi
  if [ "$status" -eq 0 ] && [ "$cleanup_failed" -ne 0 ]; then
    return 1
  fi
  return "$status"
}

database_import_frontend() (
  local database=imported-orders frontend=legacy-orders claim=database-endpoint-legacy-orders
  local address=100.69.0.240 port=6432 proxy_port=5432 legacy_backend=10.100.0.245
  local job_pod=database-import-job job_address=10.100.0.241
  local customer_pod=database-import-customer customer_address=10.100.0.242
  local legacy_id=11111111-1111-4111-8111-111111111111 project_id=22222222-2222-4222-8222-222222222222
  local management_before managed_before managed_applied network vpc uid subnet_uid legacy_uid proxy_address
  local certificate_dir certificate_secret= admin_secret=
  local tls_session_pid= tls_session_input=
  local backup_enabled=false restored= recovery_backup=
  local approved= reservation_uid= engine_claims=

  if [[ "${E2E_ONLY:-}" = database_import_frontend && "${E2E_DATABASE_POST_OPEN_RECOVERY:-}" = 1 ]]; then
    backup_enabled=true
    restored=imported-orders-checkpoint
    recovery_backup=imported-orders-after-open
  fi

  management_before=$(kubectl get cluster cluster -o jsonpath='{.spec.managementCIDR}')
  managed_before=$(kubectl get setting network.tenant-management.enabled -o json)
  managed_applied=$(jq -er '.status.applied | select(. == "false" or . == "true")' <<<"$managed_before")
  certificate_dir=$(mktemp -d)
  trap 'database_import_cleanup "$?"' EXIT
  if [ -z "$management_before" ]; then
    ensure_management_cidr "$(host_management_cidr)"
  fi
  database_setting network.tenant-management.enabled true
  wait_condition 180 subnets.fabric.cloudyfolks.io tenant-managed Ready True
  wait_not_empty 120 get net-attach-def vpc -n kube-system -o jsonpath='{.metadata.name}'

  network=$(kubectl -n "$NS" get subnet default -o jsonpath='{.spec.networkRef}')
  subnet_uid=$(kubectl -n "$NS" get subnet default -o jsonpath='{.metadata.uid}')
  vpc=$(kubectl get network "$network" -o jsonpath='{.status.vpc}')
  test -n "$subnet_uid"
  test -n "$vpc"
  container_proof_address="$job_address" container_proof_pod "$job_pod" '' | as_operator -n "$NS" create -f -
  database_import_customer_pod
  kubectl -n "$NS" wait --for=condition=Ready "pod/$job_pod" "pod/$customer_pod" --timeout=180s
  expect_equal "$job_address" "$(kubectl -n "$NS" get pod "$job_pod" -o jsonpath='{.status.podIP}')" "migration probe project address"
  expect_equal "$customer_address" "$(kubectl -n "$NS" get pod "$customer_pod" -o jsonpath='{.status.podIP}')" "customer probe project address"

  database_import_legacy_frontend
  legacy_uid=$(kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.metadata.uid}')
  test -n "$legacy_uid"
  wait_jsonpath 120 "$NS/slr-flb-$frontend" get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.status.service}'
  database_apply "$database" postgresql 17 "$backup_enabled" 5 "$(cat <<SPEC
  subnetRef: default
  suspended: true
  allowedCIDRs: [$customer_address/32]
  endpoint:
    address: $address
    loadBalancerRef: $frontend
    port: $port
SPEC
  )"
  uid=$(database_uid "$database")
  test -n "$uid"
  certificate_secret="import-tls-$database-$uid"
  admin_secret="import-admin-$database-$uid"
  kubectl -n "$NS" annotate database "$database" bedrock.cloudyfolks.io/import=true
  database_import_material
  database_import_claim
  kubectl -n "$NS" patch database "$database" --type merge -p '{"spec":{"suspended":false}}'
  wait_jsonpath 180 'the legacy frontend needs administrator approval to start' -n "$NS" get database "$database" -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'
  expect_equal "$legacy_backend" "$(kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.spec.endpoints[0]}')" "legacy backend before start"
  test -z "$(kubectl -n "$NS" get deployment "bedrock-db-proxy-$uid" --ignore-not-found -o name)"

  database_import_approve start
  database_wait_ready "$database"
  proxy_address=$(kubectl -n "$NS" get database "$database" -o jsonpath='{.status.proxyAddress}')
  test -n "$proxy_address"
  test "$proxy_address" != "$address"
  expect_equal "$legacy_uid" "$(kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.metadata.uid}')" "adopted Fabric frontend UID"
  expect_equal "$vpc $address $port/TCP $proxy_address" "$(kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.spec.vpc} {.status.vip} {.status.ports} {.spec.endpoints[0]}')" "adopted endpoint and private proxy"
  expect_equal "$address:$port" "$(database_secret_field "$database-admin" host):$(database_secret_field "$database-admin" port)" "published customer endpoint"
  expect_equal "$(cat "$certificate_dir/ca.crt")" "$(database_secret_field "$database-admin" 'ca\.crt')" "published legacy certificate authority"
  retry_until 90 database_import_reply_is "$job_pod" S
  database_import_reply_is "$customer_pod" ''
  if [ -n "$restored" ]; then
    database_sql "$database" -c "SET ROLE dbadmin; CREATE TABLE cutover_proof (value text PRIMARY KEY); INSERT INTO cutover_proof VALUES ('baseline');"
  fi

  database_import_approve open
  retry_until 90 database_import_reply_is "$customer_pod" S
  expect_equal 1 "$(database_import_customer_sql 'SELECT 1 FROM pg_stat_ssl WHERE pid = pg_backend_pid() AND ssl')" "TLS login at the legacy address with the original trust and password"
  database_import_reply_is "$job_pod" ''

  if [ "${E2E_ONLY:-}" = database_import_frontend ]; then
    local engine_uid session_result=0
    engine_uid=$(kubectl -n "$database_namespace" get clusters.postgresql.cnpg.io "db-$uid" -o jsonpath='{.metadata.uid}')
    test -n "$engine_uid"
    database_import_reply_is "$customer_pod" S "$proxy_address" "$proxy_port"
    mkfifo "$certificate_dir/session.in"
    database_import_tls_session <"$certificate_dir/session.in" >"$certificate_dir/session.log" 2>&1 &
    tls_session_pid=$!
    exec {tls_session_input}>"$certificate_dir/session.in"
    {
      cat "$certificate_dir/password"
      printf '\n'
      cat "$certificate_dir/ca.crt"
      printf '%s\n' "SELECT 'session-before:' || pg_backend_pid() FROM pg_stat_ssl WHERE pid = pg_backend_pid() AND ssl;"
    } >&"$tls_session_input"
    retry_until 30 grep -Eq '^session-before:[0-9]+$' "$certificate_dir/session.log"
    kill -0 "$tls_session_pid"

    kubectl -n "$database_namespace" delete configmap "$claim-open"
    wait_jsonpath 180 'the database endpoint needs administrator approval to reopen customer access' -n "$NS" get database "$database" -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'
    wait_jsonpath 180 0 -n "$NS" get deployment "bedrock-db-proxy-$uid" -o jsonpath='{.spec.replicas}'
    database_import_wait_withdrawn
    kubectl -n "$NS" get database "$database" -o json |
      jq -e --arg uid "$uid" '.metadata.uid == $uid and .spec.suspended != true' >/dev/null
    expect_equal "$engine_uid" "$(kubectl -n "$database_namespace" get clusters.postgresql.cnpg.io "db-$uid" -o jsonpath='{.metadata.uid}')" "original PostgreSQL engine after revocation"
    wait_jsonpath 60 1 -n "$database_namespace" get clusters.postgresql.cnpg.io "db-$uid" -o jsonpath='{.status.readyInstances}'
    printf '%s\n' '\echo session-after-attempt' "SELECT 'session-after:' || pg_backend_pid();" '\q' >&"$tls_session_input"
    exec {tls_session_input}>&-
    tls_session_input=
    retry_until 30 grep -q '^session-exit:' "$certificate_dir/session.log"
    wait "$tls_session_pid" || session_result=$?
    tls_session_pid=
    grep -qx 'session-after-attempt' "$certificate_dir/session.log"
    grep -qx 'session-exit:2' "$certificate_dir/session.log"
    expect_equal 2 "$session_result" "established PostgreSQL session exit status"
    if grep -q '^session-after:' "$certificate_dir/session.log"; then
      echo "the established PostgreSQL session returned a result after revocation" >&2
      return 1
    fi
    database_import_reply_is "$customer_pod" ''
    database_import_reply_is "$customer_pod" '' "$proxy_address" "$proxy_port"
    database_import_reply_is "$job_pod" ''
    database_import_reply_is "$job_pod" '' "$proxy_address" "$proxy_port"

    database_import_approve open
    database_wait_ready "$database"
    retry_until 90 database_import_reply_is "$customer_pod" S
    database_import_reply_is "$customer_pod" S "$proxy_address" "$proxy_port"
  fi

  if [ -n "$recovery_backup" ]; then
    database_import_backup_acknowledged_write
    approved=$(database_import_approval_ids)
    reservation_uid=$(kubectl get vips.fabric.cloudyfolks.io "db-$uid-frontend-vip" -o jsonpath='{.metadata.uid}')
    test -n "$reservation_uid"
    engine_claims=$(database_import_engine_claims)
  fi

  kubectl -n "$NS" patch database "$database" --type merge -p '{"spec":{"suspended":true}}'
  wait_jsonpath 300 Suspended -n "$NS" get database "$database" -o jsonpath='{.status.phase}'
  database_import_wait_withdrawn
  database_import_reply_is "$customer_pod" ''
  if [ -n "$restored" ]; then
    wait_empty 300 -n "$database_namespace" get pods -l "cnpg.io/cluster=db-$uid"
    expect_equal "$engine_claims" "$(database_import_engine_claims)" "retained volume bindings while the engine is stopped"
    database_import_restore_acknowledged_write
    database_import_resume_checkpoint "$approved" "$reservation_uid" "$engine_claims"
  fi
)
