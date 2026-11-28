import copy
import json
import os
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
WAL = "00000001000000000000000A"


def run_fixture(script, **env):
    return subprocess.run(
        ["bash", "-euo", "pipefail", "-c",
         "source hack/e2e/lib.sh\nsource hack/e2e/tenant/database.sh\n" + script],
        cwd=ROOT, env={**os.environ, "WAL": WAL, **env},
        capture_output=True, text=True, timeout=10,
    )


class DatabaseBackupProofsTest(unittest.TestCase):
    def test_switch_accepts_only_one_exact_uppercase_wal(self):
        script = r"""
database_sql() {
  [[ "$*" == 'orders -c select pg_walfile_name(pg_switch_wal() - 1::numeric)' ]] || return 1
  printf '%s\n' "$SWITCH_RESULT"
}
retry_until() {
  echo "archive probe: $*" >&2
  [[ "$*" == "300 database_postgres_wal_archived orders $WAL" ]]
}
database_postgres_switch_and_wait orders
"""
        result = run_fixture(script, SWITCH_RESULT=WAL)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, WAL + "\n")
        for invalid in ("", WAL.lower(), WAL[:-1], WAL + "B", WAL[:-1] + "G",
                        " " + WAL, WAL + ".history", WAL + "\n" + WAL):
            with self.subTest(switch_result=invalid):
                result = run_fixture(script, SWITCH_RESULT=invalid)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("archive probe:", result.stderr)

    def test_switch_record_end_uses_previous_segment_only_at_boundary(self):
        # PostgreSQL 17 pg_switch_wal returns the end+1 LSN; pg_walfile_name
        # divides that byte address by the segment size (XLByteToSeg).
        segment_size = 16 * 1024 * 1024
        boundary = 10 * segment_size
        self.assertEqual(boundary // segment_size, 10)
        self.assertEqual((boundary - 1) // segment_size, 9)
        self.assertEqual((boundary + 24 - 1) // segment_size, 10)

    def test_archive_probe_requires_exact_status_or_done_marker(self):
        script = r"""
database_sql() {
  [[ "$3" == "select last_archived_wal = '$WAL'
    or (pg_stat_file('pg_wal/archive_status/$WAL.done', true)).modification is not null
    from pg_stat_archiver" ]] || return 2
  if [[ "$LAST_ARCHIVED" == "$WAL" || "$MARKER" == "$WAL.done" ]]; then
    echo t
  else
    echo f
  fi
}
database_postgres_wal_archived orders "$WAL"
"""
        other_wal = WAL[:-1] + "B"
        for last, marker, accepted in [
            (WAL, "", True), (other_wal, WAL + ".done", True),
            (other_wal, other_wal + ".done", False),
            (WAL + ".partial", WAL + ".ready", False),
            ("", "", False),
        ]:
            with self.subTest(last=last, marker=marker):
                result = run_fixture(script, LAST_ARCHIVED=last, MARKER=marker)
                self.assertEqual(result.returncode == 0, accepted, result.stderr)

    def test_pitr_orders_commits_and_archives_around_target(self):
        script = r"""
NS=acme-web
database_uid() { echo a-fixture-uid; }
database_secret_field() { if [[ "$2" == username ]]; then echo dbadmin; else echo "$1"; fi; }
database_backup_checkpoint() { printf '%s %s\n' "$1" "$2" >&2; }
kubectl() {
  if [[ "$1" == apply ]]; then
    [[ "$(cat)" == *"name: e2e-first"* ]] || return 1
    echo base-backup >&2
  else
    cat >/dev/null
  fi
}
wait_jsonpath() { :; }
wait_not_empty() { :; }
sleep() { :; }
retry_until() { "$2" "${@:3}"; }
database_sql() {
  case "$3" in
    "create table if not exists proof (v text); insert into proof values ('before')") echo commit-before >&2 ;;
    "insert into proof values ('kept')") echo commit-kept >&2 ;;
    "insert into proof values ('after')") echo commit-after >&2 ;;
    "select pg_walfile_name(pg_switch_wal() - 1::numeric)") echo switch >&2; echo "$WAL" ;;
    "select last_archived_wal"*) echo "$ARCHIVE_RESULT" ;;
    "select to_char(now()"*) echo target >&2; echo 2026-11-27T00:00:00Z ;;
    "select v from proof order by v") printf 'before\nkept\n' ;;
    "select v from proof where v = 'kept'") echo kept ;;
    "select v from proof where v = 'after'") ;;
    *) return 1 ;;
  esac
}
database_backup_restore
"""
        result = run_fixture(script, ARCHIVE_RESULT="t")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr.splitlines(), [
            "commit-before", "switch", f'postgres-base-wal-archived {{"wal":"{WAL}"}}',
            "base-backup",
            "commit-kept", "switch", f'postgres-pitr-kept-wal-archived {{"wal":"{WAL}"}}',
            "target",
            "commit-after", "switch", f'postgres-pitr-after-wal-archived {{"wal":"{WAL}"}}',
        ])
        result = run_fixture(script, ARCHIVE_RESULT="f")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stderr.splitlines(), [
            "commit-before", "switch", f"WAL {WAL} did not archive for orders",
        ])

    def test_mysql_restore_requires_exact_manual_backup_job(self):
        script = r"""
database_uid() { echo source-uid; }
database_mysql_query() {
  case "$(cat)" in
    CREATE*) ;;
    "SELECT v FROM app.proof;") echo 42 ;;
    *) return 1 ;;
  esac
}
kubectl() {
  case "$3:$4" in
    apply:-f)
      [[ "$(cat)" == *"name: db-source-uid-e2e"* ]] || return 1
      echo "$BACKUP_UID"
      ;;
    get:jobs) echo "$JOBS" ;;
    *) return 1 ;;
  esac
}
wait_jsonpath() { [[ "$2:$7" == "Succeeded:db-source-uid-e2e" ]]; }
database_backup_checkpoint() { printf '%s %s\n' "$1" "$2"; }
database_apply() { echo restore; }
database_wait_ready() { :; }
state_write() { :; }
database_mysql_backup_and_restore
"""
        label = "bedrock.cloudyfolks.io/backup-of"
        job = {
            "metadata": {
                "name": "generated-job", "uid": "job-uid", "labels": {label: "db-source-uid"},
                "ownerReferences": [{
                    "uid": "manual-uid", "name": "db-source-uid-e2e", "controller": True,
                    "kind": "PerconaServerMySQLBackup", "apiVersion": "ps.percona.com/v1",
                }],
            },
            "spec": {"template": {"metadata": {"labels": {label: "db-source-uid"}}}},
            "status": {"succeeded": 1, "conditions": [{"type": "Complete", "status": "True"}]},
        }
        unrelated = copy.deepcopy(job)
        unrelated["metadata"]["ownerReferences"][0]["uid"] = "scheduled-uid"
        result = run_fixture(script, JOBS=json.dumps({"items": [unrelated, job]}), BACKUP_UID="manual-uid")
        self.assertEqual(result.returncode, 0, result.stderr)
        checkpoint, restore = result.stdout.splitlines()
        step, evidence = checkpoint.split(" ", 1)
        self.assertEqual(step, "mysql-backup-job-succeeded")
        self.assertEqual(json.loads(evidence), {
            "backup_uid": "manual-uid", "job_name": "generated-job", "job_uid": "job-uid",
        })
        self.assertEqual(restore, "restore")

        not_complete = copy.deepcopy(job)
        not_complete["status"]["conditions"][0]["status"] = "False"
        wrong_label = copy.deepcopy(job)
        wrong_label["metadata"]["labels"][label] = "source-uid"
        wrong_pod_label = copy.deepcopy(job)
        wrong_pod_label["spec"]["template"]["metadata"]["labels"][label] = "db-other-uid"
        wrong_success_count = copy.deepcopy(job)
        wrong_success_count["status"]["succeeded"] = 2
        duplicate_owner = copy.deepcopy(job)
        owners = duplicate_owner["metadata"]["ownerReferences"]
        owners.append(owners[0])
        for title, items, uid in [
            ("no job", [], "manual-uid"),
            ("same name, stale UID", [unrelated], "manual-uid"),
            ("wrong Job backup-of label", [wrong_label], "manual-uid"),
            ("wrong Pod template backup-of label", [wrong_pod_label], "manual-uid"),
            ("non-complete job", [not_complete], "manual-uid"),
            ("non-single success count", [wrong_success_count], "manual-uid"),
            ("duplicate controller owner", [duplicate_owner], "manual-uid"),
            ("multiple owned jobs", [job, wrong_label], "manual-uid"),
            ("missing backup UID", [job], ""),
        ]:
            with self.subTest(case=title):
                result = run_fixture(script, JOBS=json.dumps({"items": items}), BACKUP_UID=uid)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")
