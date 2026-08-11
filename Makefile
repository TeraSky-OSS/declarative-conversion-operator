# Image URL to use for docker-build/docker-push.
MANAGER_IMG ?= ghcr.io/vrabbi/declarative-conversion-operator:latest
WEBHOOK_SERVER_IMG ?= ghcr.io/vrabbi/declarative-conversion-webhook-server:latest
CLI_IMG ?= ghcr.io/vrabbi/declarative-conversion-operator-cli:latest

LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
CONTROLLER_GEN_VERSION ?= v0.21.0
KUSTOMIZE ?= $(LOCALBIN)/kustomize
KUSTOMIZE_VERSION ?= v5.8.1

.PHONY: all
all: build

##@ Development

.PHONY: generate
generate: controller-gen ## Generate deepcopy code for api/v1alpha1.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: manifests
manifests: controller-gen ## Generate CRD and RBAC manifests into config/.
	# allowDangerousTypes=true: NumericScaleParams.Factor is a plain float64
	# scale factor (not a Kubernetes resource.Quantity), which is fine for a
	# human-authored config field — controller-gen otherwise refuses to
	# generate a CRD schema for any float type at all.
	$(CONTROLLER_GEN) crd:allowDangerousTypes=true paths="./api/..." output:crd:artifacts:config=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=manager-role paths="./..." output:rbac:artifacts:config=config/rbac

.PHONY: helm-sync
helm-sync: manifests ## Copy generated CRDs into the Helm chart's crds/ directory.
	cp config/crd/bases/*.yaml charts/declarative-conversion-operator/crds/

.PHONY: fmt
fmt: ## Run gofmt against code.
	gofmt -l -w .

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: generate manifests fmt vet ## Run unit tests.
	go test ./... -race -count=1

.PHONY: test-e2e
test-e2e: ## Run the real end-to-end test: kind + cert-manager + Crossplane + this operator, proving the conversion webhook works against a live apiserver. Requires docker, kind, kubectl, and helm on PATH. Set KEEP_CLUSTER=1 to skip teardown for debugging.
	./hack/e2e-test.sh

##@ Build

.PHONY: build
build: generate ## Build the manager, webhook-server, and convctl binaries.
	go build -o bin/manager ./cmd/manager
	go build -o bin/webhook-server ./cmd/webhook-server
	go build -o bin/convctl ./cmd/convctl

.PHONY: run
run: manifests generate fmt vet ## Run the manager locally against the current kubeconfig context.
	go run ./cmd/manager

##@ Docker

.PHONY: docker-build-manager
docker-build-manager: ## Build the manager image.
	docker build --build-arg COMPONENT=manager -t $(MANAGER_IMG) .

.PHONY: docker-build-webhook-server
docker-build-webhook-server: ## Build the webhook-server image.
	docker build --build-arg COMPONENT=webhook-server -t $(WEBHOOK_SERVER_IMG) .

.PHONY: docker-build-cli
docker-build-cli: ## Build the convctl CLI image.
	docker build --build-arg COMPONENT=convctl -t $(CLI_IMG) .

.PHONY: docker-build
docker-build: docker-build-manager docker-build-webhook-server docker-build-cli ## Build all three images.

##@ Deployment (kustomize dev-loop; the Helm chart under charts/ is the supported install path)

.PHONY: install
install: manifests kustomize ## Install CRDs into the current cluster.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the current cluster.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found=true -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy the operator to the current cluster.
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(MANAGER_IMG)
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy the operator from the current cluster.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=true -f -

##@ Helm

.PHONY: helm-lint
helm-lint: ## Lint the Helm chart.
	helm lint charts/declarative-conversion-operator

.PHONY: helm-template
helm-template: ## Render the Helm chart with default values.
	helm template declarative-conversion-operator charts/declarative-conversion-operator --namespace declarative-conversion-system

##@ Tooling

.PHONY: controller-gen
controller-gen: $(LOCALBIN) ## Download controller-gen if not present.
	test -s $(CONTROLLER_GEN) || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: kustomize
kustomize: $(LOCALBIN) ## Download kustomize if not present.
	test -s $(KUSTOMIZE) || GOBIN=$(LOCALBIN) go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-25s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
