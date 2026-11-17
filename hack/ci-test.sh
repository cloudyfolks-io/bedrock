#!/usr/bin/env bash
set -euo pipefail
mode=$1
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
go list -deps -test -json ./... > "$tmp/packages.json"
python3 hack/ci_packages.py "$mode" < "$tmp/packages.json" > "$tmp/packages"
test -s "$tmp/packages"
xargs go test -count=1 -timeout 30m < "$tmp/packages"
