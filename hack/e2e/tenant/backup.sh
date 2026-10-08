backup_target_url='s3://seaweedfs.backup-e2e.svc:8333/bedrock-backups?insecure=true'

s3_listing() {
  kubectl -n "$S3_NS" run "s3ls-$RANDOM" --rm -i --restart=Never --image=docker.io/amazon/aws-cli:2.37.10 --image-pull-policy=IfNotPresent \
    --env=AWS_ACCESS_KEY_ID=e2e-access-key --env=AWS_SECRET_ACCESS_KEY=e2e-secret-key --env=AWS_DEFAULT_REGION=us-east-1 --env=AWS_EC2_METADATA_DISABLED=true -- \
    --endpoint-url http://seaweedfs.backup-e2e.svc:8333 s3 ls "s3://bedrock-backups/backups/"
}

listing_has() {
  s3_listing | grep -q "$1/"
}

listing_lacks() {
  ! s3_listing | grep -q "$1/"
}

backup_target() {
  kubectl apply -f test/e2e/tenant/fixtures/seaweedfs.yaml -f test/e2e/tenant/fixtures/bucket-job.yaml
  kubectl -n "$S3_NS" rollout status deployment/seaweedfs --timeout=300s
  kubectl -n "$S3_NS" wait --for=condition=complete job/make-bucket --timeout=300s
  kubectl -n bedrock-system create secret generic backup-e2e \
    --from-literal=access-key-id=e2e-access-key --from-literal=secret-access-key=e2e-secret-key --from-literal=repository-password=e2e-repo-password
  kubectl patch setting backup.credentials-secret --type merge -p '{"spec":{"value":"backup-e2e"}}'
  kubectl patch setting backup.target --type merge -p "{\"spec\":{\"value\":\"$backup_target_url\"}}"
  wait_jsonpath 300 Available -n velero get backupstoragelocation default -o jsonpath='{.status.phase}'
  kubectl -n velero rollout status deployment/velero --timeout=300s
  kubectl -n velero rollout status daemonset/node-agent --timeout=300s
  kubectl get volumesnapshotclass velero-block
  wait_condition 60 setting backup.target Ready True
}

backup_once() {
  local id
  kubectl -n "$NS" apply -f - <<BACKUP
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachineBackup
metadata:
  name: once
spec:
  vmRef: vm1
  target: default
BACKUP
  wait_jsonpath 900 Completed -n "$NS" get virtualmachinebackup once -o jsonpath='{.status.phase}'
  id=$(kubectl -n "$NS" get virtualmachinebackup once -o jsonpath='{.status.backups[0].id}')
  test -n "$id"
  expect_equal 1 "$(kubectl -n "$NS" get virtualmachinebackup once -o json | jq '.status.backups | length')" "backup entries"
  wait_not_empty 60 -n "$NS" get virtualmachinebackup once -o jsonpath='{.status.lastBackup}'
  expect_equal Completed "$(kubectl -n velero get backup "$id" -o jsonpath='{.status.phase}')" "velero backup phase"
  expect_equal vm1 "$(kubectl -n "$NS" get virtualmachine vm1 -o jsonpath='{.metadata.labels.bedrock\.cloudyfolks\.io/backup-vm}')" "backup label on the VM"
  retry_until 120 listing_has "$id"
  sleep 60
  expect_equal 1 "$(kubectl -n velero get backups -l bedrock.cloudyfolks.io/name=once -o name | wc -l)" "velero backups of once"
  echo "$id" >"$workdir/backup-id"
}

restore_hold_count() {
  kubectl -n "$NS" get virtualmachines.bedrock.cloudyfolks.io,volumes.bedrock.cloudyfolks.io -o json |
    jq '[.items[] | select(.metadata.annotations["bedrock.cloudyfolks.io/restore-hold"] != null)] | length'
}

backup_restore_replace() {
  local id before after
  id=$(cat "$workdir/backup-id")
  before=$(kubectl -n "$NS" get datavolume vm1-root -o jsonpath='{.metadata.uid}')
  kubectl -n "$NS" apply -f - <<RESTORE
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachineRestore
metadata:
  name: again
spec:
  backupRef: once
  backupID: $id
  replace: true
RESTORE
  wait_jsonpath 1200 Completed -n "$NS" get virtualmachinerestore again -o jsonpath='{.status.phase}'
  wait_jsonpath 600 Running -n "$NS" get virtualmachine vm1 -o jsonpath='{.status.phase}'
  after=$(kubectl -n "$NS" get datavolume vm1-root -o jsonpath='{.metadata.uid}')
  test "$before" != "$after"
  expect_equal 0 "$(restore_hold_count)" "restore holds"
  expect_equal Completed "$(kubectl -n velero get restore -l bedrock.cloudyfolks.io/name=again -o jsonpath='{.items[0].status.phase}')" "velero restore phase"
  kubectl -n "$NS" apply -f - <<REFUSED
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: VirtualMachineRestore
metadata:
  name: missing
spec:
  backupRef: once
  backupID: not-a-backup
  replace: true
REFUSED
  wait_reason 120 virtualmachinerestore missing Ready BackupNotFound -n "$NS"
}
