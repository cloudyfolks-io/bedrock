#!/usr/bin/env python3
"""Fail-closed E2E tiers. Nightly/manual qualification always runs every suite."""
import argparse
import json
from pathlib import Path

ALL = {"kind", "init", "bundle", "authn", "tenant", "upgrade"}


def select(paths, full=False):
    paths = set(paths)
    if full or not paths:
        return sorted(ALL)
    selected = set()
    for path in paths:
        if path.startswith(("docs/",)) or path.endswith(".md"):
            continue
        selected.add("kind")
        if path.startswith("ui/"):
            selected.add("authn")
        elif path.startswith("internal/authn/"):
            selected.add("authn")
        elif path.startswith(("internal/compute/", "internal/network/", "internal/tenancy/",
                              "internal/images/", "internal/objectstorage/")):
            selected.add("tenant")
        elif path.startswith("test/e2e/authn/"):
            selected.add("authn")
        elif path.startswith("test/e2e/tenant/"):
            selected.add("tenant")
        else:
            # Includes workflow, dependency, release, installer and upgrade changes.
            return sorted(ALL)
    return sorted(selected)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--paths-file", required=True)
    parser.add_argument("--full", action="store_true")
    args = parser.parse_args()
    paths = Path(args.paths_file).read_bytes().decode().split("\0")
    jobs = select([path for path in paths if path], args.full)
    print("jobs=" + json.dumps(jobs, separators=(",", ":")))
