#!/usr/bin/env bash
set -euo pipefail

vm=${CI_VM:-bedrock-ci}
cpus=${CI_CPUS:-12}
memory=${CI_MEMORY:-24}
disk=${CI_DISK:-100}
min_free=${CI_MIN_FREE_GB:-25}
floor=${CI_FLOOR_GB:-15}
port=${CI_REGISTRY_PORT:-5001}
registry_name=${CI_REGISTRY_NAME:-bedrock-ci-registry}
kind_image=ghcr.io/cloudyfolks-io/bedrock:dev
repo=$(git rev-parse --show-toplevel)
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
base_images=(golang:1.27 gcr.io/distroless/static:nonroot registry:3 node:24-bookworm-slim)
upgrade_env="REGISTRY=localhost:$port ARCH=$arch VERSION_A=v0.0.0-e2e.1 VERSION_B=v0.0.0-e2e.2 K0S_A=v1.36.2+k0s.0 K0S_B=v1.36.3+k0s.0"
known_jobs=(test e2e-kind e2e-bundle e2e-upgrade e2e-upgrade-abort e2e-authn)
vm_path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

if [ "$#" -eq 0 ]; then
  set -- "${known_jobs[@]}"
fi

is_known_job() {
  local job=$1 candidate
  for candidate in "${known_jobs[@]}"; do
    if [ "$candidate" = "$job" ]; then
      return 0
    fi
  done
  return 1
}

for job in "$@"; do
  if ! is_known_job "$job"; then
    echo "unknown job: $job" >&2
    exit 1
  fi
done

require() {
  local tool missing=0
  for tool in docker go helm kind limactl make; do
    if ! command -v "$tool" >/dev/null; then
      echo "$tool is required" >&2
      missing=1
    fi
  done
  if [ ! -w /dev/kvm ]; then
    echo "/dev/kvm must be writable for $(id -un)" >&2
    missing=1
  fi
  return "$missing"
}

if [ "$(uname -s)" != Linux ]; then
  echo "hack/local-ci.sh runs on a Linux host with KVM" >&2
  exit 1
fi
require

job_order=("$@")
summary=""
ran=0
job_pid=""
watcher_pid=""

cd "$repo"

free_gb() {
  df -Pk "$repo" | awk 'NR==2 {print int($4/1048576)}'
}

watch_floor() {
  local job=$1 pid=$2 free
  while sleep 10 && kill -0 "$pid" 2>/dev/null; do
    free=$(free_gb)
    if [ "$free" -lt "$floor" ]; then
      echo "only ${free}GiB free on $repo, below the ${floor}GiB floor; stopping $job" >&2
      : >"$tmp/floor-stop"
      kill -TERM "-$pid" 2>/dev/null || true
      return
    fi
  done
}

guard() {
  local free
  free=$(free_gb)
  if [ "$free" -lt "$min_free" ]; then
    echo "only ${free}GiB free on $repo, need ${min_free}GiB" >&2
    return 1
  fi
}

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

ensure_images() {
  local image
  for image in "${base_images[@]}"; do
    docker image inspect "$image" >/dev/null 2>&1 || retry 5 docker pull "$image"
  done
}

registry_up() {
  registry_down
  docker run --rm -d -p "127.0.0.1:$port:5000" --name "$registry_name" registry:3
  wait_registry
}

registry_down() {
  docker rm -f "$registry_name" >/dev/null 2>&1 || true
}

wait_registry() {
  local i
  for i in $(seq 1 30); do
    curl -sf "http://localhost:$port/v2/" && break
    sleep 1
  done
  curl -sf "http://localhost:$port/v2/"
}

remove_job_images() {
  docker image ls --filter "reference=localhost:$port/bedrock" --filter "reference=$kind_image" --quiet | sort -u | xargs -r docker image rm -f >/dev/null 2>&1 || true
}

