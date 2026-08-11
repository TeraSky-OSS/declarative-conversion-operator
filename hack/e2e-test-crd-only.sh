#!/usr/bin/env bash
#
# End-to-end test for the "native CRD only, no Crossplane installed"
# deployment shape: installs the operator with features.crossplane.enabled
# set to false and Crossplane never installed at all, then proves two
# things against a real kube-apiserver:
#
#   1. The manager and webhook-server actually come up healthy on a
#      cluster with no Crossplane CRDs present at all -- with XRD support
#      disabled, neither ever watches Crossplane's
#      CompositeResourceDefinition GVK, so there's nothing to crash on.
#   2. A CRDConversionConfig against a plain native CRD is reconciled and
#      actually converts objects, while an XRDConversionConfig is rejected
#      outright by the admission webhook (XRD support is disabled, so it
#      could never be reconciled).
#
# Runs identically in CI and locally: the only prerequisites are docker,
# kind, kubectl, and helm on PATH. Set KEEP_CLUSTER=1 to skip teardown for
# local debugging.
set -euo pipefail

# shellcheck source=hack/e2e-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/e2e-common.sh"

CLUSTER_NAME="${CLUSTER_NAME:-declarative-conversion-e2e-crd-only}"
NAMESPACE="${NAMESPACE:-declarative-conversion-system}"
IMG_TAG="e2e-crd-only-$(date +%s 2>/dev/null || echo local)"
MANAGER_IMG="ghcr.io/terasky-oss/declarative-conversion-operator:${IMG_TAG}"
WEBHOOK_IMG="ghcr.io/terasky-oss/declarative-conversion-webhook-server:${IMG_TAG}"
CERT_MANAGER_VERSION="v1.21.1"
RELEASE_NAME="declarative-conversion-operator"
GADGET_NS="default"

trap e2e_cleanup EXIT

require_cmd docker
require_cmd kind
require_cmd kubectl
require_cmd helm

create_kind_cluster
build_and_load_images
install_cert_manager
# Deliberately no install_crossplane here -- proving the operator works
# without it is the entire point of this scenario.

install_operator \
  --set features.crossplane.enabled=false \
  --set features.nativeCRD.enabled=true

log "Confirming the manager pod is healthy (no Crossplane CRDs on this cluster at all)"
kubectl -n "${NAMESPACE}" wait --for=condition=Available --timeout=120s \
  deployment -l "app.kubernetes.io/instance=${RELEASE_NAME},control-plane=controller-manager"

log "Waiting for the default ConversionWebhookServer to become Available"
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default

log "Applying the native Gadget CRD"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/crd.yaml"
kubectl wait --for=condition=Established --timeout=60s crd/gadgets.nativecrd.example.org

log "Applying the CRDConversionConfig and waiting for it to reach Applied"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/crdconversionconfig.yaml"
kubectl wait --for=condition=Applied --timeout=120s crdconversionconfig/gadgets-e2e-conversion

GROUP="nativecrd.example.org"
V1="gadgets.v1.${GROUP}"
V2="gadgets.v2.${GROUP}"

log "Creating a Gadget at spoke version v1 (also carrying a pre-existing stashed description annotation)"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/gadget-v1.yaml"
NAME_V1="e2e-gadget-v1"

log "Reading it back at the hub version (v2)"
assert_eq "$(kubectl get "${V2}" "${NAME_V1}" -n "${GADGET_NS}" -o jsonpath='{.spec.storageGB}')" "50" "FieldRename (s2h): spec.storageSize -> spec.storageGB"
assert_eq "$(kubectl get "${V2}" "${NAME_V1}" -n "${GADGET_NS}" -o jsonpath='{.spec.replicas}')" "1" "DefaultValue (s2h): spec.replicas injected with default 1 (spoke has no such field)"
assert_eq "$(kubectl get "${V2}" "${NAME_V1}" -n "${GADGET_NS}" -o jsonpath='{.spec.description}')" "restored from v1" "ToAnnotation (s2h, restoreOnReverse): annotation -> spec.description"

log "Creating a Gadget at the hub version (v2)"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/gadget-v2.yaml"
NAME_V2="e2e-gadget-v2"

log "Reading it back at spoke version v1"
assert_eq "$(kubectl get "${V1}" "${NAME_V2}" -n "${GADGET_NS}" -o jsonpath='{.spec.storageSize}')" "100" "FieldRename (h2s): spec.storageGB -> spec.storageSize"
assert_eq "$(kubectl get "${V1}" "${NAME_V2}" -n "${GADGET_NS}" -o jsonpath='{.metadata.annotations.nativecrd\.example\.org/description}')" '"created via v2"' "ToAnnotation (h2s): spec.description -> annotation (JSON-serialized)"
assert_eq "$(kubectl get "${V1}" "${NAME_V2}" -n "${GADGET_NS}" -o jsonpath='{.spec.debugMode}')" "" "Delete (h2s): spec.debugMode is not set (hub has no such field to convert from)"

log "Confirming an XRDConversionConfig is rejected outright (XRD support is disabled on this installation)"
assert_apply_rejected "${REPO_ROOT}/test/e2e/testdata/xrdconversionconfig.yaml" \
  "XRD conversion support is disabled on this installation" \
  "XRDConversionConfig apply rejected while --enable-xrd-support=false"

log "All CRD-only e2e checks passed (native CRD conversion works with no Crossplane installed; XRD support correctly rejected)"
