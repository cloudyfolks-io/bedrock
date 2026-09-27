#!/usr/bin/env bash
set -euo pipefail

profile=${CI_PROFILE:-bedrock-ci}
lima_home=${COLIMA_HOME:-$HOME/.colima}/_lima
cpus=${CI_CPUS:-6}
memory=${CI_MEMORY:-11}
disk=${CI_DISK:-16}
root_disk=${CI_ROOT_DISK:-44}
upgrade_root_disk=${CI_UPGRADE_ROOT_DISK:-64}
nested=${CI_NESTED_VIRT:-true}
min_free=${CI_MIN_FREE_GB:-25}
floor=${CI_FLOOR_GB:-15}
port=${CI_REGISTRY_PORT:-5001}
repo=$(git rev-parse --show-toplevel)
image_cache=$repo/dist/cache/images
base_images=(golang:1.27 gcr.io/distroless/static:nonroot registry:3)
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
previous_context=$(docker context show 2>/dev/null || true)
docker_host="unix://$HOME/.colima/$profile/docker.sock"
mac_env=("DOCKER_HOST=$docker_host")
proxy_http=${HTTP_PROXY:-${http_proxy:-${HTTPS_PROXY:-${https_proxy:-}}}}
proxy_https=${HTTPS_PROXY:-${https_proxy:-${HTTP_PROXY:-${http_proxy:-}}}}
vm_http=""
vm_https=""
vm_no_proxy=""
known_jobs=(test e2e-kind e2e-init e2e-bundle e2e-upgrade e2e-upgrade-abort)
default_jobs=(test e2e-kind e2e-bundle e2e-upgrade e2e-upgrade-abort)

if [ "$#" -eq 0 ]; then
  set -- "${default_jobs[@]}"
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

job_order=("$@")
summary=""
ran=0
job_pid=""
watcher_pid=""

cd "$repo"

watch_floor() {
  local job=$1 pid=$2 free
  while sleep 10 && kill -0 "$pid" 2>/dev/null; do
    free=$(df -Pk "$repo" | awk 'NR==2 {print int($4/1048576)}')
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
  free=$(df -Pk "$repo" | awk 'NR==2 {print int($4/1048576)}')
  if [ "$free" -lt "$min_free" ]; then
    echo "only ${free}GiB free on $repo, need ${min_free}GiB" >&2
    return 1
  fi
}

restore_context() {
  if [ -n "$previous_context" ]; then
    docker context use "$previous_context" >/dev/null 2>&1 || true
  fi
}

retry() {
  local attempts=$1 i code
  shift
  for i in $(seq 1 "$attempts"); do
    "$@" && return 0
    code=$?
    sleep 2
  done
  return "$code"
}

image_cache_file() {
  printf '%s' "$1" | sed 's/[^A-Za-z0-9._-]/_/g'
}

seed_image() {
  local image=$1 attempt=1
  local file="$image_cache/$(image_cache_file "$image").tar"
  if [ -f "$file" ]; then
    env "${mac_env[@]}" docker load -i "$file"
    return
  fi
  until env "${mac_env[@]}" docker pull "$image"; do
    if [ "$attempt" -ge 5 ]; then
      return 1
    fi
    sleep "$((attempt * 2))"
    attempt=$((attempt + 1))
  done
  mkdir -p "$image_cache"
  env "${mac_env[@]}" docker save -o "$file.part" "$image"
  mv "$file.part" "$file"
}

seed_images() {
  local image
  for image in "${base_images[@]}"; do
    seed_image "$image"
  done
}

