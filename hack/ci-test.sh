#!/usr/bin/env bash
set -euo pipefail
mode=$1
shard=${ENVTEST_SHARD:-}
operator=./internal/operator

# Each envtest package starts its own etcd/apiserver. Go's CPU-sized default
# can overload hosted runners with many independent control planes.
args=(-count=1 -timeout 30m)
if [ "$mode" = envtest ]; then
  args=(-p 2 "${args[@]}")
fi

if [[ "$mode" == envtest && -n "$shard" ]]; then
  go_flags=$(go env GOFLAGS)
  test_selection="(^|[[:space:]\"'])--?(test[.])?(run|skip|short|list|c|exec)(=|[[:space:]\"']|$)"
  if [[ "$go_flags" =~ $test_selection ]]; then
    printf 'Envtest shards do not accept test selection or execution flags in GOFLAGS.\n' >&2
    exit 2
  fi
fi

case "$mode:$shard" in
  envtest:operator)
    # A–E accounts for about half the operator's control-plane starts.
    pattern='^Test[A-E]'
    status=0
    go test "${args[@]}" -run "$pattern" "$operator" || status=$?
    go test "${args[@]}" -skip "$pattern" "$operator" || status=$?
    exit "$status"
    ;;
  unit:|envtest:|envtest:packages) ;;
  *)
    printf 'Invalid CI test mode/shard: %s/%s\n' "$mode" "$shard" >&2
    exit 2
    ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
go list -deps -test -json ./... > "$tmp/packages.json"
python3 hack/ci_packages.py "$mode" < "$tmp/packages.json" > "$tmp/packages"
test -s "$tmp/packages"
if [ "$shard" = packages ]; then
  operator=$(go list "$operator")
  grep -Fxq "$operator" "$tmp/packages"
  grep -Fvx "$operator" "$tmp/packages" > "$tmp/other-packages"
  mv "$tmp/other-packages" "$tmp/packages"
fi
xargs go test "${args[@]}" < "$tmp/packages"
