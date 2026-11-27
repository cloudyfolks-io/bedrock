"""Offline checks for the isolated managed-etcd fixtures. No kubectl or keys."""

import ast
import contextlib
import copy
import hashlib
import io
import ipaddress
import json
import os
from pathlib import Path
import re
import shlex
import signal
import stat
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch


GUEST = "00000000-0000-0000-0000-000000000001"
MEMBER = "00000000-0000-0000-0000-000000000002"
POLICY = "00000000-0000-0000-0000-000000000003"
ADDRESS = "10.16.0.90"
CONTAINER = "containerd://" + "f" * 64
IMAGE = "quay.io/k0sproject/etcd@sha256:" + "a" * 64


def script(name):
    return Path(__file__).with_name(f"e2e-managed-etcd-{name}.sh")


def load(name):
    path = script(name)
    body = path.read_text().split("<<'PY'\n", 1)[1].rsplit("\nPY\n", 1)[0]
    tree = ast.parse(body, filename=str(path))
    if not isinstance(tree.body[-1], ast.Try):
        raise AssertionError("Expected guarded fixture entry point")
    tree.body.pop()
    module = {}
    exec(compile(tree, str(path), "exec"), module)
    return module


class MemberRecoveryTest(unittest.TestCase):
    def setUp(self):
        self.fixture = load("restart")

    def pod(self, entry):
        builder = {"loopback": "recovery_pod", "reachable": "reachable_pod"}[entry]
        return self.fixture[builder](IMAGE, "etcd-source-execution", "b" * 64, "c" * 64)

    def test_image_identity_and_admission(self):
        for entry in ("loopback", "reachable"):
            with self.subTest(entry=entry):
                pod = self.pod(entry)
                spec = pod["spec"]
                self.assertEqual(spec["securityContext"], {
                    "runAsUser": 1001, "runAsGroup": 1001, "fsGroup": 1001,
                    "fsGroupChangePolicy": "OnRootMismatch", "seccompProfile": {"type": "RuntimeDefault"},
                })
                self.assertEqual(spec["volumes"][1]["projected"]["defaultMode"], 0o400)
                container, = spec["containers"]
                self.assertEqual("env" in container, entry == "reachable")
                starter = container["args"][0]
                self.assertIn("[[ $EUID == 1001 && $(id -g) == 1001 ]]", starter)
                self.assertIn("-r $entry && -x $entry", starter)
                subprocess.run(["bash", "-n"], input=starter, text=True, check=True)
                validate = self.fixture["require_contract"]
                validate(pod, pod)
                for field in ("runAsUser", "runAsGroup", "fsGroup"):
                    changed = copy.deepcopy(pod)
                    changed["spec"]["securityContext"][field] = 0
                    with self.assertRaises(ValueError):
                        validate(changed, pod)

    def test_source_policy_excludes_both_recoveries(self):
        source = script("source").read_text().split("metadata: {name: etcd-source-ingress}", 1)[1].split("\n---\n", 1)[0]
        key, op, values = re.search(r"- key: (\S+)\n +operator: (\S+)\n +values: \[([^\]]+)\]", source).groups()
        source_selector = {"matchExpressions": [{"key": key, "operator": op, "values": values.split(", ")}]}
        self.assertTrue(self.fixture["selects"](source_selector, {}))
        for entry in ("loopback", "reachable"):
            with self.subTest(entry=entry):
                info = self.pod(entry)["metadata"]
                policy = self.fixture["recovery_policy"](info["name"], info["labels"])
                policy["metadata"]["uid"] = POLICY
                expected_ingress = [] if entry == "loopback" else [
                    {"from": [{"podSelector": {"matchLabels": self.fixture["controller_labels"]}}],
                     "ports": [{"port": 2379, "protocol": "TCP"}]},
                ]
                self.assertEqual(policy["spec"]["ingress"], expected_ingress)
                self.assertEqual(policy["spec"]["egress"], [])
                self.assertEqual(policy["spec"]["policyTypes"], ["Ingress", "Egress"])
                records = {
                    "PodList": [], "ServiceList": [],
                    "NetworkPolicyList": [policy, {"spec": {"podSelector": source_selector}}],
                }
                self.fixture["inventory"] = lambda _, kind: records[kind]
                verify = self.fixture["verify_recovery_policy"]
                verify(POLICY, policy, info["labels"])
                records["NetworkPolicyList"].append({"spec": {"podSelector": {}}})
                with self.assertRaises(ValueError):
                    verify(POLICY, policy, info["labels"])
                records["NetworkPolicyList"].pop()
                records["ServiceList"] = [{"spec": {"selector": info["labels"]}}]
                with self.assertRaises(ValueError):
                    verify(POLICY, policy, info["labels"])

    def test_recovery_endpoint_inventory(self):
        address = ipaddress.IPv4Address(ADDRESS)
        unrelated = {"addresses": ["10.16.0.91"], "targetRef": None}
        self.fixture["inventory"] = Mock(return_value=[{"endpoints": None}, {"endpoints": []}, {"endpoints": [unrelated]}])
        verify = self.fixture["verify_unrouted_endpoint"]
        verify(MEMBER)
        verify(MEMBER, address)
        for records, ip in (
            ([{**unrelated, "targetRef": {"uid": MEMBER}}], None),
            ([{**unrelated, "addresses": [ADDRESS]}], address),
            ([{**unrelated, "targetRef": False}], address),
            ({}, address),
        ):
            with self.subTest(records=records), self.assertRaises(ValueError):
                self.fixture["inventory"].return_value.append({"endpoints": records})
                try:
                    verify(MEMBER, ip)
                finally:
                    self.fixture["inventory"].return_value.pop()

    def test_loopback_rejects_extra_listeners(self):
        with tempfile.TemporaryDirectory() as directory:
            tcp, tcp6 = (Path(directory) / table for table in ("tcp", "tcp6"))
            tcp6.touch()

            def exec_bash(*args):
                self.assertEqual(args[:7], ("exec", self.fixture["pod_name"], "-c", "etcd", "--", "/bin/bash", "-ec"))
                body = args[7].replace("/proc/net/tcp6", shlex.quote(str(tcp6))).replace("/proc/net/tcp", shlex.quote(str(tcp)))
                return json.loads(subprocess.check_output(["bash", "-ec", body, *args[8:]]))

            self.fixture["namespaced"] = exec_bash
            loopback = "0: 0100007F:094B 00000000:0000 0A\n1: 0100007F:094C 00000000:0000 0A\n"
            pod_listener = "2: 5A00100A:094B 00000000:0000 0A\n"
            tcp.write_text(loopback)
            verify = self.fixture["verify_sockets"]
            verify(self.fixture["pod_name"])
            tcp.write_text(loopback + pod_listener)
            verify(self.fixture["pod_name"], ipaddress.IPv4Address(ADDRESS))
            with self.assertRaises(subprocess.CalledProcessError):
                verify(self.fixture["pod_name"])
            tcp.write_text(loopback + "2: 00000000:094C 00000000:0000 0A\n")
            with self.assertRaises(subprocess.CalledProcessError):
                verify(self.fixture["pod_name"])

    def test_receipt_lifecycle(self):
        previous = signal.signal(signal.SIGTERM, self.fixture["interrupted"])
        self.addCleanup(signal.signal, signal.SIGTERM, previous)
        for entry, outcome, error in (
            ("loopback", "success", None),
            ("loopback", "delete-fails", OSError),
            ("loopback", "pre-gate-route", ValueError),
            ("loopback", "link-interrupted", KeyboardInterrupt),
            ("reachable", "success", None),
            ("reachable", "not-ready", ValueError),
            ("reachable", "replaced-pod", ValueError),
            ("reachable", "fsync-fails", OSError),
            ("reachable", "interrupted", KeyboardInterrupt),
            ("reachable", "link-interrupted", KeyboardInterrupt),
            ("reachable", "publish-interrupted", KeyboardInterrupt),
            ("reachable", "preexisting-receipt", FileExistsError),
        ):
            with self.subTest(entry=entry, outcome=outcome), tempfile.TemporaryDirectory() as temp:
                self.check_receipt(entry, outcome, error, Path(temp).resolve())

    def check_receipt(self, entry, outcome, error, directory):
        f = self.fixture
        pod = self.pod(entry)
        name = pod["metadata"]["name"]
        disk = {"claim": pod["spec"]["volumes"][0]["persistentVolumeClaim"]["claimName"]}
        claim_sha = "c" * 64
        frozen = "0" * 64
        status = {"endpoint": f["endpoint"], "cluster_id": "1", "member_id": "2", "peer_url": f["peer"],
                  "version": "3.7.1", "committed_revision": "7", "raft_term": "1", "raft_index": "3", "raft_applied_index": "3"}
        source = {"namespace_uid": GUEST, "cluster_uid": GUEST, "etcd": {"status": status}, "marker": {"revision": "7"}}
        proposed = copy.deepcopy(pod)
        proposed["metadata"].update(uid=MEMBER, resourceVersion="1")
        pending = copy.deepcopy(proposed)
        del pending["spec"]["schedulingGates"]
        pending["status"] = {"phase": "Pending"}
        running = copy.deepcopy(pending)
        running["spec"]["nodeName"] = "fixture-node"
        running["status"] = {"phase": "Running", "podIP": ADDRESS, "podIPs": [{"ip": ADDRESS}], "containerStatuses": [
            {"state": {"running": {"startedAt": "fixture"}}, "ready": outcome != "not-ready", "name": "etcd",
             "restartCount": 0, "imageID": IMAGE, "containerID": CONTAINER},
        ]}
        checked = copy.deepcopy(running)
        if outcome == "replaced-pod":
            checked["metadata"]["uid"] = POLICY
        scope = "synthetic-etcd-native-restart" if entry == "loopback" else "synthetic-etcd-reachable-member"
        receipt = directory / ("etcd-restart.json" if entry == "loopback" else "etcd-reachable-member.json")
        actions = []
        existing = b"existing receipt\n"
        real_link = os.link
        real_publish = f["publish"]

        def link(source, destination):
            if outcome == "preexisting-receipt":
                # Simulate another writer winning after the initial existence check.
                with receipt.open("xb") as stream:
                    stream.write(existing)
            real_link(source, destination)
            if outcome == "link-interrupted":
                signal.raise_signal(signal.SIGTERM)

        def publish(*args):
            real_publish(*args)
            if outcome == "publish-interrupted":
                signal.raise_signal(signal.SIGTERM)

        def kubectl(action, *args, body=None):
            if action == "create":
                self.assertEqual(json.loads(body), pod)
                return proposed
            if action == "patch":
                changes = json.loads(args[-3])
                self.assertEqual(changes[:2], [
                    {"op": "test", "path": "/metadata/uid", "value": MEMBER},
                    {"op": "test", "path": "/metadata/resourceVersion", "value": "1"},
                ])
                actions.append("schedule")
                return pending
            self.assertEqual(action, "delete")
            self.assertTrue(args[1].endswith("/pods/" + name))
            self.assertEqual(json.loads(body)["preconditions"], {"uid": MEMBER})
            self.assertEqual(receipt.exists(), outcome == "preexisting-receipt")
            actions.append("delete")
            if outcome == "delete-fails":
                raise OSError("fixture deletion failure")

        def endpoint_inventory(_, kind):
            self.assertEqual(kind, "EndpointSliceList")
            actions.append("inventory")
            endpoints = None if outcome != "pre-gate-route" else [{"targetRef": {"uid": MEMBER}, "addresses": [ADDRESS]}]
            return [{"endpoints": endpoints}]

        f.update(approved_json=Mock(side_effect=[source, {"status": status}]), source_identity=Mock(return_value=IMAGE),
                 check_custody=Mock(return_value=({}, {"frozen_receipt_sha256": frozen}, {}, [disk], "b" * 64)),
                 verify_live=Mock(), verify_fences=Mock(), prepare_network_policy=Mock(return_value=POLICY),
                 namespaced=kubectl, get=Mock(side_effect=[proposed, running, checked]), inventory=endpoint_inventory,
                 verify_recovery_policy=Mock(), verify_execution_attachment=Mock(), verify_sockets=Mock(),
                 observe=Mock(return_value=(("fixture-node", CONTAINER), status),
                              side_effect=KeyboardInterrupt if outcome == "interrupted" else None))
        args = (entry, GUEST, str(directory / "source.json"), "d" * 64, str(directory / "member.json"),
                "e" * 64, str(directory), "f" * 64, claim_sha, str(directory))
        sync = patch("os.fsync", side_effect=[None, OSError("directory fsync failed")]) if outcome == "fsync-fails" else contextlib.nullcontext()
        with sync, patch("os.link", link), patch.dict(f, publish=publish), \
             contextlib.redirect_stdout(io.StringIO()) as stdout, contextlib.redirect_stderr(io.StringIO()):
            if error is None:
                f["main"](*args)
            else:
                with self.assertRaises(error):
                    f["main"](*args)
                if outcome != "publish-interrupted":
                    self.assertEqual(actions[-1], "delete")
                    if outcome == "preexisting-receipt":
                        self.assertEqual(list(directory.iterdir()), [receipt])
                        self.assertEqual(receipt.read_bytes(), existing)
                    else:
                        self.assertEqual(list(directory.iterdir()), [])
                    self.assertEqual(stdout.getvalue(), "")
                    if outcome == "pre-gate-route":
                        self.assertEqual(actions, ["inventory", "delete"])
                    return
        self.assertEqual(actions, ["inventory", "schedule", "inventory"] + (["delete"] if entry == "loopback" else []))
        self.assertEqual([call.args[-1] for call in f["verify_execution_attachment"].call_args_list], [None, "fixture-node"])
        raw = receipt.read_bytes()
        info = receipt.stat()
        self.assertEqual(stat.S_IMODE(info.st_mode), 0o600)
        self.assertEqual(info.st_nlink, 1)
        self.assertEqual(stdout.getvalue(), hashlib.sha256(raw).hexdigest() + "\n")
        record = json.loads(raw)
        self.assertEqual((record["scope"], record["pod_uid"], record["execution_sha256"]), (scope, MEMBER, claim_sha))
        controller = load("controller-start")
        if entry == "reachable":
            self.assertEqual((record["pod_ip"], record["container_id"], record["network_policy_uid"]), (ADDRESS, CONTAINER, POLICY))
            self.assertEqual(record["frozen_receipt_sha256"], frozen)
            policy = f["recovery_policy"](name, pod["metadata"]["labels"])
            policy["metadata"]["uid"] = POLICY
            controller.update(get=Mock(return_value=running), inventory=lambda _, kind: [policy] if kind == "NetworkPolicyList" else [])
            self.assertEqual(controller["verified_member"](record, source, ""), ADDRESS)
        else:
            self.assertNotIn("pod_ip", record)
            with self.assertRaisesRegex(ValueError, "The loopback recovery receipt cannot be used"):
                controller["verified_member"](record, source, "")


