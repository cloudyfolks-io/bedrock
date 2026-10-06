#!/usr/bin/env bash
set -euo pipefail

version=${VERSION:-dev}
image=${IMAGE:-ghcr.io/cloudyfolks-io/bedrock:$version}
bin=${BIN:-bin/bedrock}
release_dir=${RELEASE_DIR:-dist/release}
arch=${ARCH:-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')}
emulation=${EMULATION:-true}
workdir=$(mktemp -d)
admin_password_file=${ADMIN_PASSWORD_FILE:-/var/lib/bedrock/e2e-admin-password}
export KUBECONFIG=/var/lib/k0s/pki/admin.conf

dump() {
  local status=$?
  if [ "$status" -ne 0 ]; then
    echo "--- k0s status"
    /usr/local/bin/k0s status || true
    echo "--- k0scontroller journal"
    journalctl -u k0scontroller --no-pager -n 150 2>/dev/null || true
    echo "--- authn files"
    ls -la /etc/bedrock/authn /var/lib/bedrock/authn 2>&1 || true
    grep -n authentication /etc/k0s/k0s.yaml || true
    echo "--- nodes"
    kubectl get nodes -o wide || true
    echo "--- pods"
    kubectl get pods -A -o wide || true
    echo "--- api reachability"
    grep server: /var/lib/k0s/pki/admin.conf || true
    ip -4 addr show || true
    ss -ltnp | grep 6443 || true
    curl -sk -m 5 "https://$vip:6443/healthz" || echo "vip healthz failed"
    curl -sk -m 5 https://10.96.0.1/healthz || echo "service ip healthz failed"
    kubectl get endpoints,endpointslices kubernetes -o yaml || true
    kubectl -n kube-system get configmap kube-proxy -o yaml || true
    echo "--- nat rules"
    iptables-save -t nat 2>/dev/null | grep -E 'KUBE-SERVICES|10.96.0.1' | head -20 || true
    nft list ruleset 2>/dev/null | grep -E '10.96.0.1' | head -20 || true
    echo "--- kube-proxy log"
    kubectl -n kube-system logs daemonset/kube-proxy --tail=100 || true
    echo "--- kube-vip log"
    kubectl -n kube-system logs daemonset/kube-vip --tail=100 || true
    echo "--- cluster"
    kubectl get cluster,release,host,setting -o yaml || true
    echo "--- agent log"
    journalctl -u bedrock-agent.service --no-pager -n 100 || true
    echo "--- operator log"
    kubectl -n bedrock-system logs deploy/bedrock-operator --tail=300 || true
    echo "--- addons"
    kubectl -n rook-ceph get pods -o wide || true
    kubectl -n kubevirt get pods -o wide || true
    kubectl -n cdi get pods -o wide || true
    kubectl -n traefik get pods,svc -o wide || true
    kubectl -n nmstate get pods,ds -o wide || true
    kubectl -n kube-system get ds kube-multus-ds -o wide || true
    kubectl -n kubevirt get kubevirt kubevirt -o yaml || true
    kubectl get cdi cdi -o yaml || true
    kubectl -n traefik get certificate,secret platform-tls -o yaml || true
    echo "--- fabric logs"
    for pod in $(kubectl -n kube-system get pods -o name 2>/dev/null | grep -E 'fabric|ovn|ovs' || true); do
      echo "--- $pod"; kubectl -n kube-system logs "$pod" --tail=100 --all-containers || true
    done
    echo "--- fabric-controller errors"
    kubectl -n kube-system logs deployment/fabric-controller --all-containers 2>/dev/null | grep -E '^[EW][0-9]' | head -80 || true
    echo "--- ovn NB_Global options"
    kubectl -n kube-system exec deployment/ovn-central -- ovn-nbctl get NB_Global . options 2>&1 || true
    echo "--- restarted containers"
    kubectl get pods -A --no-headers 2>/dev/null | awk '$5 != "0" {print $1, $2}' | while read -r ns pod; do
      echo "--- $ns/$pod"
      kubectl -n "$ns" get pod "$pod" -o jsonpath='{range .status.containerStatuses[*]}{.name} restarts={.restartCount} reason={.lastState.terminated.reason} exit={.lastState.terminated.exitCode}{"\n"}{end}' || true
      for container in $(kubectl -n "$ns" get pod "$pod" -o jsonpath='{.spec.containers[*].name}' 2>/dev/null); do
        kubectl -n "$ns" logs "$pod" -c "$container" --previous --tail=80 || true
      done
    done
    echo "--- events"
    kubectl get events -A --sort-by=.lastTimestamp 2>/dev/null | tail -80 || true
    echo "--- kernel oom"
    dmesg 2>/dev/null | grep -iE 'oom|killed process' | tail -20 || true
    journalctl -u k0scontroller --no-pager -n 200 || true
  fi
  rm -rf "$workdir"
}
trap dump EXIT

test "$(id -u)" -eq 0

if ! modinfo openvswitch >/dev/null 2>&1; then
  apt-get update -qq
  apt-get install -y -qq "linux-modules-extra-$(uname -r)"
fi
modprobe openvswitch
modprobe geneve

iface=$(ip -json route show default | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["dev"])')
nodeip=$(ip -json -4 addr show dev "$iface" | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["addr_info"][0]["local"])')
vip=$(echo "$nodeip" | awk -F. '{printf "%s.%s.%s.250", $1, $2, $3}')
if [ "$vip" = "$nodeip" ]; then vip=$(echo "$nodeip" | awk -F. '{printf "%s.%s.%s.251", $1, $2, $3}'); fi

VERSION=$version VIP=$vip IFACE=$iface EMULATION=$emulation envsubst < hack/e2e/cluster.yaml.tmpl > "$workdir/cluster.yaml"
cat "$workdir/cluster.yaml"

run_init() {
  "$@" | (umask 077 && tee "$workdir/init.log") | grep --line-buffered -v '^admin password: '
}

if [ -n "${BUNDLE:-}" ]; then
  mkdir -p /etc/k0s/containerd.d/certs.d/_default
  printf '[plugins."io.containerd.cri.v1.images".registry]\nconfig_path = "/etc/k0s/containerd.d/certs.d"\n' > /etc/k0s/containerd.d/cri-registry.toml
  printf 'server = "https://127.0.0.1:1"\n' > /etc/k0s/containerd.d/certs.d/_default/hosts.toml
  run_init "$bin" init -f "$workdir/cluster.yaml" --bundle "$BUNDLE" --timeout 45m
  test -f /var/lib/k0s/images/k0s-airgap.tar
  test "$(ls /var/lib/k0s/images/*.tar | wc -l)" -ge 3
  sha256sum /usr/local/bin/k0s | awk '{print "sha256:"$1}' | grep -qx "$(awk -v arch="$arch:" '$1 == arch {print $2}' "$release_dir/release.yaml")"
else
  mkdir -p "$workdir/preload"
  docker save "$image" -o "$workdir/preload/bedrock.tar"
  run_init "$bin" init -f "$workdir/cluster.yaml" --release-dir "$release_dir" --images-dir "$workdir/preload" --timeout 45m
fi

install -m 0600 /dev/null "$admin_password_file"
grep '^admin password: ' "$workdir/init.log" | sed 's/^admin password: //' >"$admin_password_file"
if [ "${GITHUB_ACTIONS:-}" = true ]; then
  echo "::add-mask::$(cat "$admin_password_file")"
fi
export ADMIN_PASSWORD_FILE="$admin_password_file"

kubectl get nodes -o wide
kubectl wait --for=condition=Ready node --all --timeout=300s
ip -4 addr show dev "$iface" | grep -q "$vip"
kubectl -n kube-system rollout status daemonset/kube-vip --timeout=120s
kubectl get cluster cluster -o jsonpath='{.status.version}' | grep -qx "$version"
kubectl wait --for=condition=Available cluster/cluster --timeout=120s
case "$image" in
localhost:*) test "$(kubectl -n bedrock-system get deploy/bedrock-operator -o jsonpath='{.spec.template.spec.containers[0].image}')" = "$image" ;;
esac
if kubectl get pods -A -o jsonpath='{range .items[*]}{.status.containerStatuses[*].state.waiting.reason}{"\n"}{end}' | grep -qE 'ImagePullBackOff|ErrImagePull'; then
  exit 1
