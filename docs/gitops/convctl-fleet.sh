#!/usr/bin/env bash
# Fleet gate: convctl diff --live + test --live against every cluster.
# Copy into a platform repo or run from a checkout of this operator.
#
# Required:
#   CONVCTL_CONFIG   path to XRDConversionConfig / CRDConversionConfig
#   CONVCTL_XRD or CONVCTL_CRD
# Cluster list (exactly one):
#   CONTEXTS         space-separated kubeconfig context names
#   KUBECONFIGS      space-separated kubeconfig file paths
#
# Optional:
#   CONVCTL          convctl binary (default: convctl on PATH)
#   KUBECONFIG       kubeconfig used with CONTEXTS (kubectl default if unset)
#   OUT_DIR          where per-cluster JUnit/JSON land (default: ./convctl-fleet-out)
#   SKIP_DIFF        set to 1 to run only test --live
#   FAIL_ON_DIFF     set to 1 to fail the gate on a coverage/claim delta (diff exit 1).
#                    Default: treat exit 1 as a review artifact; fail only on exit 2.
set -euo pipefail

CONVCTL="${CONVCTL:-convctl}"
OUT_DIR="${OUT_DIR:-./convctl-fleet-out}"
SKIP_DIFF="${SKIP_DIFF:-0}"
FAIL_ON_DIFF="${FAIL_ON_DIFF:-0}"

die() { echo "error: $*" >&2; exit 2; }

[[ -n "${CONVCTL_CONFIG:-}" ]] || die "CONVCTL_CONFIG is required"
if [[ -n "${CONVCTL_XRD:-}" && -n "${CONVCTL_CRD:-}" ]]; then
  die "set CONVCTL_XRD or CONVCTL_CRD, not both"
fi
if [[ -z "${CONVCTL_XRD:-}" && -z "${CONVCTL_CRD:-}" ]]; then
  die "CONVCTL_XRD or CONVCTL_CRD is required"
fi
if [[ -n "${CONTEXTS:-}" && -n "${KUBECONFIGS:-}" ]]; then
  die "set CONTEXTS or KUBECONFIGS, not both"
fi
if [[ -z "${CONTEXTS:-}" && -z "${KUBECONFIGS:-}" ]]; then
  die "set CONTEXTS (context names) or KUBECONFIGS (kubeconfig paths)"
fi
command -v "${CONVCTL}" >/dev/null 2>&1 || die "${CONVCTL} not found on PATH"

schema_flags=()
if [[ -n "${CONVCTL_XRD:-}" ]]; then
  schema_flags+=(--xrd "${CONVCTL_XRD}")
else
  schema_flags+=(--crd "${CONVCTL_CRD}")
fi

mkdir -p "${OUT_DIR}"
failed=0

run_one() {
  local label="$1" kubeconfig="$2" context="$3"
  local safe kube_flags=()
  safe="$(printf '%s' "${label}" | tr -c 'A-Za-z0-9._-' '_')"
  [[ -n "${kubeconfig}" ]] && kube_flags+=(--kubeconfig "${kubeconfig}")
  [[ -n "${context}" ]] && kube_flags+=(--context "${context}")

  echo "=== ${label} ==="
  if [[ "${SKIP_DIFF}" != 1 ]]; then
    local diff_rc=0
    "${CONVCTL}" diff --config "${CONVCTL_CONFIG}" --live \
      "${kube_flags[@]}" -o json \
      > "${OUT_DIR}/${safe}.diff.json" || diff_rc=$?
    if [[ "${diff_rc}" -eq 2 ]]; then
      echo "diff --live usage/cluster error on ${label}" >&2
      failed=1
    elif [[ "${diff_rc}" -eq 1 && "${FAIL_ON_DIFF}" == 1 ]]; then
      echo "diff --live reported a delta on ${label} (FAIL_ON_DIFF=1)" >&2
      failed=1
    elif [[ "${diff_rc}" -eq 1 ]]; then
      echo "diff --live reported a delta on ${label} (review ${OUT_DIR}/${safe}.diff.json)" >&2
    fi
  fi
  if ! "${CONVCTL}" test --config "${CONVCTL_CONFIG}" --live \
    "${schema_flags[@]}" "${kube_flags[@]}" \
    --output junit --output-file "${OUT_DIR}/${safe}.junit.xml" \
    --quiet; then
    echo "test --live failed on ${label}" >&2
    failed=1
  fi
}

if [[ -n "${CONTEXTS:-}" ]]; then
  for ctx in ${CONTEXTS}; do
    run_one "${ctx}" "${KUBECONFIG:-}" "${ctx}"
  done
else
  for kc in ${KUBECONFIGS}; do
    run_one "$(basename "${kc}")" "${kc}" ""
  done
fi

if [[ "${failed}" -ne 0 ]]; then
  echo "fleet gate failed; reports in ${OUT_DIR}" >&2
  exit 1
fi
echo "fleet gate passed; reports in ${OUT_DIR}"