vm_up() {
  vm_down
  colima start --profile "$profile" --vm-type vz --cpu "$cpus" --memory "$memory" --disk "$disk" --root-disk "$root_disk" --runtime docker --mount "$repo:w" --nested-virtualization="$nested"
  restore_context
  colima ssh --profile "$profile" -- sudo apt-get update -qq
  colima ssh --profile "$profile" -- sudo apt-get install -y -qq gettext-base iputils-ping pciutils
  colima ssh --profile "$profile" -- sudo sysctl -w fs.inotify.max_user_instances=8192 fs.inotify.max_user_watches=1048576 vm.max_map_count=262144
  colima ssh --profile "$profile" -- sudo tee /tmp/fix-resolv.sh >/dev/null <<'FIX_RESOLV'
#!/bin/sh
set -e
gw=$(ip route show default | awk '{for (i = 1; i <= NF; i++) if ($i == "via") print $(i + 1)}')
rm -f /etc/resolv.conf
printf 'nameserver %s\n' "$gw" > /etc/resolv.conf
systemctl restart systemd-timesyncd
FIX_RESOLV
  colima ssh --profile "$profile" -- sudo sh /tmp/fix-resolv.sh
  colima ssh --profile "$profile" -- sudo tee /usr/local/bin/kubectl >/dev/null <<'SCRIPT'
#!/bin/sh
exec /usr/local/bin/k0s kubectl "$@"
SCRIPT
  colima ssh --profile "$profile" -- sudo chmod +x /usr/local/bin/kubectl
  if ! wait_ntp; then
    echo "warning: the VM clock is not NTP synchronized" >&2
  fi
  if [ -n "$proxy_https" ]; then
    use_proxy
  fi
  seed_images
}

wait_ntp() {
  local i
  for i in $(seq 1 24); do
    if [ "$(colima ssh --profile "$profile" -- timedatectl show -p NTPSynchronized --value)" = "yes" ]; then
      return 0
    fi
    sleep 5
  done
  return 1
}

vm_url() {
  printf '%s' "$1" | sed "s#127\.0\.0\.1#$2#g;s#localhost#$2#g"
}

use_proxy() {
  local host vm_ip
  host=$(colima ssh --profile "$profile" -- getent ahostsv4 host.lima.internal | awk 'NR==1 {print $1}')
  vm_ip=$(colima ssh --profile "$profile" -- ip -4 route get 1.1.1.1 | awk '{for (i = 1; i < NF; i++) if ($i == "src") print $(i + 1)}')
  test -n "$host"
  test -n "$vm_ip"
  vm_http=$(vm_url "$proxy_http" "$host")
  vm_https=$(vm_url "$proxy_https" "$host")
  vm_no_proxy="localhost,127.0.0.1,$vm_ip,${vm_ip%.*}.0/24,.svc,.cluster.local,10.16.0.0/16,10.96.0.0/12,100.64.0.0/16"
  mkdir -p "$tmp/docker-config"
  printf '{"proxies":{"default":{"httpProxy":"%s","httpsProxy":"%s","noProxy":"localhost,127.0.0.1"}},"cliPluginsExtraDirs":["%s"]}\n' "$vm_http" "$vm_https" "$HOME/.docker/cli-plugins" > "$tmp/docker-config/config.json"
  mac_env+=("DOCKER_CONFIG=$tmp/docker-config")
}

k0s_proxy() {
  local unit
  for unit in k0scontroller k0sworker; do
    colima ssh --profile "$profile" -- sudo mkdir -p "/etc/systemd/system/$unit.service.d"
    printf '[Service]\nEnvironment=HTTP_PROXY=%s\nEnvironment=HTTPS_PROXY=%s\nEnvironment=NO_PROXY=%s\n' "$vm_http" "$vm_https" "$vm_no_proxy" | colima ssh --profile "$profile" -- sudo tee "/etc/systemd/system/$unit.service.d/proxy.conf" >/dev/null
  done
  colima ssh --profile "$profile" -- sudo systemctl daemon-reload
}

vm_down() {
  colima delete --profile "$profile" --force --data
  if [ -d "$lima_home/_disks/colima-$profile" ]; then
    LIMA_HOME="$lima_home" limactl disk delete --force "colima-$profile"
  fi
}

in_vm() {
  local vars=$1 cmd=$2 proxy=""
  if [ -n "$vm_https" ]; then
    proxy="http_proxy=$vm_http https_proxy=$vm_https no_proxy=$vm_no_proxy HTTP_PROXY=$vm_http HTTPS_PROXY=$vm_https NO_PROXY=$vm_no_proxy"
  fi
  colima ssh --profile "$profile" -- sudo env PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin $proxy $vars bash -c "cd '$repo' && $cmd"
}

emulation_flag() {
  if [ "$nested" = "true" ]; then
    echo false
  else
    echo true
  fi
}

