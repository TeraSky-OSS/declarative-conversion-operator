#!/usr/bin/env bash
#
# Measures what the manager and webhook-server processes actually hold in
# memory, before and after the cache scoping in #146/#147, on a cluster
# deliberately loaded with the objects that dominate those caches: many
# Secrets (manager) and many CRDs (webhook-server).
#
# The comparison is between two real images built from two real commits —
# BASE_REF (default: main) and the current working tree — installed in turn
# into the same loaded cluster, so the only variable is the code.
#
# The number reported is workingSetBytes from the kubelet Summary API, taken
# after each process's caches have synced. Working set rather than Go heap
# stats because it is what a container memory limit and the OOM killer are
# evaluated against, and it is obtainable identically for both images without
# either of them having to expose anything.
#
# Usage:
#   hack/measure-cache-memory.sh [--secrets N] [--crds N] [--keep]
#
# Not part of CI: it takes tens of minutes and needs a throwaway cluster.
# Re-run it when changing anything about cache scoping, and update the
# table in docs/operations/capacity.md with the result.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

CLUSTER_NAME="${CLUSTER_NAME:-dco-memory}"
# Every kubectl and helm call below pins the context explicitly. The current
# context is process-global state in a shared kubeconfig: a concurrently
# running e2e script calling `kubectl config use-context` will silently
# redirect this one, and the symptom is a measurement of the wrong cluster
# rather than an error.
KCTX="kind-${CLUSTER_NAME}"
NAMESPACE="${NAMESPACE:-dco-memory-system}"
RELEASE_NAME="${RELEASE_NAME:-dco-memory}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.16.2}"
BASE_REF="${BASE_REF:-main}"

SECRET_COUNT=2000
# Bytes of payload per Secret. The default is deliberately large: at 3 KiB
# each, 2000 Secrets are only ~8 MB, which is noise next to the CRD schemas
# the manager also caches — so a run at that size cannot tell you whether the
# Secret informer mattered. Real clusters carry Helm release state, service
# account tokens, TLS material and image pull secrets; tens of KiB each is
# ordinary.
SECRET_BYTES=65536
CRD_COUNT=300
KEEP_CLUSTER=0
SETTLE_SECONDS="${SETTLE_SECONDS:-180}"
SAMPLES="${SAMPLES:-7}"
SAMPLE_INTERVAL="${SAMPLE_INTERVAL:-20}"

while [ $# -gt 0 ]; do
  case "$1" in
    --secrets) SECRET_COUNT="$2"; shift 2 ;;
    --secret-bytes) SECRET_BYTES="$2"; shift 2 ;;
    --crds) CRD_COUNT="$2"; shift 2 ;;
    --keep) KEEP_CLUSTER=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# shellcheck source=hack/e2e-common.sh
IMG_TAG="measure"
# Must match what the chart renders (registry + repository), or the pods
# come up ErrImageNeverPull with pullPolicy=Never.
MANAGER_IMG="ghcr.io/terasky-oss/declarative-conversion-operator:${IMG_TAG}"
WEBHOOK_IMG="ghcr.io/terasky-oss/declarative-conversion-webhook-server:${IMG_TAG}"
source "${REPO_ROOT}/hack/e2e-common.sh"

for c in kind kubectl helm docker git python3; do require_cmd "$c"; done

