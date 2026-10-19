container_proof_image=docker.io/library/busybox:1.37.0
container_proof_address=10.100.0.200
container_proof_operator=system:serviceaccount:bedrock-system:bedrock-operator

as_operator() {
  kubectl --as="$container_proof_operator" --as-group=system:masters "$@"
}

container_proof_annotations() {
  local group
  group=$(fabric_by_label security-groups SecurityGroup web -o jsonpath='{.items[0].metadata.name}')
  cat <<ANNOTATIONS
v1.multus-cni.io/default-network: $NS/default
default.$NS.fabric.cloudyfolks.io/port_security: "true"
default.$NS.fabric.cloudyfolks.io/security_groups: $group
default.$NS.fabric.cloudyfolks.io/ip_address: $container_proof_address
ANNOTATIONS
}

container_proof_exec_probe() {
  cat <<'PROBE'
    readinessProbe:
      exec:
        command: ["wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/"]
      periodSeconds: 3
PROBE
}

container_proof_http_probe() {
  cat <<'PROBE'
    readinessProbe:
      httpGet:
        path: /
        port: 8080
      periodSeconds: 3
PROBE
}

container_proof_pod_spec() {
  cat <<SPEC
enableServiceLinks: false
automountServiceAccountToken: false
dnsPolicy: None
dnsConfig:
  nameservers: ["198.51.100.53"]
containers:
  - name: app
    image: $container_proof_image
    imagePullPolicy: IfNotPresent
    command: ["sh", "-c", "mkdir -p /www && echo proof-reply >/www/index.html && exec httpd -f -p 8080 -h /www"]
    ports:
      - containerPort: 8080
$1
SPEC
}

container_proof_pod() {
  cat <<POD
apiVersion: v1
kind: Pod
metadata:
  name: $1
  labels:
    proof: $1
  annotations:
$(container_proof_annotations | sed 's/^/    /')
spec:
$(container_proof_pod_spec "$2" | sed 's/^/  /')
POD
}

container_proof_deployment() {
  cat <<DEPLOYMENT
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $1
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      proof: $1
  template:
    metadata:
      labels:
        proof: $1
      annotations:
$(container_proof_annotations | sed 's/^/        /')
    spec:
$(container_proof_pod_spec "$2" | sed 's/^/      /')
DEPLOYMENT
}

container_proof_runtime_pod() {
  cat <<POD
apiVersion: v1
kind: Pod
metadata:
  name: proof-kata
spec:
  runtimeClassName: kata
  restartPolicy: Never
  containers:
    - name: app
      image: docker.io/amazon/aws-cli:2.37.10
      imagePullPolicy: IfNotPresent
      command:
        - python3
        - -c
        - "import os; open('/data/f', 'w').close(); os.setxattr('/data/f', 'user.proof', b'1'); print('xattr', os.getxattr('/data/f', 'user.proof').decode())"
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      emptyDir: {}
POD
}

container_proof_vm_script() {
  cat <<SCRIPT
#!/bin/sh
{
  echo "PROOF-POD \$(wget -q -O- -T 5 http://$1:8080/)"
  echo "PROOF-VIP \$(wget -q -O- -T 5 http://$2:8080/)"
  echo "PROOF-DONE"
} > /dev/console
SCRIPT
}

container_proof_vm() {
  cat <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: proof-vm
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
$(container_proof_vm_script "$1" "$2" | sed 's/^/      /')
  running: true
VM
}

container_proof_balancer() {
  local subnet=$1 vip=${2:-}
  cat <<BALANCER
apiVersion: fabric.cloudyfolks.io/v1
kind: Vip
metadata:
  name: proof-vip
  labels:
    bedrock.cloudyfolks.io/project: $PROJECT
spec:
  namespace: $NS
  subnet: $subnet
  type: switch_lb_vip
---
apiVersion: fabric.cloudyfolks.io/v1
kind: SwitchLBRule
metadata:
  name: proof-rule
  labels:
    bedrock.cloudyfolks.io/project: $PROJECT
spec:
  vip: ${vip:-proof-vip}
  namespace: $NS
  selector: []
  endpoints: ["$container_proof_address"]
  ports:
    - name: http
      port: 8080
      targetPort: 8080
      protocol: TCP
BALANCER
}

container_proof_fabric_ip() {
  kubectl get ips.fabric.cloudyfolks.io -o json |
    jq -r --arg ns "$NS" --arg pod "$1" '.items[] | select(.spec.namespace == $ns and .spec.podName == $pod) | .metadata.labels["fabric.cloudyfolks.io/subnet"]'
}

