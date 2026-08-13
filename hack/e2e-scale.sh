#!/usr/bin/env bash
#
# Cluster-scale e2e: kind + cert-manager + this operator (native CRD only),
# then generate a fleet of CRDs with mixed conversion strategies, create
# instances, and issue parallel kubectl-equivalent Get/List calls through
# the live apiserver conversion webhook.
#
# Defaults are a smoke size (4 CRDs × 5 CRs). For the 100×100 envelope:
#
#   TARGETS=100 INSTANCES=100 PARALLEL=32 ./hack/e2e-scale.sh
#
# Prerequisites: docker, kind, kubectl, helm.
# Set KEEP_CLUSTER=1 to skip teardown.
set -euo pipefail

# shellcheck source=hack/e2e-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/e2e-common.sh"

CLUSTER_NAME="${CLUSTER_NAME:-declarative-conversion-e2e-scale}"
NAMESPACE="${NAMESPACE:-declarative-conversion-system}"
IMG_TAG="e2e-scale-$(date +%s 2>/dev/null || echo local)"
MANAGER_IMG="ghcr.io/terasky-oss/declarative-conversion-operator:${IMG_TAG}"
WEBHOOK_IMG="ghcr.io/terasky-oss/declarative-conversion-webhook-server:${IMG_TAG}"
CERT_MANAGER_VERSION="v1.21.1"
RELEASE_NAME="declarative-conversion-operator"

TARGETS="${TARGETS:-4}"
INSTANCES="${INSTANCES:-5}"
STRATEGIES_MIN="${STRATEGIES_MIN:-3}"
STRATEGIES_MAX="${STRATEGIES_MAX:-10}"
PARALLEL="${PARALLEL:-8}"
SEED="${SEED:-1}"
SCALE_NS="${SCALE_NS:-dco-scale}"
LIST_REPEATS="${LIST_REPEATS:-3}"
GET_REPEATS="${GET_REPEATS:-1}"

trap e2e_cleanup EXIT

require_cmd docker
require_cmd kind
require_cmd kubectl
require_cmd helm

create_kind_cluster
build_and_load_images
install_cert_manager
install_operator \
  --set features.crossplane.enabled=false \
  --set features.nativeCRD.enabled=true

log "Waiting for the default ConversionWebhookServer to become Available"
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default

log "Running scalegen (targets=${TARGETS} instances=${INSTANCES} parallel=${PARALLEL})"
go run "${REPO_ROOT}/cmd/scalegen" \
  --targets "${TARGETS}" \
  --instances "${INSTANCES}" \
  --strategies-min "${STRATEGIES_MIN}" \
  --strategies-max "${STRATEGIES_MAX}" \
  --parallel "${PARALLEL}" \
  --seed "${SEED}" \
  --namespace "${SCALE_NS}" \
  --list-repeats "${LIST_REPEATS}" \
  --get-repeats "${GET_REPEATS}"

log "Scale e2e finished"
