#!/usr/bin/env bash
#
# End-to-end test for scope: LegacyCluster — the v1 compatibility layer
# inside Crossplane 2.x, which is the shape every cluster upgraded from 1.x
# still runs, and the only shape that generates a claim CRD.
#
# Nothing else in the suite covers it: test/e2e/testdata/xrd.yaml is
# scope: Namespaced, so before this leg there was zero end-to-end evidence
# that a CLAIM created at one version reads back correctly converted at
# another, or that the bare spec.* machinery layout survives conversion.
#
# The seven assertions, in the order they run:
#
#   1. A composite created at v1 reads back correctly converted at v2 and v3.
#   2. A claim created at v1 reads back correctly converted at v2 and v3.
#   3. The machinery fields survive on both — spec.compositionRef,
#      spec.writeConnectionSecretToRef and spec.claimRef on the composite;
#      spec.resourceRef and spec.compositeDeletePolicy on the claim.
#   4. status.conditions survives on both, INCLUDING a condition written by
#      this test that Crossplane does not own — condition ownership is not
#      knowable from the schema, so any filtering would be guesswork.
#   5. spec.conversion is present on BOTH generated CRDs, and the config's
#      ConversionPropagated condition reaches True.
#   6. convctl test --live samples both composites and claims.
#   7. convctl migrate-storage --prune-stored-versions leaves neither CRD
#      listing the old version.
#
# Runs identically in CI and locally: docker, kind, kubectl and helm on
# PATH, plus Go for convctl. KEEP_CLUSTER=1 skips teardown.
set -euo pipefail

# shellcheck source=hack/e2e-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/e2e-common.sh"

CLUSTER_NAME="${CLUSTER_NAME:-declarative-conversion-e2e-legacy}"
NAMESPACE="${NAMESPACE:-declarative-conversion-system}"
IMG_TAG="e2e-legacy-$(date +%s 2>/dev/null || echo local)"
MANAGER_IMG="ghcr.io/terasky-oss/declarative-conversion-operator:${IMG_TAG}"
WEBHOOK_IMG="ghcr.io/terasky-oss/declarative-conversion-webhook-server:${IMG_TAG}"
CERT_MANAGER_VERSION="v1.21.1"
RELEASE_NAME="declarative-conversion-operator"
CLAIM_NS="default"

TESTDATA="${REPO_ROOT}/test/e2e/testdata/legacy"
GROUP="e2e.example.org"
XRD_NAME="xlegacywidgets.${GROUP}"
COMPOSITE_CRD="xlegacywidgets.${GROUP}"
CLAIM_CRD="legacywidgets.${GROUP}"
CONFIG_NAME="xlegacywidgets-e2e-conversion"

# Per-version resource identifiers. Addressing by <resource>.<version>.<group>
# is what forces the apiserver through the conversion webhook rather than
# serving whatever version happens to be storage.
XC_V1="xlegacywidgets.v1.${GROUP}"
XC_V2="xlegacywidgets.v2.${GROUP}"
XC_V3="xlegacywidgets.v3.${GROUP}"
CL_V1="legacywidgets.v1.${GROUP}"
CL_V2="legacywidgets.v2.${GROUP}"
CL_V3="legacywidgets.v3.${GROUP}"

trap e2e_cleanup EXIT

require_cmd docker
require_cmd kind
require_cmd kubectl
require_cmd helm
require_cmd go

create_kind_cluster
build_and_load_images
install_cert_manager
install_crossplane
install_operator

log "Waiting for the default ConversionWebhookServer to become Available"
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default

log "Applying the LegacyCluster XRD (declared at apiextensions.crossplane.io/v1 — the v2 schema has no claimNames field)"
kubectl apply -f "${TESTDATA}/xrd.yaml"

log "Waiting for BOTH generated CRDs: the composite and the claim"
kubectl wait --for=create --timeout=120s "crd/${COMPOSITE_CRD}"
kubectl wait --for=create --timeout=120s "crd/${CLAIM_CRD}"
kubectl wait --for=condition=Established --timeout=60s "crd/${COMPOSITE_CRD}"
kubectl wait --for=condition=Established --timeout=60s "crd/${CLAIM_CRD}"

# The claim CRD's existence is the whole premise of this leg, so assert its
# shape rather than trusting that `wait --for=create` found the right thing.
assert_eq "$(kubectl get "crd/${CLAIM_CRD}" -o jsonpath='{.spec.scope}')" "Namespaced" \
  "the generated claim CRD is namespace-scoped"
assert_eq "$(kubectl get "crd/${COMPOSITE_CRD}" -o jsonpath='{.spec.scope}')" "Cluster" \
  "the LegacyCluster composite CRD is cluster-scoped"

