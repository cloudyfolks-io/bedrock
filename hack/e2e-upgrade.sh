#!/usr/bin/env bash
set -euo pipefail

: "${VERSION_A:?}" "${VERSION_B:?}" "${K0S_A:?}" "${K0S_B:?}"
registry=${REGISTRY:-localhost:5000}
arch=${ARCH:-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')}
image_a=$registry/bedrock:$VERSION_A
image_b=$registry/bedrock:$VERSION_B
image_a_file=$(printf '%s' "$image_a" | sed -E 's/[^A-Za-z0-9._-]/_/g').tar
image_b_file=$(printf '%s' "$image_b" | sed -E 's/[^A-Za-z0-9._-]/_/g').tar
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
    echo "--- disk"
    df -h / /var/lib || true
    echo "--- node conditions"
    kubectl describe nodes | sed -n '/Conditions/,/Addresses/p' || true
    echo "--- events"
    kubectl get events -A --sort-by=.lastTimestamp | tail -60 || true
    echo "--- out of memory"
    dmesg | grep -i -E 'oom|out of memory' | tail -20 || true
    echo "--- vmis"
    kubectl get vmi -A -o wide || true
  fi
  rm -rf "$workdir"
}
trap dump EXIT

BUNDLE=dist/bedrock-$VERSION_A-bundle-$arch.tar.zst VERSION=$VERSION_A IMAGE=$image_a \
  BIN=dist/bedrock-$VERSION_A-linux-$arch RELEASE_DIR=dist/release-$VERSION_A ARCH=$arch hack/e2e-init.sh

node=$(hostname | tr '[:upper:]' '[:lower:]')
test "$(kubectl get host "$node" -o jsonpath='{.spec.management.enabled}')" != "true"
kubectl wait --for=jsonpath='{.status.conditions[?(@.type=="ManagementApplied")].reason}'=ManagementDisabled host/"$node" --timeout=180s
grep -q 'config_path = "/etc/k0s/containerd.d/certs.d"' /etc/k0s/containerd.d/cri-registry.toml
mkdir -p /etc/k0s/containerd.d/certs.d/_default
printf 'server = "https://127.0.0.1:1"\n' > /etc/k0s/containerd.d/certs.d/_default/hosts.toml
rm -f "dist/bedrock-$VERSION_A-bundle-$arch.tar.zst"
df -h / /var/lib
/usr/local/bin/k0s version | grep -qx "$K0S_A"
rm -f /var/run/reboot-required /var/run/reboot-required.pkgs

"$cli_b" upgrade --to "$VERSION_B" --bundle "$bundle_b" --yes --timeout 2h > "$workdir/upgrade.log" 2>&1 &
upgrade=$!
depot=""
for _ in $(seq 1 360); do
  depot=$(kubectl get hosts -o jsonpath='{.items[*].status.depot.bundles[*].version}' 2>/dev/null || true)
  case " $depot " in *" $VERSION_B "*) break ;; esac
  if ! kill -0 "$upgrade" 2>/dev/null; then break; fi
  sleep 5
done
case " $depot " in
*" $VERSION_B "*) rm -f "$bundle_b" ;;
*)
  if kill -0 "$upgrade" 2>/dev/null; then
    echo "no host reports the depot for $VERSION_B" >&2
    exit 1
  fi
  ;;
esac
if [ -n "$abort_in" ]; then
  phase=""
  for _ in $(seq 1 720); do
    phase=$(kubectl get cluster cluster -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [ "$phase" = "$abort_in" ]; then break; fi
    if ! kill -0 "$upgrade" 2>/dev/null; then break; fi
    sleep 5
  done
  test "$phase" = "$abort_in"
  aborted=0
  for _ in $(seq 1 60); do
    if "$cli_b" upgrade abort --yes; then
      aborted=1
      break
    fi
    sleep 5
  done
  test "$aborted" -eq 1
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
  test -f /var/lib/k0s/images/k0s-airgap.tar
  test ! -e "/var/lib/k0s/images/k0s-airgap-$VERSION_B.tar"
  for _ in $(seq 1 60); do
    if [ -z "$(kubectl get nodeupgrades -o name)" ]; then break; fi
    sleep 5
  done
  test -z "$(kubectl get nodeupgrades -o name)"
  kubectl wait --for=condition=Ready node --all --timeout=300s
  grep -qx 'server = "https://127.0.0.1:1"' /etc/k0s/containerd.d/certs.d/_default/hosts.toml
  if kubectl get pods -A -o jsonpath='{range .items[*]}{.status.containerStatuses[*].state.waiting.reason}{"\n"}{end}' | grep -qE 'ImagePullBackOff|ErrImagePull'; then
    exit 1
  fi
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
test -f "/var/lib/k0s/images/k0s-airgap-$VERSION_B.tar"
test ! -e /var/lib/k0s/images/k0s-airgap.tar
if [ "$image_a_file" != "$image_b_file" ]; then
  test ! -e "/var/lib/k0s/images/$image_a_file"
fi
test "$(kubectl get node "$node" -o jsonpath='{.spec.unschedulable}')" != "true"
kubectl wait --for=condition=Ready node --all --timeout=300s
grep -qx 'server = "https://127.0.0.1:1"' /etc/k0s/containerd.d/certs.d/_default/hosts.toml
if kubectl get pods -A -o jsonpath='{range .items[*]}{.status.containerStatuses[*].state.waiting.reason}{"\n"}{end}' | grep -qE 'ImagePullBackOff|ErrImagePull'; then
  exit 1
fi
echo "e2e-upgrade passed"
