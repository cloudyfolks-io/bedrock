database_import_valkey_probe_pod() {
  local pod=$1 ip=$2
  container_proof_address="$ip" container_proof_pod "$pod" '' |
    kubectl -n "$NS" create --dry-run=client -f - -o json |
    jq '.spec.containers += [{name:"valkey-client", image:"docker.io/amazon/aws-cli:2.37.10", imagePullPolicy:"IfNotPresent", command:["sleep", "7200"]}]' |
    as_operator -n "$NS" create -f -
}

database_import_valkey_request() {
  local pod=$1 target=$2 target_port=$3 identity=$4 password_file=$5 operation=$6 client
  client=$(cat <<'PY'
import errno
import socket
import ssl
import sys


def exchange(connection, response, *parts):
    fields = [b"$%d\r\n%s\r\n" % (len(part), part) for part in parts]
    connection.sendall(b"*%d\r\n" % len(fields) + b"".join(fields))
    return response.readline(4096)


def execute(connection, password, operation):
    key = b"cutover:orders"
    value = b"acknowledged-before-suspend"
    with connection.makefile("rb") as response:
        auth = exchange(connection, response, b"AUTH", b"dbadmin", password)
        if auth.startswith(b"-WRONGPASS "):
            return "WRONG_PASSWORD"
        if auth != b"+OK\r\n":
            raise SystemExit("Unexpected Valkey authentication response")

        if operation == "ping" and exchange(connection, response, b"PING") == b"+PONG\r\n":
            return "PONG"
        if operation == "write":
            if exchange(connection, response, b"SET", key, value) != b"+OK\r\n":
                raise SystemExit("Valkey did not acknowledge the customer write")
            count = exchange(connection, response, b"WAITAOF", b"1", b"0", b"10000")
            if count == b"*2\r\n" and response.readline(64) == b":1\r\n" and response.readline(64) == b":0\r\n":
                return "FSYNCED"
        if operation == "read":
            length = exchange(connection, response, b"GET", key)
            if length == b"$%d\r\n" % len(value) and response.read(len(value) + 2) == value + b"\r\n":
                return "VALUE_MATCHED"
    raise SystemExit("Unexpected Valkey response for " + operation)


def connect(target, port, identity, ca):
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.load_verify_locations(cadata=ca)
    try:
        with socket.create_connection((target, int(port)), timeout=5) as transport:
            return context.wrap_socket(transport, server_hostname=identity)
    except ssl.SSLCertVerificationError as error:
        ip_address_mismatch = 64
        if error.verify_code != ip_address_mismatch:
            raise
        print("WRONG_IDENTITY")
    except (socket.timeout, ssl.SSLEOFError):
        print("BLOCKED")
    except OSError as error:
        blocked = (errno.ECONNREFUSED, errno.ECONNRESET, errno.EHOSTUNREACH, errno.ENETUNREACH, errno.ETIMEDOUT)
        if error.errno not in blocked:
            raise
        print("BLOCKED")
    return None


target, port, identity, operation = sys.argv[1:]
password = sys.stdin.buffer.readline().rstrip(b"\n")
ca = sys.stdin.buffer.read().decode("ascii")
connection = connect(target, port, identity, ca)
if connection is not None:
    with connection:
        connection.settimeout(15)
        print(execute(connection, password, operation))
PY
  )
  {
    cat "$password_file"
    printf '\n'
    cat "$certificate_dir/ca.crt"
  } | kubectl -n "$NS" exec -i "$pod" -c valkey-client -- python3 -c "$client" "$target" "$target_port" "$identity" "$operation"
}

database_import_valkey_expect() {
  local expected=$1 pod=$2 target=$3 target_port=$4 identity=$5 password_file=$6 operation=$7 result
  result=$(database_import_valkey_request "$pod" "$target" "$target_port" "$identity" "$password_file" "$operation") || return 1
  expect_equal "$expected" "$result" "Valkey $operation from $pod to $target:$target_port"
}

database_import_valkey_ping() {
  database_import_valkey_expect "$1" "$2" "$3" "$4" "$address" "$certificate_dir/password" ping
}

database_import_valkey_storage() {
  kubectl -n "$database_namespace" get pvc "db-$uid-data" -o json |
    jq -er 'select(.status.phase == "Bound" and (.metadata.uid | length > 0) and (.spec.volumeName | length > 0)) | [.metadata.uid, .spec.volumeName] | join(" ")'
}