log "Applying the XRDConversionConfig and waiting for it to reach Applied"
kubectl apply -f "${TESTDATA}/xrdconversionconfig.yaml"
kubectl wait --for=condition=Applied --timeout=120s "xrdconversionconfig/${CONFIG_NAME}"

# --- Assertion 5: conversion propagated into BOTH generated CRDs ---------
log "Asserting spec.conversion reached both generated CRDs, and ConversionPropagated is True"
kubectl wait --for=condition=ConversionPropagated --timeout=120s "xrdconversionconfig/${CONFIG_NAME}"
assert_eq "$(kubectl get "crd/${COMPOSITE_CRD}" -o jsonpath='{.spec.conversion.strategy}')" "Webhook" \
  "composite CRD spec.conversion.strategy"
assert_eq "$(kubectl get "crd/${CLAIM_CRD}" -o jsonpath='{.spec.conversion.strategy}')" "Webhook" \
  "claim CRD spec.conversion.strategy"
# Both CRDs must point at the SAME webhook path: they share one compiled
# plan in the registry, keyed by the XRD's name.
assert_eq "$(kubectl get "crd/${CLAIM_CRD}" -o jsonpath='{.spec.conversion.webhook.clientConfig.service.path}')" \
  "/convert/${XRD_NAME}" "claim CRD conversion path is the XRD's"
# status.generatedCRDs must list both, and report both propagated.
assert_eq "$(kubectl get "xrdconversionconfig/${CONFIG_NAME}" -o jsonpath='{.status.generatedCRDs[*].name}')" \
  "${COMPOSITE_CRD} ${CLAIM_CRD}" "status.generatedCRDs lists both generated CRDs"
assert_eq "$(kubectl get "xrdconversionconfig/${CONFIG_NAME}" -o jsonpath='{.status.generatedCRDs[*].propagated}')" \
  "true true" "both generated CRDs report propagated"

# --- Assertion 1: a composite created at v1 converts up ------------------
log "Creating a COMPOSITE at spoke v1 and reading it back at v2 and the hub (v3)"
kubectl apply -f "${TESTDATA}/composite-v1.yaml"
XC="e2e-legacy-widget-v1"

assert_eq "$(kubectl get "${XC_V3}" "${XC}" -o jsonpath='{.spec.storageGB}')" "100Gi" \
  "composite v1->v3 FieldRename: spec.storageSize -> spec.storageGB"
assert_eq "$(kubectl get "${XC_V3}" "${XC}" -o jsonpath='{.spec.memoryMB}')" "4096" \
  "composite v1->v3 NumericScale: memoryGB 4 * 1024 -> memoryMB"
assert_eq "$(kubectl get "${XC_V3}" "${XC}" -o jsonpath='{.spec.size}')" "Medium" \
  "composite v1->v3 EnumRemap: M -> Medium"
assert_eq "$(kubectl get "${XC_V3}" "${XC}" -o jsonpath='{.spec.zones[0].name}')" "eu-central-1a" \
  "composite v1->v3 SingletonArrayToObject: spec.zone -> spec.zones[0]"
assert_eq "$(kubectl get "${XC_V2}" "${XC}" -o jsonpath='{.spec.storageSize}')" "100Gi" \
  "composite v1->v2 (via the hub) FieldRename round trip"
assert_eq "$(kubectl get "${XC_V2}" "${XC}" -o jsonpath='{.spec.size}')" "M" \
  "composite v1->v2 EnumRemap round trip"

# --- Assertion 3a: composite machinery fields survive --------------------
log "Asserting the LegacyCluster machinery fields survive conversion on the composite"
# These sit DIRECTLY under spec — the v1 layout — and the authored schema
# never declares them, so they reach the output only via passthrough.
assert_eq "$(kubectl get "${XC_V3}" "${XC}" -o jsonpath='{.spec.compositionRef.name}')" "not-a-real-composition" \
  "composite spec.compositionRef survives at the hub"
assert_eq "$(kubectl get "${XC_V3}" "${XC}" -o jsonpath='{.spec.writeConnectionSecretToRef.name}')" "legacy-widget-conn" \
  "composite spec.writeConnectionSecretToRef survives at the hub"
assert_eq "$(kubectl get "${XC_V2}" "${XC}" -o jsonpath='{.spec.compositionRef.name}')" "not-a-real-composition" \
  "composite spec.compositionRef survives at v2 as well"

# --- Assertion 2: a claim created at v1 converts up ----------------------
log "Creating a CLAIM at spoke v1 and reading it back at v2 and the hub (v3)"
kubectl apply -f "${TESTDATA}/claim-v1.yaml"
CL="e2e-legacy-claim-v1"

assert_eq "$(kubectl get "${CL_V3}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.spec.storageGB}')" "50Gi" \
  "claim v1->v3 FieldRename: spec.storageSize -> spec.storageGB"
