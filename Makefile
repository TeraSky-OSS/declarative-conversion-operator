# Image URL to use for docker-build/docker-push.
MANAGER_IMG ?= ghcr.io/terasky-oss/declarative-conversion-operator:latest
WEBHOOK_SERVER_IMG ?= ghcr.io/terasky-oss/declarative-conversion-webhook-server:latest
CLI_IMG ?= ghcr.io/terasky-oss/declarative-conversion-operator-cli:latest

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

.PHONY: helm-upgrade-crds
helm-upgrade-crds: ## Diff this chart's CRDs against the current cluster (APPLY=1 to apply).
	./hack/upgrade-crds.sh --chart charts/declarative-conversion-operator $(if $(filter 1,$(APPLY)),--apply,)

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
test-e2e: ## Run the real end-to-end test: kind + cert-manager + Crossplane + this operator, both features enabled, proving the conversion webhook works against a live apiserver. Requires docker, kind, kubectl, and helm on PATH. Set KEEP_CLUSTER=1 to skip teardown for debugging.
	./hack/e2e-test.sh

.PHONY: test-e2e-crd-only
test-e2e-crd-only: ## Run the e2e test for the native-CRD-only deployment shape: no Crossplane installed, features.crossplane.enabled=false. Same prerequisites as test-e2e.
	./hack/e2e-test-crd-only.sh

.PHONY: test-e2e-crossplane-only
test-e2e-crossplane-only: ## Run the e2e test for the Crossplane-only deployment shape: features.nativeCRD.enabled=false. Same prerequisites as test-e2e.
	./hack/e2e-test-crossplane-only.sh

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

##@ Observability

PROMTOOL ?= $(LOCALBIN)/promtool
PROMTOOL_VERSION ?= 2.54.1

.PHONY: promtool
promtool: $(LOCALBIN) ## Download promtool into bin/ if not already on PATH or in LOCALBIN.
	@if command -v promtool >/dev/null 2>&1; then \
		echo "Using promtool from PATH: $$(command -v promtool)"; \
	elif [ -x "$(PROMTOOL)" ]; then \
		echo "Using $(PROMTOOL)"; \
	else \
		echo "Downloading promtool $(PROMTOOL_VERSION) to $(PROMTOOL)"; \
		OS=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
		ARCH=$$(uname -m); \
		case "$$ARCH" in x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; esac; \
		TMP=$$(mktemp -d); \
		URL="https://github.com/prometheus/prometheus/releases/download/v$(PROMTOOL_VERSION)/prometheus-$(PROMTOOL_VERSION).$${OS}-$${ARCH}.tar.gz"; \
		curl -fsSL "$$URL" | tar -xz -C "$$TMP"; \
		cp "$$TMP"/prometheus-$(PROMTOOL_VERSION).$${OS}-$${ARCH}/promtool "$(PROMTOOL)"; \
		chmod +x "$(PROMTOOL)"; \
		rm -rf "$$TMP"; \
	fi

.PHONY: test-prometheus
test-prometheus: promtool ## Run promtool unit tests for chart PrometheusRule alerts (hack/prometheus/).
	@if command -v promtool >/dev/null 2>&1; then \
		promtool test rules hack/prometheus/rules.test.yml; \
	else \
		$(PROMTOOL) test rules hack/prometheus/rules.test.yml; \
	fi

##@ Local Dev Environment

# Same shape as hack/e2e-common.sh's setup (kind + cert-manager + Crossplane
# + this operator's own Helm chart), but left running afterward for
# interactive use instead of being torn down by a test script. Also installs
# kube-prometheus-stack with anonymous Grafana and enables the chart's
# ServiceMonitors / PrometheusRules / dashboard ConfigMaps. A fixed image
# tag (rather than e2e's timestamped one) keeps `make dev-up` idempotent and
# safe to re-run after every code change: it rebuilds, reloads, and restarts
# the running pods so the new code actually takes effect.
DEV_CLUSTER_NAME ?= declarative-conversion-dev
DEV_NAMESPACE ?= declarative-conversion-system
DEV_RELEASE_NAME ?= declarative-conversion-operator
DEV_IMG_TAG ?= dev
DEV_MANAGER_IMG ?= ghcr.io/terasky-oss/declarative-conversion-operator:$(DEV_IMG_TAG)
DEV_WEBHOOK_IMG ?= ghcr.io/terasky-oss/declarative-conversion-webhook-server:$(DEV_IMG_TAG)
DEV_CERT_MANAGER_VERSION ?= v1.21.1
DEV_MONITORING_NAMESPACE ?= monitoring-system
DEV_MONITORING_RELEASE ?= monitoring
# Pin so re-runs stay reproducible; bump deliberately when upgrading the stack.
DEV_KUBE_PROM_STACK_VERSION ?= 88.3.0

