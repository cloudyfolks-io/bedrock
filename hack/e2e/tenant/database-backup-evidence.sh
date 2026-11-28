database_backup_case=
database_backup_checkpoints=

database_backup_begin() {
  database_backup_case=
  database_backup_checkpoints=
  case "$1" in
    database_backup_restore|database_mysql_backup_and_restore|database_mysql_restore_from_deleted|database_backup_egress)
      database_backup_case=$1
      ;;
  esac
}

# Call in the case shell, not in a command substitution or pipeline.
# Supply only the named observations below, never raw status, events, logs, or credentials.
database_backup_checkpoint() {
  local step=${1:-} facts=${2-'{}'} schema entry
  case "$database_backup_case:$step" in
    database_backup_restore:postgres-base-wal-archived|\
    database_backup_restore:postgres-pitr-kept-wal-archived|\
    database_backup_restore:postgres-pitr-after-wal-archived)
      schema=wal ;;
    database_backup_restore:postgres-pitr-base-backup|\
    database_backup_restore:postgres-pitr-backup-size|\
    database_mysql_backup_and_restore:mysql-base-backup)
      schema=backup ;;
    database_mysql_backup_and_restore:mysql-backup-job-succeeded)
      schema=job ;;
    database_backup_egress:egress)
      schema=egress ;;
    database_backup_restore:postgres-pitr-restore-ready|\
    database_mysql_backup_and_restore:mysql-backup-ready|\
    database_mysql_backup_and_restore:mysql-restore-ready|\
    database_mysql_restore_from_deleted:mysql-deleted-source-restore-ready)
      schema=readiness ;;
    database_backup_restore:postgres-pitr-restore-verify|\
    database_mysql_backup_and_restore:mysql-restore-verify|\
    database_mysql_restore_from_deleted:mysql-source-deletion|\
    database_mysql_restore_from_deleted:mysql-deleted-source-restore-verify)
      schema=marker ;;
    *)
      printf '%s\n' 'Unknown database backup checkpoint' >&2
      return 1 ;;
  esac

  if [[ $# -gt 2 || ${#facts} -gt 2048 ]] || ! entry=$(jq -cse --arg step "$step" --arg schema "$schema" '
    def count: type == "number" and . >= 0 and . <= 9007199254740991 and floor == .;
    def status: . == "True" or . == "False" or . == "Unknown";
    def uid_pattern: "[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}";
    def uid: type == "string" and test("\\A" + uid_pattern + "\\z");
    def wal: type == "string" and test("\\A[0-9A-F]{24}\\z");
    def utc: type == "string" and test("\\A[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]{1,9})?Z\\z");
     def oneof($values): . as $value | any($values[]; . == $value);
     select(length == 1) | .[0] as $facts |
     select($schema != "egress" or ($facts | type == "object" and keys == ["engine","outcome","run_uid","stage"])) |
    select($facts | type == "object" and all(to_entries[];
      .key as $key | .value |
      if $schema == "wal" then
        if $key | oneof(["wal","last_archived_wal","last_failed_wal"]) then wal
        elif $key | oneof(["archived_count","failed_count"]) then count else false end
      elif $schema == "readiness" then
        if $key == "phase" then oneof(["Provisioning","Ready","Unhealthy","Suspended","Restoring","Upgrading","Deleting","Failed"])
        elif $key | oneof(["ready","engine_ready","gate_ready","endpoint_ready","backup_healthy"]) then status else false end
      elif $schema == "backup" then
        if $key == "backup_phase" then oneof(["pending","started","running","completed","failed","walArchivingFailing","Starting","Running","Succeeded","Failed","Error"])
        elif $key | oneof(["last_at","size_sampled_at","target_time"]) then utc
        elif $key == "size_bytes" then count
        elif $key == "backup_uid" then uid else false end
       elif $schema == "job" then
         if $key | oneof(["backup_uid","job_uid"]) then uid
         elif $key == "job_name" then type == "string" and test("\\Axb-db-" + uid_pattern + "-e2e-rgw\\z")
         else false end
       elif $schema == "egress" then
         if $key == "engine" then oneof(["both","postgresql","mysql"])
         elif $key == "stage" then oneof(["preflight","discovery","root","baseline","dry-run","apply-deny","denied",
            "apply-allow","allowed","postgres-wal","mysql-canary","backup","mysql-job-live","mysql-job","verify","cleanup","complete"])
         elif $key == "outcome" then oneof(["started","passed","failed"])
         elif $key == "run_uid" then uid
         else false end
       else false end
    )) | {step:$step, recorded_at:(now | strftime("%Y-%m-%dT%H:%M:%SZ")), facts:$facts}
  ' 2>/dev/null <<<"$facts"); then
    printf '%s\n' 'Invalid database backup checkpoint facts' >&2
    return 1
  fi
  database_backup_checkpoints+="$entry"$'\n'
}

database_backup_failure() (
  local status=$1 dir=${E2E_DATABASE_BACKUP_EVIDENCE_DIR:-dist/e2e-tenant-evidence} run file receipt
  [[ -n $database_backup_case ]] || return 0
  receipt=$(jq -ces --arg case "$database_backup_case" --argjson status "$status" '
    {schema_version:1, case:$case, exit_status:$status, failed_at:(now | strftime("%Y-%m-%dT%H:%M:%SZ")),
     last_checkpoint:(last | .step), checkpoints:.}
  ' <<<"$database_backup_checkpoints") || return 1

  umask 077
  mkdir -p -- "$dir" || return 1
  run=$(mktemp -d -- "$dir/database-backup.XXXXXX") || return 1
  file=$run/evidence.json
  if ! (set -o noclobber; printf '%s\n' "$receipt" >"$file" && chmod -- 600 "$file"); then
    rm -f -- "$file"
    return 1
  fi
  printf 'Database backup failure evidence: %s\n' "$file"
)
