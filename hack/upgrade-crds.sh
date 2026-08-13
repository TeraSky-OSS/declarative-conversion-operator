#!/usr/bin/env bash
#
# Diff (and optionally apply) this chart's CRDs against the current cluster.
# Helm installs CRDs from crds/ once and never upgrades them; run this before
# `helm upgrade` whenever a chart version changes the CRD schema.
#
# Usage:
#   ./hack/upgrade-crds.sh --chart charts/declarative-conversion-operator
#   ./hack/upgrade-crds.sh --chart oci://ghcr.io/terasky-oss/charts/declarative-conversion-operator --version 0.2.0
#   ./hack/upgrade-crds.sh --chart charts/declarative-conversion-operator --apply
#   ./hack/upgrade-crds.sh --chart charts/declarative-conversion-operator --print
set -euo pipefail

CHART=""
VERSION=""
APPLY=0
PRINT_ONLY=0

usage() {
  cat <<'EOF'
Diff (and optionally apply) this chart's CRDs against the current cluster.
Helm installs CRDs from crds/ once and never upgrades them; run this before
helm upgrade whenever a chart version changes the CRD schema.

Usage:
  ./hack/upgrade-crds.sh --chart charts/declarative-conversion-operator
  ./hack/upgrade-crds.sh --chart oci://ghcr.io/terasky-oss/charts/declarative-conversion-operator --version 0.2.0
  ./hack/upgrade-crds.sh --chart charts/declarative-conversion-operator --apply
  ./hack/upgrade-crds.sh --chart charts/declarative-conversion-operator --print
EOF
  exit "${1:-0}"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --chart) CHART="${2:?--chart requires a value}"; shift 2 ;;
    --version) VERSION="${2:?--version requires a value}"; shift 2 ;;
    --apply) APPLY=1; shift ;;
    --print) PRINT_ONLY=1; shift ;;
    -h|--help) usage 0 ;;
    *) echo "unknown argument: $1" >&2; usage 2 ;;
  esac
done

if [ -z "${CHART}" ]; then
  echo "FAIL: --chart is required (local path or oci://... reference)" >&2
  usage 2
fi

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "FAIL: required command '$1' not found on PATH" >&2
    exit 2
  fi
}

require_cmd helm
HELM_ARGS=(show crds "${CHART}")
if [ -n "${VERSION}" ]; then
  HELM_ARGS+=(--version "${VERSION}")
fi

CRDS="$(helm "${HELM_ARGS[@]}")"
if [ -z "${CRDS}" ]; then
  echo "FAIL: helm show crds returned empty output for ${CHART}${VERSION:+ (version ${VERSION})}" >&2
  exit 1
fi

if [ "${PRINT_ONLY}" -eq 1 ]; then
  printf '%s\n' "${CRDS}"
  exit 0
fi

require_cmd kubectl
TMP="$(mktemp)"
trap 'rm -f "${TMP}"' EXIT
printf '%s\n' "${CRDS}" > "${TMP}"

set +e
kubectl diff -f "${TMP}"
diff_rc=$?
set -e
if [ "${diff_rc}" -gt 1 ]; then
  echo "FAIL: kubectl diff failed (exit ${diff_rc}); not applying CRDs" >&2
  exit "${diff_rc}"
fi
if [ "${diff_rc}" -eq 0 ]; then
  echo "OK: cluster CRDs already match ${CHART}${VERSION:+:${VERSION}}"
  exit 0
fi

if [ "${APPLY}" -ne 1 ]; then
  echo "CRDs differ from the cluster. Re-run with --apply to update them, then helm upgrade."
  exit 1
fi

kubectl apply -f "${TMP}"
echo "OK: applied CRDs from ${CHART}${VERSION:+:${VERSION}}"
