database_import_mysql_client() {
  local pod=$1 pod_address=$2 image=$3
  container_proof_address="$pod_address" container_proof_pod "$pod" '' |
    kubectl -n "$NS" create --dry-run=client -f - -o json |
    jq --arg image "$image" --arg address "$address" --arg name "$wrong_name" '
      .spec.hostAliases = [{ip:$address, hostnames:[$name]}] |
      .spec.containers += [{name:"mysql-client", image:$image, imagePullPolicy:"IfNotPresent", command:["sleep", "7200"]}]
    ' | as_operator -n "$NS" create -f -
}

database_import_mysql_query() {
  local pod=$1 host=$2 user=$3 password_file=$4 ca_file=$5 sql=$6
  {
    cat "$password_file"
    printf '\n'
    cat "$ca_file"
  } | kubectl -n "$NS" exec -i "$pod" -c mysql-client -- sh -ec '
    IFS= read -r MYSQL_PWD
    ca=$(mktemp)
    trap '\''rm -f "$ca"'\'' EXIT
    cat >"$ca"
    export MYSQL_PWD
    mysql --no-defaults --protocol=TCP --connect-timeout=5 --batch --raw --skip-column-names \
      --ssl-mode=VERIFY_IDENTITY --ssl-ca="$ca" --host="$1" --port="$2" --user="$3" --execute="$4"
  ' mysql-query "$host" "$port" "$user" "$sql"
}

database_import_mysql_row() {
  local result
  result=$(database_import_mysql_query "$1" "$address" cutover_reader "$certificate_dir/reader-password" "$certificate_dir/ca.crt" \
    "SELECT value FROM app.cutover_proof; SHOW SESSION STATUS LIKE 'Ssl_cipher';") || return 1
  [[ "$result" == $'42\nSsl_cipher\t'?* ]]
}

database_import_mysql_refused() {
  local expected=$1 output
  shift
  if output=$(database_import_mysql_query "$@" 2>&1) || ! grep -Eq "^ERROR ($expected) \\(" <<<"$output"; then
    printf 'The MySQL request did not fail with %s\n' "$expected" >&2
    return 1
  fi
}

database_import_mysql_source_backup() {
  local admin_password reader_password
  admin_password=$(<"$certificate_dir/password")
  reader_password=$(<"$certificate_dir/reader-password")
  if ! database_mysql_query "$source_uid" >/dev/null 2>&1 <<SQL
CREATE TABLE app.cutover_proof (value INT PRIMARY KEY);
INSERT INTO app.cutover_proof VALUES (42);
ALTER USER 'dbadmin'@'%' IDENTIFIED BY '$admin_password' REQUIRE SSL;
CREATE USER 'cutover_reader'@'%' IDENTIFIED BY '$reader_password' REQUIRE SSL;
GRANT SELECT ON app.cutover_proof TO 'cutover_reader'@'%';
SQL
  then
    printf 'The MySQL source could not create the test row and accounts\n' >&2
    return 1
  fi
  kubectl -n "$database_namespace" create -f - <<BACKUP
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLBackup
metadata:
  name: $source_backup
spec:
  clusterName: db-$source_uid
  storageName: rgw
BACKUP
  wait_jsonpath 900 Succeeded -n "$database_namespace" get perconaservermysqlbackup "$source_backup" -o jsonpath='{.status.state}'
}

