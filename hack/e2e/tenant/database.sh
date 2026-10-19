database_class_manifest() {
  cat <<CLASS
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: DatabaseClass
metadata:
  name: $1
spec:
  cpu: "$2"
  memory: $3
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
  class: e2e-small
  storageGi: $storage
  backup:
    enabled: $backup
${extra:+$extra}
DATABASE
}

database_uid() {
  kubectl -n "$NS" get database "$1" -o jsonpath='{.metadata.uid}'
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

database_secret_field() {
  kubectl -n "$NS" get secret "$1" -o jsonpath="{.data.$2}" | base64 -d
}

database_extras_prepare() {
  database_class_manifest e2e-small 500m 1Gi | kubectl apply -f -
  database_class_manifest e2e-medium 1 2Gi | kubectl apply -f -
  kubectl patch addon percona-ps --type merge -p '{"spec":{"enabled":true}}'
  wait_condition 300 addon percona-ps Ready True
  kubectl patch setting database.enabled --type merge -p '{"spec":{"value":"true"}}'
}

database_mysql_create() {
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
  database_proof_vm "$(database_address sqldb)" "$(database_address cachedb)" | kubectl -n "$NS" apply -f -
  wait_jsonpath 600 Running -n "$NS" get virtualmachine dbproof-vm -o jsonpath='{.status.phase}'
  retry_until 300 database_proof_reported
  expect_equal 0a "$(database_proof_value MYSQL)" "MySQL protocol byte"
  expect_equal -NOAUTH "$(database_proof_value VALKEY)" "Valkey reply to PING"
  kubectl -n "$NS" delete virtualmachine dbproof-vm
}

database_mysql_access() {
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
  local uid restored restored_uid
  uid=$(database_uid sqldb)
  database_mysql_query "$uid" <<<"CREATE TABLE app.proof (v int); INSERT INTO app.proof VALUES (42);"
  kubectl -n bedrock-dbaas apply -f - <<BACKUP
apiVersion: ps.percona.com/v1alpha1
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
  kubectl -n "$NS" delete database sqlimport sqlresize cachedb pgupgrade --ignore-not-found
  wait_gone 900 -n "$NS" database sqlimport
  wait_gone 900 -n "$NS" database sqlresize
  wait_gone 900 -n "$NS" database cachedb
  wait_gone 900 -n "$NS" database pgupgrade
}