class ControllerRecoveryTest(unittest.TestCase):
    def setUp(self):
        self.fixture = load("controller-start")

    def test_tls_probe_only_mounts_member_credentials(self):
        f = self.fixture
        disk = {"name": "clone", "mountPath": "/var/lib/k0s"}
        source = {"controller": {"statefulset_uid": GUEST, "pod": "source-controller"}}
        template = {"spec": {"containers": [{"volumeMounts": [disk]}], "securityContext": {}}}
        stored = {"stateful_sets": {"items": [{"metadata": {"uid": GUEST}, "spec": {"template": template}}]},
                  "config_maps": {"items": [
                      {"metadata": {"namespace": f["namespace"], "name": "kmc-entrypoint-etcd-source-config"},
                       "data": {"k0smotron-entrypoint.sh": "exec true"}},
                      {"metadata": {"namespace": f["namespace"], "name": "kmc-etcd-source-config"},
                       "data": {"K0SMOTRON_K0S_YAML": "{}"}},
                  ]}}
        projections = [{"secret": {"name": "fixture-" + role, "items": [
            {"key": "tls.crt", "path": public}, {"key": "tls.key", "path": private}]}}
            for role, (public, private) in f["signers"].items()]
        pod = f["controller_pod"](stored, source, "etcd-controller-execution", {"image": IMAGE}, ADDRESS, "k0s",
                                  projections, [], "claim-sha", "member-sha")
        spec = pod["spec"]
        signer, tls = spec["initContainers"]
        app, = spec["containers"]
        self.assertEqual(spec["securityContext"], {})
        self.assertNotIn("securityContext", app)
        self.assertEqual(signer["securityContext"], {"allowPrivilegeEscalation": False})
        self.assertEqual(tls["securityContext"], {
            "runAsUser": 0, "runAsGroup": 0, "runAsNonRoot": False, "allowPrivilegeEscalation": False,
            "readOnlyRootFilesystem": True, "capabilities": {"drop": ["ALL"]},
        })
        self.assertEqual(app["volumeMounts"], [disk])
        self.assertEqual(tls["volumeMounts"], [{"name": "member-tls", "mountPath": "/signers", "readOnly": True}])
        projection = next(volume["projected"] for volume in spec["volumes"] if volume["name"] == "member-tls")
        self.assertEqual(projection["defaultMode"], 0o400)
        self.assertEqual(sorted((entry["secret"]["name"], item["key"], item["path"])
                                for entry in projection["sources"] for item in entry["secret"]["items"]), [
            ("fixture-apiserver-etcd-client", "tls.crt", "apiserver-etcd-client.crt"),
            ("fixture-apiserver-etcd-client", "tls.key", "apiserver-etcd-client.key"),
            ("fixture-etcd", "tls.crt", "etcd-ca.crt"),
        ])
        f["validate_admitted"](pod, pod)
        changed = copy.deepcopy(pod)
        changed["spec"]["initContainers"][1]["volumeMounts"].append(signer["volumeMounts"][0])
        with self.assertRaises(ValueError):
            f["validate_admitted"](changed, pod)

    def test_child_route_checks_service_and_pod_uids(self):
        f = self.fixture
        source = {"api_service": {"uid": GUEST}}
        owner = {"kind": "Service", "name": f["api_name"], "uid": GUEST}
        empty = {"addressType": "IPv4", "metadata": {"ownerReferences": [owner]}, "endpoints": None}
        target = {"kind": "Pod", "name": f["target"], "namespace": f["namespace"], "uid": MEMBER}
        entry = {"addresses": [ADDRESS], "targetRef": target, "conditions": {"ready": True}}
        ready = {**empty, "endpoints": [entry], "ports": [{"name": name, "port": port, "protocol": "TCP"}
                                                         for name, port in (("api", 6443), ("konnectivity", 8132))]}
        f["endpoint_slices"] = Mock(return_value=[empty])
        route = lambda: f["route_ready"](source, MEMBER, ADDRESS)
        self.assertFalse(route())
        self.assertTrue(f["no_endpoints"](f["api_name"]))
        f["endpoint_slices"].return_value = [empty, ready]
        self.assertTrue(route())
        self.assertFalse(f["no_endpoints"](f["api_name"]))
        f["endpoint_slices"].return_value = [{**ready, "endpoints": [{**entry, "conditions": {"ready": True, "terminating": True}}]}]
        self.assertFalse(route())
        for records in ({}, False, [entry, entry], [{**entry, "targetRef": None}],
                        [{**entry, "targetRef": {**target, "uid": POLICY}}],
                        [{**entry, "conditions": {"ready": True, "terminating": 0}}]):
            with self.subTest(records=records), self.assertRaises(ValueError):
                f["endpoint_slices"].return_value = [{**ready, "endpoints": records}]
                route()
        f["endpoint_slices"].return_value = [{**empty, "metadata": {"ownerReferences": [{**owner, "uid": POLICY}]}}, ready]
        with self.assertRaises(ValueError):
            route()


if __name__ == "__main__":
    unittest.main()