warn_docker_desktop() {
  if pgrep -x com.docker.backend >/dev/null 2>&1; then
    echo "warning: Docker Desktop is running and will compete with the VM for RAM" >&2
  fi
}

wait_registry() {
  local i
  for i in $(seq 1 30); do
    curl -sf "http://localhost:$port/v2/" && break
    sleep 1
  done
  curl -sf "http://localhost:$port/v2/"
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
  vm_up
  env "${mac_env[@]}" KUBECONFIG="$tmp/kubeconfig" make e2e-kind
  vm_down
}

run_e2e_init() {
  vm_up
  if [ -n "$vm_https" ]; then
    k0s_proxy
  fi
  env "${mac_env[@]}" make build release binaries VERSION=dev
  retry 3 env "${mac_env[@]}" docker build -t ghcr.io/cloudyfolks-labs/bedrock:dev -f Containerfile .
  in_vm "VERSION=dev KUBECONFIG=/var/lib/k0s/pki/admin.conf BIN=dist/bedrock-dev-linux-$arch ARCH=$arch EMULATION=$(emulation_flag)" hack/e2e-init.sh
  vm_down
}

run_e2e_bundle() {
  vm_up
  env "${mac_env[@]}" docker run --rm -d -p "$port:5000" --name registry registry:3
  wait_registry
  env "${mac_env[@]}" make build release binaries VERSION=dev IMAGE=localhost:$port/bedrock:dev PIN_DIGESTS=1
  retry 3 env "${mac_env[@]}" docker build -t localhost:$port/bedrock:dev -f Containerfile .
  retry 3 env "${mac_env[@]}" docker push localhost:$port/bedrock:dev
  env "${mac_env[@]}" make bundle VERSION=dev IMAGE=localhost:$port/bedrock:dev PIN_DIGESTS=1 BUNDLE_ARCH=$arch
  env "${mac_env[@]}" docker rm -f registry
  in_vm "KUBECONFIG=/var/lib/k0s/pki/admin.conf BUNDLE=dist/bedrock-dev-bundle-$arch.tar.zst IMAGE=localhost:$port/bedrock:dev BIN=dist/bedrock-dev-linux-$arch ARCH=$arch EMULATION=$(emulation_flag)" hack/e2e-init.sh
  rm -f "dist/bedrock-dev-bundle-$arch.tar.zst"
  vm_down
}

run_upgrade() {
  local abort_in=$1 root_disk=$upgrade_root_disk
  vm_up
  if [ "$(colima ssh --profile "$profile" -- timedatectl show -p NTPSynchronized --value)" != "yes" ]; then
    echo "the VM clock is not NTP synchronized; the upgrade Preflight would block" >&2
    return 1
  fi
  env "${mac_env[@]}" docker run --rm -d -p "$port:5000" --name registry registry:3
  wait_registry
  env "${mac_env[@]}" REGISTRY=localhost:$port ARCH=$arch VERSION_A=v0.0.0-e2e.1 VERSION_B=v0.0.0-e2e.2 K0S_A=v1.36.2+k0s.0 K0S_B=v1.36.3+k0s.0 hack/e2e-upgrade-build.sh
  env "${mac_env[@]}" docker rm -f registry
  env "${mac_env[@]}" docker system prune -af
  colima ssh --profile "$profile" -- sudo fstrim --all --verbose || echo "warning: fstrim failed" >&2
  in_vm "REGISTRY=localhost:$port ARCH=$arch VERSION_A=v0.0.0-e2e.1 VERSION_B=v0.0.0-e2e.2 K0S_A=v1.36.2+k0s.0 K0S_B=v1.36.3+k0s.0 EMULATION=$(emulation_flag)${abort_in:+ ABORT_IN=$abort_in}" hack/e2e-upgrade.sh
  rm -f dist/bedrock-*-bundle-*.tar.zst
  vm_down
}

run_e2e_upgrade() { run_upgrade ""; }
run_e2e_upgrade_abort() { run_upgrade ControlPlane; }

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
  rm -f dist/bedrock-*-bundle-*.tar.zst
  rm -rf dist/.bundle-*
  restore_context
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

warn_docker_desktop

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
