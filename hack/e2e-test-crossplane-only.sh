#!/usr/bin/env bash
#
# End-to-end test for the "Crossplane only, native CRD support disabled"
# deployment shape: installs the operator with features.nativeCRD.enabled
# set to false, then proves two things against a real kube-apiserver:
#
#   1. XRD/Crossplane conversion still works exactly as it does with both
#      features enabled -- disabling native CRD support must not regress
#      the XRD path. This re-checks a representative sample of strategies
#      rather than the full 23 hack/e2e-test.sh already covers, since the
#      underlying conversion behavior isn't what this scenario exists to
#      test -- the toggle's effect is.
#   2. A CRDConversionConfig against a native CRD is rejected outright by
#      the admission webhook (native CRD support is disabled, so it could
#      never be reconciled).
#
# Runs identically in CI and locally: the only prerequisites are docker,
# kind, kubectl, and helm on PATH. Set KEEP_CLUSTER=1 to skip teardown for
# local debugging.
set -euo pipefail

# shellcheck source=hack/e2e-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/e2e-common.sh"

CLUSTER_NAME="${CLUSTER_NAME:-declarative-conversion-e2e-crossplane-only}"
NAMESPACE="${NAMESPACE:-declarative-conversion-system}"
IMG_TAG="e2e-crossplane-only-$(date +%s 2>/dev/null || echo local)"
MANAGER_IMG="ghcr.io/terasky-oss/declarative-conversion-operator:${IMG_TAG}"
WEBHOOK_IMG="ghcr.io/terasky-oss/declarative-conversion-webhook-server:${IMG_TAG}"
CERT_MANAGER_VERSION="v1.21.1"
RELEASE_NAME="declarative-conversion-operator"
COMPOSITE_NS="default"

trap e2e_cleanup EXIT

require_cmd docker
require_cmd kind
require_cmd kubectl
require_cmd helm

create_kind_cluster
build_and_load_images
install_cert_manager
install_crossplane

install_operator \
  --set features.crossplane.enabled=true \
  --set features.nativeCRD.enabled=false

log "Waiting for the default ConversionWebhookServer to become Available"
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default

log "Applying the e2e XWidget XRD"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/xrd.yaml"
kubectl wait --for=create --timeout=60s crd/xwidgets.e2e.example.org
kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.e2e.example.org

log "Applying the XRDConversionConfig and waiting for it to reach Applied"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/xrdconversionconfig.yaml"
kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-e2e-conversion

GROUP="e2e.example.org"
V1="xwidgets.v1.${GROUP}"
V2="xwidgets.v2.${GROUP}"
V3="xwidgets.v3.${GROUP}"

log "Creating a composite resource at spoke version v1 and reading it back at the hub (v3)"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/composite-v1.yaml"
NAME_V1="e2e-widget-v1"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.zones[0].name}')" "eu-central-1a" "SingletonArrayToObject (s2h): spec.zone -> spec.zones[0]"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.volumes[0].sizeGB}')" "10" "ForEach (s2h): spec.volumes[].size -> spec.volumes[].sizeGB"

log "Creating a composite resource at spoke version v2 and reading it back at the hub (v3)"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/composite-v2.yaml"
NAME_V2="e2e-widget-v2"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.storageGB}')" "256" "FieldRename (s2h): spec.storageSize -> spec.storageGB"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.description}')" "restored from v2" "ToAnnotation (s2h, restoreOnReverse): annotation -> spec.description"

log "Creating a composite resource at the hub version (v3) and reading it back at v2 and v1"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/composite-v3.yaml"
NAME_V3="e2e-widget-v3"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.size}')" "L" "EnumRemap (h2s): spec.size Large -> L"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.memoryGB}')" "4" "NumericScale (h2s): spec.memoryMB 4096 / factor 1024 -> spec.memoryGB"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.computeUnits}')" "1" "DefaultValue (h2s): spec.computeUnits injected with default 1 (hub has no such field)"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.debugMode}')" "" "Delete (h2s): spec.debugMode is not set (hub has no such field to convert from)"

log "Confirming a CRDConversionConfig is rejected outright (native CRD support is disabled on this installation)"
assert_apply_rejected "${REPO_ROOT}/test/e2e/testdata/crdconversionconfig.yaml" \
  "native CRD conversion support is disabled on this installation" \
  "CRDConversionConfig apply rejected while --enable-crd-support=false"

log "All Crossplane-only e2e checks passed (XRD conversion unaffected; native CRD support correctly rejected)"
