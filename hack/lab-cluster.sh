#!/usr/bin/env bash
set -euo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source "$repo/hack/e2e/lib.sh"

NS=${NS:?set NS to the project namespace}
lab_name=${LAB_CLUSTER_NAME:-labk8s}
lab_address=${LAB_CLUSTER_ADDRESS:?set LAB_CLUSTER_ADDRESS to a free address of the default Subnet}
lab_image=${LAB_WORKER_IMAGE:-ubuntu-24.04}
lab_version=${LAB_K0S_VERSION:-v1.36.3+k0s.1}
lab_pod_cidr=100.64.0.0/16
lab_service_cidr=100.65.0.0/16
lab_worker=w1
lab_vm=$lab_name-$lab_worker

usage() {
  cat <<USAGE
usage: NS=<project namespace> PROJECT=<project> LAB_CLUSTER_ADDRESS=<free address> hack/lab-cluster.sh <stage>

stages:
  proof   a hand-made k0smotron Cluster and one worker VM: the worker joins and its Node is Ready
  full    a KubernetesCluster with two workers: both Nodes Ready, a deployment, logs, one worker removed, then the delete
  down    delete the objects of the proof

The host needs a Bedrock cluster with the addon clusters, KVM, internet egress for the worker,
a project with the Subnet default and a SecurityGroup that opens the cluster ports.
USAGE
}

log() {
  printf '== %s\n' "$*"
}

security_group() {
  cat <<GROUP
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: SecurityGroup
metadata:
  name: $lab_name
spec:
  ingress:
    - protocol: tcp
      port: 6443
      cidr: 0.0.0.0/0
    - protocol: tcp
      port: 8132
      cidr: 0.0.0.0/0
    - protocol: tcp
      port: 9443
      cidr: 0.0.0.0/0
    - protocol: all
      securityGroupRef: $lab_name
  egress:
    - protocol: all
      cidr: 0.0.0.0/0
GROUP
}

cluster_annotations() {
  local group
  group=$(kubectl get security-groups.fabric.cloudyfolks.io -l "bedrock.cloudyfolks.io/kind=SecurityGroup,bedrock.cloudyfolks.io/name=$lab_name,bedrock.cloudyfolks.io/project=${PROJECT:?set PROJECT}" -o jsonpath='{.items[0].metadata.name}')
  cat <<ANNOTATIONS
v1.multus-cni.io/default-network: $NS/default
default.$NS.fabric.cloudyfolks.io/port_security: "true"
default.$NS.fabric.cloudyfolks.io/security_groups: $group
default.$NS.fabric.cloudyfolks.io/ip_address: $lab_address
ANNOTATIONS
}

cluster_patch() {
  cat <<PATCH
spec:
  template:
    metadata:
      annotations:
$(cluster_annotations | sed 's/^/        /')
PATCH
}

cluster_manifest() {
  cat <<CLUSTER
apiVersion: k0smotron.io/v1beta2
kind: Cluster
metadata:
  name: $lab_name
spec:
  version: $lab_version
  replicas: 1
  externalAddress: $lab_address
  service:
    type: ClusterIP
    apiPort: 6443
    konnectivityPort: 8132
  storage:
    type: kine
    kine:
      dataSourceURL: sqlite:///var/lib/k0s/db/state.db
  persistence:
    type: pvc
    autoDeletePVCs: true
    persistentVolumeClaim:
      spec:
        accessModes: [ReadWriteOnce]
        storageClassName: block
        resources:
          requests:
            storage: 10Gi
  resources:
    requests:
      cpu: 500m
      memory: 1Gi
    limits:
      cpu: "2"
      memory: 4Gi
  k0sConfig:
    apiVersion: k0s.k0sproject.io/v1beta1
    kind: ClusterConfig
    spec:
      api:
        sans: ["$lab_address"]
      network:
        provider: kuberouter
        podCIDR: $lab_pod_cidr
        serviceCIDR: $lab_service_cidr
  patches:
    - target:
        kind: StatefulSet
        component: control-plane
      patch:
        type: strategic
        content: |
$(cluster_patch | sed 's/^/          /')
CLUSTER
}

token_request() {
  cat <<REQUEST
apiVersion: k0smotron.io/v1beta2
kind: JoinTokenRequest
metadata:
  name: $lab_vm
spec:
  clusterName: $lab_name
  expiry: 24h
  role: worker
REQUEST
}

cloud_init() {
  cat <<CLOUDINIT
#cloud-config
write_files:
  - path: /etc/k0s/join.token
    permissions: "0600"
    content: |
      $1
runcmd:
  - curl -sSLf https://get.k0s.sh | K0S_VERSION=$lab_version sh
  - k0s install worker --token-file /etc/k0s/join.token
  - k0s start
CLOUDINIT
}

worker_vm() {
  cat <<VM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachine
metadata:
  name: $lab_vm
spec:
  compute:
    cores: 2
    memoryMiB: 4096
  image:
    name: $lab_image
    scope: cluster
  rootDisk:
    sizeGi: 40
  networks:
    - subnetRef: default
      securityGroups: [$lab_name]
  cloudInit:
    secretRef:
      name: $lab_vm-cloudinit
  running: true
VM
}