container_proof_ready() {
  [ "$(kubectl -n "$NS" get pod "$1" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = True ]
}

container_proof_vm_reported() {
  [[ "$(guest_console_log proof-vm)" == *PROOF-DONE* ]]
}

container_proof_vm_value() {
  guest_console_log proof-vm | tr -d '\r' | sed -n "s/.*PROOF-$1 //p" | sed 's/ *$//' | tail -1
}

container_proof_pod_checks() {
  local subnet vip
  container_proof_pod proof-pod "$(container_proof_exec_probe)" | as_operator -n "$NS" apply -f -
  retry_until 180 container_proof_ready proof-pod
  expect_equal "1" "$(kubectl -n "$NS" exec proof-pod -- sh -c "ip -o link show | grep -vc ' lo:'")" "pod interfaces"
  expect_contains "$(kubectl -n "$NS" exec proof-pod -- ip -4 -o addr show)" "$container_proof_address/" "pinned pod address"
  expect_equal "$container_proof_address" "$(kubectl -n "$NS" get pod proof-pod -o jsonpath='{.status.podIP}')" "pod ip in the status"
  subnet=$(fabric_by_label subnets Subnet default -o jsonpath='{.items[0].metadata.name}')
  expect_equal "$subnet" "$(container_proof_fabric_ip proof-pod)" "fabric ip object of the pod"
  container_proof_balancer "$subnet" | kubectl apply -f -
  wait_not_empty 120 get vips.fabric.cloudyfolks.io proof-vip -o jsonpath='{.status.v4ip}'
  vip=$(kubectl get vips.fabric.cloudyfolks.io proof-vip -o jsonpath='{.status.v4ip}')
  retry_until 120 container_proof_rule_programmed
  container_proof_vm "$container_proof_address" "$vip" | kubectl -n "$NS" apply -f -
  wait_jsonpath 600 Running -n "$NS" get virtualmachine proof-vm -o jsonpath='{.status.phase}'
  retry_until 300 container_proof_vm_reported
  expect_equal proof-reply "$(container_proof_vm_value POD)" "vm reaches the pod"
  expect_equal proof-reply "$(container_proof_vm_value VIP)" "vm reaches the pod through the switch load balancer"
}

container_proof_rule_programmed() {
  [ -n "$(kubectl get switchlbrules.fabric.cloudyfolks.io proof-rule -o jsonpath='{.status.service}')" ]
}

container_proof_deployment_checks() {
  container_proof_deployment proof-deploy "$(container_proof_exec_probe)" | as_operator -n "$NS" apply -f -
  wait_jsonpath 180 1 -n "$NS" get deployment proof-deploy -o jsonpath='{.status.availableReplicas}'
  expect_equal "$container_proof_address" "$(kubectl -n "$NS" get pods -l proof=proof-deploy -o jsonpath='{.items[0].status.podIP}')" "deployment pod address"
}

container_proof_probe_limit() {
  container_proof_pod proof-http "$(container_proof_http_probe)" | as_operator -n "$NS" apply -f -
  wait_jsonpath 120 Running -n "$NS" get pod proof-http -o jsonpath='{.status.phase}'
  sleep 45
  if container_proof_ready proof-http; then
    echo "a pod with an httpGet probe became Ready: the node reaches the pod network" >&2
    return 1
  fi
}

container_proof_runtime_checks() {
  if [ ! -e /dev/kvm ] || ! kubectl get runtimeclass kata >/dev/null 2>&1; then
    echo "no KVM or no kata runtime class: skipping the kata checks"
    return 0
  fi
  container_proof_runtime_pod | kubectl -n "$NS" apply -f -
  wait_jsonpath 300 Succeeded -n "$NS" get pod proof-kata -o jsonpath='{.status.phase}'
  expect_equal "xattr 1" "$(kubectl -n "$NS" logs proof-kata)" "xattr in an emptyDir of a kata pod"
}

container_proof_tenant_refusal() {
  container_proof_deployment proof-denied "$(container_proof_exec_probe)" |
    expect_refused "only the platform may set network selection annotations" as_user e2e-user --as-group=system:masters -n "$NS" apply -f -
}

container_proof_cleanup() {
  if [ "$1" -ne 0 ]; then
    kubectl -n "$NS" describe pod proof-pod proof-http proof-kata >&2 || true
    guest_console_log proof-vm >&2 || true
  fi
  kubectl -n "$NS" delete pod proof-pod proof-http proof-kata --ignore-not-found --wait=false
  kubectl -n "$NS" delete deployment proof-deploy --ignore-not-found
  kubectl -n "$NS" delete virtualmachine proof-vm --ignore-not-found
  kubectl delete switchlbrules.fabric.cloudyfolks.io proof-rule --ignore-not-found
  kubectl delete vips.fabric.cloudyfolks.io proof-vip --ignore-not-found
  wait_empty 180 -n "$NS" get pods -l 'proof'
  wait_gone 180 virtualmachine proof-vm -n "$NS"
  wait_gone 180 datavolume proof-vm-root -n "$NS"
  wait_gone 180 switchlbrules.fabric.cloudyfolks.io proof-rule
  wait_gone 180 vips.fabric.cloudyfolks.io proof-vip
}

container_network_proof() {
  (
    trap 'container_proof_cleanup "$?"' EXIT
    container_proof_tenant_refusal
    container_proof_pod_checks
    kubectl -n "$NS" delete pod proof-pod --wait=true
    wait_empty 60 -n "$NS" get pods -l proof=proof-pod
    container_proof_deployment_checks
    kubectl -n "$NS" delete deployment proof-deploy
    wait_empty 180 -n "$NS" get pods -l proof=proof-deploy
    container_proof_probe_limit
    container_proof_runtime_checks
  )
}
