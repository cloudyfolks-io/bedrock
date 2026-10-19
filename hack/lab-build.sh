#!/usr/bin/env bash
set -euo pipefail

ns=${NS:-acme-web}
registry_address=${LAB_REGISTRY_ADDRESS:-10.100.0.220}
git_address=${LAB_GIT_ADDRESS:-10.100.0.221}
registry_port=5000
registry_ref=$registry_address:$registry_port
work=${LAB_BUILD_DIR:-/var/lib/bedrock/lab-build}
crane=${CRANE:-crane}
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
platform=linux/$arch
git_image=alpine/git:v2.54.0
buildkit_image=moby/buildkit:v0.31.2
builder_image=paketobuildpacks/builder-jammy-base:0.4.640
serve_images=(docker.io/library/registry:3 docker.io/library/nginx:1.29-alpine docker.io/library/busybox:1.37.0)
trusted_images=("$git_image lab.local/git:lab build.git-image" "$buildkit_image lab.local/buildkit:lab build.buildkit-image" "$builder_image lab.local/builder:lab build.builder-image")
git_token_file=$work/git-token
export KUBECONFIG=${KUBECONFIG:-/var/lib/k0s/pki/admin.conf}

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source "$repo/hack/e2e/lib.sh"

usage() {
  cat <<USAGE
usage: hack/lab-build.sh <stage>

stages, in order:
  prepare     make a lab CA, add it to the build images, load every image on this node, write two git repositories
  serve       start a registry and a git server as Containers of the project, with fixed addresses
  dockerfile  run a Dockerfile build, check that BuildKit cannot read the git token, read the logs, check the pushed image
  buildpacks  run a buildpacks build and check the pushed image
  cancel      delete a running build and check that the Job, the pod and the ConfigMap are gone
  down        remove the lab objects and reset the Settings
  all         prepare, serve, dockerfile, buildpacks and cancel; run down after

The lab runs as root on a node of a Bedrock cluster with KVM and an enabled kata addon.
The project namespace (NS, default acme-web) needs the default Subnet and a route to the internet.
The node needs crane, openssl, git, curl and k0s. The Settings build.git-image, build.buildkit-image
and build.builder-image are set to images with the lab CA until the stage down.
LAB_API_TOKEN, an API token of a project member, is required: the stage dockerfile reads the logs through the proxy.
USAGE
}

log() {
  printf '== %s\n' "$*"
}

make_certificates() {
  mkdir -p "$work"
  openssl req -x509 -newkey rsa:2048 -nodes -keyout "$work/ca.key" -out "$work/ca.crt" -subj /CN=lab-build-ca -days 3
  openssl req -newkey rsa:2048 -nodes -keyout "$work/tls.key" -out "$work/tls.csr" -subj /CN=lab-build
  printf 'subjectAltName=IP:%s,IP:%s\n' "$registry_address" "$git_address" >"$work/san.ext"
  openssl x509 -req -in "$work/tls.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" -CAcreateserial -out "$work/tls.crt" -days 3 -extfile "$work/san.ext"
  printf 'lab-git-token-%s' "$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')" >"$git_token_file"
}

import_image() {
  k0s ctr --namespace k8s.io images import "$1"
}

load_image() {
  local image=$1 file
  file=$work/$(basename "${image%%:*}").tar
  "$crane" pull --platform "$platform" "$image" "$file"
  import_image "$file"
}

trust_image() {
  local base=$1 target=$2 name layer
  name=$(basename "${target%%:*}")
  layer=$work/layer-$name
  rm -rf "$layer"
  mkdir -p "$layer/etc/ssl/certs"
  "$crane" export --platform "$platform" "$base" - | tar -xO etc/ssl/certs/ca-certificates.crt >"$layer/etc/ssl/certs/ca-certificates.crt"
  cat "$work/ca.crt" >>"$layer/etc/ssl/certs/ca-certificates.crt"
  tar -C "$layer" -cf "$layer.tar" etc
  "$crane" mutate "$base" --platform "$platform" --append "$layer.tar" --tag "$target" --output "$work/$name.tar"
  import_image "$work/$name.tar"
}