control_plane_ready() {
  [ "$(kubectl -n "$NS" get pod "kmc-$lab_name-0" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ]
}

child_exec() {
  kubectl -n "$NS" exec "kmc-$lab_name-0" -- k0s kubectl --kubeconfig /var/lib/k0s/pki/admin.conf "$@"
}

child_node_ready_in_pod() {
  [ "$(child_exec get node "$lab_vm" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ]
}

stage_proof() {
  log "security group and control plane"
  security_group | kubectl -n "$NS" apply -f -
  wait_condition 180 securitygroup "$lab_name" Ready True -n "$NS"
  cluster_manifest | kubectl -n "$NS" apply -f -
  retry_until 900 control_plane_ready
  wait_not_empty 180 -n "$NS" get secret "$lab_name-kubeconfig" -o jsonpath='{.data.value}'
  log "join token and worker"
  token_request | kubectl -n "$NS" apply -f -
  wait_not_empty 180 -n "$NS" get secret "$lab_vm" -o jsonpath='{.data.token}'
  local token
  token=$(kubectl -n "$NS" get secret "$lab_vm" -o jsonpath='{.data.token}' | base64 -d)
  kubectl -n "$NS" create secret generic "$lab_vm-cloudinit" --from-literal=userdata="$(cloud_init "$token")" --dry-run=client -o yaml | kubectl -n "$NS" apply -f -
  worker_vm | kubectl -n "$NS" apply -f -
  wait_jsonpath 900 Running -n "$NS" get virtualmachine "$lab_vm" -o jsonpath='{.status.phase}'
  log "worker join"
  retry_until 1200 child_node_ready_in_pod
  echo "PROOF-NODE $(child_exec get node -o wide)"
  log "konnectivity: logs and exec in the child"
  child_exec -n kube-system get pods -o wide
  local pod
  pod=$(child_exec -n kube-system get pods -l k8s-app=konnectivity-agent -o jsonpath='{.items[0].metadata.name}')
  child_exec -n kube-system logs "$pod" --tail=5
  child_exec -n kube-system exec "$pod" -- true
  echo "PROOF-DONE"
}

kubernetes_cluster_manifest() {
  cat <<CLUSTER
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: KubernetesCluster
metadata:
  name: $lab_name
spec:
  version: $lab_version
  controlPlane:
    address: $lab_address
    securityGroups: [$lab_name]
  workers:
    - name: w1
      image:
        name: $lab_image
      securityGroups: [$lab_name]
    - name: w2
      image:
        name: $lab_image
      securityGroups: [$lab_name]
CLUSTER
}

ready_nodes() {
  child_exec get nodes -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' | grep -c True
}

two_nodes_ready() {
  [ "$(ready_nodes)" -eq 2 ]
}

hello_logged() {
  [[ "$(child_exec logs deployment/hello 2>/dev/null)" == *lab-log* ]]
}

stage_full() {
  log "security group and cluster"
  security_group | kubectl -n "$NS" apply -f -
  wait_condition 180 securitygroup "$lab_name" Ready True -n "$NS"
  kubernetes_cluster_manifest | kubectl -n "$NS" apply -f -
  wait_jsonpath 900 true -n "$NS" get kubernetescluster "$lab_name" -o jsonpath='{.status.controlPlane.ready}'
  log "two workers join"
  wait_jsonpath 1800 Ready -n "$NS" get kubernetescluster "$lab_name" -o jsonpath='{.status.phase}'
  retry_until 1800 two_nodes_ready
  echo "PROOF-NODES $(child_exec get nodes -o wide)"
  log "a deployment and its logs in the child"
  child_exec create deployment hello --image=docker.io/library/busybox:1.37.0 -- sh -c 'echo lab-log; exec sleep 3600'
  child_exec rollout status deployment/hello --timeout=600s
  retry_until 300 hello_logged
  log "one worker removed"
  kubectl -n "$NS" patch kubernetescluster "$lab_name" --type json -p '[{"op":"remove","path":"/spec/workers/1"}]'
  wait_gone 600 -n "$NS" virtualmachine "$lab_name-w2"
  expect_equal "$lab_name-w1" "$(kubectl -n "$NS" get virtualmachines -l bedrock.cloudyfolks.io/cluster="$lab_name" -o jsonpath='{.items[*].metadata.name}')" "only the first worker VM is left"
  child_exec delete node "$lab_name-w2"
  log "delete"
  kubectl -n "$NS" delete kubernetescluster "$lab_name"
  wait_gone 900 -n "$NS" kubernetescluster "$lab_name"
  wait_empty 60 -n "$NS" get virtualmachines -l bedrock.cloudyfolks.io/cluster="$lab_name"
  wait_gone 60 -n "$NS" statefulset "kmc-$lab_name"
  echo "FULL-DONE"
}

stage_down() {
  kubectl -n "$NS" delete virtualmachine "$lab_vm" --ignore-not-found
  kubectl -n "$NS" delete secret "$lab_vm-cloudinit" --ignore-not-found
  kubectl -n "$NS" delete jointokenrequest.k0smotron.io "$lab_vm" --ignore-not-found --wait=false
  kubectl -n "$NS" delete kubernetescluster "$lab_name" --ignore-not-found
  kubectl -n "$NS" delete cluster.k0smotron.io "$lab_name" --ignore-not-found
  kubectl -n "$NS" delete securitygroup "$lab_name" --ignore-not-found
}

case "${1:-}" in
proof) stage_proof ;;
full) stage_full ;;
down) stage_down ;;
*)
  usage
  exit 2
  ;;
esac
