database_class=e2e-small
database_namespace=bedrock-dbaas
database_probe_vm=database-probe

database_skipped() {
  [ -n "$(state_read database-skip)" ]
}

database_setting() {
  kubectl patch setting "$1" --type merge -p "{\"spec\":{\"value\":\"$2\"}}"
  wait_jsonpath 60 "$2" get setting "$1" -o jsonpath='{.status.applied}'
}

database_uid() {
  kubectl -n "$NS" get database "$1" -o jsonpath='{.metadata.uid}'
}

database_primary_pod() {
  kubectl -n "$database_namespace" get pods -l "cnpg.io/cluster=$(database_uid "$1"),cnpg.io/instanceRole=primary" -o jsonpath='{.items[0].metadata.name}'
}

database_sql() {
  local name=$1
  shift
  kubectl -n "$database_namespace" exec "$(database_primary_pod "$name")" -c postgres -- psql -X -q -A -t -h /controller/run -U postgres -d "${DATABASE_SQL_DB:-app}" -v ON_ERROR_STOP=1 "$@"
}

database_manifest() {
  local name=$1 extra=${2:-}
  cat <<DATABASE
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Database
metadata:
  name: $name
spec:
  engine: postgresql
  version: "17"
  class: $database_class
  storageGi: 5
  subnetRef: default
$extra
DATABASE
}

database_class_manifest() {
  cat <<CLASS
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: DatabaseClass
metadata:
  name: $database_class
spec:
  cpu: 500m
  memory: 1Gi
  minStorageGi: 1
  maxStorageGi: 20
  highAvailability: false
  enabled: true
CLASS
}

database_prepare() {
  if [ -z "${E2E_DATABASE_VIP_RANGE:-}" ]; then
    echo "E2E_DATABASE_VIP_RANGE is not set: skipping the database steps"
    state_write database-skip 1
    return 0
  fi
  state_clear database-skip
  database_setting database.enabled true
  database_setting database.vip-range "$E2E_DATABASE_VIP_RANGE"
  kubectl patch addon cnpg --type merge -p '{"spec":{"enabled":true}}'
  wait_condition 900 addon cnpg Ready True
  wait_not_empty 120 get databaseversion postgresql-17 -o jsonpath='{.spec.image}'
  database_class_manifest | kubectl apply -f -
  wait_not_empty 120 get namespace "$database_namespace" -o jsonpath='{.metadata.name}'
  wait_not_empty 120 get resourcequota databases -n "$database_namespace" -o jsonpath='{.status.hard}'
}

database_secret_field() {
  kubectl -n "$NS" get secret "$1" -o jsonpath="{.data.$2}" | base64 -d
}

database_create() {
  if database_skipped; then
    return 0
  fi
  database_manifest orders | kubectl -n "$NS" apply -f -
  wait_jsonpath 1800 Ready -n "$NS" get database orders -o jsonpath='{.status.phase}'
  wait_condition 60 database orders Ready True -n "$NS"
  local uid ip
  uid=$(database_uid orders)
  ip=$(kubectl -n "$NS" get database orders -o jsonpath='{.status.addresses[0].ip}')
  test -n "$ip"
  expect_equal orders-admin "$(kubectl -n "$NS" get database orders -o jsonpath='{.status.credentialsSecret}')" "credentials secret of the database"
  expect_equal dbadmin "$(database_secret_field orders-admin username)" "admin user"
  expect_equal "$ip" "$(database_secret_field orders-admin host)" "admin secret host"
  expect_equal app "$(database_secret_field orders-admin database)" "admin secret database"
  test -n "$(database_secret_field orders-admin password)"
  expect_contains "$(database_secret_field orders-admin uri)" "postgresql://dbadmin:" "admin secret uri"
  expect_equal "" "$(kubectl -n "$NS" get pods -l "cnpg.io/cluster=$uid" -o name)" "engine pods in the project namespace"
  expect_equal "" "$(kubectl -n "$NS" get clusters.postgresql.cnpg.io -o name 2>/dev/null || true)" "engine clusters in the project namespace"
  expect_equal "" "$(kubectl -n "$NS" get secrets -o name | grep -F "$uid" || true)" "platform secrets in the project namespace"
  test -n "$(kubectl -n "$database_namespace" get pods -l "cnpg.io/cluster=$uid" -o name)"
  expect_equal 1 "$(kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=$uid,app.kubernetes.io/component=endpoint" -o name | wc -l)" "proxy pods"
  expect_equal "$ip" "$(kubectl -n "$NS" get pods -l "app.kubernetes.io/instance=$uid,app.kubernetes.io/component=endpoint" -o jsonpath='{.items[0].status.podIP}')" "proxy pod address"
  wait_condition 60 database orders EngineReady True -n "$NS"
  wait_condition 60 database orders GateReady True -n "$NS"
  wait_condition 60 database orders EndpointReady True -n "$NS"
}