database_import_valkey() (
  local database=imported-cache frontend=legacy-cache claim=database-endpoint-legacy-cache
  local address=100.69.0.241 port=6380 proxy_port=6379 legacy_backend=10.100.0.248
  local job_pod=database-import-cache-job job_address=10.100.0.243
  local customer_pod=database-import-cache-customer customer_address=10.100.0.244
  local legacy_id=33333333-3333-4333-8333-333333333333 project_id=22222222-2222-4222-8222-222222222222
  local management_before managed_before managed_applied network vpc uid subnet_uid subnet_cidr legacy_uid proxy_address
  local certificate_dir certificate_secret= admin_secret= tls_session_pid= tls_session_input= restored= recovery_backup=
  local approved reservation_uid storage engine_uid connection_uri

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
  wait_not_empty 120 get databaseversion valkey-9.1 -o jsonpath='{.spec.image}'

  network=$(kubectl -n "$NS" get subnet default -o jsonpath='{.spec.networkRef}')
  subnet_uid=$(kubectl -n "$NS" get subnet default -o jsonpath='{.metadata.uid}')
  subnet_cidr=$(kubectl -n "$NS" get subnet default -o jsonpath='{.spec.cidr}')
  vpc=$(kubectl get network "$network" -o jsonpath='{.status.vpc}')
  test -n "$subnet_uid"
  test -n "$vpc"
  database_import_valkey_probe_pod "$job_pod" "$job_address"
  database_import_valkey_probe_pod "$customer_pod" "$customer_address"
  kubectl -n "$NS" wait --for=condition=Ready "pod/$job_pod" "pod/$customer_pod" --timeout=180s
  expect_equal "$job_address" "$(kubectl -n "$NS" get pod "$job_pod" -o jsonpath='{.status.podIP}')" "Valkey migration job address"
  expect_equal "$customer_address" "$(kubectl -n "$NS" get pod "$customer_pod" -o jsonpath='{.status.podIP}')" "Valkey customer address"

  database_import_legacy_frontend
  legacy_uid=$(kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.metadata.uid}')
  test -n "$legacy_uid"
  wait_jsonpath 120 "$NS/slr-flb-$frontend" get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.status.service}'
  database_apply "$database" valkey 9.1 false 2 "$(cat <<SPEC
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
  printf '%s' 'wrong-synthetic-password' >"$certificate_dir/wrong-password"
  database_import_claim
  kubectl -n "$NS" patch database "$database" --type merge -p '{"spec":{"suspended":false}}'
  wait_jsonpath 180 'the legacy frontend needs administrator approval to start' -n "$NS" get database "$database" -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'
  expect_equal "$legacy_backend" "$(kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.spec.endpoints[0]}')" "Valkey legacy backend before start approval"
  kubectl_absent deployment "bedrock-db-proxy-$uid" -n "$NS"

  database_import_approve start
  database_wait_ready "$database"
  proxy_address=$(kubectl -n "$NS" get database "$database" -o jsonpath='{.status.proxyAddress}')
  test -n "$proxy_address"
  python3 -c 'import ipaddress, sys
