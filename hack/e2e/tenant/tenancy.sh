create_user() {
  local name=$1 groups=${2:-}
  kubectl apply -f - <<USER
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: User
metadata:
  name: $name
  namespace: bedrock-system
  labels:
    bedrock.cloudyfolks.io/kind: User
    bedrock.cloudyfolks.io/name: $name
spec:
  username: $name
  displayName: $name
  groups: [$groups]
USER
}

create_token() {
  local user=$1 token token_name
  token=$(python3 -c 'import secrets, string; print("brk_" + "".join(secrets.choice(string.ascii_letters + string.digits) for _ in range(40)))')
  token_name=$(printf '%s' "$token" | sha256sum | awk '{print $1}')
  kubectl apply -f - <<TOKEN
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: APIToken
metadata:
  name: $token_name
  namespace: bedrock-system
  labels:
    bedrock.cloudyfolks.io/kind: APIToken
    bedrock.cloudyfolks.io/name: ${token_name:0:63}
spec:
  userRef: $user
  description: e2e tenant
TOKEN
  (umask 077 && printf '%s' "$token" >"$TOKEN_DIR/$user")
}

token_authenticates() {
  local user=$1 token
  token=$(cat "$TOKEN_DIR/$user")
  [ "$(kubectl --kubeconfig /dev/null --server "https://$VIP:6443" --certificate-authority /var/lib/k0s/pki/ca.crt --token "$token" auth whoami -o jsonpath='{.status.userInfo.username}')" = "bedrock:$user" ]
}

tenancy_users() {
  local user
  create_user alice
  create_user bob
  create_user carol
  create_user dana ops
  create_user erin
  kubectl apply -f - <<GROUP
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Group
metadata:
  name: ops
  namespace: bedrock-system
  labels:
    bedrock.cloudyfolks.io/kind: Group
    bedrock.cloudyfolks.io/name: ops
spec:
  members: [dana]
GROUP
  for user in alice bob carol dana erin admin; do
    create_token "$user"
    retry_until 120 token_authenticates "$user"
  done
}

tenancy_tenant() {
  kubectl apply -f - <<TENANT
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Tenant
metadata:
  name: $TENANT
spec:
  displayName: Acme
  owners:
    - kind: User
      name: alice
  quota:
    cpu: "8"
    memory: 16Gi
    storage: 200Gi
    elasticIPs: 2
    projects: 2
  network:
    vpcCIDR: 10.100.0.0/16
TENANT
  wait_jsonpath 120 10.100.0.0/16 get network "$TENANT" -o jsonpath='{.spec.cidr}'
  expect_equal Tenant "$(kubectl get network "$TENANT" -o jsonpath='{.metadata.ownerReferences[0].kind}')" "owner of the Network"
  kubectl get clusterrole "tenant:$TENANT:owner"
  expect_equal bedrock:alice "$(kubectl get clusterrolebinding "tenant:$TENANT:owner" -o jsonpath='{.subjects[0].name}')" "tenant owner binding"
  wait_condition 300 tenant "$TENANT" Ready True
  wait_not_empty 60 get tenant "$TENANT" -o jsonpath='{.status.vpc}'
}

project_manifest() {
  local name=$1 members=$2 quota=$3 tenant=${4:-$TENANT}
  cat <<PROJECT
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Project
metadata:
  name: $name
spec:
  tenantRef: $tenant
  displayName: $name
  members: $members
  quota: $quota
PROJECT
}

tenancy_projects() {
  local members='[{ref: {kind: User, name: bob}, role: editor}, {ref: {kind: User, name: carol}, role: viewer}, {ref: {kind: User, name: dana}, role: owner}, {ref: {kind: Group, name: ops}, role: owner}]'
  local quota='{cpu: "4", memory: 8Gi, storage: 100Gi, vms: 5}' labels
  project_manifest "$PROJECT" "$members" "$quota" | as_user alice create -f -
  project_manifest rival '[]' "$quota" other | expect_refused "may not manage tenant other" as_user alice create -f -
  wait_condition 300 project "$PROJECT" Ready True
  labels=$(kubectl get namespace "$NS" --show-labels)
  expect_contains "$labels" "bedrock.cloudyfolks.io/tenant=$TENANT" "namespace labels"
  expect_contains "$labels" "bedrock.cloudyfolks.io/project=$PROJECT" "namespace labels"
  expect_contains "$labels" "application-aware-quota/enable-gating" "namespace labels"
  expect_equal 10.100.0.0/24 "$(kubectl -n "$NS" get subnet default -o jsonpath='{.spec.cidr}')" "default subnet"
  expect_equal 10.100.0.0/24 "$(kubectl get project "$PROJECT" -o jsonpath='{.status.subnet}')" "project status subnet"
  kubectl -n "$NS" get applicationawareresourcequotas.aaq.kubevirt.io project-quota -o jsonpath='{.spec.hard}' | grep -q 'requests.cpu'
  expect_equal 4 "$(kubectl -n "$NS" get applicationawareresourcequotas.aaq.kubevirt.io project-quota -o jsonpath='{.spec.hard.requests\.cpu}')" "aaq cpu"
  kubectl -n "$NS" get rolebinding bedrock-project-owner bedrock-project-editor bedrock-project-viewer
  kubectl -n "$NS" get networkpolicy default
  project_manifest data '[]' '{cpu: "4", memory: 8Gi, storage: 100Gi}' | as_user alice create -f -
  wait_jsonpath 120 10.100.1.0/24 get project data -o jsonpath='{.status.subnet}'
  wait_jsonpath 120 2 get tenant "$TENANT" -o jsonpath='{.status.usage.projects}'
  project_manifest extra '[]' '{cpu: "1", memory: 1Gi, storage: 1Gi}' | expect_refused "has reached its project quota of 2" as_user alice create -f -
}

sleeper_manifest() {
  local name=$1 cpu=$2 image=$3
  cat <<DEPLOYMENT
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $name
spec:
  replicas: 1
  selector:
    matchLabels:
      app: $name
  template:
    metadata:
      labels:
        app: $name
    spec:
      containers:
        - name: pause
          image: $image
          imagePullPolicy: Never
          resources:
            requests:
              cpu: "$cpu"
              memory: 64Mi
DEPLOYMENT
}

tenancy_workloads() {
  local pause
  pause=$(k0s ctr --namespace k8s.io images ls -q | grep '/pause:' | head -1)
  test -n "$pause"
  sleeper_manifest sleeper 1 "$pause" | as_user bob -n "$NS" create -f -
  sleeper_manifest big 5 "$pause" | as_user bob -n "$NS" create -f -
  retry_until 180 pod_runs sleeper
  retry_until 120 pod_is_gated big
  sleep 30
  test -z "$(kubectl -n "$NS" get pod -l app=big -o jsonpath='{.items[0].status.containerStatuses[0].state.running}')"
  expect_equal no "$(as_user carol -n "$NS" auth can-i get secrets)" "carol reads secrets"
  expect_equal no "$(as_user carol -n "$NS" auth can-i create deployments)" "carol creates deployments"
  expect_equal yes "$(as_user carol -n "$NS" auth can-i get pods)" "carol reads pods"
  expect_equal no "$(as_user bob -n "$NS" auth can-i create virtualmachines.kubevirt.io)" "bob creates kubevirt VMs"
  expect_refused "are not allowed in a project" as_user bob -n "$NS" run privileged --image="$pause" --overrides='{"spec":{"containers":[{"name":"privileged","image":"'"$pause"'","imagePullPolicy":"Never","securityContext":{"privileged":true}}]}}'
  kubectl -n "$NS" delete deployment sleeper big
}

pod_runs() {
  [ "$(kubectl -n "$NS" get pod -l app="$1" -o jsonpath='{.items[0].status.phase}')" = Running ]
}

pod_is_gated() {
  [ -n "$(kubectl -n "$NS" get pod -l app="$1" -o jsonpath='{.items[0].spec.schedulingGates}')" ]
}

tenancy_quota_edges() {
  as_user alice patch project data --type merge -p '{"spec":{"quota":{"cpu":"6"}}}'
  wait_reason 120 project data QuotaReady TenantQuotaExceeded
  expect_equal 4 "$(kubectl -n "$TENANT-data" get applicationawareresourcequotas.aaq.kubevirt.io project-quota -o jsonpath='{.spec.hard.requests\.cpu}')" "aaq cpu keeps its value"
  expect_equal True "$(kubectl get project "$PROJECT" -o jsonpath='{.status.conditions[?(@.type=="QuotaReady")].status}')" "older project keeps its quota"
  as_user alice patch project data --type merge -p '{"spec":{"quota":{"cpu":"4"}}}'
  wait_condition 120 project data QuotaReady True
}