database_probe_script() {
  cat <<SCRIPT
#!/bin/sh
printf '\000\000\000\010\004\322\026\057' | nc -w 8 $1 5432 | od -An -c | sed 's/^/DBPROBE-REPLY /' > /dev/console
echo DBPROBE-DONE > /dev/console
SCRIPT
}

database_probe_vm() {
  cat <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: $database_probe_vm
spec:
  instanceType: u1.nano
  preference: cirros
  image:
    name: cirros
    scope: cluster
  rootDisk:
    sizeGi: 2
  networks:
    - subnetRef: default
      securityGroups: [web]
  cloudInit:
    userData: |
$(database_probe_script "$1" | sed 's/^/      /')
  running: true
VM
}

database_probe_reported() {
  [[ "$(guest_console_log "$database_probe_vm")" == *DBPROBE-DONE* ]]
}

database_probe_reply() {
  guest_console_log "$database_probe_vm" | tr -d '\r' | sed -n 's/.*DBPROBE-REPLY //p' | tr -d ' ' | tail -1
}

database_sslrequest() {
  if database_skipped; then
    return 0
  fi
  local ip
  ip=$(kubectl -n "$NS" get database orders -o jsonpath='{.status.addresses[0].ip}')
  database_probe_vm "$ip" | kubectl -n "$NS" apply -f -
  wait_jsonpath 600 Running -n "$NS" get virtualmachine "$database_probe_vm" -o jsonpath='{.status.phase}'
  retry_until 300 database_probe_reported || {
    guest_console_log "$database_probe_vm" >&2 || true
    return 1
  }
  expect_equal N "$(database_probe_reply)" "answer of the proxy to an SSLRequest"
  kubectl -n "$NS" delete virtualmachine "$database_probe_vm"
  wait_gone 180 virtualmachine "$database_probe_vm" -n "$NS"
  wait_gone 180 datavolume "$database_probe_vm-root" -n "$NS"
}

database_access() {
  if database_skipped; then
    return 0
  fi
  kubectl -n "$NS" apply -f - <<ACCESS
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: DatabaseSchema
metadata:
  name: shop
spec:
  databaseRef: orders
---
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: DatabaseUser
metadata:
  name: alice
spec:
  databaseRef: orders
  grants:
    - database: app
      level: readwrite
    - database: shop
      level: readonly
ACCESS
  wait_condition 300 databaseschema shop Ready True -n "$NS"
  wait_condition 300 databaseuser alice Ready True -n "$NS"
  expect_equal shop "$(kubectl -n "$NS" get databaseschema shop -o jsonpath='{.status.name}')" "schema name"
  expect_equal alice "$(kubectl -n "$NS" get databaseuser alice -o jsonpath='{.status.username}')" "user name"
  expect_equal alice "$(database_secret_field alice-db username)" "credentials of the user"
  test -n "$(database_secret_field alice-db password)"
  local roles
  roles=$(database_sql orders -c "select rolname from pg_roles where rolname in ('alice','shop_ro','shop_rw','shop_all','app_rw') order by 1" | tr '\n' ' ')
  expect_equal "alice app_rw shop_all shop_ro shop_rw " "$roles" "roles in the engine"
  expect_equal "shop" "$(database_sql orders -c "select datname from pg_database where datname = 'shop'")" "database of the schema"
  expect_equal 2 "$(database_sql orders -c "select count(*) from pg_auth_members m join pg_roles r on r.oid = m.member where r.rolname = 'alice'")" "group roles of the user"
}

