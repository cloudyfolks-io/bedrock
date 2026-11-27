from contextlib import ExitStack, redirect_stderr, redirect_stdout
from copy import deepcopy
import io
import json
import os
from pathlib import Path
import runpy
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from uuid import UUID


HERE = Path(__file__).resolve().parent
DRIVER = HERE / "e2e/tenant/database-backup-egress-run.py"
RUN = runpy.run_path(str(DRIVER))
GLOBALS = RUN["Run"].run.__globals__
FAILED = RUN["Failed"]
API = RUN["API_GROUP"]
OWNER = RUN["OWNER"]
PLATFORM = RUN["PLATFORM"]
PROJECT = RUN["PROJECT"]
RGW = "rook-ceph-rgw-bedrock-rgw"
RGW_IP = "10.96.45.7"


def uid(number):
    return str(UUID(int=number))


def endpoint(namespace, service, name, address, port):
    return {"metadata": {"namespace": namespace, "labels": {"kubernetes.io/service-name": service}},
            "addressType": "IPv4", "ports": [{"name": name, "protocol": "TCP", "port": port}],
            "endpoints": [{"addresses": [address], "conditions": {"ready": True}}]}


class Cluster:
    resources = {"Database": "databases", "NetworkPolicy": "networkpolicies",
                 "Backup": "backups", "PerconaServerMySQLBackup": "perconaservermysqlbackups"}

    def __init__(self, mode):
        self.mode = mode
        self.objects = {}
        self.operations = []
        self.probes = []
        self.backend = "198.51.100.4"
        self.serial = 100
        self.pod = None
        self.job = self.job_pod = self.manual = None
        self.delayed = None
        self.recovery_reads = 0
        self.live_checks = []
        if mode == "existing":
            self.objects[(API, PROJECT, "databases", "tech379-mysql")] = {"metadata": {"uid": uid(1)}}

    def key(self, document):
        md = document["metadata"]
        return document["apiVersion"], md["namespace"], self.resources[document["kind"]], md["name"]

    def persist(self, obj):
        self.serial += 1
        obj = deepcopy(obj)
        md = obj["metadata"]
        md.setdefault("uid", uid(self.serial))
        md["resourceVersion"] = str(self.serial)
        self.objects[self.key(obj)] = obj
        return obj

    def roots(self):
        return [obj for obj in self.objects.values() if obj.get("kind") == "Database"]

    def get_pod(self, selector):
        root = self.roots()[0]
        instance = "db-" + root["metadata"]["uid"]
        labels = {"app.kubernetes.io/instance": instance, "bedrock.cloudyfolks.io/database-role": "engine"}
        self.pod = {
            "metadata": {"name": instance + "-0", "namespace": PLATFORM, "uid": uid(18),
                         "annotations": {"fabric.cloudyfolks.io/ip_address": "10.0.0.4"}, "labels": labels},
            "spec": {},
            "status": {"phase": "Running", "podIP": "10.0.0.4", "conditions": [
                {"type": "Ready", "status": "True"}], "containerStatuses": [
                {"name": name, "ready": True, "containerID": "container-id"}
                for name in ("postgres", "mysql")]},
        }
        assert set(selector.split(",")) == {f"{k}={v}" for k, v in labels.items()}
        return json.dumps({"items": [self.pod]})

    def start_job(self, backup):
        self.manual = backup
        backup["status"] = {"state": "Running"}
        instance = backup["spec"]["clusterName"]
        label = {"bedrock.cloudyfolks.io/backup-of": instance}
        name = f'xb-{backup["metadata"]["name"]}-rgw'
        self.job = {
            "apiVersion": "batch/v1", "kind": "Job", "status": {},
            "metadata": {"name": name, "namespace": PLATFORM, "uid": uid(12), "labels": label,
                         "ownerReferences": [self.owner(backup)]},
            "spec": {"template": {"metadata": {"labels": label}}},
        }
        self.job_pod = {
            "metadata": {"name": name + "-pod", "namespace": PLATFORM, "uid": uid(13), "labels": label,
                         "ownerReferences": [self.owner(self.job)],
                         "annotations": {"fabric.cloudyfolks.io/ip_address": "10.0.0.5"}},
            "spec": {},
            "status": {"phase": "Running", "podIP": "10.0.0.5", "containerStatuses": [
                {"name": "xtrabackup", "state": {"running": {}}, "containerID": "job-container"}]},
        }
        if self.mode == "job-too-fast":
            self.finish_job()

    def finish_job(self):
        self.manual["status"] = {"state": "Succeeded"}
        self.job["status"] = {"conditions": [{"type": "Complete", "status": "True"}]}
        self.job_pod["status"]["phase"] = "Succeeded"
        self.job_pod["status"]["containerStatuses"][0]["state"] = {"terminated": {"exitCode": 0}}
        if self.mode == "job-replaced":
            self.job_pod["metadata"]["uid"] = uid(14)

    @staticmethod
    def owner(obj):
        return {"apiVersion": obj["apiVersion"], "kind": obj["kind"], "controller": True,
                **{key: obj["metadata"][key] for key in ("name", "uid")}}

    def realized(self, policy, pod, *, backup_peer=None):
        if backup_peer is not None:
            assert policy["metadata"]["name"].endswith("-e2e-backup-egress")
            assert pod == self.job_pod["metadata"]["name"]
            self.live_checks.append((backup_peer, self.job_pod["status"]["phase"]))
            return self.mode != "job-wrong-fabric"
        return True

    def kube(self, *args, body=None):
        namespace = None
        if args[0] == "-n":
            namespace, args = args[1], args[2:]
        action, *rest = args
        if action == "get":
            if rest[0] == "endpointslices.discovery.k8s.io":
                items = [endpoint("default", "kubernetes", "https", "192.0.2.4", 6443)]
                if self.mode != "discovery":
                    items.append(endpoint("rook-ceph", RGW, "http", self.backend, 8080))
                return json.dumps({"items": items})
            if rest[0] == "service":
                assert (namespace, rest[1]) == ("rook-ceph", RGW)
                return json.dumps({"metadata": {"namespace": namespace, "name": RGW, "uid": uid(2)},
                                   "spec": {"clusterIP": RGW_IP, "ports": [
                                       {"name": "http", "protocol": "TCP", "port": 80}]}})
            if rest[0] == "pods":
                if rest[2].startswith("batch.kubernetes.io/controller-uid="):
                    assert rest[2] == f"batch.kubernetes.io/controller-uid={uid(12)}"
                    return json.dumps({"items": [] if self.mode == "job-missing-pod" else [self.job_pod]})
                return self.get_pod(rest[2])
            if rest[0] == "jobs.batch":
                assert self.job and rest[1] == self.job["metadata"]["name"]
                return json.dumps(self.job)
            if rest[0] == "pod":
                if self.job_pod and rest[1] == self.job_pod["metadata"]["name"]:
                    if self.mode == "job-ends-during-acl":
                        self.finish_job()
                    response = json.dumps(self.job_pod)
                    self.finish_job()
                    return response
                assert rest[1] == self.pod["metadata"]["name"]
                return json.dumps(self.pod)
            resource, group = rest[0].split(".", 1)
            if self.delayed:
                key, obj = self.delayed
                api, ns, kind, name = key
                if (api.split("/")[0], ns, kind, name) == (group, namespace, resource, rest[1]):
                    self.recovery_reads += 1
                    if self.recovery_reads < 3 or self.mode == "no-appearance":
                        return ""
                    self.objects[key] = obj
                    self.delayed = None
            matches = [obj for (api, ns, kind, name), obj in self.objects.items()
                       if api.split("/")[0] == group and ns == namespace and kind == resource and name == rest[1]]
            return json.dumps(matches[0]) if matches else ""
        if action == "exec":
            assert rest[-2:] == [RGW_IP, "80"]
            assert rest[0] == self.pod["metadata"]["name"]
            engine_policies = [obj for obj in self.objects.values() if obj.get("kind") == "NetworkPolicy"
                               and obj["metadata"]["name"].endswith("-e2e-egress")]
            connected = not engine_policies or self.backend + "/32" in json.dumps(engine_policies[0]["spec"])
            if self.mode == "bad-exec" and not connected:
                raise FAILED()
            result = "connected" if connected else "timed-out"
            self.probes.append(result)
            return result
        if action in ("create", "replace"):
            kind = body["kind"]
            dry_run = "--dry-run=server" in rest
            self.operations.append(("dry-run" if dry_run else action, self.key(body)))
            if dry_run and action == "create":
                if self.mode == "invalid-dry-run":
                    changed = deepcopy(body)
                    changed["spec"]["egress"] = [{}]
                    return json.dumps(changed)
                return json.dumps(body)
            existing = self.objects.get(self.key(body))
            if action == "replace":
                assert body["metadata"]["resourceVersion"] == existing["metadata"]["resourceVersion"]
                body = deepcopy(body)
                body["metadata"]["uid"] = existing["metadata"]["uid"]
            else:
                assert existing is None
            if dry_run:
                return json.dumps(body)
            obj = self.persist(body)
            if kind == "Database":
                obj["status"] = {"phase": "Ready", "applied": {"instances": 1}, "instances": 1}
            if kind == "NetworkPolicy" and self.mode in (
                    "lost-create", "foreign-policy", "no-appearance", "sigint", "sigterm"):
                key = self.key(obj)
                if self.mode == "foreign-policy":
                    obj["metadata"]["annotations"][OWNER] = uid(9)
                    obj["metadata"]["uid"] = uid(10)
                self.delayed = key, self.objects.pop(key)
                if self.mode.startswith("sig"):
                    signal.raise_signal(signal.SIGTERM if self.mode == "sigterm" else signal.SIGINT)
                raise FAILED()
            if action == "replace" and self.mode == "drift":
                self.backend = "198.51.100.8"
            if kind == "Backup":
                obj["status"] = {"phase": "completed"}
                self.roots()[0]["status"]["backup"] = {
                    "lastAt": "2026-11-27T00:00:00Z",
                    "items": [{"id": obj["metadata"]["name"], "phase": "completed"}],
                }
            if kind == "PerconaServerMySQLBackup":
                self.start_job(obj)
            return json.dumps(obj)
        if action == "delete":
            assert rest[0] == "--raw"
            _, group, version, _, ns, resource, name = rest[1].strip("/").split("/")
            key = (f"{group}/{version}", ns, resource, name)
            self.operations.append(("delete", key))
            obj = self.objects[key]
            assert body["preconditions"] == {k: obj["metadata"][k] for k in ("uid", "resourceVersion")}
            if self.mode in ("cleanup", "sigterm") and resource == "networkpolicies":
                raise FAILED()
            del self.objects[key]
            return "{}"
        raise AssertionError(args)