database_import_mysql_backup_secret() {
  kubectl -n "$database_namespace" get secret database-backups-s3 -o json |
    jq -e --arg name "$backup_secret" --arg project "$NS" --arg target "$database" --arg uid "$uid" \
      --arg bucket "$bucket" --arg source "$source_uid" --arg destination "$destination" '
      .data as $keys
      | select(all(["ACCESS_KEY_ID","ACCESS_SECRET_KEY","AWS_ACCESS_KEY_ID","AWS_SECRET_ACCESS_KEY"][]; ($keys[.] // "" | length > 0)))
      | {apiVersion:"v1", kind:"Secret", type:"Opaque", metadata:{name:$name, annotations:{
          "dadehat.cloudyfolks.com/source":("databases/" + $source),
          "bedrock.cloudyfolks.io/import-project":$project,
          "bedrock.cloudyfolks.io/import-target":$target,
          "bedrock.cloudyfolks.io/import-target-uid":$uid}},
        data:($keys | {ACCESS_KEY_ID, ACCESS_SECRET_KEY, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY} + {
          bucket:($bucket | @base64), prefix:($source | @base64),
          serverName:($source | @base64), destination:($destination | @base64)})}
    ' | kubectl -n "$database_namespace" create -f -
}

database_import_mysql_cleanup() {
  local status=$1 failed=0
  database_import_cleanup "$status" || failed=1
  kubectl -n "$NS" delete database "$source_database" --ignore-not-found --wait=false || failed=1
  wait_gone 600 database "$source_database" -n "$NS" || failed=1
  if [ -n "$source_backup" ]; then
    kubectl -n "$database_namespace" delete perconaservermysqlbackup "$source_backup" --ignore-not-found --wait=false || failed=1
  fi
  if [ -n "$backup_secret" ]; then
    if wait_gone 60 database "$database" -n "$NS"; then
      kubectl -n "$database_namespace" delete secret "$backup_secret" --ignore-not-found || failed=1
    else
      failed=1
    fi
  fi
  if [ "$status" -ne 0 ]; then
    return "$status"
  fi
  return "$failed"
}

database_import_mysql() (
  if [ "${E2E_ONLY:-}" != database_import_mysql ]; then
    printf 'Set E2E_ONLY=database_import_mysql to run this case\n' >&2
    exit 1
  fi
  local suffix source_database database frontend claim job_pod customer_pod
  local address=100.69.0.232 port=6336 proxy_port=3306 legacy_backend=10.100.0.235
  local job_address=10.100.0.233 customer_address=10.100.0.234 wrong_name=wrong-mysql.example.invalid
  local project_id=22222222-2222-4222-8222-222222222222 legacy_id
  local management_before managed_before managed_applied network vpc subnet_uid legacy_uid proxy_address
  local certificate_dir certificate_secret= admin_secret= backup_secret= tls_session_pid= tls_session_input= restored= recovery_backup=
  local uid source_uid source_backup= source_secret bucket destination backup_directory image runtime

  suffix=$(openssl rand -hex 4)
  source_database="mysql-source-$suffix"
  database="mysql-import-$suffix"
  frontend="legacy-mysql-$suffix"
  claim="database-endpoint-$frontend"
  job_pod="mysql-import-job-$suffix"
  customer_pod="mysql-import-client-$suffix"
  legacy_id=$(< /proc/sys/kernel/random/uuid)
  test -n "$legacy_id"
  runtime=$(kubectl get setting database.mysql.runtime-class -o jsonpath='{.status.applied}')
  test -z "$runtime"
  database_prepare
  database_extras_prepare
  wait_not_empty 120 get databaseversion mysql-8.4 -o jsonpath='{.spec.image}'
  image=$(kubectl get databaseversion mysql-8.4 -o jsonpath='{.spec.image}')
  management_before=$(kubectl get cluster cluster -o jsonpath='{.spec.managementCIDR}')
  managed_before=$(kubectl get setting network.tenant-management.enabled -o json)
  managed_applied=$(jq -er '.status.applied | select(. == "false" or . == "true")' <<<"$managed_before")
  certificate_dir=$(mktemp -d)
  trap 'database_import_mysql_cleanup "$?"' EXIT
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

  database_apply "$source_database" mysql 8.4 true 5
  database_wait_ready "$source_database" 1200
  source_uid=$(database_uid "$source_database")
  test -n "$source_uid"
  source_secret="db-$source_uid-superuser"
  source_backup="db-$source_uid-e2e"
  kubectl -n "$database_namespace" get secret "$source_secret" -o json |
    jq -ceS '.data as $users
      | select(all(["root","operator","xtrabackup","monitor","replication","orchestrator","heartbeat"][]; ($users[.] // "" | length > 0)))
      | $users' >"$certificate_dir/source-users"

  database_import_mysql_client "$job_pod" "$job_address" "$image"
  database_import_mysql_client "$customer_pod" "$customer_address" "$image"
  kubectl -n "$NS" wait --for=condition=Ready "pod/$job_pod" "pod/$customer_pod" --timeout=180s
  expect_equal "$job_address" "$(kubectl -n "$NS" get pod "$job_pod" -o jsonpath='{.status.podIP}')" "migration probe address"
  expect_equal "$customer_address" "$(kubectl -n "$NS" get pod "$customer_pod" -o jsonpath='{.status.podIP}')" "customer probe address"
  database_import_legacy_frontend
  legacy_uid=$(kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.metadata.uid}')
  test -n "$legacy_uid"
  wait_jsonpath 120 "$NS/slr-flb-$frontend" get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.status.service}'

  database_apply "$database" mysql 8.4 false 5 "$(cat <<SPEC
  subnetRef: default
  suspended: true
  allowedCIDRs: [$customer_address/32]
  restore:
    sourceUID: $source_uid
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
  backup_secret="import-backup-$source_uid-$uid"
  kubectl -n "$NS" annotate database "$database" bedrock.cloudyfolks.io/import=true
  database_import_material
  openssl rand -hex 24 | tr -d '\n' >"$certificate_dir/reader-password"
  openssl rand -hex 24 | tr -d '\n' >"$certificate_dir/wrong-password"
  openssl req -x509 -sha256 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
    -subj /CN=unrelated-database-fixture-ca -keyout "$certificate_dir/wrong-ca.key" -out "$certificate_dir/wrong-ca.crt"

  database_import_mysql_source_backup
  bucket=$(kubectl -n "$database_namespace" get perconaservermysql "db-$source_uid" -o jsonpath='{.spec.backup.storages.rgw.s3.bucket}')
  destination=$(kubectl -n "$database_namespace" get perconaservermysqlbackup "$source_backup" -o jsonpath='{.status.destination}')
  backup_directory=${destination#"s3://$bucket/$source_uid/"}
  test -n "$bucket"
  test "$backup_directory" != "$destination"
  [[ "$backup_directory" =~ ^[a-zA-Z0-9._:-]+$ ]]
  database_import_mysql_backup_secret

  kubectl -n "$NS" delete database "$source_database" --wait=false
  wait_gone 600 database "$source_database" -n "$NS"
  wait_gone 180 secret "$source_secret" -n "$database_namespace"
  expect_equal "$NS $source_database" "$(kubectl -n "$database_namespace" get configmap "tombstone-$source_uid" -o jsonpath='{.data.namespace} {.data.name}')" "retained source ownership"
  cmp -s "$certificate_dir/source-users" <(kubectl -n "$database_namespace" get secret "tombstone-$source_uid" -o json | jq -ceS '.data')

  database_import_claim
  kubectl -n "$NS" patch database "$database" --type merge -p '{"spec":{"suspended":false}}'
  wait_jsonpath 180 'the legacy frontend needs administrator approval to start' -n "$NS" get database "$database" -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'
  expect_equal "$legacy_uid $legacy_backend" "$(kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.metadata.uid} {.spec.endpoints[0]}')" "legacy frontend before start"
  database_import_approve start
  database_wait_ready "$database" 1800
  wait_jsonpath 120 Succeeded -n "$database_namespace" get perconaservermysqlrestore "db-$uid-restore" -o jsonpath='{.status.state}'
  kubectl -n "$database_namespace" get perconaservermysqlrestore "db-$uid-restore" -o json |
    jq -e --arg destination "$destination" --arg bucket "$bucket" --arg prefix "$source_uid" --arg secret "$backup_secret" '
      .spec.backupSource as $backup | $backup.destination == $destination and
      ($backup.storage.s3 | .bucket == $bucket and .prefix == $prefix and .credentialsSecret == $secret)
    ' >/dev/null
  cmp -s "$certificate_dir/source-users" <(kubectl -n "$database_namespace" get secret "db-$uid-superuser" -o json | jq -ceS '.data')
  proxy_address=$(kubectl -n "$NS" get database "$database" -o jsonpath='{.status.proxyAddress}')
  test -n "$proxy_address"
  test "$proxy_address" != "$address"
  expect_equal "$legacy_uid $vpc $address $port/TCP $proxy_address" "$(kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.metadata.uid} {.spec.vpc} {.status.vip} {.status.ports} {.spec.endpoints[0]}')" "adopted MySQL frontend"
  expect_equal "$address:$port" "$(database_secret_field "$database-admin" host):$(database_secret_field "$database-admin" port)" "published MySQL endpoint"
  cmp -s "$certificate_dir/ca.crt" <(database_secret_field "$database-admin" 'ca\.crt')
  cmp -s "$certificate_dir/password" <(database_secret_field "$database-admin" password)
  retry_until 120 database_import_mysql_row "$job_pod"
  database_import_mysql_refused '2003|2013' "$customer_pod" "$address" cutover_reader "$certificate_dir/reader-password" "$certificate_dir/ca.crt" 'SELECT 1;'

  database_import_approve open
  retry_until 120 database_import_mysql_row "$customer_pod"
  expect_equal 42 "$(database_import_mysql_query "$customer_pod" "$address" dbadmin "$certificate_dir/password" "$certificate_dir/ca.crt" 'SELECT value FROM app.cutover_proof;')" "restored row with the original administrator"
  database_import_mysql_refused '2003|2013' "$job_pod" "$address" cutover_reader "$certificate_dir/reader-password" "$certificate_dir/ca.crt" 'SELECT 1;'
  database_import_mysql_refused 2026 "$customer_pod" "$address" cutover_reader "$certificate_dir/reader-password" "$certificate_dir/wrong-ca.crt" 'SELECT 1;'
  database_import_mysql_refused 2026 "$customer_pod" "$wrong_name" cutover_reader "$certificate_dir/reader-password" "$certificate_dir/ca.crt" 'SELECT 1;'
  database_import_mysql_refused 1045 "$customer_pod" "$address" cutover_reader "$certificate_dir/wrong-password" "$certificate_dir/ca.crt" 'SELECT 1;'
  database_import_mysql_refused 1142 "$customer_pod" "$address" cutover_reader "$certificate_dir/reader-password" "$certificate_dir/ca.crt" 'INSERT INTO app.cutover_proof VALUES (43);'
)