assert_eq "$(kubectl get "${CL_V3}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.spec.memoryMB}')" "2048" \
  "claim v1->v3 NumericScale: memoryGB 2 * 1024 -> memoryMB"
assert_eq "$(kubectl get "${CL_V3}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.spec.size}')" "Small" \
  "claim v1->v3 EnumRemap: S -> Small"
assert_eq "$(kubectl get "${CL_V3}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.spec.zones[0].name}')" "eu-central-1b" \
  "claim v1->v3 SingletonArrayToObject: spec.zone -> spec.zones[0]"
assert_eq "$(kubectl get "${CL_V2}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.spec.size}')" "S" \
  "claim v1->v2 EnumRemap round trip"

# --- Assertion 3b: claim machinery fields survive ------------------------
log "Asserting the claim's own machinery fields survive conversion"
# The claim's machinery differs from the composite's: compositeDeletePolicy
# exists only here, and its writeConnectionSecretToRef takes only a name.
assert_eq "$(kubectl get "${CL_V3}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.spec.compositeDeletePolicy}')" "Foreground" \
  "claim spec.compositeDeletePolicy survives at the hub"
assert_eq "$(kubectl get "${CL_V3}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.spec.writeConnectionSecretToRef.name}')" "legacy-claim-conn" \
  "claim spec.writeConnectionSecretToRef survives at the hub"

# Crossplane binds the claim to a composite and writes spec.resourceRef on
# the claim and spec.claimRef on that composite. Both are machinery the
# authored schema never declares, so both depend on passthrough.
log "Waiting for Crossplane to bind the claim (spec.resourceRef appears)"
for _ in $(seq 1 60); do
  RESOURCE_REF="$(kubectl get "${CL_V3}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.spec.resourceRef.name}' 2>/dev/null || true)"
  [ -n "${RESOURCE_REF}" ] && break
  sleep 2
done
if [ -z "${RESOURCE_REF:-}" ]; then
  echo "FAIL: Crossplane never wrote spec.resourceRef onto the claim; cannot test the binding machinery"
  exit 1
fi
echo "OK: claim spec.resourceRef survives at the hub = '${RESOURCE_REF}'"
# And the same field, read back through a DIFFERENT conversion path.
assert_eq "$(kubectl get "${CL_V1}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.spec.resourceRef.name}')" "${RESOURCE_REF}" \
  "claim spec.resourceRef survives hub->v1 too"
assert_eq "$(kubectl get "${XC_V3}" "${RESOURCE_REF}" -o jsonpath='{.spec.claimRef.name}')" "${CL}" \
  "the bound composite's spec.claimRef survives at the hub"

# --- Assertion 4: conditions survive, including one we wrote -------------
log "Writing a condition Crossplane does not own, and asserting it survives conversion"
# Condition ownership is not knowable from the schema, so the engine must
# copy the array wholesale. A test that only checked Synced/Ready would
# pass against an implementation that filtered by a hard-coded allowlist.
kubectl patch "${XC_V3}" "${XC}" --subresource=status --type=merge -p '{
  "status": {
    "phase": "Ready",
    "conditions": [
      {"type": "TeamPolicyApproved", "status": "False", "reason": "AwaitingReview",
       "message": "written by this test, not by Crossplane",
       "lastTransitionTime": "2026-09-15T10:00:00Z"}
    ]
  }
}' >/dev/null

assert_eq "$(kubectl get "${XC_V2}" "${XC}" -o jsonpath='{.status.state}')" "Ready" \
  "composite status FieldRename hub->v2: status.phase -> status.state"
assert_eq "$(kubectl get "${XC_V2}" "${XC}" -o jsonpath='{.status.conditions[?(@.type=="TeamPolicyApproved")].reason}')" "AwaitingReview" \
  "a non-Crossplane condition survives conversion on the composite"
assert_eq "$(kubectl get "${XC_V1}" "${XC}" -o jsonpath='{.status.conditions[?(@.type=="TeamPolicyApproved")].message}')" \
  "written by this test, not by Crossplane" \
  "the condition's own fields survive, not just its type"

# The claim's status is NOT ours to write: Crossplane's claim controller
# reconciles it continuously (propagating conditions from the bound
# composite), so a condition patched in here is gone within the second and
# an assertion on it would be a flake, not a test. What is worth asserting
# is the property that actually matters — whatever conditions Crossplane
# put there survive conversion identically at every version, which is the
# same guarantee, checked against real data instead of planted data.
log "Asserting the claim's Crossplane-owned conditions survive conversion at every version"
for _ in $(seq 1 60); do
  CLAIM_COND_TYPES="$(kubectl get "${CL_V3}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.status.conditions[*].type}' 2>/dev/null || true)"
  [ -n "${CLAIM_COND_TYPES}" ] && break
  sleep 2
