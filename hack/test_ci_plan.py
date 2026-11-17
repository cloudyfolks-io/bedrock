import json
import unittest
from pathlib import Path

from ci_packages import partition
from ci_plan import ALL, select


class PlanTest(unittest.TestCase):
    def test_full_qualification(self):
        self.assertEqual(set(select(["ui/login/index.ts"], full=True)), ALL)
        self.assertEqual(set(select([])), ALL)

    def test_fail_closed(self):
        for path in ["go.mod", ".github/workflows/ci.yml", "internal/new/file.go",
                     "internal/cli/upgrade.go", "internal/operator/upgrade.go",
                     "hack/e2e-init.sh", "release/components.yaml", "Makefile"]:
            with self.subTest(path=path):
                self.assertEqual(set(select([path])), ALL)

    def test_tiers(self):
        self.assertEqual(select(["docs/design.md"]), [])
        self.assertEqual(select(["internal/authn/server/server.go"]), ["authn", "kind"])
        self.assertEqual(select(["internal/compute/container.go"]), ["kind", "tenant"])
        self.assertEqual(select(["internal/authn/a.go", "internal/network/b.go"]),
                         ["authn", "kind", "tenant"])

    def test_packages_cover_each_original_once(self):
        main = {"Main": True}
        objects = [
            {"ImportPath": "local/unit", "Module": main},
            {"ImportPath": "local/env", "Module": main},
            {"ImportPath": "local/env [local/env.test]", "ForTest": "local/env",
             "Module": main, "Deps": ["sigs.k8s.io/controller-runtime/pkg/envtest"]},
            {"ImportPath": "local/env.test", "Module": main},
            {"ImportPath": "external"},
        ]
        fast, slow = partition("".join(json.dumps(obj) for obj in objects))
        self.assertEqual(fast, ["local/unit"])
        self.assertEqual(slow, ["local/env"])
        self.assertFalse(set(fast) & set(slow))

    def test_workflow_retains_all_e2e_and_parallel_envtest(self):
        workflow = (Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml").read_text()
        for job in ALL:
            self.assertIn(f"  e2e-{job}:", workflow)
        self.assertIn("name: e2e-upgrade-abort", workflow)
        envtest = workflow.split("  envtest:\n")[1].split("  test:")[0]
        self.assertNotIn("needs:", envtest)
        self.assertIn("make test-envtest", envtest)
        self.assertIn("schedule:", workflow)
        self.assertIn("workflow_dispatch:", workflow)
        self.assertIn("needs: [fast-checks, envtest]", workflow)
        self.assertIn('test "$FAST" = success && test "$ENVTEST" = success', workflow)
        release = (Path(__file__).resolve().parents[1] / ".github/workflows/release.yml").read_text()
        self.assertIn("uses: ./.github/workflows/ci.yml", release)
        self.assertIn("needs: qualification", release)
