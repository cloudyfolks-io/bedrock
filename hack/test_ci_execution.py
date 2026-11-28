import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class ExecutionTest(unittest.TestCase):
    def test_envtest_shards_cover_packages_and_preserve_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            go = root / "go"
            go.write_text("""#!/usr/bin/env python3
import json, os, sys
if sys.argv[1:] == ["env", "GOFLAGS"]:
    print(os.environ["TEST_GOFLAGS"])
elif sys.argv[1:] == ["list", "./internal/operator"]:
    print("local/operator")
elif sys.argv[1] == "list":
    envtest = ["sigs.k8s.io/controller-runtime/pkg/envtest"]
    for path, deps in [
        ("local/unit", []),
        ("local/env", envtest),
        ("local/operator", envtest),
    ]:
        print(json.dumps({"ImportPath": path, "Module": {"Main": True}, "Deps": deps}))
else:
    with open(os.environ["TRACE"], "a") as trace:
        trace.write(json.dumps(sys.argv[1:]) + "\\n")
    sys.exit(int(os.environ["TEST_FAIL_ON"] in sys.argv))
""")
            go.chmod(0o755)
            trace = root / "trace"
            def run(mode, shard="", fail_on="", goflags=""):
                trace.write_text("")
                env = dict(os.environ, PATH=f"{root}:{os.environ['PATH']}",
                           TRACE=str(trace), ENVTEST_SHARD=shard, TEST_FAIL_ON=fail_on,
                           TEST_GOFLAGS=goflags)
                result = subprocess.run(["bash", "hack/ci-test.sh", mode],
                                        cwd=ROOT, env=env, capture_output=True)
                return result, [json.loads(line) for line in trace.read_text().splitlines()]

            base = ["-count=1", "-timeout", "30m"]
            unit = ["test", *base, "local/unit"]
            envtest = ["test", "-p", "2", *base]
            for mode, shard, args in [
                ("unit", "", unit),
                ("envtest", "", [*envtest, "local/env", "local/operator"]),
                ("envtest", "packages", [*envtest, "local/env"]),
            ]:
                result, commands = run(mode, shard)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(commands, [args])

            result, commands = run("envtest", fail_on="local/env")
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(len(commands), 1)

            for fail_on in ["", "-run", "-skip"]:
                with self.subTest(fail_on=fail_on):
                    result, commands = run("envtest", "operator", fail_on)
                    self.assertEqual(result.returncode, int(bool(fail_on)), result.stderr)
                    self.assertEqual(commands, [
                        [*envtest, "-run", "^Test[A-E]", "./internal/operator"],
                        [*envtest, "-skip", "^Test[A-E]", "./internal/operator"],
                    ])

            result, commands = run("envtest", "unexpected")
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(commands, [])

            for shard in ("operator", "packages"):
                for goflags in ("-run=^TestCluster", "'--test.skip=^TestCluster$'",
                                "-list=.", "-short", "-c", "-exec=true"):
                    with self.subTest(shard=shard, goflags=goflags):
                        result, commands = run("envtest", shard, goflags=goflags)
                        self.assertEqual(result.returncode, 2, result.stderr)
                        self.assertIn(b"GOFLAGS", result.stderr)
                        self.assertEqual(commands, [])

            result, commands = run("envtest", "operator", goflags="-tags=debug")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(len(commands), 2)
