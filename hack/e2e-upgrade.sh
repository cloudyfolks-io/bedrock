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
init_a=${INIT_A:-hack/e2e-init.sh}
assert_authn=${ASSERT_AUTHN:-}
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
    echo "--- hosts"
    kubectl get hosts -o yaml || true
    echo "--- pods"
    kubectl get pods -A -o wide || true
    echo "--- workloads"
    kubectl get deployments,daemonsets,statefulsets -A -o wide || true
    echo "--- fabric-cni log"
    kubectl -n kube-system logs daemonset/fabric-cni --tail=100 || true
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
    if [ -n "$assert_authn" ]; then
      echo "--- authn"
      kubectl -n bedrock-system get deploy/bedrock-authn -o wide || true
      kubectl -n bedrock-system logs deploy/bedrock-authn --tail=100 --all-containers || true
      kubectl -n traefik get certificate platform-tls -o yaml || true
      kubectl get host "$(hostname | tr '[:upper:]' '[:lower:]')" -o jsonpath='{.status.authn}' || true
      grep -n authentication /etc/k0s/k0s.yaml || true
      ls -l /etc/bedrock/authn || true
    fi
  fi
  rm -rf "$workdir"
}
trap dump EXIT

BUNDLE=dist/bedrock-$VERSION_A-bundle-$arch.tar.zst VERSION=$VERSION_A IMAGE=$image_a \
  BIN=dist/bedrock-$VERSION_A-linux-$arch RELEASE_DIR=dist/release-$VERSION_A ARCH=$arch bash "$init_a"

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
  for _ in $(seq 1 360); do
    if ! kill -0 "$upgrade" 2>/dev/null; then break; fi
    sleep 5
  done
  if kill -0 "$upgrade" 2>/dev/null; then
    kill "$upgrade" 2>/dev/null || true
    echo "the abort did not finish within 30 minutes" >&2
    exit 1
  fi
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
  test -n "$(kubectl -n bedrock-system get deploy/bedrock-operator -o jsonpath='{.spec.template.metadata.annotations.kubectl\.kubernetes\.io/restartedAt}')"
  test -n "$(kubectl -n kube-system get daemonset/fabric-cni -o jsonpath='{.spec.template.metadata.annotations.kubectl\.kubernetes\.io/restartedAt}')"
  test -f /var/lib/k0s/images/k0s-airgap.tar
  test ! -e "/var/lib/k0s/images/k0s-airgap-$VERSION_B.tar"
  for _ in $(seq 1 60); do
    if [ -z "$(kubectl get nodeupgrades -o name)" ]; then break; fi
    sleep 5
  done
  test -z "$(kubectl get nodeupgrades -o name)"
  test -z "$(find /var/lib/k0s -maxdepth 1 -name '.pre-restore-*')"
  test "$(find /var/lib/bedrock/backups -maxdepth 1 -name "bedrock-$VERSION_A-*.tar.gz" | wc -l)" -eq 1
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
if [ -n "$assert_authn" ]; then
  apiserver_has_authn() {
    local pid
    pid=$(pgrep -o -x kube-apiserver || true)
    [ -n "$pid" ] && tr '\0' '\n' <"/proc/$pid/cmdline" | grep -q '^--authentication-config=' && kubectl get --raw=/readyz >/dev/null 2>&1
  }
  restarted=0
  for _ in $(seq 1 120); do
    if apiserver_has_authn; then
      restarted=1
      break
    fi
    sleep 10
  done
  test "$restarted" -eq 1
  grep -q 'authentication-config: /etc/bedrock/authn/authentication.yaml' /etc/k0s/k0s.yaml
  test -s /etc/bedrock/authn/authentication.yaml
  test -s /etc/bedrock/authn/webhook.kubeconfig
  kubectl wait --for=condition=Ready node --all --timeout=300s
  kubectl get cluster cluster -o jsonpath='{.status.components[?(@.name=="authn")].available}' | grep -qx true
  kubectl -n bedrock-system rollout status deployment/bedrock-authn --timeout=300s
  kubectl -n cert-manager get secret bedrock-ca -o jsonpath='{.metadata.labels.bedrock\.cloudyfolks\.io/kind}' | grep -qx PlatformCA
  kubectl -n bedrock-system get secret bedrock-authn-webhook-token -o jsonpath='{.metadata.labels.bedrock\.cloudyfolks\.io/authn}' | grep -qx true
  issuer=""
  for _ in $(seq 1 60); do
    issuer=$(kubectl -n traefik get certificate platform-tls -o jsonpath='{.spec.issuerRef.name}' 2>/dev/null || true)
    if [ "$issuer" = bedrock-ca ]; then break; fi
    sleep 10
  done
  test "$issuer" = bedrock-ca
  kubectl -n traefik wait --for=condition=Ready certificate/platform-tls --timeout=300s
  kubectl -n cert-manager get secret bedrock-ca -o jsonpath='{.data.ca\.crt}' | base64 -d >"$workdir/bedrock-ca.crt"
  vip=$(kubectl get cluster cluster -o jsonpath='{.spec.api.vip}')
  platform_host=$(echo "$vip" | tr '.' '-').sslip.io
  served=0
  for _ in $(seq 1 60); do
    if curl -sf -m 10 --cacert "$workdir/bedrock-ca.crt" --resolve "sso.$platform_host:443:$vip" "https://sso.$platform_host/.well-known/openid-configuration" >"$workdir/discovery.json"; then
      served=1
      break
    fi
    sleep 5
  done
  test "$served" -eq 1
  grep -q "https://sso.$platform_host" "$workdir/discovery.json"
  (umask 077 && "$cli_b" authn create-admin >"$workdir/create-admin")
  grep -q '^admin password: ' "$workdir/create-admin"
  "$cli_b" authn create-admin | grep -qx 'admin exists'
  token=$(python3 -c 'import secrets, string; print("brk_" + "".join(secrets.choice(string.ascii_letters + string.digits) for _ in range(40)))')
  token_name=$(printf '%s' "$token" | sha256sum | awk '{print $1}')
  kubectl apply -f - <<TOKEN
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: APIToken
metadata:
  name: $token_name
  namespace: bedrock-system
  labels:
    bedrock.cloudyfolks.io/kind: APIToken
    bedrock.cloudyfolks.io/name: ${token_name:0:63}
spec:
  userRef: admin
  description: e2e upgrade
TOKEN
  whoami=""
  for _ in $(seq 1 30); do
    whoami=$(kubectl --kubeconfig /dev/null --server "https://$vip:6443" --certificate-authority /var/lib/k0s/pki/ca.crt --token "$token" auth whoami -o jsonpath='{.status.userInfo.username} {.status.userInfo.groups}' 2>/dev/null || true)
    case "$whoami" in "bedrock:admin "*) break ;; esac
    sleep 10
  done
  case "$whoami" in
  "bedrock:admin "*bedrock:bedrock-admins*) ;;
  *)
    echo "kubectl auth whoami with a Bedrock API token answered: $whoami" >&2
    exit 1
    ;;
  esac
fi
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
kubectl -n bedrock-system wait --for=delete virtualmachine/bedrock-smoke --timeout=300s
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
