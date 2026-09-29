GO ?= go
CONTROLLER_GEN ?= $(shell $(GO) env GOPATH)/bin/controller-gen
CONTROLLER_GEN_VERSION ?= v0.20.0
ENVTEST ?= $(shell $(GO) env GOPATH)/bin/setup-envtest
ENVTEST_VERSION ?= release-0.24
ENVTEST_K8S_VERSION ?= 1.36.x
BIN ?= bin/bedrock
VERSION ?= dev
IMAGE ?= ghcr.io/cloudyfolks-io/bedrock:$(VERSION)
HELM ?= helm
CRANE ?= $(shell $(GO) env GOPATH)/bin/crane
CRANE_VERSION ?= v0.22.1
RELEASE_CONFIG ?= release/components.yaml
PIN_DIGESTS ?=
BUNDLE_ARCH ?= amd64
K0S_VERSION ?=
UPGRADE_FROM ?=

.PHONY: build test lint generate crds envtest-assets e2e-kind e2e-init e2e-bundle controller-gen release crane bundle binaries rbac local-ci ui

ui:
	cd ui && pnpm install --frozen-lockfile && pnpm -r test && pnpm -r typecheck && pnpm --filter login build
	touch internal/authn/server/ui/dist/.keep

build: ui
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-X github.com/cloudyfolks-io/bedrock/internal/cli.Version=$(VERSION)" -o $(BIN) ./cmd/bedrock

binaries: ui
	for arch in amd64 arm64; do CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags "-X github.com/cloudyfolks-io/bedrock/internal/cli.Version=$(VERSION)" -o dist/bedrock-$(VERSION)-linux-$$arch ./cmd/bedrock; done

test: envtest-assets
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" $(GO) test ./... -count=1 -timeout 30m

lint:
	$(GO) vet ./...
	test -z "$$(gofmt -l .)"

controller-gen:
	@test -x $(CONTROLLER_GEN) || $(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

generate: controller-gen
	$(CONTROLLER_GEN) object paths=./api/...

crds: controller-gen
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:artifacts:config=manifests/00-crds

envtest-assets:
	@test -x $(ENVTEST) || $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path >/dev/null

crane:
	@test -x $(CRANE) || $(GO) install github.com/google/go-containerregistry/cmd/crane@$(CRANE_VERSION)

e2e-kind: build crds release crane
	CRANE=$(CRANE) hack/e2e-kind.sh

e2e-init: build release
	hack/e2e-init.sh

release: build binaries
	@command -v $(HELM) >/dev/null || { echo "helm is required"; exit 1; }
	$(BIN) release build --config $(RELEASE_CONFIG) --version $(VERSION) --image $(IMAGE) --out dist/release --helm $(HELM) --cache-dir dist/cache --binaries dist $(if $(PIN_DIGESTS),--pin-digests,) $(if $(K0S_VERSION),--k0s-version $(K0S_VERSION),) $(if $(UPGRADE_FROM),--upgrade-from $(UPGRADE_FROM),)

rbac: build
	$(BIN) release rbac --release dist/release --out manifests/90-bedrock/operator-rbac.yaml

bundle: build release
	$(BIN) bundle build --release dist/release --arch $(BUNDLE_ARCH) --out dist/bedrock-$(VERSION)-bundle-$(BUNDLE_ARCH).tar.zst --cache-dir dist/cache --bedrock-binary dist/bedrock-$(VERSION)-linux-$(BUNDLE_ARCH)

e2e-bundle: bundle
	BUNDLE=dist/bedrock-$(VERSION)-bundle-$(BUNDLE_ARCH).tar.zst hack/e2e-init.sh

local-ci:
	hack/local-ci.sh $(JOBS)