vm_provision() {
  cat <<'SCRIPT'
#!/bin/sh
set -e
sysctl -w fs.inotify.max_user_instances=8192 fs.inotify.max_user_watches=1048576 vm.max_map_count=262144
cat >/usr/local/bin/kubectl <<'KUBECTL'
#!/bin/sh
exec /usr/local/bin/k0s kubectl "$@"
KUBECTL
chmod +x /usr/local/bin/kubectl
SCRIPT
}

vm_config() {
  cat <<CONFIG
base:
- template:_images/ubuntu-24.04
vmType: qemu
cpus: $cpus
memory: ${memory}GiB
disk: ${disk}GiB
mounts:
- location: "$repo"
  writable: true
containerd:
  system: false
  user: false
provision:
- mode: system
  script: |
$(vm_provision | sed 's/^/    /')
CONFIG
}

vm_up() {
  vm_down
  vm_config >"$tmp/vm.yaml"
  limactl start --name "$vm" --tty=false "$tmp/vm.yaml"
  if ! wait_ntp; then
    echo "warning: the VM clock is not NTP synchronized" >&2
  fi
}

vm_down() {
  if limactl list --quiet 2>/dev/null | grep -qx "$vm"; then
    limactl delete --force "$vm"
  fi
}

vm_shell() {
  limactl shell --workdir / "$vm" "$@"
}

ntp_synced() {
  [ "$(vm_shell timedatectl show -p NTPSynchronized --value)" = "yes" ]
}

wait_ntp() {
  local i
  for i in $(seq 1 24); do
    if ntp_synced; then
      return 0
    fi
    sleep 5
  done
  return 1
}

in_vm() {
  local vars=$1 cmd=$2
  vm_shell sudo env PATH="$vm_path" $vars bash -c "cd \"$repo\" && $cmd"
}

emulation_flag() {
  if vm_shell test -c /dev/kvm; then
    echo false
  else
    echo true
  fi
}

run_test() {
  case "$(helm version --short 2>/dev/null)" in
  v3.*) ;;
  *)
    echo "helm 3 is required" >&2
    return 1
    ;;
  esac
  make generate crds
  git diff --exit-code
  test -z "$(git status --porcelain)"
  make lint
  make test
  make release VERSION=ci
  hack/check-release.sh
  make rbac
  git diff --exit-code manifests/90-bedrock/operator-rbac.yaml
}

run_e2e_kind() {
  ensure_images
  KUBECONFIG="$tmp/kubeconfig" make e2e-kind
  remove_job_images
}

run_e2e_bundle() {
  ensure_images
  registry_up
  make build release binaries VERSION=dev IMAGE="localhost:$port/bedrock:dev" PIN_DIGESTS=1
  retry 3 docker build -t "localhost:$port/bedrock:dev" -f Containerfile .
  retry 3 docker push "localhost:$port/bedrock:dev"
  make bundle VERSION=dev IMAGE="localhost:$port/bedrock:dev" PIN_DIGESTS=1 BUNDLE_ARCH="$arch"
  registry_down
  remove_job_images
  vm_up
  in_vm "KUBECONFIG=/var/lib/k0s/pki/admin.conf BUNDLE=dist/bedrock-dev-bundle-$arch.tar.zst IMAGE=localhost:$port/bedrock:dev BIN=dist/bedrock-dev-linux-$arch ARCH=$arch EMULATION=$(emulation_flag)" hack/e2e-init.sh
  rm -f "dist/bedrock-dev-bundle-$arch.tar.zst"
  vm_down
}

