#!/usr/bin/env bash
set -euo pipefail
mode=$1
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
go list -deps -test -json ./... > "$tmp/packages.json"
python3 hack/ci_packages.py "$mode" < "$tmp/packages.json" > "$tmp/packages"
test -s "$tmp/packages"
# Each envtest package starts its own etcd/apiserver. Go's CPU-sized default
# can overload hosted runners with many independent control planes.
args=()
if [ "$mode" = envtest ]; then
  args+=(-p 2)
fi
xargs go test "${args[@]}" -count=1 -timeout 30m < "$tmp/packages"