set_setting() {
  kubectl patch setting "$1" --type merge -p "{\"spec\":{\"value\":\"$2\"}}"
}

write_repositories() {
  local source=$work/source bare=$work/repos
  rm -rf "$source" "$bare"
  mkdir -p "$source/dockerfile" "$source/pack" "$bare"
  printf 'FROM docker.io/library/busybox:1.37.0\nRUN sleep 60\nRUN echo lab-built >/built\nCMD ["cat", "/built"]\n' >"$source/dockerfile/Dockerfile"
  printf 'FROM docker.io/library/busybox:1.37.0\nRUN sleep 900\n' >"$source/dockerfile/Dockerfile.slow"
  printf 'module lab\n\ngo 1.22\n' >"$source/pack/go.mod"
  printf 'package main\n\nimport "fmt"\n\nfunc main() { fmt.Println("lab-pack") }\n' >"$source/pack/main.go"
  for name in dockerfile pack; do
    git -C "$source/$name" init -q -b main
    git -C "$source/$name" add .
    git -C "$source/$name" -c user.name=lab -c user.email=lab@example.test commit -q -m lab
    git clone -q --bare "$source/$name" "$bare/$name.git"
    git -C "$bare/$name.git" update-server-info
    git -C "$source/$name" rev-parse HEAD >"$work/$name.revision"
  done
  tar -C "$bare" -czf "$work/repos.tgz" .
  base64 -w0 "$work/repos.tgz" >"$work/repos.b64"
}

write_start_scripts() {
  cat >"$work/registry-start.sh" <<'START'
printf '%s' "$TLS_CRT" >/tmp/tls.crt
printf '%s' "$TLS_KEY" >/tmp/tls.key
exec registry serve /etc/distribution/config.yml
START
  cat >"$work/git-start.sh" <<'START'
printf '%s' "$TLS_CRT" >/tmp/tls.crt
printf '%s' "$TLS_KEY" >/tmp/tls.key
mkdir -p /srv/git
printf '%s' "$REPOS" | base64 -d | tar -xzf - -C /srv/git
printf 'server { listen 443 ssl; ssl_certificate /tmp/tls.crt; ssl_certificate_key /tmp/tls.key; root /srv/git; }\n' >/etc/nginx/conf.d/default.conf
exec nginx -g 'daemon off;'
START
}

stage_prepare() {
  local entry base target setting
  log "making the lab CA and the git repositories"
  make_certificates
  write_repositories
  write_start_scripts
  for image in "${serve_images[@]}"; do
    load_image "$image"
  done
  for entry in "${trusted_images[@]}"; do
    read -r base target setting <<<"$entry"
    log "adding the lab CA to $base"
    trust_image "$base" "$target"
    set_setting "$setting" "$target"
  done
}

serve_container() {
  local name=$1 address=$2 image=$3 start=$4 extra_env=$5
  kubectl -n "$ns" apply -f - <<CONTAINER
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Container
metadata:
  name: $name
spec:
  image: $image
  command: ["sh", "-c", "eval \"\$START\""]
  replicas: 1
  strategy: Recreate
  env:
    - name: START
      valueFrom:
        secretKeyRef: {name: lab-serve, key: $start}
    - name: TLS_CRT
      valueFrom:
        secretKeyRef: {name: lab-serve, key: tls.crt}
    - name: TLS_KEY
      valueFrom:
        secretKeyRef: {name: lab-serve, key: tls.key}
$extra_env
  network:
    subnetRef: default
    address: $address
CONTAINER
}