fi
kubectl -n cert-manager rollout status deployment/cert-manager --timeout=300s
kubectl -n kube-system rollout status daemonset/kube-multus-ds --timeout=300s
kubectl -n nmstate rollout status deployment/nmstate-operator --timeout=300s
kubectl -n kube-system rollout status deployment/snapshot-controller --timeout=300s
kubectl -n rook-ceph rollout status deployment/rook-ceph-operator --timeout=300s
kubectl -n kubevirt rollout status deployment/virt-operator --timeout=300s
kubectl -n cdi rollout status deployment/cdi-operator --timeout=300s
kubectl -n traefik rollout status deployment/traefik --timeout=300s
kubectl wait --for=jsonpath='{.status.phase}'=Deployed -n kubevirt kubevirt/kubevirt --timeout=900s
kubectl -n kubevirt get kubevirt kubevirt -o jsonpath='{.spec.configuration.developerConfiguration.useEmulation}' | grep -qx "$emulation"
kubectl wait --for=jsonpath='{.status.phase}'=Deployed cdi/cdi --timeout=600s
kubectl wait --for=condition=VirtualizationReady cluster/cluster --timeout=300s
kubectl wait --for=condition=PlatformReady cluster/cluster --timeout=300s
kubectl get cluster cluster -o jsonpath='{.status.conditions[?(@.type=="StorageReady")].reason}' | grep -qx NoDevices
kubectl get clusterissuer bedrock-selfsigned -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' | grep -qx True
kubectl -n traefik get secret platform-tls -o jsonpath='{.type}' | grep -qx kubernetes.io/tls
kubectl -n traefik get tlsstore default -o jsonpath='{.spec.defaultCertificate.secretName}' | grep -qx platform-tls
for _ in $(seq 1 60); do
  lb=$(kubectl -n traefik get svc traefik -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || true)
  if [ "$lb" = "$vip" ]; then break; fi
  sleep 5
