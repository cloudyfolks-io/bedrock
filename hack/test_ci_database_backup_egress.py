import json
from pathlib import Path
import subprocess
import sys
import unittest


HELPER = Path(__file__).parent / "e2e/tenant/database-backup-egress.py"
UID = "0b0c0d0e-1111-4222-8333-444455556666"
INSTANCE = f"db-{UID}"
API = ("default", "kubernetes", "https")
RGW = ("rook-ceph", "rook-ceph-rgw-bedrock-rgw", "http")
ENGINE = {
    "app.kubernetes.io/instance": INSTANCE,
    "bedrock.cloudyfolks.io/database-role": "engine",
}


def endpoint(address, ready=True):
    return {"conditions": {"ready": ready}, "addresses": [address]}


def endpoint_slice(service, port, *endpoints, family="IPv4", protocol="TCP"):
    namespace, name, port_name = service
    return {
        "metadata": {"namespace": namespace, "labels": {"kubernetes.io/service-name": name}},
        "addressType": family,
        "ports": [{"name": port_name, "port": port, "protocol": protocol}],
        "endpoints": list(endpoints),
    }


class BackupEgressTest(unittest.TestCase):
    def run_helper(self, *slices, engine="postgresql", uid=UID, options=(), raw=None):
        return subprocess.run(
            [sys.executable, str(HELPER), engine, uid, *options],
            input=raw if raw is not None else json.dumps({"kind": "List", "items": slices}),
            text=True,
            capture_output=True,
        )

    def policies(self, *slices, **kwargs):
        result = self.run_helper(*slices, **kwargs)
        self.assertEqual(result.returncode, 0, result.stderr)
        value = json.loads(result.stdout)
        self.assertEqual(value["apiVersion"], "v1")
        self.assertEqual(value["kind"], "List")
        return value["items"]

    def assert_rejected(self, result, message):
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertIn(message, result.stderr)

    def test_exact_backends_and_stable_deduplication(self):
        slices = [
            endpoint_slice(API, 6443, endpoint("192.0.2.9", None), endpoint("192.0.2.2"),
                           endpoint("192.0.2.7", False)),
            endpoint_slice(API, 6443, endpoint("192.0.2.9")),
            endpoint_slice(API, 6444, {"addresses": ["192.0.2.10"]}),
            endpoint_slice(API, 6443, endpoint("2001:db8::1"), family="IPv6"),
            endpoint_slice(RGW, 8080, endpoint("198.51.100.6")),
            endpoint_slice(("other", RGW[1], "http"), 8080, endpoint("198.51.100.7")),
            endpoint_slice((RGW[0], "other-service", "http"), 8080, endpoint("198.51.100.8")),
            endpoint_slice((RGW[0], RGW[1], "metrics"), 80, endpoint("198.51.100.9")),
            endpoint_slice(RGW, 80, endpoint("198.51.100.10"), protocol="UDP"),
        ]
        (policy,) = self.policies(*slices)
        self.assertEqual([policy], self.policies(*reversed(slices)))
        self.assertEqual(policy["kind"], "NetworkPolicy")
        self.assertEqual(policy["metadata"], {
            "namespace": "bedrock-dbaas",
            "name": f"{INSTANCE}-e2e-egress",
            "annotations": {"fabric.cloudyfolks.io/network_policy_for": "fabric"},
        })
        self.assertNotIn("ingress", policy["spec"])
        self.assertEqual(policy["spec"]["policyTypes"], ["Egress"])
        self.assertEqual(policy["spec"]["podSelector"], {"matchLabels": ENGINE})
        dns, *backends = policy["spec"]["egress"]
        self.assertEqual(dns, {
            "to": [{
                "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kube-system"}},
                "podSelector": {"matchLabels": {"k8s-app": "kube-dns"}},
            }],
            "ports": [{"port": 53, "protocol": "UDP"}, {"port": 53, "protocol": "TCP"}],
        })
        self.assertEqual(backends, [
            {"to": [{"ipBlock": {"cidr": "192.0.2.2/32"}}, {"ipBlock": {"cidr": "192.0.2.9/32"}}],
             "ports": [{"protocol": "TCP", "port": 6443}]},
            {"to": [{"ipBlock": {"cidr": "192.0.2.10/32"}}], "ports": [{"protocol": "TCP", "port": 6444}]},
            {"to": [{"ipBlock": {"cidr": "198.51.100.6/32"}}], "ports": [{"protocol": "TCP", "port": 8080}]},
        ])

    def test_mysql_job_reaches_only_its_database_and_dns(self):
        engine, backup = self.policies(
            endpoint_slice(API, 6443, endpoint("192.0.2.9")),
            endpoint_slice(RGW, 8080, endpoint("198.51.100.6")),
            engine="mysql",
        )
        self.assertEqual(backup["metadata"], {
            **engine["metadata"], "name": f"{INSTANCE}-e2e-backup-egress",
        })
        self.assertEqual(backup["spec"], {
            "policyTypes": ["Egress"],
            "podSelector": {"matchLabels": {"bedrock.cloudyfolks.io/backup-of": INSTANCE}},
            "egress": [
                engine["spec"]["egress"][0],
                {
                    "to": [{"podSelector": {"matchLabels": {
                        **ENGINE, "app.kubernetes.io/component": "database",
                    }}}],
                    "ports": [{"protocol": "TCP", "port": 6450}],
                },
            ],
        })

    def test_negative_probe_removes_only_rgw_even_when_ports_match(self):
        api = endpoint_slice(API, 6443, endpoint("192.0.2.9"))
        rgw = endpoint_slice(RGW, 6443, endpoint("198.51.100.6"))
        allowed = self.policies(api, rgw)
        denied = self.policies(api, rgw, options=("--deny-rgw",))
        self.assertEqual(allowed[0]["metadata"], denied[0]["metadata"])
        self.assertEqual(denied[0]["spec"]["egress"][1], {
            "to": [{"ipBlock": {"cidr": "192.0.2.9/32"}}],
            "ports": [{"protocol": "TCP", "port": 6443}],
        })
        self.assertEqual(len(denied[0]["spec"]["egress"]), 2)
        self.assert_rejected(self.run_helper(api, options=("--deny-rgw",)), "no ready IPv4 TCP targets")
        overlapping = endpoint_slice(RGW, 6443, endpoint("192.0.2.9"))
        self.assert_rejected(self.run_helper(api, overlapping, options=("--deny-rgw",)), "share a target")

    def test_missing_targets_fail_closed(self):
        api = endpoint_slice(API, 6443, endpoint("192.0.2.9"))
        rgw = endpoint_slice(RGW, 8080, endpoint("198.51.100.6"))
        self.assert_rejected(self.run_helper(rgw), "no ready IPv4 TCP targets for default/kubernetes:https")
        for unusable in (
            endpoint_slice(RGW, 8080),
            endpoint_slice(RGW, 8080, endpoint("198.51.100.6", False)),
            endpoint_slice(RGW, 8080, endpoint("2001:db8::1"), family="IPv6"),
            endpoint_slice(RGW, 8080, endpoint("198.51.100.6"), protocol="UDP"),
            endpoint_slice((*RGW[:2], "https"), 8080, endpoint("198.51.100.6")),
            endpoint_slice(("other", *RGW[1:]), 8080, endpoint("198.51.100.6")),
        ):
            with self.subTest(unusable=unusable):
                self.assert_rejected(
                    self.run_helper(api, unusable),
                    "no ready IPv4 TCP targets for rook-ceph/rook-ceph-rgw-bedrock-rgw:http",
                )

    def test_invalid_targets_fail_before_output(self):
        rgw = endpoint_slice(RGW, 8080, endpoint("198.51.100.6"))
        good_api = endpoint_slice(API, 6443, endpoint("192.0.2.9"))
        for port in (False, "6443", 6443.0, None, -1, 0, 65536):
            with self.subTest(port=port):
                invalid = endpoint_slice(API, port, endpoint("192.0.2.9"))
                self.assert_rejected(self.run_helper(good_api, rgw, invalid), "invalid TCP target port")
        for address in (3221225993, "192.0.2.9/32", "192.000.2.9", "2001:db8::1", "192.0.2.999", "0.0.0.0"):
            with self.subTest(address=address):
                invalid = endpoint_slice(API, 6443, endpoint(address))
                self.assert_rejected(self.run_helper(good_api, rgw, invalid), "IPv4 endpoint address")
        invalid = endpoint_slice(API, 6443, endpoint("192.0.2.9", 0))
        self.assert_rejected(self.run_helper(good_api, rgw, invalid), "readiness must be boolean or null")
        duplicate = endpoint_slice(API, 6443, endpoint("192.0.2.9"))
        duplicate["ports"].append({"name": "https", "port": 6444, "protocol": "TCP"})
        self.assert_rejected(self.run_helper(good_api, rgw, duplicate), "ambiguous named TCP target port")

    def test_invalid_input_and_uid_produce_no_output(self):
        self.assert_rejected(self.run_helper(raw="{"), "Expecting property name")
        self.assert_rejected(self.run_helper(raw='{"items": {}}'), "items must be an array")
        self.assert_rejected(self.run_helper(uid="db-prod"), "database UID")


if __name__ == "__main__":
    unittest.main()
