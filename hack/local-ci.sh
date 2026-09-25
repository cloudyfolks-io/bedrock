#!/usr/bin/env bash
set -euo pipefail

profile=${CI_PROFILE:-bedrock-ci}
cpus=${CI_CPUS:-6}
memory=${CI_MEMORY:-11}
disk=${CI_DISK:-60}
min_free=${CI_MIN_FREE_GB:-25}
port=${CI_REGISTRY_PORT:-5001}
repo=$(git rev-parse --show-toplevel)
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
known_jobs=(test e2e-kind e2e-init e2e-bundle e2e-upgrade e2e-upgrade-abort)

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

job_order=("$@")
declare -A job_status
declare -A job_minutes
for job in "${job_order[@]}"; do
  job_status[$job]=SKIP
done

cd "$repo"

guard() {
  local free
  free=$(df -Pk "$repo" | awk 'NR==2 {print int($4/1048576)}')
  if [ "$free" -lt "$min_free" ]; then
    echo "only ${free}GiB free on $repo, need ${min_free}GiB" >&2
    return 1
  fi
}

profile_exists() {
  colima list 2>/dev/null | awk 'NR>1 {print $1}' | grep -qx "$profile"
}

vm_up() {
  colima start --profile "$profile" --vm-type vz --cpu "$cpus" --memory "$memory" --disk "$disk" --runtime docker --mount "$repo:w"
  colima ssh --profile "$profile" -- sudo apt-get update -qq
  colima ssh --profile "$profile" -- sudo apt-get install -y -qq gettext-base
  colima ssh --profile "$profile" -- sudo tee /usr/local/bin/kubectl >/dev/null <<'SCRIPT'
#!/bin/sh
exec /usr/local/bin/k0s kubectl "$@"
SCRIPT
  colima ssh --profile "$profile" -- sudo chmod +x /usr/local/bin/kubectl
}

vm_down() {
  colima delete --profile "$profile" --force
}

in_vm() {
  local vars=$1 cmd=$2
  colima ssh --profile "$profile" -- sudo env PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin $vars bash -c "cd '$repo' && $cmd"
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
  DOCKER_CONTEXT=colima-$profile make e2e-kind
  vm_down
}

run_e2e_init() {
  vm_up
  DOCKER_CONTEXT=colima-$profile make build release binaries VERSION=dev
  DOCKER_CONTEXT=colima-$profile docker build -t ghcr.io/cloudyfolks-labs/bedrock:dev -f Containerfile .
  in_vm "VERSION=dev KUBECONFIG=/var/lib/k0s/pki/admin.conf BIN=dist/bedrock-dev-linux-$arch ARCH=$arch" hack/e2e-init.sh
  vm_down
}

run_e2e_bundle() {
  vm_up
  DOCKER_CONTEXT=colima-$profile docker run --rm -d -p "$port:5000" --name registry registry:3
  wait_registry
  DOCKER_CONTEXT=colima-$profile make build release binaries VERSION=dev IMAGE=localhost:$port/bedrock:dev PIN_DIGESTS=1
  DOCKER_CONTEXT=colima-$profile docker build -t localhost:$port/bedrock:dev -f Containerfile .
  DOCKER_CONTEXT=colima-$profile docker push localhost:$port/bedrock:dev
  DOCKER_CONTEXT=colima-$profile make bundle VERSION=dev IMAGE=localhost:$port/bedrock:dev PIN_DIGESTS=1 BUNDLE_ARCH=$arch
  DOCKER_CONTEXT=colima-$profile docker rm -f registry
  in_vm "KUBECONFIG=/var/lib/k0s/pki/admin.conf BUNDLE=dist/bedrock-dev-bundle-$arch.tar.zst IMAGE=localhost:$port/bedrock:dev BIN=dist/bedrock-dev-linux-$arch ARCH=$arch" hack/e2e-init.sh
  rm -f "dist/bedrock-dev-bundle-$arch.tar.zst"
  vm_down
}

run_upgrade() {
  local abort_in=$1
  vm_up
  DOCKER_CONTEXT=colima-$profile docker run --rm -d -p "$port:5000" --name registry registry:3
  wait_registry
  DOCKER_CONTEXT=colima-$profile REGISTRY=localhost:$port ARCH=$arch VERSION_A=v0.0.0-e2e.1 VERSION_B=v0.0.0-e2e.2 K0S_A=v1.36.2+k0s.0 K0S_B=v1.36.3+k0s.0 hack/e2e-upgrade-build.sh
  DOCKER_CONTEXT=colima-$profile docker rm -f registry
  DOCKER_CONTEXT=colima-$profile docker system prune -af
  in_vm "REGISTRY=localhost:$port ARCH=$arch VERSION_A=v0.0.0-e2e.1 VERSION_B=v0.0.0-e2e.2 K0S_A=v1.36.2+k0s.0 K0S_B=v1.36.3+k0s.0${abort_in:+ ABORT_IN=$abort_in}" hack/e2e-upgrade.sh
  rm -f dist/bedrock-*-bundle-*.tar.zst
  vm_down
}

run_e2e_upgrade() { run_upgrade ""; }
run_e2e_upgrade_abort() { run_upgrade ControlPlane; }

cleanup() {
  if profile_exists; then
    vm_down
  fi
  rm -f dist/bedrock-*-bundle-*.tar.zst
  rm -rf dist/cache
  for job in "${job_order[@]}"; do
    echo "$job: ${job_status[$job]} (${job_minutes[$job]:-0}m)"
  done
}
trap cleanup EXIT

warn_docker_desktop

stop=0
rc=0
for job in "${job_order[@]}"; do
  if [ "$stop" -eq 1 ]; then
    continue
  fi
  start=$(date +%s)
  if ( guard && "run_${job//-/_}" ); then
    job_status[$job]=PASS
  else
    job_status[$job]=FAIL
    stop=1
    rc=1
  fi
  job_minutes[$job]=$(( ($(date +%s) - start) / 60 ))
done

exit "$rc"
