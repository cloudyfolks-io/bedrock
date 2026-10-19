owned_kinds=(
  virtualmachines.kubevirt.io
  deployments.apps
  horizontalpodautoscalers.autoscaling
  datavolumes.cdi.kubevirt.io
  persistentvolumeclaims
  volumesnapshots.snapshot.storage.k8s.io
  subnets.fabric.cloudyfolks.io
  ovn-eips.fabric.cloudyfolks.io
  ovn-fips.fabric.cloudyfolks.io
  router-lb-rules.fabric.cloudyfolks.io
  dns-zones.fabric.cloudyfolks.io
  security-groups.fabric.cloudyfolks.io
  backups.velero.io
  schedules.velero.io
  restores.velero.io
  cephobjectstoreaccounts.ceph.rook.io
  cephobjectstoreusers.ceph.rook.io
)

owned_objects_gone() {
  local joined
  joined=$(IFS=,; echo "${owned_kinds[*]}")
  list_empty get "$joined" -A -l "bedrock.cloudyfolks.io/project=$PROJECT"
}

cascade_project() {
  kubectl delete project data
  wait_gone 300 namespace acme-data
  kubectl delete project "$PROJECT"
  wait_gone 600 namespace "$NS"
  retry_until 300 owned_objects_gone || {
    kubectl get "$(IFS=,; echo "${owned_kinds[*]}")" -A -l "bedrock.cloudyfolks.io/project=$PROJECT" >&2
    return 1
  }
  wait_empty 120 get subnets.fabric.cloudyfolks.io -l "bedrock.cloudyfolks.io/project=$PROJECT"
}

cascade_tenant() {
  kubectl delete tenant "$TENANT"
  wait_gone 300 network "$TENANT"
  wait_gone 300 clusterrole "tenant:$TENANT:owner"
  wait_gone 300 clusterrolebinding "tenant:$TENANT:owner"
  wait_gone 300 vpcs.fabric.cloudyfolks.io "$TENANT"
}