database_backup_manifest() {
  cat <<BACKUP
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata:
  name: $1
  namespace: $database_namespace
  labels:
    app.kubernetes.io/instance: $(database_uid orders)
spec:
  method: plugin
  cluster:
    name: $(database_uid orders)
  pluginConfiguration:
    name: barman-cloud.cloudnative-pg.io
BACKUP
}

database_restore_manifest() {
  cat <<RESTORE
  restore:
    sourceUID: $1
    targetTime: "$2"
RESTORE
}

database_backup_restore() {
  if database_skipped; then
    return 0
  fi
  local uid target
  uid=$(database_uid orders)
  database_sql orders -c "create table if not exists proof (v text); insert into proof values ('before')" >/dev/null
  database_backup_manifest e2e-first | kubectl apply -f -
  wait_jsonpath 900 completed -n "$database_namespace" get backups.postgresql.cnpg.io e2e-first -o jsonpath='{.status.phase}'
  wait_not_empty 600 get database orders -n "$NS" -o jsonpath='{.status.backup.lastAt}'
  database_sql orders -c "insert into proof values ('kept'); select pg_switch_wal()" >/dev/null
  sleep 15
  target=$(database_sql orders -c "select to_char(now() at time zone 'utc', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')")
  sleep 5
  database_sql orders -c "insert into proof values ('after'); select pg_switch_wal()" >/dev/null
  sleep 20
  database_wait_backup_size
  {
    database_manifest orders-restored "$(database_restore_manifest "$uid" "$target")"
  } | kubectl -n "$NS" apply -f -
  wait_jsonpath 1800 Ready -n "$NS" get database orders-restored -o jsonpath='{.status.phase}'
  expect_equal "before kept" "$(database_sql orders-restored -c "select v from proof order by v" | tr '\n' ' ' | sed 's/ $//')" "rows of the restored database"
  expect_equal "kept" "$(database_sql orders-restored -c "select v from proof where v = 'kept'")" "row before the target time"
  expect_equal "" "$(database_sql orders-restored -c "select v from proof where v = 'after'")" "row after the target time"
  expect_equal dbadmin "$(database_secret_field orders-restored-admin username)" "admin secret of the restored database"
  [ "$(database_secret_field orders-admin password)" != "$(database_secret_field orders-restored-admin password)" ]
}

database_wait_backup_size() {
  wait_not_empty 900 get database orders -n "$NS" -o jsonpath='{.status.backup.sizeBytes}'
}

database_isolation() {
  if database_skipped; then
    return 0
  fi
  local resource
  for resource in pods pods/exec pods/log secrets services configmaps deployments.apps clusters.postgresql.cnpg.io objectstores.barmancloud.cnpg.io; do
    expect_equal no "$(as_user alice auth can-i get "$resource" -n "$database_namespace" || true)" "alice reading $resource of $database_namespace"
  done
  expect_equal no "$(as_user alice auth can-i create certificates.cert-manager.io -n "$NS" || true)" "alice making a certificate"
  expect_equal no "$(as_user alice auth can-i create clusterissuers.cert-manager.io || true)" "alice making an issuer"
  expect_equal no "$(as_user alice auth can-i create databaseversions.bedrock.cloudyfolks.io || true)" "alice writing the catalog"
  expect_equal yes "$(as_user alice auth can-i list databaseversions.bedrock.cloudyfolks.io || true)" "alice reading the catalog"
  expect_equal yes "$(as_user alice auth can-i create databases.bedrock.cloudyfolks.io -n "$NS" || true)" "alice making a database"
  expect_refused "Forbidden" as_user alice -n "$database_namespace" get pods
  expect_refused "only an administrator" as_user alice apply -f - <<VERSION
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: DatabaseClass
metadata:
  name: alice-class
spec:
  cpu: 500m
  memory: 1Gi
  minStorageGi: 1
  maxStorageGi: 2
VERSION
}

