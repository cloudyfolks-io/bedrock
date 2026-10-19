etcd_backups=/var/lib/bedrock/backups
restore_vms=(lab-rs-1 lab-rs-2 lab-rs-3)

restore_fixture() {
  local index
  for index in 1 2 3; do
    vm_manifest "lab-rs-$index" "$NS" "$(guest_script plain '')" "$(
      network_yaml default
      prefer_node_yaml "$(lab_node "$index")"
    )" | kubectl apply -f -
  done
  for index in 1 2 3; do
    start_guest "$NS" "lab-rs-$index"
  done
}

vm_listing() {
  local name
  for name in "${restore_vms[@]}"; do
    echo "$name $(vm_node "$NS" "$name") $(guest_boot "$NS" "$name")"
  done
}

listing_complete() {
  [ "$(vm_listing | awk 'NF == 3' | wc -l)" -eq "${#restore_vms[@]}" ]
}

eviction_events() {
  kubectl get events -A --field-selector reason=TaintManagerEviction --no-headers 2>/dev/null | wc -l || true
}

snapshot_after() {
  find "$etcd_backups" -maxdepth 1 -name 'etcd-*.tar.gz.age' |
    awk -v floor="$1" '{ stamp = $0; sub(/.*\/etcd-/, "", stamp); sub(/\.tar\.gz\.age$/, "", stamp); if (stamp > floor) print }' |
    sort | tail -n 1
}

snapshot_written() {
  [ -n "$(snapshot_after "$1")" ]
}

backup_work_idle() {
  [ -z "$(find "$etcd_backups" -maxdepth 1 -name '.work-*')" ]
}

schedule_reset_applied() {
  local applied default
  applied=$(kubectl get setting backup.etcd-schedule -o jsonpath='{.status.applied}')
  default=$(kubectl get setting backup.etcd-schedule -o jsonpath='{.status.default}')
  [ -n "$applied" ] && [ "$applied" = "$default" ]
}

take_etcd_snapshot() {
  local floor=$1
  kubectl patch setting backup.etcd-schedule --type merge -p '{"spec":{"value":"* * * * *"}}' >/dev/null
  retry_until 1800 snapshot_written "$floor"
  kubectl patch setting backup.etcd-schedule --type merge -p '{"spec":{"value":""}}' >/dev/null
  retry_until 300 schedule_reset_applied
  retry_until 1800 backup_work_idle
  snapshot_after "$floor"
}

restore_node_one() {
  local script
  install -m 0755 "$bedrock_bin" /usr/local/bin/bedrock
  script=$(mktemp)
  docs_restore_script "For a sealed snapshot, use this script." "$repo/docs/backup.md" >"$script"
  docs_restore_script_valid "$script" || {
    echo "docs/backup.md has no sealed snapshot restore script under Etcd snapshots" >&2
    return 1
  }
  sed -i "1s#^SNAPSHOT=[^ ]*#SNAPSHOT=$1#;1s# sh -eu # sh -eux #" "$script"
  bash -x "$script"
  rm -f "$script"
}

lease_renewed_since() {
  local renewed
  renewed=$(kubectl -n kube-node-lease get lease "$2" -o jsonpath='{.spec.renewTime}') || return 1
  [ -n "$renewed" ] && [ "$(date -u -d "$renewed" +%s)" -ge "$1" ]
}

leases_renewed_since() {
  local index
  for index in 1 2 3; do
    lease_renewed_since "$1" "$(lab_node "$index")" || return 1
  done
}

restore_settle() {
  local name
  retry_until 1800 leases_renewed_since "$1"
  kubectl wait --for=condition=Ready node --all --timeout=900s >/dev/null
  for name in "${restore_vms[@]}"; do
    wait_vm_running 1800 "$NS" "$name"
  done
  retry_until 1800 listing_complete
  vm_listing
}
