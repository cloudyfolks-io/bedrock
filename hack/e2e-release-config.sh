#!/usr/bin/env bash
set -euo pipefail

source=${1:-release/components.yaml}
target=${2:-dist/components-e2e.yaml}

mkdir -p "$(dirname "$target")"
awk '
  /^  - name: / { skipping = ($3 == "catalog") }
  /^[^ ]/ { skipping = 0 }
  !skipping { print }
' "$source" >"$target"
