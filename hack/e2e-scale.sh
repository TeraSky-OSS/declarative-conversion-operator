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
# Set RESULT_JSON to write the run's measurements as JSON — latency
# percentiles, throughput, plus the webhook-server's cold-start time and
# its loaded working set read off the cluster. That is what the nightly
# Scale workflow publishes as an artifact and diffs against the previous
# run.
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
QPS="${QPS:-100}"
BURST="${BURST:-200}"
RESET="${RESET:-1}"
LIST_REPEATS="${LIST_REPEATS:-3}"
GET_REPEATS="${GET_REPEATS:-1}"
RESULT_JSON="${RESULT_JSON:-}"

trap e2e_cleanup EXIT

require_cmd docker
require_cmd kind
require_cmd kubectl
require_cmd helm
require_cmd python3

create_kind_cluster
build_and_load_images
install_cert_manager
install_operator \
  --set features.crossplane.enabled=false \
  --set features.nativeCRD.enabled=true

log "Waiting for the default ConversionWebhookServer to become Available"
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default

log "Running scalegen (targets=${TARGETS} instances=${INSTANCES} parallel=${PARALLEL} qps=${QPS})"
SCALE_ARGS=(
  --targets "${TARGETS}"
  --instances "${INSTANCES}"
  --strategies-min "${STRATEGIES_MIN}"
  --strategies-max "${STRATEGIES_MAX}"
  --parallel "${PARALLEL}"
  --seed "${SEED}"
  --namespace "${SCALE_NS}"
  --list-repeats "${LIST_REPEATS}"
  --get-repeats "${GET_REPEATS}"
  --qps "${QPS}"
  --burst "${BURST}"
)
if [ "${RESET}" != "0" ]; then
  SCALE_ARGS+=(--reset)
fi
if [ -n "${RESULT_JSON}" ]; then
  SCALE_ARGS+=(--result-json "${RESULT_JSON}")
fi

# The run's exit status is kept rather than propagated immediately: a run
# that ended with get/list errors is exactly the run whose numbers and
# cluster-side observations are worth keeping, and `set -e` would throw
# them away.
scale_rc=0
go run "${REPO_ROOT}/cmd/scalegen" "${SCALE_ARGS[@]}" || scale_rc=$?

if [ -n "${RESULT_JSON}" ] && [ -f "${RESULT_JSON}" ]; then
  # Roll the webhook-server before measuring, and only after the traffic is
  # finished so it cannot perturb the latency numbers.
  #
  # Without this the cold-start figure is meaningless: the replicas started
  # before any of this fleet existed, so they synced zero targets in
  # microseconds, and the artifact would trend that forever. Restarting them
  # now makes them compile the whole generated fleet, which is the number
  # this run is supposed to publish — and it makes the working set that
  # follows a loaded steady state rather than an empty one.
  dep="$(kubectl -n "${NAMESPACE}" get deploy \
    -l app.kubernetes.io/name=declarative-conversion-webhook-server \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  if [ -n "${dep}" ]; then
    log "Restarting ${dep} so the cold start is measured against the generated fleet"
    kubectl -n "${NAMESPACE}" rollout restart "deployment/${dep}"
    kubectl -n "${NAMESPACE}" rollout status "deployment/${dep}" --timeout=600s
  else
    echo "WARN: no webhook-server deployment found; cold start will not be measured" >&2
  fi

  log "Collecting cluster-side observations (cold start, loaded working set)"
  # Not `|| true`. The script tolerates partial collection internally — one
  # unscrapeable pod does not discard the run — but if it collected nothing
  # at all, the artifact is missing half of what a nightly run exists to
  # publish, and reporting that green would hide it. The result file is
  # still written and still uploaded, so failing here loses no data.
  observe_rc=0
  python3 "${REPO_ROOT}/hack/scale-observe.py" \
    --result "${RESULT_JSON}" --namespace "${NAMESPACE}" || observe_rc=$?
fi

if [ "${scale_rc}" -ne 0 ]; then
  echo "FAIL: the scale run reported errors (exit ${scale_rc})"
  exit "${scale_rc}"
fi
if [ "${observe_rc:-0}" -ne 0 ]; then
  echo "FAIL: cluster-side observations could not be collected (exit ${observe_rc})"
  exit "${observe_rc}"
fi

log "Scale e2e finished"