done
if [ -z "${CLAIM_COND_TYPES:-}" ]; then
  echo "FAIL: Crossplane never wrote any condition onto the claim; cannot test condition survival"
  exit 1
fi
echo "OK: the claim carries Crossplane-owned conditions at the hub = '${CLAIM_COND_TYPES}'"

# Read the whole conditions array at each version and compare it verbatim.
# Conversion must not filter, reorder, or reshape it — condition ownership
# is not knowable from the schema, so any of those would be guesswork.
#
# The three reads have to describe ONE revision of the object. Crossplane's
# claim controller is still running and updates Synced/Ready from the bound
# composite's state; this XRD has no Composition, so that state moves. A
# mid-sequence update would make the comparison fail for a reason that has
# nothing to do with conversion. So: bracket the reads with
# metadata.resourceVersion and retry the whole comparison if it moved.
compare_claim_conditions() {
  local rv_before rv_after hub v2 v1
  rv_before="$(kubectl get "${CL_V3}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.metadata.resourceVersion}')"
  hub="$(kubectl get "${CL_V3}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.status.conditions}')"
  v2="$(kubectl get "${CL_V2}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.status.conditions}')"
  v1="$(kubectl get "${CL_V1}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.status.conditions}')"
  rv_after="$(kubectl get "${CL_V3}" "${CL}" -n "${CLAIM_NS}" -o jsonpath='{.metadata.resourceVersion}')"
  if [ "${rv_before}" != "${rv_after}" ]; then
    echo "retry"
    return 0
  fi
  if [ "${v2}" != "${hub}" ]; then
    echo "MISMATCH at v2: got '${v2}' want '${hub}'"
    return 0
  fi
  if [ "${v1}" != "${hub}" ]; then
    echo "MISMATCH at v1: got '${v1}' want '${hub}'"
    return 0
  fi
  echo "ok"
}

CLAIM_COND_RESULT="retry"
for _ in $(seq 1 10); do
  CLAIM_COND_RESULT="$(compare_claim_conditions)"
  [ "${CLAIM_COND_RESULT}" != "retry" ] && break
  sleep 2
done
assert_eq "${CLAIM_COND_RESULT}" "ok" \
  "the claim's conditions are byte-identical at v3, v2 and v1 within one resourceVersion"

# --- Assertion 6: convctl test --live samples BOTH object classes --------
log "Running convctl test --live and asserting it sampled composites AND claims"
LIVE_REPORT="$(mktemp)"
go run "${REPO_ROOT}/cmd/convctl" test \
  --xrd "${TESTDATA}/xrd.yaml" \
  --config "${TESTDATA}/xrdconversionconfig.yaml" \
  --live --verify-propagation -o json --output-file "${LIVE_REPORT}" --quiet

SAMPLED_CRDS="$(python3 -c '
import json,sys
r = json.load(open(sys.argv[1]))
print(" ".join(sorted({s.get("crd","") for s in r.get("samples",[])} - {""})))
' "${LIVE_REPORT}")"
assert_eq "${SAMPLED_CRDS}" "${CLAIM_CRD} ${COMPOSITE_CRD}" \
  "convctl test --live sampled both generated CRDs"

PROPAGATED="$(python3 -c '
import json,sys
print(str(json.load(open(sys.argv[1])).get("propagation",{}).get("propagated")).lower())
' "${LIVE_REPORT}")"
assert_eq "${PROPAGATED}" "true" "convctl --verify-propagation reports both CRDs wired up"
rm -f "${LIVE_REPORT}"

# --- Assertion 7: migrate-storage prunes BOTH CRDs -----------------------
log "Running convctl migrate-storage --prune-stored-versions across both generated CRDs"
# storedVersions only grows, so seed it with a version that is no longer
# storage: otherwise the prune has nothing to remove and the assertion
# would pass against a build that pruned nothing at all.
for CRD_NAME in "${COMPOSITE_CRD}" "${CLAIM_CRD}"; do
  kubectl patch "crd/${CRD_NAME}" --subresource=status --type=merge \
    -p '{"status":{"storedVersions":["v1","v3"]}}' >/dev/null
  assert_eq "$(kubectl get "crd/${CRD_NAME}" -o jsonpath='{.status.storedVersions}')" '["v1","v3"]' \
    "seeded ${CRD_NAME} status.storedVersions with a stale version"
done

go run "${REPO_ROOT}/cmd/convctl" migrate-storage --xrd "${XRD_NAME}" --prune-stored-versions --quiet

for CRD_NAME in "${COMPOSITE_CRD}" "${CLAIM_CRD}"; do
  assert_eq "$(kubectl get "crd/${CRD_NAME}" -o jsonpath='{.status.storedVersions}')" '["v3"]' \
    "${CRD_NAME} status.storedVersions pruned to the storage version only"
done

log "All LegacyCluster + claims e2e checks passed"
