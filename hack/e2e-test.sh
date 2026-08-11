#!/usr/bin/env bash
#
# End-to-end test: creates a kind cluster, installs cert-manager and
# Crossplane, builds and loads this repo's images into the cluster,
# installs this operator via its own Helm chart, then proves — against a
# real kube-apiserver, not just pkg/engine offline — that a composite
# resource created at one served version is actually converted by the
# webhook this operator wires up when read back at another.
#
# Runs identically in CI and locally: the only prerequisites are docker,
# kind, kubectl, and helm on PATH. Set KEEP_CLUSTER=1 to skip teardown
# for local debugging.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-declarative-conversion-e2e}"
NAMESPACE="${NAMESPACE:-declarative-conversion-system}"
IMG_TAG="e2e-$(date +%s 2>/dev/null || echo local)"
MANAGER_IMG="ghcr.io/vrabbi/declarative-conversion-operator:${IMG_TAG}"
WEBHOOK_IMG="ghcr.io/vrabbi/declarative-conversion-webhook-server:${IMG_TAG}"
CERT_MANAGER_VERSION="v1.21.1"
RELEASE_NAME="declarative-conversion-operator"
COMPOSITE_NAME="e2e-widget"
COMPOSITE_NS="default"

log() { echo "==> $*"; }

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "FAIL: required command '$1' not found on PATH" >&2
    exit 2
  fi
}

dump_diagnostics() {
  echo "--- kubectl get pods -A ---"
  kubectl get pods -A || true
  echo "--- xrdconversionconfig ---"
  kubectl get xrdconversionconfig -o yaml || true
  echo "--- conversionwebhookserver ---"
  kubectl get conversionwebhookserver -o yaml || true
  echo "--- xwidgets.e2e.example.org CRD ---"
  kubectl get crd xwidgets.e2e.example.org -o yaml || true
  echo "--- manager logs ---"
  kubectl -n "${NAMESPACE}" logs -l "app.kubernetes.io/instance=${RELEASE_NAME},control-plane=controller-manager" --all-containers --tail=300 || true
  echo "--- webhook-server logs ---"
  kubectl -n "${NAMESPACE}" logs -l "app.kubernetes.io/name=declarative-conversion-webhook-server" --all-containers --tail=300 || true
  echo "--- recent events (all namespaces) ---"
  kubectl get events -A --sort-by=.lastTimestamp 2>/dev/null | tail -50 || true
}

cleanup() {
  local exit_code=$?
  if [ "${exit_code}" -ne 0 ]; then
    echo "=== e2e failed (exit ${exit_code}); dumping diagnostics ==="
    dump_diagnostics
  fi
  if [ "${KEEP_CLUSTER:-0}" = "1" ]; then
    echo "=== KEEP_CLUSTER=1 set; leaving cluster '${CLUSTER_NAME}' running ==="
  else
    echo "=== deleting kind cluster '${CLUSTER_NAME}' ==="
    kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
  fi
  exit "${exit_code}"
}
trap cleanup EXIT

assert_eq() {
  local got="$1" want="$2" desc="$3"
  if [ "${got}" != "${want}" ]; then
    echo "FAIL: ${desc}: expected '${want}', got '${got}'"
    exit 1
  fi
  echo "OK: ${desc} = '${got}'"
}

require_cmd docker
require_cmd kind
require_cmd kubectl
require_cmd helm

if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  log "Reusing existing kind cluster '${CLUSTER_NAME}'"
else
  log "Creating kind cluster '${CLUSTER_NAME}'"
  kind create cluster --name "${CLUSTER_NAME}"
fi
kubectl config use-context "kind-${CLUSTER_NAME}"

log "Building manager and webhook-server images (tag ${IMG_TAG})"
docker build --build-arg COMPONENT=manager -t "${MANAGER_IMG}" "${REPO_ROOT}"
docker build --build-arg COMPONENT=webhook-server -t "${WEBHOOK_IMG}" "${REPO_ROOT}"

