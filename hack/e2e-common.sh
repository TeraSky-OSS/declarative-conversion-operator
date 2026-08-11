#!/usr/bin/env bash
#
# Shared helpers for the e2e test scripts (hack/e2e-test*.sh). Sourced, not
# executed directly -- every function here reads from variables the calling
# script is expected to have set (CLUSTER_NAME, NAMESPACE, RELEASE_NAME,
# IMG_TAG, MANAGER_IMG, WEBHOOK_IMG, CERT_MANAGER_VERSION) rather than
# hardcoding them, since the three e2e scripts each use their own cluster
# name and namespace to stay independent of one another.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log() { echo "==> $*"; }

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "FAIL: required command '$1' not found on PATH" >&2
    exit 2
  fi
}

assert_eq() {
  local got="$1" want="$2" desc="$3"
  if [ "${got}" != "${want}" ]; then
    echo "FAIL: ${desc}: expected '${want}', got '${got}'"
    exit 1
  fi
  echo "OK: ${desc} = '${got}'"
}

# assert_apply_rejected runs `kubectl apply -f <file>` and asserts it FAILS
# with stderr containing want_substr -- used to prove the admission webhook
# actually rejects a config kind that's disabled on this installation, not
# just "something unrelated went wrong."
assert_apply_rejected() {
  local file="$1" want_substr="$2" desc="$3"
  local out
  if out="$(kubectl apply -f "${file}" 2>&1)"; then
    echo "FAIL: ${desc}: expected kubectl apply to be rejected, but it succeeded"
    exit 1
  fi
  if ! grep -qF "${want_substr}" <<<"${out}"; then
    echo "FAIL: ${desc}: rejection message did not contain '${want_substr}'; got: ${out}"
    exit 1
  fi
  echo "OK: ${desc} (rejected as expected)"
}

dump_diagnostics() {
  echo "--- kubectl get pods -A ---"
  kubectl get pods -A || true
  echo "--- xrdconversionconfig ---"
  kubectl get xrdconversionconfig -o yaml || true
  echo "--- crdconversionconfig ---"
  kubectl get crdconversionconfig -o yaml || true
  echo "--- conversionwebhookserver ---"
  kubectl get conversionwebhookserver -o yaml || true
  echo "--- describe manager pod(s) ---"
  kubectl -n "${NAMESPACE}" describe pod -l "app.kubernetes.io/instance=${RELEASE_NAME},control-plane=controller-manager" || true
  echo "--- manager logs (current container) ---"
  kubectl -n "${NAMESPACE}" logs -l "app.kubernetes.io/instance=${RELEASE_NAME},control-plane=controller-manager" --all-containers --tail=300 || true
  echo "--- manager logs (previous container, if it crashed and restarted) ---"
  kubectl -n "${NAMESPACE}" logs -l "app.kubernetes.io/instance=${RELEASE_NAME},control-plane=controller-manager" --all-containers --tail=300 --previous || true
  echo "--- webhook-server logs ---"
  kubectl -n "${NAMESPACE}" logs -l "app.kubernetes.io/name=declarative-conversion-webhook-server" --all-containers --tail=300 || true
  echo "--- recent events (all namespaces) ---"
  kubectl get events -A --sort-by=.lastTimestamp 2>/dev/null | tail -50 || true
}

# e2e_cleanup is meant to be registered via `trap e2e_cleanup EXIT` by the
# calling script. It dumps diagnostics on failure, then tears down the kind
# cluster unless KEEP_CLUSTER=1 (for local debugging).
e2e_cleanup() {
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

create_kind_cluster() {
  if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
    log "Reusing existing kind cluster '${CLUSTER_NAME}'"
  else
    log "Creating kind cluster '${CLUSTER_NAME}'"
    kind create cluster --name "${CLUSTER_NAME}"
  fi
  kubectl config use-context "kind-${CLUSTER_NAME}"
}

build_and_load_images() {
  log "Building manager and webhook-server images (tag ${IMG_TAG})"
  docker build --build-arg COMPONENT=manager -t "${MANAGER_IMG}" "${REPO_ROOT}"
  docker build --build-arg COMPONENT=webhook-server -t "${WEBHOOK_IMG}" "${REPO_ROOT}"

  log "Loading images into kind cluster"
  kind load docker-image "${MANAGER_IMG}" --name "${CLUSTER_NAME}"
  kind load docker-image "${WEBHOOK_IMG}" --name "${CLUSTER_NAME}"
}

install_cert_manager() {
  log "Installing cert-manager ${CERT_MANAGER_VERSION}"
  kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
  kubectl -n cert-manager wait --for=condition=Available --timeout=180s deployment --all
}

install_crossplane() {
  log "Installing Crossplane"
  helm repo add crossplane-stable https://charts.crossplane.io/stable --force-update
  helm repo update crossplane-stable
  helm upgrade --install crossplane crossplane-stable/crossplane \
    --namespace crossplane-system --create-namespace \
    --wait --timeout 180s
}

# install_operator installs this repo's own Helm chart with the
# locally-built images, retrying on the known first-install race: the
# chart's ConversionWebhookServer sample CR is validated by the same
# manager pod whose admission webhook may not be serving yet
# (failurePolicy: Fail, intentionally fail-closed). Extra --set flags
# (e.g. feature toggles) are passed through as positional arguments.
install_operator() {
  log "Installing declarative-conversion-operator (this repo's Helm chart) with the locally-built images"
  local attempt=1
  until helm upgrade --install "${RELEASE_NAME}" "${REPO_ROOT}/charts/declarative-conversion-operator" \
    --namespace "${NAMESPACE}" --create-namespace \
    --set image.manager.tag="${IMG_TAG}" \
    --set image.webhookServer.tag="${IMG_TAG}" \
    --set image.pullPolicy=Never \
    "$@" \
    --wait --timeout 180s; do
    if [ "${attempt}" -ge 5 ]; then
      echo "FAIL: helm install of ${RELEASE_NAME} did not succeed after ${attempt} attempts"
      exit 1
    fi
    echo "helm install attempt ${attempt} failed (likely the admission webhook's backing pod wasn't ready yet) -- retrying in 15s"
    attempt=$((attempt + 1))
    sleep 15
  done
}
