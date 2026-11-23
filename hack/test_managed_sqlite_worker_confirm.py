import ast
import base64
import binascii
import gzip
import io
from pathlib import Path
import re
import unittest


class OriginalTokenTest(unittest.TestCase):
    def test_terminal_lf(self):
        script = Path(__file__).with_name("e2e-managed-sqlite-worker-confirm.sh").read_text()
        python = script.split("<<'PY'\n", 1)[1].rsplit("\nPY\n", 1)[0]
        function = next(node for node in ast.parse(python).body
                        if isinstance(node, ast.FunctionDef) and node.name == "original_token")
        namespace = dict(base64=base64, gzip=gzip, io=io, re=re,
                         require=self.assertTrue, bootstrap="sqlite-worker-bootstrap",
                         identity=lambda secret, uid: self.assertEqual(secret["metadata"]["uid"], uid))

        endpoint = "https://192.0.2.1:6443"
        token = base64.b64encode(gzip.compress(f"server: {endpoint}\ntoken: abcdef.0123456789abcdef\n".encode()))
        files = {
            "k0s_sha256": "/usr/local/bin/k0s",
            "airgap_sha256": "/var/lib/k0s/images/k0s-airgap.tar",
            "busybox_tar_sha256": "/var/lib/k0s/images/busybox.tar",
        }
        assets = {key: char * 64 for key, char in zip(files, "abc")}
        checksums = "".join(f"'{assets[key]}' {path}\n" for key, path in files.items()).encode()
        exec(compile(ast.Module(body=[function], type_ignores=[]), str(Path(__file__)), "exec"), namespace)
        for name, value, valid in (
            ("absent", token, True),
            ("present", token + b"\n", True),
            ("repeated", token + b"\n\n", False),
            ("embedded", token[:8] + b"\n" + token[8:], False),
        ):
            with self.subTest(lf=name):
                userdata = b"    content: " + base64.b64encode(value) + b"\n" + checksums
                secret = {"metadata": {"uid": "fixture"}, "data": {"userdata": base64.b64encode(userdata)}}
                namespace["parent"] = lambda *_: secret
                args = ({"api_endpoint": endpoint}, {"bootstrap_secret_uid": "fixture"}, assets)
                if valid:
                    self.assertEqual(namespace["original_token"](*args), "abcdef")
                else:
                    with self.assertRaises(binascii.Error):
                        namespace["original_token"](*args)


if __name__ == "__main__":
    unittest.main()
