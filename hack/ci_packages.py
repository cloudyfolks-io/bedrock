#!/usr/bin/env python3
"""Partition `go list -deps -test -json ./...` without dropping a package.

Test variants carry their full dependency closure. Any package that can load
envtest is assigned to the slow job, including future helper packages.
"""
import json
import sys


def partition(text):
    decoder = json.JSONDecoder()
    packages, slow = set(), set()
    while text.strip():
        text = text.lstrip()
        obj, end = decoder.raw_decode(text)
        text = text[end:]
        if not obj.get("Module", {}).get("Main"):
            continue
        path = obj.get("ForTest", obj["ImportPath"])
        if "[" in path or path.endswith(".test"):
            continue
        packages.add(path)
        deps = obj.get("Deps", []) + obj.get("Imports", [])
        if any(dep == "sigs.k8s.io/controller-runtime/pkg/envtest" for dep in deps):
            slow.add(path)
    return sorted(packages - slow), sorted(packages & slow)


if __name__ == "__main__":
    fast, slow = partition(sys.stdin.read())
    mode = sys.argv[1]
    if mode not in ("unit", "envtest"):
        raise SystemExit("expected unit or envtest")
    print("\n".join(fast if mode == "unit" else slow))
