# Tools and test assets. Override LOCALBIN and ENVTEST_ASSETS to reuse
# binaries that are already installed elsewhere.
LOCALBIN ?= $(CURDIR)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
SETUP_ENVTEST ?= $(LOCALBIN)/setup-envtest
ENVTEST_K8S_VERSION ?= 1.37.0
ENVTEST_ASSETS ?= $(shell $(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)/envtest -p path)

GOTESTFLAGS ?= ./...
VERSION ?= dev
IMG ?= ghcr.io/mudler/agent-controller:$(VERSION)

.PHONY: all
all: generate manifests fmt vet build

.PHONY: generate
generate: ## Generate DeepCopy methods.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: manifests
manifests: ## Generate CRDs and RBAC.
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=agent-controller-controller paths="./internal/controller/..." output:rbac:dir=config/rbac/controller
	$(CONTROLLER_GEN) rbac:roleName=agent-controller-server paths="./internal/server/..." output:rbac:dir=config/rbac/server

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test:
	KUBEBUILDER_ASSETS="$(ENVTEST_ASSETS)" go test -race -count=1 $(GOTESTFLAGS)

.PHONY: build
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(LOCALBIN)/agent-controller ./cmd/agent-controller

.PHONY: docker-build
docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMG) .