database_engine_objects() {
  local uid=$1 kind
  for kind in clusters.postgresql.cnpg.io scheduledbackups.postgresql.cnpg.io objectstores.barmancloud.cnpg.io deployments services secrets networkpolicies certificates.cert-manager.io persistentvolumeclaims pods configmaps; do
    kubectl -n "$database_namespace" get "$kind" -l "app.kubernetes.io/instance=$uid" -o name
  done
}

database_engine_gone() {
  [ -z "$(database_engine_objects "$1" 2>/dev/null)" ]
}

database_cleanup() {
  if database_skipped; then
    return 0
  fi
  local first second
  first=$(database_uid orders)
  second=$(database_uid orders-restored)
  kubectl -n "$NS" delete databaseuser alice databaseschema shop --wait=true
  kubectl -n "$NS" delete database orders orders-restored
  wait_gone 600 database orders -n "$NS"
  wait_gone 600 database orders-restored -n "$NS"
  retry_until 300 database_engine_gone "$first" || {
    database_engine_objects "$first" >&2
    return 1
  }
  retry_until 300 database_engine_gone "$second" || {
    database_engine_objects "$second" >&2
    return 1
  }
  expect_equal "" "$(kubectl -n "$NS" get secrets,deployments,configmaps,securitygroups.bedrock.cloudyfolks.io -o name | grep -E 'orders|database-' || true)" "objects of the databases in the project namespace"
  kubectl -n "$database_namespace" get configmap "tombstone-$first"
  kubectl -n "$database_namespace" get configmap "tombstone-$second"
  database_tombstone_has_a_time "tombstone-$first"
  kubectl delete databaseclass "$database_class"
}

database_tombstone_has_a_time() {
  test -n "$(kubectl -n "$database_namespace" get configmap "$1" -o jsonpath='{.metadata.annotations.bedrock\.cloudyfolks\.io/delete-after}')"
}

database_medium_class_manifest() {
  cat <<CLASS
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: DatabaseClass
metadata:
  name: e2e-medium
spec:
  cpu: "1"
  memory: 2Gi
  minStorageGi: 1
  maxStorageGi: 100
  highAvailability: false
  enabled: true
CLASS
}

database_apply() {
  local name=$1 engine=$2 version=$3 backup=$4 storage=$5 extra=${6:-}
  kubectl -n "$NS" apply -f - <<DATABASE
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Database
metadata:
  name: $name
spec:
  engine: $engine
  version: "$version"
  class: $database_class
  storageGi: $storage
  backup:
    enabled: $backup
${extra:+$extra}
DATABASE
}

database_address() {
  kubectl -n "$NS" get database "$1" -o jsonpath='{.status.addresses[0].ip}'
}

database_wait_ready() {
  wait_jsonpath "${2:-900}" Ready -n "$NS" get database "$1" -o jsonpath='{.status.phase}'
}

database_engine_pod() {
  kubectl -n bedrock-dbaas get pods -l "$2" -o jsonpath='{.items[?(@.status.phase=="Running")].metadata.name}' | cut -d' ' -f1
}

database_mysql_query() {
  local uid=$1 pod
  pod=$(database_engine_pod "$uid" "app.kubernetes.io/instance=$uid,mysql.percona.com/primary=true")
  kubectl -n bedrock-dbaas exec -i "$pod" -c mysql -- sh -c 'MYSQL_PWD="$(cat /etc/mysql/mysql-users-secret/root)" exec mysql --no-defaults --socket=/var/lib/mysql/mysql.sock -uroot --batch --raw --skip-column-names'
}

