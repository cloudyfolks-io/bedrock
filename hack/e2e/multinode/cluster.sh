TENANT=acme
NS=acme-web
NS2=acme-data
rook_conf=--conf=/var/lib/rook/rook-ceph/rook-ceph.config

op_exec() {
  timeout 180 kubectl -n rook-ceph exec deploy/rook-ceph-operator -- "$@"
}

ceph_op() {
  op_exec ceph "$rook_conf" --connect-timeout=10 "$@"
}

health_ok() {
  ceph_op status -f json | jq -e '.health.status == "HEALTH_OK"' >/dev/null
}

wait_install() {
  local condition
  kubectl wait --for=condition=Ready node --all --timeout=900s
  for condition in VirtualizationReady PlatformReady StorageReady; do
    wait_condition 3600 cluster cluster "$condition" True || { diagnose_cluster; return 1; }
  done
}

diagnose_cluster() {
  kubectl get nodes,hosts -o wide || true
  kubectl get pods -A -o wide || true
  kubectl get cluster cluster -o yaml || true
  ceph_op status || true
}

diagnose_vms() {
  local namespace pod
  for namespace in "$NS" "$NS2"; do
    kubectl -n "$namespace" get virtualmachines,volumes,virtualmachineinstances.kubevirt.io,virtualmachineinstancemigrations.kubevirt.io,datavolumes,pvc,pods -o wide || true
    for pod in $(kubectl -n "$namespace" get pods -l kubevirt.io=virt-launcher -o name); do
      echo "--- $pod"
      kubectl -n "$namespace" logs "$pod" -c guest-console-log --tail=60 || true
    done
  done
  kubectl get nodes -o wide || true
  kubectl get events -A --sort-by=.lastTimestamp | tail -80 || true
  ceph_op status || true
}

setup_tenant() {
  create_user alice
  kubectl apply -f - <<TENANTS
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
    cpu: "32"
    memory: 64Gi
    storage: 400Gi
    elasticIPs: 2
    projects: 2
  network:
    vpcCIDR: 10.100.0.0/16
TENANTS
  wait_jsonpath 120 10.100.0.0/16 get network "$TENANT" -o jsonpath='{.spec.cidrs[0]}'
  kubectl patch network "$TENANT" --type merge -p '{"spec":{"enableExternal":false}}'
  wait_condition 300 tenant "$TENANT" Ready True
  project_manifest web '[]' '{cpu: "12", memory: 24Gi, storage: 150Gi, vms: 12}' | kubectl apply -f -
  project_manifest data '[]' '{cpu: "12", memory: 24Gi, storage: 150Gi, vms: 12}' | kubectl apply -f -
  wait_condition 300 project web Ready True
  wait_condition 300 project data Ready True
  images_cluster_image
}

etcd_member_count() {
  k0s etcd member-list 2>/dev/null | jq '.members | length'
}

etcd_members_are() {
  [ "$(etcd_member_count)" = "$1" ]
}

nodes_ready_are() {
  [ "$(kubectl get nodes --no-headers | grep -cw Ready)" = "$1" ]
}

check_control_plane() {
  retry_until 1800 nodes_ready_are 3 || { kubectl get nodes -o wide >&2; return 1; }
  expect_equal 3 "$(kubectl get hosts -o json | jq '[.items[] | select(.spec.roles | index("control-plane"))] | length')" "hosts with the control-plane role"
  retry_until 1800 etcd_members_are 3 || { echo "etcd members: $(etcd_member_count), want 3" >&2; return 1; }
  wait_jsonpath 600 3 -n kube-system get daemonset kube-vip -o jsonpath='{.status.numberReady}'
}

osds_per_host() {
  ceph_op osd tree -f json | jq -r '[.nodes[] | select(.type == "host") | (.children // [] | length)] | sort | join(" ")'
}

osds_up_are() {
  [ "$(ceph_op osd stat -f json | jq .num_up_osds)" = "$1" ]
}

mons_in_quorum_are() {
  [ "$(ceph_op quorum_status -f json | jq '.quorum_names | length')" = "$1" ]
}

check_ceph_topology() {
  retry_until 1800 mons_in_quorum_are 3 || {
    ceph_op status >&2
    return 1
  }
  retry_until 1800 osds_up_are 6 || { ceph_op osd tree >&2; return 1; }
  expect_equal "2 2 2" "$(osds_per_host)" "OSDs per host"
  retry_until 3600 health_ok || { ceph_op status >&2; return 1; }
}

check_pool_sizes() {
  local pools
  pools=$(ceph_op osd pool ls detail -f json)
  test "$(jq '[.[] | select(.type == 1)] | length' <<<"$pools")" -gt 0
  expect_equal "" "$(jq -r '[.[] | select(.type == 1 and .size != 3) | "\(.pool_name):\(.size)"] | join(",")' <<<"$pools")" "replicated pools whose size is not 3"
}
