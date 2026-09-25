#!/usr/bin/env bash
set -euo pipefail

registry=${REGISTRY:-localhost:5000}
arch=${ARCH:-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')}

build() {
  local version=$1 k0s=$2 from=$3
  local image=$registry/bedrock:$version
  make build release VERSION="$version" IMAGE="$image" PIN_DIGESTS=1 K0S_VERSION="$k0s" UPGRADE_FROM="$from"
  docker build --build-arg VERSION="$version" -t "$image" -f Containerfile .
  docker push "$image"
  make bundle VERSION="$version" IMAGE="$image" PIN_DIGESTS=1 K0S_VERSION="$k0s" UPGRADE_FROM="$from" BUNDLE_ARCH="$arch"
  rm -rf "dist/release-$version"
  cp -r dist/release "dist/release-$version"
}

build "$VERSION_A" "$K0S_A" ""
build "$VERSION_B" "$K0S_B" "$VERSION_A"
ls -l dist/bedrock-*-bundle-"$arch".tar.zst dist/bedrock-*-linux-"$arch"
