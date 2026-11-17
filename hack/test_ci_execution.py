import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class ExecutionTest(unittest.TestCase):
    def test_envtest_bounds_control_planes_and_preserves_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            go = root / "go"
            go.write_text("""#!/usr/bin/env python3
import json, os, sys
if sys.argv[1] == "list":
    for path, deps in [
        ("local/unit", []),
        ("local/env", ["sigs.k8s.io/controller-runtime/pkg/envtest"]),
    ]:
        print(json.dumps({"ImportPath": path, "Module": {"Main": True}, "Deps": deps}))
else:
    with open(os.environ["TRACE"], "a") as trace:
        trace.write(json.dumps(sys.argv[1:]) + "\\n")
    sys.exit(int(os.environ["TEST_EXIT"]))
""")
            go.chmod(0o755)
            trace = root / "trace"
            env = dict(os.environ, PATH=f"{root}:{os.environ['PATH']}",
                       TRACE=str(trace), TEST_EXIT="0")
            for mode, package in [("unit", "local/unit"), ("envtest", "local/env")]:
                result = subprocess.run(["bash", "hack/ci-test.sh", mode],
                                        cwd=ROOT, env=env, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                args = json.loads(trace.read_text().splitlines()[-1])
                self.assertEqual(args[-1], package)
                self.assertIn("-count=1", args)
                self.assertEqual(args.count(package), 1)
                if mode == "envtest":
                    self.assertEqual(args[1:3], ["-p", "2"])
                else:
                    self.assertNotIn("-p", args)
            trace.write_text("")
            env["TEST_EXIT"] = "1"
            result = subprocess.run(["bash", "hack/ci-test.sh", "envtest"],
                                    cwd=ROOT, env=env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(len(trace.read_text().splitlines()), 1)
