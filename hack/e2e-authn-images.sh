#!/usr/bin/env bash
set -euo pipefail

arch=${ARCH:-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')}
crane=${CRANE:-crane}
out=dist/cache/e2e-authn
images=(docker.io/osixia/openldap:1.5.0 ghcr.io/dexidp/dex:v2.45.1)

mkdir -p "$out"

retry() {
  local attempts=$1 i code=1
  shift
  for i in $(seq 1 "$attempts"); do
    "$@" && return 0
    code=$?
    sleep "$((i * 2))"
  done
  return "$code"
}

pinned=""
for image in "${images[@]}"; do
  name=$(basename "${image%%:*}")
  file="$out/$name.tar"
  digest=$(retry 3 "$crane" digest --platform "linux/$arch" "$image")
  pin="$image@$digest"
  if [ -f "$file" ] && grep -qxF "$pin" "$out/images.txt" 2>/dev/null; then
    pinned="$pinned$pin"$'\n'
    continue
  fi
  retry 3 "$crane" pull --platform "linux/$arch" "$image" "$file"
  pinned="$pinned$pin"$'\n'
done
printf '%s' "$pinned" >"$out/images.txt"

ls -l "$out"
cat "$out/images.txt"