database_valkey_cli() {
  local uid=$1 pod
  shift
  pod=$(database_engine_pod "$uid" "app.kubernetes.io/instance=$uid,app.kubernetes.io/component=engine")
  kubectl -n bedrock-dbaas exec "$pod" -c valkey -- valkey-cli -e --no-auth-warning "$@"
}

database_extras_prepare() {
  if database_skipped; then
    return 0
  fi
  database_medium_class_manifest | kubectl apply -f -
  kubectl patch addon percona-ps --type merge -p '{"spec":{"enabled":true}}'
  wait_condition 300 addon percona-ps Ready True
}

database_mysql_create() {
  if database_skipped; then
    return 0
  fi
  local uid
  database_apply sqldb mysql 8.4 true 5
  database_wait_ready sqldb
  uid=$(database_uid sqldb)
  wait_not_empty 60 -n "$NS" get secret sqldb-admin -o jsonpath='{.data.password}'
  expect_equal "$uid" "$(kubectl -n bedrock-dbaas get perconaservermysql "$uid" -o jsonpath='{.metadata.name}')" "engine object name"
  wait_not_empty 60 -n "$NS" get database sqldb -o jsonpath='{.status.addresses[0].ip}'
  wait_empty 30 -n "$NS" get pods -l app.kubernetes.io/name=percona-server
}

database_valkey_create() {
  if database_skipped; then
    return 0
  fi
  database_apply cachedb valkey 9.1 false 2
  database_wait_ready cachedb
  wait_not_empty 60 -n "$NS" get secret cachedb-admin -o jsonpath='{.data.password}'
  wait_not_empty 60 -n "$NS" get database cachedb -o jsonpath='{.status.addresses[0].ip}'
  wait_empty 30 -n "$NS" get pods -l app.kubernetes.io/component=engine
}

database_proof_script() {
  cat <<SCRIPT
#!/bin/sh
{
  echo "DBPROOF-MYSQL \$(nc -w 5 $1 3306 </dev/null | head -c 5 | od -An -tx1 | tr -d ' \n' | tail -c 2)"
  echo "DBPROOF-VALKEY \$(printf 'PING\r\n' | nc -w 5 $2 6379 | head -1 | cut -c1-6)"
  echo "DBPROOF-DONE"
} > /dev/console
SCRIPT
}

database_proof_vm() {
  cat <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: dbproof-vm
spec:
  instanceType: u1.nano
  preference: cirros
  image:
    name: cirros
    scope: cluster
  rootDisk:
    sizeGi: 2
  networks:
    - subnetRef: default
      securityGroups: [web]
  cloudInit:
    userData: |
$(database_proof_script "$1" "$2" | sed 's/^/      /')
  running: true
VM
}

database_proof_reported() {
  [[ "$(guest_console_log dbproof-vm)" == *DBPROOF-DONE* ]]
}

database_proof_value() {
  guest_console_log dbproof-vm | tr -d '\r' | sed -n "s/.*DBPROOF-$1 //p" | sed 's/ *$//' | tail -1
}

database_vm_greetings() {
  if database_skipped; then
    return 0
  fi
  database_proof_vm "$(database_address sqldb)" "$(database_address cachedb)" | kubectl -n "$NS" apply -f -
  wait_jsonpath 600 Running -n "$NS" get virtualmachine dbproof-vm -o jsonpath='{.status.phase}'
  retry_until 300 database_proof_reported
  expect_equal 0a "$(database_proof_value MYSQL)" "MySQL protocol byte"
  expect_equal -NOAUTH "$(database_proof_value VALKEY)" "Valkey reply to PING"
  kubectl -n "$NS" delete virtualmachine dbproof-vm
}