stage_serve() {
  kubectl -n "$ns" create secret generic lab-serve \
    --from-file=tls.crt="$work/tls.crt" --from-file=tls.key="$work/tls.key" \
    --from-file=registry-start.sh="$work/registry-start.sh" --from-file=git-start.sh="$work/git-start.sh" \
    --from-file=repos="$work/repos.b64" --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n "$ns" create secret docker-registry lab-push --docker-server="$registry_ref" --docker-username=robot --docker-password=pushword --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n "$ns" create secret generic lab-git --from-literal=username=robot --from-file=token="$git_token_file" --dry-run=client -o yaml | kubectl apply -f -
  serve_container lab-registry "$registry_address" docker.io/library/registry:3 registry-start.sh "    - name: REGISTRY_HTTP_TLS_CERTIFICATE
      value: /tmp/tls.crt
    - name: REGISTRY_HTTP_TLS_KEY
      value: /tmp/tls.key"
  serve_container lab-git "$git_address" docker.io/library/nginx:1.29-alpine git-start.sh "    - name: REPOS
      valueFrom:
        secretKeyRef: {name: lab-serve, key: repos}"
  wait_jsonpath 600 Running -n "$ns" get container lab-registry -o jsonpath='{.status.phase}'
  wait_jsonpath 600 Running -n "$ns" get container lab-git -o jsonpath='{.status.phase}'
}

build_manifest() {
  local name=$1 type=$2 repository=$3 dockerfile=$4 image=$5
  cat <<BUILD
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Build
metadata:
  name: $name
spec:
  type: $type
  source:
    git:
      url: https://$git_address/$repository.git
      revision: $(cat "$work/$repository.revision")
      secretRef: lab-git
  dockerfilePath: $dockerfile
  output:
    image: $registry_ref/$image
    pushSecretRef: lab-push
  cache:
    enabled: true
  network:
    subnetRef: default
BUILD
}

build_pod() {
  kubectl -n "$ns" get pods -l "bedrock.cloudyfolks.io/kind=Build,bedrock.cloudyfolks.io/name=$1" -o jsonpath='{.items[0].metadata.name}'
}