.PHONY: dev-up
dev-up: ## Stand up (or refresh) a full local dev environment: kind + cert-manager + Crossplane + kube-prometheus-stack (anonymous Grafana) + this operator with metrics/dashboards enabled. Safe to re-run after code changes. Requires docker, kind, kubectl, and helm on PATH.
	@command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
	@command -v kind >/dev/null 2>&1 || { echo "kind is required (https://kind.sigs.k8s.io)" >&2; exit 1; }
	@command -v kubectl >/dev/null 2>&1 || { echo "kubectl is required" >&2; exit 1; }
	@command -v helm >/dev/null 2>&1 || { echo "helm is required" >&2; exit 1; }
	@if kind get clusters 2>/dev/null | grep -qx "$(DEV_CLUSTER_NAME)"; then \
		echo "==> Reusing existing kind cluster '$(DEV_CLUSTER_NAME)'"; \
	else \
		echo "==> Creating kind cluster '$(DEV_CLUSTER_NAME)'"; \
		kind create cluster --name $(DEV_CLUSTER_NAME); \
	fi
	kubectl config use-context kind-$(DEV_CLUSTER_NAME)
	@echo "==> Building manager and webhook-server images ($(DEV_IMG_TAG))"
	docker build --build-arg COMPONENT=manager -t $(DEV_MANAGER_IMG) .
	docker build --build-arg COMPONENT=webhook-server -t $(DEV_WEBHOOK_IMG) .
	@echo "==> Loading images into the kind cluster"
	kind load docker-image $(DEV_MANAGER_IMG) --name $(DEV_CLUSTER_NAME)
	kind load docker-image $(DEV_WEBHOOK_IMG) --name $(DEV_CLUSTER_NAME)
	@if kubectl get ns cert-manager >/dev/null 2>&1; then \
		echo "==> cert-manager namespace already exists, skipping install"; \
	else \
		echo "==> Installing cert-manager $(DEV_CERT_MANAGER_VERSION)"; \
		kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/$(DEV_CERT_MANAGER_VERSION)/cert-manager.yaml; \
	fi
	kubectl -n cert-manager wait --for=condition=Available --timeout=180s deployment --all
	@echo "==> Installing Crossplane"
	helm repo add crossplane-stable https://charts.crossplane.io/stable --force-update
	helm repo update crossplane-stable
	helm upgrade --install crossplane crossplane-stable/crossplane \
		--namespace crossplane-system --create-namespace \
		--wait --timeout 180s
	@echo "==> Installing kube-prometheus-stack (Prometheus + Grafana, anonymous auth)"
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update
	helm repo update prometheus-community
	helm upgrade --install $(DEV_MONITORING_RELEASE) prometheus-community/kube-prometheus-stack \
		--namespace $(DEV_MONITORING_NAMESPACE) --create-namespace \
		--version $(DEV_KUBE_PROM_STACK_VERSION) \
		--values hack/dev-monitoring-values.yaml \
		--wait --timeout 600s
	@echo "==> Installing/upgrading $(DEV_RELEASE_NAME) with the locally-built dev images + monitoring"
	helm upgrade --install $(DEV_RELEASE_NAME) charts/declarative-conversion-operator \
		--namespace $(DEV_NAMESPACE) --create-namespace \
		--set image.manager.tag=$(DEV_IMG_TAG) \
		--set image.webhookServer.tag=$(DEV_IMG_TAG) \
		--set image.pullPolicy=Never \
		--set metrics.serviceMonitor.enabled=true \
		--set metrics.prometheusRule.enabled=true \
		--set dashboards.enabled=true \
		--wait --timeout 180s
	@echo "==> Restarting pods so the freshly-loaded image content takes effect (tag is fixed across re-runs)"
	kubectl -n $(DEV_NAMESPACE) rollout restart deployment/$(DEV_RELEASE_NAME)-manager
	kubectl -n $(DEV_NAMESPACE) rollout status deployment/$(DEV_RELEASE_NAME)-manager --timeout=120s
	kubectl -n $(DEV_NAMESPACE) rollout restart deployment/default-webhook-server
	kubectl -n $(DEV_NAMESPACE) rollout status deployment/default-webhook-server --timeout=120s
	@echo "==> Waiting for the default ConversionWebhookServer to become Available"
	kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default
	@echo ""
	@echo "==> Local dev environment ready."
	@echo "    kubectl context: kind-$(DEV_CLUSTER_NAME)"
	@echo "    Operator ns:     $(DEV_NAMESPACE)"
	@echo "    Monitoring ns:   $(DEV_MONITORING_NAMESPACE)"
	@echo "    Grafana (no login):"
	@echo "      kubectl -n $(DEV_MONITORING_NAMESPACE) port-forward svc/$(DEV_MONITORING_RELEASE)-grafana 3000:80"
	@echo "      open http://localhost:3000  (anonymous Admin; dashboards under search)"
	@echo "    Prometheus:"
	@echo "      kubectl -n $(DEV_MONITORING_NAMESPACE) port-forward svc/$(DEV_MONITORING_RELEASE)-kube-prometheus-prometheus 9090:9090"
	@echo "    Made a code change? Just run 'make dev-up' again to rebuild, reload, and restart."
	@echo "    Tear it down with: make dev-down"

.PHONY: dev-down
dev-down: ## Delete the local dev kind cluster created by dev-up.
	kind delete cluster --name $(DEV_CLUSTER_NAME)

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
