import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class PreparationTest(unittest.TestCase):
    def test_embedded_release_is_rendered_before_each_docker_build(self):
        self.assertIn("COPY dist/release /release", (ROOT / "Containerfile").read_text())
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        for job in ("e2e-init", "e2e-bundle", "e2e-authn", "e2e-tenant"):
            block = re.search(
                rf"^  {job}:\n(.*?)(?=^  [a-z]|\Z)", workflow, re.M | re.S
            ).group(1)
            with self.subTest(job=job):
                before, after = block.split("- run: docker build", 1)
                render = "make build release" if job == "e2e-init" else "make build binaries release-from-build"
                self.assertIn(render, before)
                if job != "e2e-init":
                    self.assertIn("IMAGE=localhost:5000/bedrock:dev PIN_DIGESTS=1", before)
                    self.assertLess(after.index("docker push"), after.index("bundle-from-release"))
                if job == "e2e-tenant":
                    self.assertIn("RELEASE_CONFIG=dist/components-e2e.yaml", before)

    def test_host_install_runners_are_supported(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        supported = (ROOT / "release/components.yaml").read_text()
        for job in ("e2e-init", "e2e-bundle", "e2e-authn", "e2e-tenant", "e2e-upgrade"):
            runner = re.search(rf"^  {job}:\n.*?runs-on: (\S+)", workflow, re.M | re.S).group(1)
            self.assertEqual(runner, "ubuntu-24.04", job)
            self.assertIn(runner, supported)

    def test_upgrade_prepares_each_versions_embedded_release(self):
        # Execute the real orchestration with stubbed builders. No registries,
        # Docker daemon, UI compilation or network access are needed.
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
