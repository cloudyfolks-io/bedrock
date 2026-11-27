import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OFFLINE_INSTALL_JOBS = ("e2e-authn", "e2e-tenant")


def ci_jobs():
    workflow = (ROOT / ".github/workflows/ci.yml").read_text()
    return dict(re.findall(
        r"^  ([a-z][a-z0-9-]*):\n(.*?)(?=^  [a-z][a-z0-9-]*:\n|\Z)",
        workflow.partition("\njobs:\n")[2], re.M | re.S,
    ))


class PreparationTest(unittest.TestCase):
    def test_kubernetes_absence_checks_require_a_successful_response(self):
        script = """
set -euo pipefail
source "$1"
kubectl() {
  [[ " $* " == *" -o name "* ]] || return 2
  if [[ " $* " == *" pod fixture "* && " $* " != *" --ignore-not-found "* ]]; then
    return 2
  fi
  case "$response" in
    absent) return 0 ;;
    present) printf 'pod/fixture\\n' ;;
    unavailable) return 1 ;;
  esac
}
response=absent
kubectl_absent pod fixture
list_empty get pods
for response in present unavailable; do
  if kubectl_absent pod fixture || list_empty get pods; then
    exit 1
  fi
done
"""
        subprocess.run(["bash", "-c", script, "test", str(ROOT / "hack/e2e/lib.sh")], check=True)

    def test_embedded_release_is_rendered_before_each_docker_build(self):
        containerfile = (ROOT / "Containerfile").read_text().splitlines()
        self.assertIn("COPY --from=build /src/dist/release /release", containerfile)
        jobs = ci_jobs()
        for job in OFFLINE_INSTALL_JOBS:
            with self.subTest(job=job):
                self.assertIn(job, jobs)
                before_build, build, after_build = jobs[job].partition("- run: docker build")
                self.assertTrue(build, "the job must build the Bedrock image")
                before_push, push, after_push = after_build.partition("- run: docker push")
                self.assertTrue(push, "the job must push the built Bedrock image")
                self.assertIn("make build binaries release-from-build", before_build)
                self.assertNotIn("bundle-from-release", before_build + before_push)
                self.assertIn("make release-from-build bundle-from-release", after_push)
                pin = "IMAGE=localhost:5000/bedrock:dev PIN_DIGESTS=1"
                self.assertIn(pin, before_build)
                self.assertIn(pin, after_push)
                if job == "e2e-tenant":
                    config = "RELEASE_CONFIG=dist/components-e2e.yaml"
                    self.assertIn(config, before_build)
                    self.assertIn(config, after_push)

    def test_host_install_runners_are_supported(self):
        jobs = ci_jobs()
        supported = (ROOT / "release/components.yaml").read_text().splitlines()
        for job in (*OFFLINE_INSTALL_JOBS, "e2e-upgrade"):
            with self.subTest(job=job):
                self.assertIn(job, jobs)
                runners = re.findall(r"^    runs-on: (\S+)$", jobs[job], re.M)
                self.assertEqual(runners, ["ubuntu-24.04"])
                self.assertIn(f"  - {runners[0]}", supported)

    def test_upgrade_prepares_each_versions_embedded_release(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "hack").mkdir()
            (root / "tools").mkdir()
            (root / "Makefile").write_text("release-from-build:\n")
            (root / "hack/e2e-release-config.sh").write_text("#!/bin/sh\nexit 0\n")
            (root / "hack/e2e-release-config.sh").chmod(0o755)
            make = root / "tools/make"
            make.write_text("""#!/usr/bin/env python3
import os, pathlib, sys
args = sys.argv[1:]
opts = dict(arg.split("=", 1) for arg in args if "=" in arg)
version = opts["VERSION"]
with open(os.environ["TRACE"], "a") as log:
    log.write("make " + " ".join(args) + "\\n")
if "release-from-build" in args:
    target = pathlib.Path("dist/release")
    target.mkdir(parents=True, exist_ok=True)
    (target / "version").write_text(version)
if "bundle-from-release" in args:
    assert pathlib.Path("dist/release/version").read_text() == version
    pathlib.Path(f"dist/bedrock-{version}-bundle-amd64.tar.zst").touch()
    pathlib.Path(f"dist/bedrock-{version}-linux-amd64").touch()
""")
            docker = root / "tools/docker"
            docker.write_text("""#!/usr/bin/env python3
import os, pathlib, sys
with open(os.environ["TRACE"], "a") as log:
    log.write("docker " + " ".join(sys.argv[1:]) + "\\n")
if sys.argv[1] == "build":
    version = next(arg.split("=", 1)[1] for arg in sys.argv if arg.startswith("VERSION="))
    assert pathlib.Path("dist/release/version").read_text() == version
""")
            make.chmod(0o755)
            docker.chmod(0o755)
            env = dict(os.environ, PATH=f"{root / 'tools'}:{os.environ['PATH']}",
                       TRACE=str(root / "trace"), VERSION_A="vA", VERSION_B="vB",
                       K0S_A="k0sA", K0S_B="k0sB", ARCH="amd64", VERSION_A_REF="")
            subprocess.run(["bash", str(ROOT / "hack/e2e-upgrade-build.sh")],
                           cwd=root, env=env, check=True, capture_output=True)
            trace = (root / "trace").read_text().splitlines()
            for version in ("vA", "vB"):
                prepare = next(i for i, line in enumerate(trace)
                               if "build binaries release-from-build" in line and f"VERSION={version} " in line)
                build = next(i for i, line in enumerate(trace)
                             if line.startswith("docker build") and f"VERSION={version} " in line)
                publish = next(i for i, line in enumerate(trace)
                               if line == f"docker push localhost:5000/bedrock:{version}")
                bundle = next(i for i, line in enumerate(trace)
                              if "bundle-from-release" in line and f"VERSION={version} " in line)
                self.assertLess(prepare, build)
                self.assertLess(build, publish)
                self.assertLess(publish, bundle)
                self.assertIn("RELEASE_CONFIG=dist/components-e2e.yaml", trace[prepare])
                self.assertEqual((root / f"dist/release-{version}/version").read_text(), version)