cleanup() {
  if [ "${KEEP_CLUSTER}" -eq 0 ]; then
    kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
  fi
  git worktree remove --force "${BASELINE_TREE:-/nonexistent}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ---------------------------------------------------------------- load

# make_secrets creates SECRET_COUNT Secrets spread over 20 namespaces, each
# carrying SECRET_BYTES of payload — the shape of a cluster full of Helm
# release state, service account tokens and TLS material. These are exactly
# the objects a cluster-wide Secret informer would hold and the operator
# would never read.
#
# Applying them in one stream is too much for a kind apiserver at this size,
# so they go in batches.
make_secrets() {
  log "Creating ${SECRET_COUNT} Secrets of ${SECRET_BYTES} bytes each"
  local payload
  payload="$(head -c "${SECRET_BYTES}" /dev/urandom | base64 -w0)"
  local i ns
  for i in $(seq 0 19); do
    kubectl --context "${KCTX}" create namespace "memload-${i}" >/dev/null 2>&1 || true
  done
  local batch=200 start end
  for start in $(seq 1 "${batch}" "${SECRET_COUNT}"); do
    end=$(( start + batch - 1 ))
    [ "${end}" -gt "${SECRET_COUNT}" ] && end="${SECRET_COUNT}"
    {
      for i in $(seq "${start}" "${end}"); do
        ns="memload-$(( i % 20 ))"
        printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: load-%s\n  namespace: %s\ntype: Opaque\ndata:\n  blob: %s\n---\n' \
          "${i}" "${ns}" "${payload}"
      done
    } | kubectl --context "${KCTX}" apply -f - >/dev/null
  done
}

# make_crds creates CRD_COUNT CRDs whose OpenAPI schemas are large enough to
# be representative of Crossplane-generated composite CRDs — the schema, not
# the object header, is what makes a CRD informer expensive.
make_crds() {
  log "Creating ${CRD_COUNT} CRDs with large schemas"
  python3 "${REPO_ROOT}/hack/gen-load-crds.py" "${CRD_COUNT}" | kubectl --context "${KCTX}" apply -f - >/dev/null
  kubectl --context "${KCTX}" wait --for=condition=Established --timeout=300s crd -l dco.terasky.com/memload=true >/dev/null
}

# ------------------------------------------------------------- measure

# pod_memory reports the working-set bytes of the first pod matching a
# label selector, via the kubelet Summary API.
#
# Not scraped from the process's own /metrics: both images are distroless
# (no shell, no HTTP client to exec), and a dedicated Prometheus registry
# does not carry the process collectors unless something registers them —
# which the manager does and the webhook-server historically did not. Asking
# the kubelet gives one number, obtained the same way, for both processes
# and both builds.
#
# workingSetBytes rather than RSS: it is what the kernel's OOM killer and a
# container memory limit are evaluated against, so it is the number an
# operator actually has to size.
pod_memory() {
  local selector="$1" pod node
  pod="$(kubectl --context "${KCTX}" -n "${NAMESPACE}" get pod -l "${selector}" \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)"
  if [ -z "${pod}" ]; then
    return 1
  fi
  node="$(kubectl --context "${KCTX}" -n "${NAMESPACE}" get pod "${pod}" -o jsonpath='{.spec.nodeName}')"
  kubectl --context "${KCTX}" get --raw "/api/v1/nodes/${node}/proxy/stats/summary" |
    python3 -c '
import json, sys
want = sys.argv[1]
summary = json.load(sys.stdin)
for pod in summary.get("pods", []):
    if pod.get("podRef", {}).get("name") == want:
        print(pod.get("memory", {}).get("workingSetBytes", ""))
        break
' "${pod}"
}

# settle waits for caches to sync and for the Go heap to reach a steady
# state. Without the second part the numbers are dominated by whether a GC
# happened to have run.
settle() {
  kubectl --context "${KCTX}" -n "${NAMESPACE}" rollout status deployment --timeout=300s >/dev/null
  log "Letting the processes settle (${SETTLE_SECONDS}s)"
  sleep "${SETTLE_SECONDS}"
}

# measure_into samples each process several times rather than once.
#
# A single sample is not reproducible: the same baseline build measured
# twice on the same cluster came back 159 MiB and 217 MiB, because working
# set depends on where in its GC cycle the process happened to be. The
# median of N samples is stable; the min approximates the post-GC floor,
# which is the closest thing to "what this actually retains". Both are
# reported, with the spread, so a reader can see how much to trust them.
measure_into() {
  local label="$1"
  settle

  local mgr_samples=() wh_samples=() i
  for i in $(seq 1 "${SAMPLES}"); do
    mgr_samples+=("$(pod_memory "control-plane=controller-manager" || echo 0)")
    wh_samples+=("$(pod_memory "app.kubernetes.io/name=declarative-conversion-webhook-server" || echo 0)")
    if [ "${i}" -lt "${SAMPLES}" ]; then
      sleep "${SAMPLE_INTERVAL}"
    fi
  done

  echo "RESULT ${label} manager $(summarize "${mgr_samples[@]}")"
  echo "RESULT ${label} webhook-server $(summarize "${wh_samples[@]}")"
}

# summarize prints min/median/max over the samples, in bytes and MiB.
summarize() {
  python3 -c '
import statistics, sys
vals = [int(v) for v in sys.argv[1:] if v and v != "0"]
if not vals:
    print("samples=0 (all scrapes failed)")
    raise SystemExit
mib = lambda b: round(b / 1048576, 1)
print(f"samples={len(vals)} "
      f"min={min(vals)} ({mib(min(vals))} MiB) "
      f"median={int(statistics.median(vals))} ({mib(statistics.median(vals))} MiB) "
      f"max={max(vals)} ({mib(max(vals))} MiB)")
' "$@"
}

# install_chart installs one copy of the chart, retrying the same
# first-install race the e2e scripts handle: the chart's own
# ConversionWebhookServer is validated by the manager pod whose admission
# webhook may not be serving yet (failurePolicy: Fail, by design).
install_chart() {
  local chart="$1" attempt=1
  until helm --kube-context "${KCTX}" upgrade --install "${RELEASE_NAME}" "${chart}" \
    --namespace "${NAMESPACE}" --create-namespace \
    --set image.manager.tag="${IMG_TAG}" --set image.webhookServer.tag="${IMG_TAG}" \
    --set image.pullPolicy=Never --wait --timeout 300s; do
    if [ "${attempt}" -ge 5 ]; then
      echo "FAIL: helm install of ${chart} did not succeed after ${attempt} attempts"
      exit 1
    fi
    echo "helm install attempt ${attempt} failed -- retrying in 15s"
    attempt=$((attempt + 1))
    sleep 15
  done
}

# ---------------------------------------------------------------- main

create_kind_cluster
install_cert_manager
install_crossplane
make_secrets
make_crds

log "=== Baseline: building images from ${BASE_REF} ==="
BASELINE_TREE="$(mktemp -d)/baseline"
git worktree add --detach "${BASELINE_TREE}" "${BASE_REF}" >/dev/null
(
  cd "${BASELINE_TREE}"
  docker build --build-arg COMPONENT=manager -t "${MANAGER_IMG}" .
  docker build --build-arg COMPONENT=webhook-server -t "${WEBHOOK_IMG}" .
)
kind load docker-image "${MANAGER_IMG}" --name "${CLUSTER_NAME}"
kind load docker-image "${WEBHOOK_IMG}" --name "${CLUSTER_NAME}"
# The chart comes from the baseline tree too: a chart change that alters
# resource limits would otherwise be attributed to the code.
install_chart "${BASELINE_TREE}/charts/declarative-conversion-operator"
measure_into "baseline(${BASE_REF})"

log "=== Candidate: building images from the working tree ==="
build_and_load_images
install_chart "${REPO_ROOT}/charts/declarative-conversion-operator"
# A rollout restart is required: the image tag is unchanged, so Helm sees no
# diff in the pod template and would leave the baseline pods running.
kubectl --context "${KCTX}" -n "${NAMESPACE}" rollout restart deployment >/dev/null
measure_into "candidate(worktree)"

log "Done. Secrets=${SECRET_COUNT} x ${SECRET_BYTES}B, CRDs=${CRD_COUNT}"