database_mysql_access() {
  if database_skipped; then
    return 0
  fi
  local uid
  uid=$(database_uid sqldb)
  kubectl -n "$NS" apply -f - <<ACCESS
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: DatabaseUser
metadata:
  name: sqluser
spec:
  databaseRef: sqldb
  grants:
    - database: app
      level: readwrite
---
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: DatabaseSchema
metadata:
  name: reports
spec:
  databaseRef: sqldb
ACCESS
  wait_not_empty 300 -n "$NS" get secret sqluser-db -o jsonpath='{.data.password}'
  retry_until 300 database_mysql_has_user_and_schema "$uid"
}

database_mysql_has_user_and_schema() {
  [ "$(database_mysql_query "$1" <<<"SELECT count(*) FROM mysql.user WHERE user='sqluser';")" = 1 ] &&
    [ "$(database_mysql_query "$1" <<<"SELECT count(*) FROM information_schema.schemata WHERE schema_name='reports';")" = 1 ]
}

database_valkey_access() {
  if database_skipped; then
    return 0
  fi
  local uid password
  uid=$(database_uid cachedb)
  kubectl -n "$NS" apply -f - <<ACCESS
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: DatabaseUser
metadata:
  name: cacheuser
spec:
  databaseRef: cachedb
  keyPrefix: "orders:"
  grants:
    - database: app
      level: readwrite
ACCESS
  wait_not_empty 300 -n "$NS" get secret cacheuser-db -o jsonpath='{.data.password}'
  password=$(database_secret_field cacheuser-db password)
  retry_until 120 database_valkey_user_set "$uid" "$password"
  expect_contains "$(database_valkey_cli "$uid" --user cacheuser --pass "$password" SET other:1 x 2>&1)" NOPERM "key outside the prefix"
  expect_contains "$(database_valkey_cli "$uid" --user cacheuser --pass "$password" SET orders:1 x)" OK "key inside the prefix"
}

database_valkey_user_set() {
  [ "$(database_valkey_cli "$1" --user cacheuser --pass "$2" SET orders:probe x)" = OK ]
}

database_mysql_backup_and_restore() {
  if database_skipped; then
    return 0
  fi
  local uid restored restored_uid
  uid=$(database_uid sqldb)
  database_mysql_query "$uid" <<<"CREATE TABLE app.proof (v int); INSERT INTO app.proof VALUES (42);"
  kubectl -n bedrock-dbaas apply -f - <<BACKUP
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLBackup
metadata:
  name: $uid-e2e
spec:
  clusterName: $uid
  storageName: rgw
BACKUP
  wait_jsonpath 900 Succeeded -n bedrock-dbaas get perconaservermysqlbackup "$uid-e2e" -o jsonpath='{.status.state}'
  database_apply sqlrestore mysql 8.4 true 5 "  restore:
    sourceUID: $uid"
  database_wait_ready sqlrestore
  restored_uid=$(database_uid sqlrestore)
  restored=$(database_mysql_query "$restored_uid" <<<"SELECT v FROM app.proof;")
  expect_equal 42 "$restored" "row of the restored database"
  state_write database-source-uid "$uid"
}

database_mysql_restore_from_deleted() {
  if database_skipped; then
    return 0
  fi
  local uid restored_uid
  uid=$(state_read database-source-uid)
  kubectl -n "$NS" delete database sqldb sqlrestore
  wait_gone 600 -n "$NS" database sqldb
  database_apply sqlimport mysql 8.4 true 5 "  restore:
    sourceUID: $uid"
  database_wait_ready sqlimport
  restored_uid=$(database_uid sqlimport)
  expect_equal 42 "$(database_mysql_query "$restored_uid" <<<"SELECT v FROM app.proof;")" "row restored from a UID without a live database"
}

