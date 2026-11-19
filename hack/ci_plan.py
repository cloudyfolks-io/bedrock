#!/usr/bin/env python3
import argparse
import json
from pathlib import Path

ALL = {"kind", "authn", "tenant", "upgrade"}


def select(paths, full=False):
    paths = set(paths)
    if full:
        return sorted(ALL)
    if paths and all(path.startswith("docs/") or path.endswith(".md") for path in paths):
        return []
    return ["kind"]


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--paths-file", required=True)
    parser.add_argument("--full", action="store_true")
    args = parser.parse_args()
    paths = Path(args.paths_file).read_bytes().decode().split("\0")
    jobs = select([path for path in paths if path], args.full)
    print("jobs=" + json.dumps(jobs, separators=(",", ":")))
