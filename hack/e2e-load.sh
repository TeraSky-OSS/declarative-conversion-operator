#!/usr/bin/env bash
#
# Load e2e: kind + cert-manager + this operator (native CRD only), then POST
# synthetic ConversionReview batches of varying object count and size against
# the live webhook-server. Prints latency / throughput / error-rate numbers
# for docs/operations/capacity.md.
#
# Prerequisites: docker, kind, kubectl, helm, python3, curl.
# Set KEEP_CLUSTER=1 to skip teardown.
set -euo pipefail

# shellcheck source=hack/e2e-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/e2e-common.sh"

CLUSTER_NAME="${CLUSTER_NAME:-declarative-conversion-e2e-load}"
NAMESPACE="${NAMESPACE:-declarative-conversion-system}"
IMG_TAG="e2e-load-$(date +%s 2>/dev/null || echo local)"
MANAGER_IMG="ghcr.io/terasky-oss/declarative-conversion-operator:${IMG_TAG}"
WEBHOOK_IMG="ghcr.io/terasky-oss/declarative-conversion-webhook-server:${IMG_TAG}"
CERT_MANAGER_VERSION="v1.21.1"
RELEASE_NAME="declarative-conversion-operator"
LOCAL_PORT="${LOCAL_PORT:-19443}"
PF_PID=""
PF_LOG="$(mktemp "${TMPDIR:-/tmp}/e2e-load-pf.XXXXXX")"

load_cleanup() {
  local code=$?
  if [ -n "${PF_PID}" ]; then
    kill "${PF_PID}" >/dev/null 2>&1 || true
  fi
  rm -f "${PF_LOG}"
  (exit "${code}")
  e2e_cleanup
}
trap load_cleanup EXIT

require_cmd docker
require_cmd kind
require_cmd kubectl
require_cmd helm
require_cmd python3
require_cmd curl

create_kind_cluster
build_and_load_images
install_cert_manager
install_operator \
  --set features.crossplane.enabled=false \
  --set features.nativeCRD.enabled=true

log "Waiting for the default ConversionWebhookServer to become Available"
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default

log "Applying the native Gadget CRD and CRDConversionConfig"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/crd.yaml"
kubectl wait --for=condition=Established --timeout=60s crd/gadgets.nativecrd.example.org
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/crdconversionconfig.yaml"
kubectl wait --for=condition=Applied --timeout=120s crdconversionconfig/gadgets-e2e-conversion

log "Port-forwarding webhook-server Service to localhost:${LOCAL_PORT}"
kubectl -n "${NAMESPACE}" port-forward svc/default-webhook-server "${LOCAL_PORT}:443" >"${PF_LOG}" 2>&1 &
PF_PID=$!

URL="https://127.0.0.1:${LOCAL_PORT}/convert/gadgets.nativecrd.example.org"
for i in $(seq 1 30); do
  code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 2 -X POST "${URL}" -H 'Content-Type: application/json' -d '{}' || true)"
  # 200 (empty review decoded as bad/missing request still HTTP 200/400) or
  # 400 means TLS+HTTP is up. 000 means connection refused.
  if [ "${code}" != "000" ] && [ -n "${code}" ]; then
    break
  fi
  if [ "${i}" -eq 30 ]; then
    echo "FAIL: webhook port-forward never accepted connections (see ${PF_LOG})"
    cat "${PF_LOG}" || true
    exit 1
  fi
  sleep 1
done

log "Sending synthetic ConversionReview batches"
python3 "${REPO_ROOT}/hack/e2e-load-client.py" --url "${URL}"

log "Load e2e finished"
