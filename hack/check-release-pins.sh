#!/usr/bin/env bash
set -euo pipefail

dir=${1:-dist/release}

awk '/^upgradeFrom:/{in_list=1; next} in_list && /^ *- /{found=1; next} {in_list=0} END{exit found ? 0 : 1}' "$dir/release.yaml" || { echo "release.yaml: upgradeFrom is empty" >&2; exit 1; }
if [ -n "${REQUIRE_PINNED_IMAGE:-}" ]; then
  grep -q '^image: .*@sha256:[0-9a-f]\{64\}$' "$dir/release.yaml" || { echo "release.yaml: the bedrock image is not pinned by digest" >&2; exit 1; }
fi
