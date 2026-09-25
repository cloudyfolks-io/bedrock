#!/usr/bin/env bash
set -euo pipefail

: "${VERSION_A:?}" "${VERSION_B:?}" "${K0S_A:?}" "${K0S_B:?}"
registry=${REGISTRY:-localhost:5000}
arch=${ARCH:-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')}
image_a=$registry/bedrock:$VERSION_A
image_b=$registry/bedrock:$VERSION_B
bundle_b=dist/bedrock-$VERSION_B-bundle-$arch.tar.zst
cli_b=dist/bedrock-$VERSION_B-linux-$arch
abort_in=${ABORT_IN:-}
workdir=$(mktemp -d)
export KUBECONFIG=/var/lib/k0s/pki/admin.conf

dump() {
  local status=$?
  if [ "$status" -ne 0 ]; then
    echo "--- upgrade log"
    cat "$workdir/upgrade.log" || true
    echo "--- cluster"
    kubectl get cluster cluster -o yaml || true
    echo "--- node upgrades"
    kubectl get nodeupgrades -o yaml || true
    echo "--- autopilot"
    kubectl get plans.autopilot.k0sproject.io -o yaml || true
    kubectl get controlnodes.autopilot.k0sproject.io -o yaml || true
    echo "--- hosts"
    kubectl get hosts -o yaml || true
    echo "--- pods"
    kubectl get pods -A -o wide || true
    echo "--- operator log"
    kubectl -n bedrock-system logs deploy/bedrock-operator --tail=300 || true
    echo "--- agent log"
    journalctl -u bedrock-agent.service --no-pager -n 300 || true
    echo "--- k0s log"
    journalctl -u k0scontroller --no-pager -n 200 || true
    echo "--- depot and backups"
    find /var/lib/bedrock -maxdepth 4 | head -100 || true
  fi
  rm -rf "$workdir"
}
trap dump EXIT

BUNDLE=dist/bedrock-$VERSION_A-bundle-$arch.tar.zst VERSION=$VERSION_A IMAGE=$image_a \
  BIN=dist/bedrock-$VERSION_A-linux-$arch RELEASE_DIR=dist/release-$VERSION_A ARCH=$arch hack/e2e-init.sh

rm -f "dist/bedrock-$VERSION_A-bundle-$arch.tar.zst"
df -h / /var/lib
node=$(hostname | tr '[:upper:]' '[:lower:]')
/usr/local/bin/k0s version | grep -qx "$K0S_A"
rm -f /var/run/reboot-required /var/run/reboot-required.pkgs

"$cli_b" upgrade --to "$VERSION_B" --bundle "$bundle_b" --yes --timeout 2h > "$workdir/upgrade.log" 2>&1 &
upgrade=$!
if [ -n "$abort_in" ]; then
  phase=""
  for _ in $(seq 1 720); do
    phase=$(kubectl get cluster cluster -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [ "$phase" = "$abort_in" ]; then break; fi
    sleep 5
  done
  test "$phase" = "$abort_in"
  "$cli_b" upgrade abort --yes
fi
set +e
wait "$upgrade"
status=$?
set -e
cat "$workdir/upgrade.log"

if [ -n "$abort_in" ]; then
  test "$status" -ne 0
  reason=""
  for _ in $(seq 1 180); do
    reason=$(kubectl get cluster cluster -o jsonpath='{.status.phase}/{.status.conditions[?(@.type=="Progressing")].reason}' 2>/dev/null || true)
    if [ "$reason" = "Idle/Aborted" ]; then break; fi
    sleep 5
  done
  test "$reason" = "Idle/Aborted"
  kubectl get cluster cluster -o jsonpath='{.status.version}' | grep -qx "$VERSION_A"
  kubectl get cluster cluster -o jsonpath='{.spec.desiredVersion}' | grep -qx "$VERSION_A"
  /usr/local/bin/k0s version | grep -qx "$K0S_A"
  test "$(kubectl -n bedrock-system get deploy/bedrock-operator -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$image_a"
  kubectl -n bedrock-system rollout status deploy/bedrock-operator --timeout=300s
  kubectl get host "$node" -o jsonpath='{.status.restore.backup}' | grep -q "^/var/lib/bedrock/backups/bedrock-$VERSION_A-"
  for _ in $(seq 1 60); do
    if [ -z "$(kubectl get nodeupgrades -o name)" ]; then break; fi
    sleep 5
  done
  test -z "$(kubectl get nodeupgrades -o name)"
  kubectl wait --for=condition=Ready node --all --timeout=300s
  echo "e2e-upgrade abort passed"
  exit 0
fi

test "$status" -eq 0
tail -1 "$workdir/upgrade.log" | grep -qx "cluster upgraded to $VERSION_B"
for phase in ControlPlane Components Workers Verify; do
  grep -q "^phase $phase: " "$workdir/upgrade.log"
done
kubectl get cluster cluster -o jsonpath='{.status.version}' | grep -qx "$VERSION_B"
kubectl get cluster cluster -o jsonpath='{.status.phase}' | grep -qx Idle
kubectl get cluster cluster -o jsonpath='{.status.conditions[?(@.type=="Available")].reason}' | grep -qx Upgraded
kubectl get cluster cluster -o jsonpath='{.status.conditions[?(@.type=="UpgradeBlocked")].reason}' | grep -qx Passed
kubectl get cluster cluster -o jsonpath='{.status.backups[0].location}' | grep -q "^host:$node:/var/lib/bedrock/backups/bedrock-$VERSION_A-"
ls /var/lib/bedrock/backups/bedrock-"$VERSION_A"-*.tar.gz
/usr/local/bin/k0s version | grep -qx "$K0S_B"
kubectl get host "$node" -o jsonpath='{.status.k0sVersion}' | grep -qx "$K0S_B"
kubectl get host "$node" -o jsonpath='{.status.agentVersion}' | grep -qx "$VERSION_B"
test "$(kubectl -n bedrock-system get deploy/bedrock-operator -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$image_b"
kubectl -n bedrock-system rollout status deploy/bedrock-operator --timeout=300s
kubectl -n bedrock-system get events --field-selector involvedObject.name=bedrock-smoke -o name | grep -q .
if kubectl -n bedrock-system get virtualmachine bedrock-smoke 2>/dev/null; then exit 1; fi
test -z "$(kubectl get nodeupgrades -o name)"
test ! -e "/var/lib/bedrock/staged/$VERSION_B"
test ! -e /var/lib/bedrock/previous/k0s
test "$(kubectl get node "$node" -o jsonpath='{.spec.unschedulable}')" != "true"
kubectl wait --for=condition=Ready node --all --timeout=300s
echo "e2e-upgrade passed"