database_mysql_resize() {
  if database_skipped; then
    return 0
  fi
  local uid before
  database_apply sqlresize mysql 8.4 false 5
  database_wait_ready sqlresize
  uid=$(database_uid sqlresize)
  before=$(database_address sqlresize)
  kubectl -n "$NS" patch database sqlresize --type merge -p '{"spec":{"class":"e2e-medium","storageGi":8}}'
  wait_jsonpath 900 8 -n "$NS" get database sqlresize -o jsonpath='{.status.applied.storageGi}'
  retry_until 600 database_volume_is "$uid" 8Gi
  database_wait_ready sqlresize
  expect_equal "$before" "$(database_address sqlresize)" "proxy address after the resize"
}

database_volume_is() {
  [ "$(kubectl -n bedrock-dbaas get pvc -l "app.kubernetes.io/instance=$1" -o jsonpath='{.items[0].status.capacity.storage}')" = "$2" ]
}

database_postgres_upgrade() {
  if database_skipped; then
    return 0
  fi
  local uid stamp
  database_apply pgupgrade postgresql 16 true 5
  database_wait_ready pgupgrade
  uid=$(database_uid pgupgrade)
  kubectl -n "$NS" patch database pgupgrade --type merge -p '{"spec":{"version":"17"}}'
  wait_jsonpath 1200 17 -n "$NS" get database pgupgrade -o jsonpath='{.status.applied.version}'
  database_wait_ready pgupgrade
  expect_contains "$(kubectl -n "$NS" get database pgupgrade -o jsonpath='{.status.image}')" "postgresql:17." "image after the upgrade"
  expect_contains "$(kubectl -n bedrock-dbaas get backups.postgresql.cnpg.io -o name)" "$uid" "backup before the upgrade"
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  kubectl -n "$NS" annotate database pgupgrade --overwrite "bedrock.cloudyfolks.io/restart=$stamp" bedrock.cloudyfolks.io/refresh-image=true
  wait_jsonpath 600 "$stamp" -n "$NS" get database pgupgrade -o jsonpath='{.status.applied.restartToken}'
  database_wait_ready pgupgrade
}

database_valkey_suspend_resume_logs() {
  if database_skipped; then
    return 0
  fi
  local uid address password logs
  uid=$(database_uid cachedb)
  address=$(database_address cachedb)
  password=$(database_secret_field cachedb-admin password)
  kubectl -n "$NS" patch database cachedb --type merge -p '{"spec":{"suspended":true}}'
  wait_jsonpath 300 Suspended -n "$NS" get database cachedb -o jsonpath='{.status.phase}'
  wait_jsonpath 120 0 -n bedrock-dbaas get deployment "$uid" -o jsonpath='{.spec.replicas}'
  kubectl -n "$NS" patch database cachedb --type merge -p '{"spec":{"suspended":false}}'
  database_wait_ready cachedb 600
  expect_equal "$address" "$(database_address cachedb)" "address after the resume"
  expect_equal "$password" "$(database_secret_field cachedb-admin password)" "admin secret after the resume"
  logs=$(curl -sf --cacert "$BEDROCK_CA" -m 30 -H "Authorization: Bearer $(cat "$TOKEN_DIR/admin")" "https://api.$HOST/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/$NS/databases/cachedb/logs?tail=20")
  expect_contains "$logs" "Ready to accept connections" "engine log through the proxy"
}

database_extras_cleanup() {
  if database_skipped; then
    return 0
  fi
  kubectl -n "$NS" delete databaseuser sqluser cacheuser --ignore-not-found
  kubectl -n "$NS" delete databaseschema reports --ignore-not-found
  kubectl -n "$NS" delete database sqlimport sqlresize cachedb pgupgrade --ignore-not-found
  wait_gone 900 -n "$NS" database sqlimport
  wait_gone 900 -n "$NS" database sqlresize
  wait_gone 900 -n "$NS" database cachedb
  wait_gone 900 -n "$NS" database pgupgrade
  kubectl delete databaseclass e2e-medium --ignore-not-found
}