done
test "$lb" = "$vip"
code=$(curl -sk -o /dev/null -w '%{http_code}' -m 10 "https://$vip/")
test "$code" = "404"
test "$(kubectl -n nmstate get ds nmstate-handler -o jsonpath='{.status.desiredNumberScheduled}')" = "0"
kubectl get storageclass block 2>/dev/null && exit 1
systemctl is-active bedrock-agent.service
node=$(hostname | tr '[:upper:]' '[:lower:]')
for _ in $(seq 1 60); do
  cores=$(kubectl get host "$node" -o jsonpath='{.status.inventory.cpu.cores}' 2>/dev/null || true)
  if [ "${cores:-0}" -gt 0 ] 2>/dev/null; then break; fi
  sleep 5
done
if ! [ "${cores:-0}" -gt 0 ] 2>/dev/null; then
  echo "agent did not report cpu cores on host $node"
  exit 1
fi
test "$(kubectl get host "$node" -o jsonpath='{.status.inventory.memoryBytes}')" -gt 0
test "$(kubectl get host "$node" -o jsonpath='{.status.inventory.disks[0].path}')" != ""
kubectl get hostconfig "$node" -o jsonpath='{.spec.modules}' | grep -q br_netfilter
kubectl get node "$node" -o jsonpath='{.metadata.labels.bedrock\.cloudyfolks\.io/managed}' | grep -qx false
if kubectl --as=system:node:other --as-group=system:nodes patch host "$node" --subresource=status --type=merge -p '{"status":{"kubernetesVersion":"x"}}' 2>"$workdir/impersonate.err"; then
  echo "impersonated status patch must be denied"
  exit 1
fi
grep -q 'a node may only update the status of its own Host' "$workdir/impersonate.err"
kubectl patch host "$node" --type=merge -p '{"spec":{"management":{"enabled":true}}}'
kubectl wait --for=condition=ManagementApplied host/"$node" --timeout=180s
for _ in $(seq 1 30); do
  desired=$(kubectl -n nmstate get ds nmstate-handler -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || true)
  if [ "${desired:-0}" = "1" ]; then break; fi
  sleep 5
done
test "${desired:-0}" = "1"
test -f /etc/sysctl.d/90-bedrock.conf
grep -q 'net.ipv4.ip_forward = 1' /etc/sysctl.d/90-bedrock.conf
kubectl get node "$node" -o jsonpath='{.metadata.labels.bedrock\.cloudyfolks\.io/managed}' | grep -qx true
kubectl -n kube-system rollout status daemonset/kured --timeout=180s
kubectl patch host "$node" --type=merge -p '{"spec":{"management":{"enabled":false}}}'
kubectl get host "$(hostname | tr '[:upper:]' '[:lower:]')" -o jsonpath='{.spec.roles}' | grep -q ceph-osd
kubectl get setting storage.replicas -o jsonpath='{.spec.value}' | grep -qx 1
token=$("$bin" token create --roles workload --expiry 10m)
test -n "$token"
echo "$token" | python3 -c 'import base64,json,sys; t=sys.stdin.read().strip(); t+="="*(-len(t)%4); json.loads(base64.urlsafe_b64decode(t))["k0sToken"]'
echo "e2e-init passed"
