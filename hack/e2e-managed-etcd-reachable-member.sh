#!/usr/bin/env bash
set +x
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/e2e-managed-etcd-restart.sh"
run_etcd_member_recovery reachable "$@"