container_running() {
  [ -n "$(kubectl -n "$ns" get pod "$1" -o jsonpath="{.status.containerStatuses[?(@.name==\"$2\")].state.running}" 2>/dev/null)" ]
}

registry_get() {
  kubectl -n "$ns" exec "deploy/lab-registry" -- wget --no-check-certificate -q -O- --header 'Accept: application/vnd.oci.image.manifest.v1+json' "https://127.0.0.1:$registry_port/v2/$1"
}

expect_image_pushed() {
  expect_contains "$(registry_get "$1/tags/list")" "\"$2\"" "tag $2 of $1 in the registry"
  expect_contains "$(registry_get "$1/manifests/$2")" '"layers"' "manifest of $1:$2"
}

expect_token_hidden() {
  local pod=$1 token seen
  token=$(cat "$git_token_file")
  retry_until 600 container_running "$pod" build
  seen=$(kubectl -n "$ns" exec "$pod" -c build -- sh -c 'env; cat /proc/*/environ 2>/dev/null | tr "\0" "\n"; ls -a /docker-config; cat /docker-config/config.json')
  if [[ "$seen" == *"$token"* ]]; then
    echo "the BuildKit container can read the git token" >&2
    return 1
  fi
  expect_contains "$seen" "DOCKER_CONFIG=/docker-config" "push credential path in the BuildKit container"
  expect_contains "$seen" "config.json" "push credential file in the BuildKit container"
  if [[ "$(kubectl -n "$ns" get pod "$pod" -o yaml)" == *"$token"* ]]; then
    echo "the pod holds the git token" >&2
    return 1
  fi
}

build_logs() {
  local host vip
  : "${LAB_API_TOKEN:?LAB_API_TOKEN is required to read the logs through the proxy}"
  host=$(kubectl get setting platform.host -o jsonpath='{.spec.value}')
  vip=$(kubectl get cluster cluster -o jsonpath='{.spec.api.vip}')
  kubectl -n cert-manager get secret bedrock-ca -o jsonpath='{.data.ca\.crt}' | base64 -d >"$work/platform-ca.crt"
  curl -fsS --cacert "$work/platform-ca.crt" --resolve "api.$host:443:$vip" -H "Authorization: Bearer $LAB_API_TOKEN" \
    "https://api.$host/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/$ns/builds/$1/logs"
}

expect_logs() {
  local logs
  logs=$(build_logs "$1")
  expect_contains "$logs" "==> git-clone <==" "clone first in the logs of $1"
  expect_contains "$logs" "Cloning into" "clone output in the logs of $1"
  expect_contains "$logs" "$2" "build output in the logs of $1"
}

stage_dockerfile() {
  build_manifest lab-docker dockerfile dockerfile Dockerfile lab/docker:1 | kubectl -n "$ns" apply -f -
  wait_jsonpath 600 Running -n "$ns" get build lab-docker -o jsonpath='{.status.phase}'
  expect_token_hidden "$(build_pod lab-docker)"
  wait_jsonpath 1200 Succeeded -n "$ns" get build lab-docker -o jsonpath='{.status.phase}'
  expect_equal "$registry_ref/lab/docker:1" "$(kubectl -n "$ns" get build lab-docker -o jsonpath='{.status.image}')" "image in the status"
  expect_image_pushed lab/docker 1
  expect_image_pushed lab/docker cache
  expect_logs lab-docker "exporting to image"
}

stage_buildpacks() {
  build_manifest lab-pack buildpacks pack Dockerfile lab/pack:1 | kubectl -n "$ns" apply -f -
  wait_jsonpath 2400 Succeeded -n "$ns" get build lab-pack -o jsonpath='{.status.phase}'
  expect_image_pushed lab/pack 1
  expect_logs lab-pack "Paketo"
}

build_children_gone() {
  [ -z "$(kubectl -n "$ns" get jobs,pods,configmaps -l "bedrock.cloudyfolks.io/kind=Build,bedrock.cloudyfolks.io/name=$1" -o name)" ]
}

stage_cancel() {
  local pod
  set_setting build.registry-mirrors "docker.io=$registry_ref/mirror"
  build_manifest lab-cancel dockerfile dockerfile Dockerfile.slow lab/cancel:1 | kubectl -n "$ns" apply -f -
  wait_jsonpath 600 Running -n "$ns" get build lab-cancel -o jsonpath='{.status.phase}'
  pod=$(build_pod lab-cancel)
  kubectl -n "$ns" get configmap lab-cancel-buildkitd
  kubectl -n "$ns" delete build lab-cancel
  retry_until 180 build_children_gone lab-cancel
  wait_gone 60 pod "$pod" -n "$ns"
  expect_equal "" "$(kubectl -n "$ns" get secrets -l bedrock.cloudyfolks.io/kind=Build -o name)" "no secret left by a build"
  set_setting build.registry-mirrors ""
}

stage_down() {
  kubectl -n "$ns" delete build --all --ignore-not-found
  kubectl -n "$ns" delete container lab-registry lab-git --ignore-not-found
  kubectl -n "$ns" delete secret lab-serve lab-push lab-git --ignore-not-found
  for setting in build.git-image build.buildkit-image build.builder-image build.registry-mirrors; do
    set_setting "$setting" ""
  done
  rm -rf "$work"
}

stage=${1:-}
case "$stage" in
  prepare) stage_prepare ;;
  serve) stage_serve ;;
  dockerfile) stage_dockerfile ;;
  buildpacks) stage_buildpacks ;;
  cancel) stage_cancel ;;
  down) stage_down ;;
  all)
    stage_prepare
    stage_serve
    stage_dockerfile
    stage_buildpacks
    stage_cancel
    ;;
  *)
    usage
    exit 2
    ;;
esac