run_e2e_authn() {
  ensure_images
  registry_up
  make build release binaries VERSION=dev IMAGE="localhost:$port/bedrock:dev" PIN_DIGESTS=1
  retry 3 docker build -t "localhost:$port/bedrock:dev" -f Containerfile .
  retry 3 docker push "localhost:$port/bedrock:dev"
  make bundle VERSION=dev IMAGE="localhost:$port/bedrock:dev" PIN_DIGESTS=1 BUNDLE_ARCH="$arch"
  registry_down
  remove_job_images
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go test -c -tags e2e -o "dist/e2e-authn-$arch.test" ./test/e2e/authn
  ARCH="$arch" hack/e2e-authn-images.sh
  vm_up
  in_vm "KUBECONFIG=/var/lib/k0s/pki/admin.conf BUNDLE=dist/bedrock-dev-bundle-$arch.tar.zst IMAGE=localhost:$port/bedrock:dev BIN=dist/bedrock-dev-linux-$arch ARCH=$arch EMULATION=$(emulation_flag)" hack/e2e-init.sh
  in_vm "BEDROCK_BIN=dist/bedrock-dev-linux-$arch E2E_TEST=dist/e2e-authn-$arch.test" hack/e2e-authn.sh
  rm -f "dist/bedrock-dev-bundle-$arch.tar.zst"
  vm_down
}

run_upgrade() {
  local job_env=$1
  ensure_images
  registry_up
  env $upgrade_env hack/e2e-upgrade-build.sh
  registry_down
  remove_job_images
  vm_up
  if ! ntp_synced; then
    echo "the VM clock is not NTP synchronized; the upgrade Preflight would block" >&2
    return 1
  fi
  in_vm "$upgrade_env $job_env EMULATION=$(emulation_flag)" hack/e2e-upgrade.sh
  rm -f dist/bedrock-*-bundle-*.tar.zst
  vm_down
}

run_e2e_upgrade() { run_upgrade ""; }
run_e2e_upgrade_abort() { run_upgrade ABORT_IN=ControlPlane; }

cache_size() {
  if [ -d dist/cache ]; then
    du -sh dist/cache | awk '{print $1}'
  else
    echo 0B
  fi
}

cleanup() {
  if [ -n "$job_pid" ]; then
    kill -TERM "-$job_pid" 2>/dev/null || true
  fi
  if [ -n "$watcher_pid" ]; then
    kill -TERM "-$watcher_pid" 2>/dev/null || true
  fi
  if [ -n "$job_pid" ]; then
    wait "$job_pid" 2>/dev/null || true
  fi
  if [ -n "$watcher_pid" ]; then
    wait "$watcher_pid" 2>/dev/null || true
  fi
  vm_down
  registry_down
  remove_job_images
  rm -f dist/bedrock-*-bundle-*.tar.zst
  rm -rf dist/.bundle-*
  rm -rf "$tmp"
  printf '%s' "$summary"
  local i=$ran
  while [ "$i" -lt "${#job_order[@]}" ]; do
    echo "${job_order[$i]}: SKIP (0m)"
    i=$((i + 1))
  done
  echo "cache: $(cache_size) in dist/cache"
}
tmp=$(mktemp -d)
trap cleanup EXIT

stop=0
rc=0
for job in "${job_order[@]}"; do
  if [ "$stop" -eq 1 ]; then
    continue
  fi
  ran=$((ran + 1))
  start=$(date +%s)
  rm -f "$tmp/floor-stop"
  set -m
  (
    set -e
    guard
    "run_${job//-/_}"
  ) </dev/null &
  job_pid=$!
  watch_floor "$job" "$job_pid" &
  watcher_pid=$!
  set +m
  set +e
  wait "$job_pid"
  code=$?
  set -e
  kill -TERM "-$watcher_pid" 2>/dev/null || true
  wait "$watcher_pid" 2>/dev/null || true
  job_pid=""
  watcher_pid=""
  if [ "$code" -eq 0 ] && [ ! -f "$tmp/floor-stop" ]; then
    status=PASS
  else
    status=FAIL
    stop=1
    rc=1
  fi
  minutes=$(( ($(date +%s) - start) / 60 ))
  printf -v line '%s: %s (%sm)\n' "$job" "$status" "$minutes"
  summary="${summary}${line}"
done

exit "$rc"