log "Loading images into kind cluster"
kind load docker-image "${MANAGER_IMG}" --name "${CLUSTER_NAME}"
kind load docker-image "${WEBHOOK_IMG}" --name "${CLUSTER_NAME}"

log "Installing cert-manager ${CERT_MANAGER_VERSION}"
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
kubectl -n cert-manager wait --for=condition=Available --timeout=180s deployment --all

log "Installing Crossplane"
helm repo add crossplane-stable https://charts.crossplane.io/stable --force-update
helm repo update crossplane-stable
helm upgrade --install crossplane crossplane-stable/crossplane \
  --namespace crossplane-system --create-namespace \
  --wait --timeout 180s

log "Installing declarative-conversion-operator (this repo's Helm chart) with the locally-built images"
# The chart bundles a ConversionWebhookServer sample CR in the same release
# as the manager Deployment whose admission webhook validates that very
# CR (failurePolicy: Fail, intentionally fail-closed). On a fresh install
# the apiserver can try to validate the CR before the manager pod's webhook
# is actually serving yet, rejecting it with a connection-refused error —
# a real first-install race, not a CI-only quirk (the same fix — retry —
# is what a human hitting this would naturally do). Helm's own retries of
# `upgrade --install` are idempotent (already-created resources are left
# alone), so retrying here just re-attempts whatever failed.
attempt=1
until helm upgrade --install "${RELEASE_NAME}" "${REPO_ROOT}/charts/declarative-conversion-operator" \
  --namespace "${NAMESPACE}" --create-namespace \
  --set image.manager.tag="${IMG_TAG}" \
  --set image.webhookServer.tag="${IMG_TAG}" \
  --set image.pullPolicy=Never \
  --wait --timeout 180s; do
  if [ "${attempt}" -ge 5 ]; then
    echo "FAIL: helm install of ${RELEASE_NAME} did not succeed after ${attempt} attempts"
    exit 1
  fi
  echo "helm install attempt ${attempt} failed (likely the admission webhook's backing pod wasn't ready yet) -- retrying in 15s"
  attempt=$((attempt + 1))
  sleep 15
done

log "Waiting for the default ConversionWebhookServer to become Available"
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default

log "Applying the e2e XWidget XRD"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/xrd.yaml"
kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.e2e.example.org

log "Applying the XRDConversionConfig and waiting for it to reach Applied"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/xrdconversionconfig.yaml"
kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-e2e-conversion

log "Creating a composite resource at the spoke version (v1)"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/composite-v1.yaml"

log "Reading it back at the hub version (v2) to confirm the webhook actually converted it"
got="$(kubectl get "xwidgets.v2.e2e.example.org" "${COMPOSITE_NAME}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.storageGB}')"
assert_eq "${got}" "500" "v1->v2: spec.storageSize renamed to spec.storageGB"

log "Setting the hub-only 'description' field at v2"
kubectl patch "xwidgets.v2.e2e.example.org" "${COMPOSITE_NAME}" -n "${COMPOSITE_NS}" \
  --type=merge -p '{"spec":{"description":"created via v2"}}'

got="$(kubectl get "xwidgets.v2.e2e.example.org" "${COMPOSITE_NAME}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.description}')"
assert_eq "${got}" "created via v2" "v2 natively stores spec.description"

log "Reading it back at the spoke version (v1) to confirm the hub-only field is dropped, not erroring"
got="$(kubectl get "xwidgets.v1.e2e.example.org" "${COMPOSITE_NAME}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.storageSize}')"
assert_eq "${got}" "500" "v2->v1: spec.storageGB still renamed back to spec.storageSize"

got="$(kubectl get "xwidgets.v1.e2e.example.org" "${COMPOSITE_NAME}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.description}')"
assert_eq "${got}" "" "v2->v1: spec.description (hub-only) does not leak into v1"

log "All e2e conversion checks passed"