def proof(script, *args, **kwargs):
    if "database_postgres_seed_backup" in script:
        return "0000000100000000000000AA"
    if "database_mysql_backup_job" in script:
        return json.dumps({"backup_uid": args[2], "job_uid": uid(12), "job_name": f"xb-{args[1]}-rgw"})
    if "database_mysql_exec" in script:
        return ""
    raise AssertionError(script)


def bounded_fixture(seconds, check, **kwargs):
    for _ in range(4):
        value = check()
        if value:
            return value
    raise FAILED()


class BackupEgressRunTest(unittest.TestCase):
    def run_case(self, mode):
        cluster = Cluster(mode)
        stderr = io.StringIO()
        receipts = []

        def record(milestones, status):
            receipts.append((deepcopy(milestones), status, cluster.operations[:]))
            if mode == "sigterm":
                signal.raise_signal(signal.SIGTERM)

        with ExitStack() as stack:
            stack.enter_context(patch.dict(GLOBALS, {
                "kube": cluster.kube, "prerequisite": lambda: None,
                "realized": cluster.realized, "proof": proof, "poll": bounded_fixture,
                "nb": lambda *_: [],
                "write_receipt": record,
            }))
            stack.enter_context(redirect_stderr(stderr))
            stack.enter_context(redirect_stdout(io.StringIO()))
            run = RUN["Run"]()
            stack.enter_context(patch.dict(GLOBALS, {"Run": lambda: run}))
            for signum in (signal.SIGTERM, signal.SIGINT):
                stack.callback(signal.signal, signum, signal.getsignal(signum))
            result = RUN["main"](["--run"])
        if result:
            self.assertEqual(len(receipts), 1)
            progress, recorded_status, cluster.at_receipt = receipts[0]
            self.assertEqual(recorded_status, result)
        else:
            self.assertEqual(receipts, [])
            progress = run.milestones
        cluster.stderr = stderr.getvalue()
        self.assertEqual(len({entry.pop("run_uid") for entry in progress}), 1)
        return cluster, result, progress

    def test_single_engine_probe_order_and_owned_deletes(self):
        cluster, result, progress = self.run_case("success")
        self.assertEqual(result, 0, progress)
        self.assertEqual(cluster.objects, {})
        self.assertEqual(cluster.probes, ["connected", "timed-out", "timed-out", "connected", "connected"] * 2)
        self.assertEqual(cluster.live_checks, [("10.0.0.4", "Running")])
        self.assertEqual([entry["engine"] for entry in progress if entry["stage"] == "complete"],
                         ["postgresql", "mysql"])
        created = [key for command, key in cluster.operations if command == "create"]
        self.assertEqual([key[2] for key in created], [
            "databases", "networkpolicies", "backups",
            "databases", "networkpolicies", "networkpolicies", "perconaservermysqlbackups",
        ])
        self.assertCountEqual(created, [key for command, key in cluster.operations if command == "delete"])

    def test_discovery_and_existing_root_fail_before_any_mutation(self):
        for mode, stage in (("discovery", "discovery"), ("existing", "preflight")):
            with self.subTest(mode=mode):
                cluster, result, progress = self.run_case(mode)
                self.assertEqual(result, 1)
                self.assertEqual(cluster.operations, [])
                self.assertEqual(progress[-1], {"engine": "both", "stage": stage, "outcome": "failed"})

    def test_dry_run_mismatch_fails_before_policy_create(self):
        cluster, result, progress = self.run_case("invalid-dry-run")
        self.assertEqual(result, 1)
        self.assertEqual(progress[-1]["stage"], "dry-run")
        self.assertFalse(any(command == "create" and key[2] == "networkpolicies"
                             for command, key in cluster.operations))
        self.assertEqual(cluster.objects, {})

    def test_create_error_after_write_still_removes_the_exact_owned_policy(self):
        cluster, result, progress = self.run_case("lost-create")
        self.assertEqual(result, 1)
        self.assertEqual(progress[-1], {"engine": "postgresql", "stage": "apply-deny", "outcome": "failed"})
        self.assertEqual(cluster.objects, {})
        self.assertEqual(cluster.recovery_reads, 3)
        self.assertEqual([key[2] for command, key in cluster.operations if command == "delete"],
                         ["networkpolicies", "databases"])
        self.assertFalse(any(command == "delete" for command, _ in cluster.at_receipt))

    def test_foreign_policy_is_not_deleted_and_cleanup_failure_is_reported(self):
        for mode in ("foreign-policy", "cleanup"):
            with self.subTest(mode=mode):
                cluster, result, progress = self.run_case(mode)
                self.assertEqual(result, 1)
                self.assertEqual([key[2] for key in cluster.objects], ["networkpolicies"])
                self.assertIn("cleanup incomplete (run ", cluster.stderr)
                self.assertEqual(progress[-1]["stage"], "apply-deny" if mode == "foreign-policy" else "cleanup")
                if mode == "foreign-policy":
                    self.assertEqual(cluster.recovery_reads, 3)
                    self.assertFalse(any(command == "delete" and key[2] == "networkpolicies"
                                         for command, key in cluster.operations))

    def test_unconfirmed_create_stops_recovery_after_bounded_retries(self):
        cluster, result, progress = self.run_case("no-appearance")
        self.assertEqual(result, 1)
        self.assertEqual(cluster.recovery_reads, 4)
        self.assertEqual(progress[-1]["stage"], "apply-deny")
        self.assertIn("cleanup incomplete", cluster.stderr)

    def test_signals_keep_original_status_through_receipt_and_cleanup(self):
        for mode, status in (("sigint", 130), ("sigterm", 143)):
            with self.subTest(mode=mode):
                cluster, result, progress = self.run_case(mode)
                self.assertEqual(result, status)
                self.assertEqual(progress[-1]["stage"], "apply-deny")
                self.assertEqual(cluster.recovery_reads, 3)
                self.assertFalse(any(command == "delete" for command, _ in cluster.at_receipt))
                self.assertEqual([key[2] for command, key in cluster.operations if command == "delete"],
                                 ["networkpolicies", "databases"])
                self.assertEqual("cleanup incomplete" in cluster.stderr, mode == "sigterm")

    def test_direct_driver_requires_exact_opt_in_before_kubectl(self):
        with tempfile.TemporaryDirectory() as directory:
            sentinel = Path(directory, "called")
            stub = Path(directory, "kubectl")
            stub.write_text('#!/bin/sh\n: >"$SENTINEL"\nexit 99\n')
            stub.chmod(0o755)
            env = dict(os.environ, SENTINEL=str(sentinel), KUBECONFIG=str(Path(directory, "unused")),
                       PATH=directory + os.pathsep + os.environ["PATH"])
            for options in ([], ["--help"], ["--unknown"], ["--run", "--unknown"], ["--run", "--run"]):
                with self.subTest(options=options):
                    result = subprocess.run([sys.executable, str(DRIVER), *options], env=env,
                                            capture_output=True, text=True, timeout=5)
                    self.assertEqual(result.returncode, 2, result.stderr)
                    self.assertIn("Usage:", result.stderr)
                    self.assertFalse(sentinel.exists())

    def test_mysql_cannot_count_backup_without_live_and_matching_pod_evidence(self):
        for mode in ("job-missing-pod", "job-too-fast", "job-wrong-fabric",
                     "job-ends-during-acl", "job-replaced"):
            with self.subTest(mode=mode):
                cluster, result, progress = self.run_case(mode)
                self.assertEqual(result, 1)
                self.assertEqual(cluster.objects, {})
                self.assertEqual(progress[-1]["engine"], "mysql")
                self.assertEqual(progress[-1]["stage"], "mysql-job" if mode == "job-replaced" else "mysql-job-live")
                self.assertNotIn("mysql", [entry["engine"] for entry in progress if entry["stage"] == "complete"])

    def test_failure_receipt_is_private_and_only_accepts_fixed_milestones(self):
        entry = {"engine": "postgresql", "stage": "apply-deny", "outcome": "failed", "run_uid": uid(3)}
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {
            "E2E_DATABASE_BACKUP_EVIDENCE_DIR": directory,
        }), redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as stderr:
            RUN["write_receipt"]([entry], 37)
            RUN["write_receipt"]([dict(entry, stage="private-fixture-secret")], 37)
            (path,) = Path(directory).glob("database-backup.*/evidence.json")
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)
            document = json.loads(path.read_text())
            self.assertEqual(document["exit_status"], 37)
            self.assertEqual(document["checkpoints"][0]["facts"], entry)
            self.assertNotIn("private-fixture-secret", stderr.getvalue())

    def test_drift_or_exec_failure_prevents_backup_and_removes_policy(self):
        for mode, stage in (("drift", "allowed"), ("bad-exec", "denied")):
            with self.subTest(mode=mode):
                cluster, result, progress = self.run_case(mode)
                self.assertEqual(result, 1)
                self.assertEqual(progress[-1]["stage"], stage)
                self.assertFalse(any(key[2] in ("backups", "perconaservermysqlbackups")
                                     for _, key in cluster.operations))
                self.assertEqual(cluster.objects, {})

    def test_fabric_check_pins_port_group_member_and_post_lb_tuples(self):
        name, pod = "db-test-egress", "db-test-0"
        group = RUN["port_group_name"](name)
        match = (f"inport == @{group} && ip && (ip4.dst == 192.0.2.4/32 || "
                 "ip4.dst == 192.0.2.7/32) && tcp.dst == 6443")
        policy = {"metadata": {"name": name}, "spec": {"egress": [
            {"ports": [{"port": 6443}], "to": [{"ipBlock": {"cidr": f"192.0.2.{last}/32"}} for last in (4, 7)]},
        ]}}
        port_group = {"name": group, "ports": uid(1), "acls": [uid(2), uid(3)],
                      "external_ids": {"np": f"{PLATFORM}/{name}"}}
        drop = {"direction": "from-lport", "action": "drop", "options": {"apply-after-lb": "true"},
                "match": f"inport == @{group} && ip"}
        allow = {**drop, "action": "allow-related", "match": match}
        for label, acl, ports, expected in (
            ("exact", allow, uid(1), True),
            ("pre-LB", {**allow, "options": {}}, uid(1), False),
            ("missing endpoint", {**allow, "match": match.replace("192.0.2.7", "192.0.2.8")}, uid(1), False),
            ("extra pod", allow, [uid(1), uid(8)], False),
            ("wrong port", {**allow, "match": match.replace("6443", "443")}, uid(1), False),
        ):
            with self.subTest(label=label):
                output = iter(([{**port_group, "ports": ports}], [{"_uuid": uid(1)}], [drop, acl]))
                with patch.dict(GLOBALS, {"nb": lambda *_: next(output)}):
                    self.assertEqual(RUN["realized"](policy, pod), expected)

    def test_mysql_fabric_requires_exact_post_dnat_tcp_6450_and_engine_address_set(self):
        name, pod, engine_ip = "db-test-e2e-backup-egress", "backup-pod", "192.0.2.5"
        policy = {"metadata": {"name": name}}
        group = RUN["port_group_name"](name)
        allow, except_ = f"{group}.egress.allow.IPv4.1", f"{group}.egress.except.IPv4.1"
        drop = {"direction": "from-lport", "action": "drop", "options": {"apply-after-lb": "true"},
                "match": f"inport == @{group} && ip"}
        match = f"{drop['match']} && ip4.dst == ${allow} && ip4.dst != ${except_} && tcp.dst == 6450"
        permit = {**drop, "action": "allow-related", "match": match}
        pg = {"name": group, "ports": uid(1), "acls": [uid(2), uid(3)], "external_ids": {
            "np": f"{PLATFORM}/{name}"}}
        address_set = {"addresses": engine_ip, "external_ids": {"np": f"{PLATFORM}/{name}/egress"}}
        for label, acl, members, rows, exceptions, expected in (
            ("exact", permit, uid(1), [address_set], [], True),
            ("foreign port group member", permit, uid(9), [address_set], [], False),
            ("missing ACL", None, uid(1), [address_set], [], False),
            ("wrong TCP port", {**permit, "match": match.replace("6450", "3306")},
             uid(1), [address_set], [], False),
            ("too broad", {**permit, "match": match + " || ip"}, uid(1), [address_set], [], False),
            ("pre-DNAT", {**permit, "options": {}}, uid(1), [address_set], [], False),
            ("missing address set", permit, uid(1), [], [], False),
            ("wrong IP", permit, uid(1), [{**address_set, "addresses": "192.0.2.6"}], [], False),
            ("not exactly one IP", permit, uid(1),
             [{**address_set, "addresses": [engine_ip, "192.0.2.6"]}], [], False),
            ("destination excluded", permit, uid(1), [address_set], [engine_ip], False),
        ):
            with self.subTest(label=label):
                output = iter(([{**pg, "ports": members}], [{"_uuid": uid(1)}],
                               [drop, acl] if acl else [drop], rows,
                               [{**address_set, "addresses": exceptions}]))
                with patch.dict(GLOBALS, {"nb": lambda *_: next(output)}):
                    self.assertEqual(RUN["realized"](policy, pod, backup_peer=engine_ip), expected)


if __name__ == "__main__":
    unittest.main()
