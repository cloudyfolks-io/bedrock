import json
import os
import re
import shutil
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
HARNESS = ROOT / "hack/e2e-tenant.sh"
LIB = ROOT / "hack/e2e/tenant/database-backup-evidence.sh"
WAL = "0000000100000000000000AF"
UID = "12345678-1234-1234-1234-123456789abc"


class DatabaseBackupEvidenceTest(unittest.TestCase):
    def setUp(self):
        scratch = tempfile.TemporaryDirectory()
        self.addCleanup(scratch.cleanup)
        self.root = Path(scratch.name)
        self.evidence = self.root / "evidence"
        self.env = {key: value for key, value in os.environ.items()
                    if key != "BASH_ENV" and not key.startswith("E2E_")}
        self.env.update(E2E_DATABASE_BACKUP_EVIDENCE_DIR=str(self.evidence),
                        TZ="America/Los_Angeles")

    def run_harness(self, cases, adapter=None, **env):
        steps = self.root / "e2e/tenant"
        steps.mkdir(parents=True)
        harness = HARNESS.read_text()
        (self.root / HARNESS.name).write_text(harness)
        for step_file in re.search(r"^for step_file in (.*); do$", harness, re.M)[1].split():
            (steps / (step_file + ".sh")).touch()
        shutil.copyfile(LIB, steps / LIB.name)
        if adapter is not None:
            (steps / "database-backup-egress-run.py").write_text(adapter)
        noops = re.search(r"^steps=\(\n(.*?)^\)", harness, re.M | re.S)[1].split()
        (steps / "database.sh").write_text(
            "".join(f"{name}() {{ :; }}\n" for name in noops) + cases)
        (self.root / "e2e/lib.sh").write_text("""
kubectl() {
  case "$*" in
    "get cluster cluster "*) echo 203.0.113.4 ;;
    "get setting platform.host "*) echo example.test ;;
    "-n cert-manager get secret bedrock-ca "*) echo Y2E= ;;
  esac
}
pin_hosts() { :; }
install() { :; }
k0s() { :; }
ip() { :; }
dump_logs() { :; }
dump_nmstate() { :; }
issue_tokens() { :; }
have_evidence() {
  local files=("$E2E_DATABASE_BACKUP_EVIDENCE_DIR"/database-backup.*/evidence.json)
  if [[ -s ${files[0]} ]]; then echo yes; else echo no; fi
}
dump_api_group() {
  if [[ $1 == bedrock.cloudyfolks.io ]]; then echo "diagnostics:$(have_evidence)"; fi
}
e2e_cleanup() { echo "cleanup:$(have_evidence)"; }
""")
        password = self.root / "unused-password-file"
        password.touch()
        return subprocess.run(
            ["bash", HARNESS.name], cwd=self.root, capture_output=True, text=True,
            env=dict(self.env, ADMIN_PASSWORD_FILE=str(password), E2E_TEST="/bin/bash", **env))

    def read_evidence(self, result, case, exit_status, last_checkpoint):
        self.assertEqual(result.returncode, exit_status, result.stderr)
        paths = list(self.evidence.glob("database-backup.*/evidence.json"))
        self.assertEqual(len(paths), 1, result.stderr)
        path = paths[0]
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(path.parent.stat().st_mode), 0o700)
        evidence = json.loads(path.read_text())
        self.assertEqual(set(evidence), {"schema_version", "case", "exit_status",
                                         "failed_at", "last_checkpoint", "checkpoints"})
        self.assertEqual(evidence["schema_version"], 1)
        self.assertEqual(evidence["case"], case)
        self.assertEqual(evidence["exit_status"], exit_status)
        self.assertEqual(evidence["last_checkpoint"], last_checkpoint)
        self.assertRegex(evidence["failed_at"], r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$")
        return evidence

    def test_full_failure_writes_evidence_before_diagnostics_and_cleanup(self):
        result = self.run_harness(f"""
database_backup_restore() {{
  database_backup_checkpoint postgres-base-wal-archived '{{"wal":"{WAL}"}}'
  database_backup_checkpoint postgres-pitr-kept-wal-archived '{{"wal":"{WAL}"}}'
  database_backup_checkpoint postgres-pitr-after-wal-archived '{{"wal":"{WAL}"}}'
  return 37
}}
""")
        evidence = self.read_evidence(result, "database_backup_restore", 37,
                                      "postgres-pitr-after-wal-archived")
        self.assertLess(result.stdout.index("diagnostics:yes"), result.stdout.index("cleanup:yes"))
        self.assertEqual([entry["step"] for entry in evidence["checkpoints"]],
                         ["postgres-base-wal-archived", "postgres-pitr-kept-wal-archived",
                          "postgres-pitr-after-wal-archived"])
        for entry in evidence["checkpoints"]:
            self.assertEqual(entry["facts"], {"wal": WAL})
            self.assertRegex(entry["recorded_at"], r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$")

    def test_only_failure_keeps_job_evidence_without_cleanup(self):
        job = {"backup_uid": UID, "job_name": f"xb-db-{UID}-e2e-rgw", "job_uid": UID}
        invalid_job = dict(job, job_name="private-fixture-secret")
        result = self.run_harness(f"""
database_mysql_backup_and_restore() {{
  if database_backup_checkpoint mysql-backup-job-succeeded '{json.dumps(invalid_job)}'; then
    return 1
  fi
  database_backup_checkpoint mysql-backup-job-succeeded '{json.dumps(job)}'
  return 29
}}
""", E2E_ONLY="database_mysql_backup_and_restore")
        evidence = self.read_evidence(result, "database_mysql_backup_and_restore", 29,
                                      "mysql-backup-job-succeeded")
        self.assertEqual(evidence["checkpoints"][0]["facts"], job)
        self.assertIn("diagnostics:yes", result.stdout)
        self.assertNotIn("cleanup:", result.stdout)
        self.assertNotIn("private-fixture-secret", result.stdout + result.stderr)

    def test_diagnostics_and_cleanup_do_not_replace_backup_exit(self):
        result = self.run_harness(f"""
database_backup_restore() {{
  database_backup_checkpoint postgres-base-wal-archived '{{"wal":"{WAL}"}}'
  return 37
}}
dump_cluster() {{ echo "diagnostics:$(have_evidence)"; exit 71; }}
dump_workloads() {{ exit 72; }}
e2e_cleanup() {{ echo "cleanup:$(have_evidence)"; exit 73; }}
""")
        self.read_evidence(result, "database_backup_restore", 37, "postgres-base-wal-archived")
        self.assertLess(result.stdout.index("diagnostics:yes"), result.stdout.index("cleanup:yes"))

    def test_successful_suite_still_exposes_cleanup_failure(self):
        result = self.run_harness("e2e_cleanup() { return 73; }\n")
        self.assertEqual(result.returncode, 73, result.stderr)
        self.assertFalse(self.evidence.exists())

    def test_egress_opt_in_bypasses_generic_diagnostics_and_preserves_exit(self):
        result = self.run_harness("", E2E_ONLY="database_backup_egress", adapter="""
import sys
assert sys.argv[1:] == ["--run"]
print("scoped adapter")
sys.exit(37)
""")
        self.assertEqual(result.returncode, 37, result.stderr)
        self.assertEqual(result.stdout, "scoped adapter\n")
        self.assertNotIn("diagnostics:", result.stdout)
        self.assertNotIn("cleanup:", result.stdout)
        self.assertFalse(self.evidence.exists())

    def test_from_failure_does_not_reuse_previous_backup_case(self):
        result = self.run_harness(f"""
database_mysql_backup_and_restore() {{
  database_backup_checkpoint mysql-base-backup '{{"backup_phase":"Succeeded","backup_uid":"{UID}"}}'
}}
database_mysql_restore_from_deleted() {{ return 39; }}
""", E2E_FROM="database_mysql_backup_and_restore")
        evidence = self.read_evidence(result, "database_mysql_restore_from_deleted", 39, None)
        self.assertEqual(evidence["checkpoints"], [])
        self.assertNotIn("cleanup:", result.stdout)

    def test_unrelated_failure_does_not_capture_previous_backup_case(self):
        result = self.run_harness("""
database_backup_restore() { database_backup_checkpoint postgres-pitr-restore-verify; }
database_isolation() { return 41; }
""", E2E_FROM="database_backup_restore")
        self.assertEqual(result.returncode, 41, result.stderr)
        self.assertFalse(self.evidence.exists())
        self.assertIn("diagnostics:no", result.stdout)

    def test_success_does_not_write_evidence(self):
        result = self.run_harness("""
database_backup_restore() { database_backup_checkpoint postgres-pitr-restore-verify; }
""", E2E_ONLY="database_backup_restore")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.evidence.exists())
        self.assertNotIn("diagnostics:", result.stdout)
        self.assertNotIn("cleanup:", result.stdout)

    def test_write_failure_does_not_change_case_exit_or_skip_diagnostics(self):
        self.evidence.touch()
        result = self.run_harness("database_backup_restore() { return 43; }\n",
                                  E2E_ONLY="database_backup_restore")
        self.assertEqual(result.returncode, 43, result.stderr)
        self.assertIn("diagnostics:no", result.stdout)
        self.assertIn("Could not write database backup failure evidence", result.stderr)
        self.assertNotIn("cleanup:", result.stdout)

    def test_rejects_untrusted_fields_and_values_without_echoing_them(self):
        result = self.run_harness(f"""
reject() {{ ! database_backup_checkpoint "$@"; }}
database_backup_restore() {{
  reject private-fixture-secret
  reject mysql-backup-job-succeeded
  reject postgres-base-wal-archived '{{"wal":"{WAL}","password":"private-fixture-secret"}}'
  reject postgres-base-wal-archived '{{"wal":"private-fixture-secret"}}'
  reject postgres-base-wal-archived '{{"wal":"{WAL}\\\\n"}}'
  reject postgres-base-wal-archived '{{}}{{}}'
  reject postgres-base-wal-archived 'private-fixture-secret'
  reject postgres-pitr-restore-ready '{{"ready":{{"reason":"private-fixture-secret"}}}}'
  reject postgres-pitr-base-backup '{{"backup_phase":"private-fixture-secret"}}'
  database_backup_checkpoint postgres-pitr-restore-ready '{{"phase":"Ready","ready":"True"}}'
  return 47
}}
""", E2E_ONLY="database_backup_restore")
        evidence = self.read_evidence(result, "database_backup_restore", 47, "postgres-pitr-restore-ready")
        self.assertEqual(len(evidence["checkpoints"]), 1)
        self.assertNotIn("private-fixture-secret", result.stdout + result.stderr + json.dumps(evidence))
