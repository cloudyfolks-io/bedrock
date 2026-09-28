#!/usr/bin/env bash
set -euo pipefail

name=${LIMA_NAME:-bedrock}
version=${VERSION:-dev}

if ! limactl list --format '{{.Name}}' | grep -qx "$name"; then
  limactl start --name "$name" --tty=false --cpus 4 --memory 8 --disk 60 template://ubuntu-24.04
fi
GOOS=linux GOARCH=$(limactl shell "$name" uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/') CGO_ENABLED=0 go build -trimpath -ldflags "-X github.com/cloudyfolks-io/bedrock/internal/cli.Version=$version" -o bin/bedrock-linux ./cmd/bedrock
limactl shell "$name" sudo mkdir -p /opt/bedrock/bin /opt/bedrock/dist /opt/bedrock/hack
limactl copy bin/bedrock-linux "$name:/tmp/bedrock"
limactl copy -r dist/release "$name:/tmp/release"
limactl copy -r hack "$name:/tmp/hack"
limactl shell "$name" sudo bash -c 'install -m 0755 /tmp/bedrock /opt/bedrock/bin/bedrock && rm -rf /opt/bedrock/dist/release && mv /tmp/release /opt/bedrock/dist/release && rm -rf /opt/bedrock/hack && mv /tmp/hack /opt/bedrock/hack && apt-get install -y -qq gettext-base docker.io >/dev/null'
docker save "ghcr.io/cloudyfolks-labs/bedrock:$version" -o /tmp/bedrock-image.tar
limactl copy /tmp/bedrock-image.tar "$name:/tmp/bedrock-image.tar"
limactl shell "$name" sudo docker load -i /tmp/bedrock-image.tar
limactl shell "$name" sudo bash -c "cd /opt/bedrock && VERSION=$version PATH=/opt/bedrock/bin:\$PATH bash hack/e2e-init.sh"