vip, proxy = map(ipaddress.ip_address, sys.argv[1:3])
subnet = ipaddress.ip_network(sys.argv[3])
sys.exit(vip in subnet or proxy not in subnet)' "$address" "$proxy_address" "$subnet_cidr"
  expect_equal "$legacy_uid" "$(kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.metadata.uid}')" "adopted Valkey frontend UID"
  expect_equal "$vpc $address $port/TCP $proxy_address" "$(kubectl get loadbalancers.fabric.cloudyfolks.io "$frontend" -o jsonpath='{.spec.vpc} {.status.vip} {.status.ports} {.spec.endpoints[0]}')" "Valkey frontend and private proxy"
  expect_equal "$address:$port" "$(database_secret_field "$database-admin" host):$(database_secret_field "$database-admin" port)" "published Valkey endpoint"
  expect_equal "$(cat "$certificate_dir/ca.crt")" "$(database_secret_field "$database-admin" 'ca\.crt')" "published Valkey certificate authority"
  connection_uri=$(database_secret_field "$database-admin" uri)
  test "${connection_uri%%:*}" = rediss
  retry_until 90 database_import_valkey_ping PONG "$job_pod" "$address" "$port"
  database_import_valkey_ping PONG "$job_pod" "$proxy_address" "$proxy_port"
  database_import_valkey_ping BLOCKED "$customer_pod" "$address" "$port"
  database_import_valkey_ping BLOCKED "$customer_pod" "$proxy_address" "$proxy_port"

  database_import_approve open
  retry_until 90 database_import_valkey_ping PONG "$customer_pod" "$address" "$port"
  database_import_valkey_ping PONG "$customer_pod" "$proxy_address" "$proxy_port"
  retry_until 90 database_import_valkey_ping BLOCKED "$job_pod" "$address" "$port"
  database_import_valkey_ping BLOCKED "$job_pod" "$proxy_address" "$proxy_port"
  database_import_valkey_expect WRONG_IDENTITY "$customer_pod" "$address" "$port" "$proxy_address" "$certificate_dir/password" ping
  database_import_valkey_expect WRONG_PASSWORD "$customer_pod" "$address" "$port" "$address" "$certificate_dir/wrong-password" ping
  database_import_valkey_expect FSYNCED "$customer_pod" "$address" "$port" "$address" "$certificate_dir/password" write

  approved=$(database_import_approval_ids)
  reservation_uid=$(kubectl get vips.fabric.cloudyfolks.io "db-$uid-frontend-vip" -o jsonpath='{.metadata.uid}')
  engine_uid=$(kubectl -n "$database_namespace" get deployment "db-$uid" -o jsonpath='{.metadata.uid}')
  storage=$(database_import_valkey_storage)
  test -n "$reservation_uid"
  test -n "$engine_uid"
  test -n "$storage"
  kubectl -n "$NS" patch database "$database" --type merge -p '{"spec":{"suspended":true}}'
  wait_jsonpath 300 Suspended -n "$NS" get database "$database" -o jsonpath='{.status.phase}'
  database_import_wait_withdrawn
  wait_jsonpath 120 0 -n "$database_namespace" get deployment "db-$uid" -o jsonpath='{.spec.replicas}'
  wait_empty 300 -n "$database_namespace" get pods -l "app.kubernetes.io/instance=db-$uid,app.kubernetes.io/component=engine"
  expect_equal "$storage" "$(database_import_valkey_storage)" "Valkey volume during suspension"
  expect_equal "$approved" "$(database_import_approval_ids)" "Valkey approvals during suspension"
  expect_equal "$reservation_uid $address" "$(kubectl get vips.fabric.cloudyfolks.io "db-$uid-frontend-vip" -o jsonpath='{.metadata.uid} {.status.v4ip}')" "Valkey VIP during suspension"
  database_import_valkey_ping BLOCKED "$customer_pod" "$address" "$port"
  database_import_valkey_ping BLOCKED "$customer_pod" "$proxy_address" "$proxy_port"

  as_user bob -n "$NS" patch database "$database" --type merge -p '{"spec":{"suspended":false}}'
  database_wait_ready "$database"
  retry_until 90 database_import_valkey_ping PONG "$customer_pod" "$address" "$port"
  database_import_valkey_expect VALUE_MATCHED "$customer_pod" "$address" "$port" "$address" "$certificate_dir/password" read
  expect_equal "$uid $proxy_address" "$(kubectl -n "$NS" get database "$database" -o jsonpath='{.metadata.uid} {.status.proxyAddress}')" "Valkey database and private proxy after resume"
  expect_equal "$engine_uid" "$(kubectl -n "$database_namespace" get deployment "db-$uid" -o jsonpath='{.metadata.uid}')" "Valkey engine after resume"
  expect_equal "$storage" "$(database_import_valkey_storage)" "Valkey volume after resume"
  expect_equal "$approved" "$(database_import_approval_ids)" "Valkey approvals after resume"
  expect_equal "$reservation_uid $address" "$(kubectl get vips.fabric.cloudyfolks.io "db-$uid-frontend-vip" -o jsonpath='{.metadata.uid} {.status.v4ip}')" "Valkey VIP after resume"
  database_import_valkey_ping BLOCKED "$job_pod" "$address" "$port"
  database_import_valkey_ping BLOCKED "$job_pod" "$proxy_address" "$proxy_port"
)
