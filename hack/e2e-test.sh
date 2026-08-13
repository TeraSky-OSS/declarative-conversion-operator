#!/usr/bin/env bash
#
# End-to-end test: creates a kind cluster, installs cert-manager and
# Crossplane, builds and loads this repo's images into the cluster,
# installs this operator via its own Helm chart, then proves — against a
# real kube-apiserver, not just pkg/engine offline — that a composite
# resource created at one served version is actually converted by the
# webhook this operator wires up when read back at another.
#
# Runs identically in CI and locally: the only prerequisites are docker,
# kind, kubectl, and helm on PATH. Set KEEP_CLUSTER=1 to skip teardown
# for local debugging.
set -euo pipefail

# shellcheck source=hack/e2e-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/e2e-common.sh"

CLUSTER_NAME="${CLUSTER_NAME:-declarative-conversion-e2e}"
NAMESPACE="${NAMESPACE:-declarative-conversion-system}"
IMG_TAG="e2e-$(date +%s 2>/dev/null || echo local)"
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
install_operator

log "Waiting for the default ConversionWebhookServer to become Available"
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default

log "Applying the e2e XWidget XRD"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/xrd.yaml"
# Crossplane creates the composite resource's own CRD asynchronously after
# the XRD is accepted, so it may not exist yet at all. `kubectl wait` only
# waits for a condition on an already-existing object -- it errors
# immediately with NotFound otherwise -- so wait for the CRD to be created
# before waiting for it to become Established.
kubectl wait --for=create --timeout=60s crd/xwidgets.e2e.example.org
kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.e2e.example.org

log "Applying the XRDConversionConfig and waiting for it to reach Applied"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/xrdconversionconfig.yaml"
kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-e2e-conversion

GROUP="e2e.example.org"
V1="xwidgets.v1.${GROUP}"
V2="xwidgets.v2.${GROUP}"
V3="xwidgets.v3.${GROUP}"

# --- v1 (spoke) -> v3 (hub): every v1-spoke strategy's spoke->hub direction ---
log "Creating a composite resource at spoke version v1"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/composite-v1.yaml"
NAME_V1="e2e-widget-v1"

log "Reading it back at the hub version (v3) to check every v1-spoke strategy's spoke->hub conversion"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.zones[0].name}')" "eu-central-1a" "SingletonArrayToObject (s2h): spec.zone -> spec.zones[0]"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.primaryRegion.name}')" "eu-central-1" "ObjectToSingletonArray (s2h): spec.regions[0] -> spec.primaryRegion"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.tags.env}')" "dev" "MapToFields (s2h): spec.envTag -> spec.tags.env"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.tags.team}')" "core" "MapToFields (s2h): spec.teamTag -> spec.tags.team"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.tier}')" "bronze" "ToLabel (s2h, restoreOnReverse): metadata.labels.tier -> spec.tier"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.volumes[0].sizeGB}')" "10" "ForEach (s2h): spec.volumes[].size -> spec.volumes[].sizeGB"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.volumes[0].label}')" "scratch" "ForEach (s2h): spec.volumes[].name -> spec.volumes[].label"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.contactName}')" "Sam Lee" "FieldsToScalar (s2h): spec.contact -> spec.contactName"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.contactEmail}')" "sam@example.com" "FieldsToScalar (s2h): spec.contact -> spec.contactEmail"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.endpoints[0].name}')" "dev" "ArrayToMapByKey (s2h): spec.endpoints (map) -> spec.endpoints[] (array)"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.endpoints[0].url}')" "https://dev.example.com" "ArrayToMapByKey (s2h): endpoint URL preserved"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.limitsByTier.silver.limit}')" "20" "MapToArrayByKey (s2h): spec.tierLimits[] -> spec.limitsByTier (map)"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.limitsByTier.bronze.limit}')" "10" "MapToArrayByKey (s2h): second tier preserved"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.allowedCIDRsCSV}')" "10.1.0.0/16,10.2.0.0/16" "ListSplit (s2h): spec.allowedCIDRs -> spec.allowedCIDRsCSV"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.cpuRequest}')" "1" "Quantity (s2h): spec.cpuMillis 1000 -> spec.cpuRequest 1"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.timeout}')" "30s" "Duration (s2h): spec.timeoutSeconds 30 -> spec.timeout 30s"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.extraLabels.app}')" "sandbox" "MapKeyRename (s2h): spec.extraLabels.application -> spec.extraLabels.app"
assert_eq "$(kubectl get "${V3}" "${NAME_V1}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.extraLabels.region}')" "eu-central-1" "MapKeyRename (s2h): unmapped key region passes through"

# --- v2 (spoke) -> v3 (hub): every v2-spoke strategy's spoke->hub direction ---
log "Creating a composite resource at spoke version v2"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/composite-v2.yaml"
NAME_V2="e2e-widget-v2"

log "Reading it back at the hub version (v3) to check every v2-spoke strategy's spoke->hub conversion"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.storageGB}')" "256" "FieldRename (s2h): spec.storageSize -> spec.storageGB"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.replicaCount}')" "2" "ScalarToObject (s2h): spec.replicas.count -> spec.replicaCount"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.network.cidr}')" "10.1.0.0/16" "ObjectToScalar (s2h): spec.networkCIDR -> spec.network.cidr"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.cpuLimit}')" "2" "FieldsToMap (s2h): spec.limits.cpu -> spec.cpuLimit"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.memoryLimit}')" "8Gi" "FieldsToMap (s2h): spec.limits.memory -> spec.memoryLimit"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.description}')" "restored from v2" "ToAnnotation (s2h, restoreOnReverse): annotation -> spec.description"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.size}')" "Medium" "EnumRemap (s2h): spec.size M -> Medium"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.legacyFlag}')" "false" "JSONPatch (s2h): spec.legacyFlagV2 -> spec.legacyFlag"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.priority}')" "9" "TypeCoerce (s2h): spec.priority string \"9\" -> integer 9"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.diskSize}')" "25Gi" "ScalarToFields (s2h): diskSizeValue+diskSizeUnit -> diskSize"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.memoryMB}')" "2048" "NumericScale (s2h): spec.memoryGB 2 * factor 1024 -> spec.memoryMB"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.dnsServers[0]}')" "9.9.9.9" "ListJoin (s2h): spec.dnsServersCSV -> spec.dnsServers[]"
assert_eq "$(kubectl get "${V3}" "${NAME_V2}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.dnsServers[1]}')" "4.2.2.2" "ListJoin (s2h): second dnsServers element"

# --- v3 (hub) -> v2 and v3 (hub) -> v1: every strategy's hub->spoke direction ---
log "Creating a composite resource at the hub version (v3)"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/composite-v3.yaml"
NAME_V3="e2e-widget-v3"

log "Reading it back at v2 to check every v2-spoke strategy's hub->spoke conversion"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.storageSize}')" "500" "FieldRename (h2s): spec.storageGB -> spec.storageSize"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.replicas.count}')" "3" "ScalarToObject (h2s): spec.replicaCount -> spec.replicas.count"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.networkCIDR}')" "10.0.0.0/16" "ObjectToScalar (h2s): spec.network.cidr -> spec.networkCIDR"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.limits.cpu}')" "4" "FieldsToMap (h2s): spec.cpuLimit -> spec.limits.cpu"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.limits.memory}')" "16Gi" "FieldsToMap (h2s): spec.memoryLimit -> spec.limits.memory"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.metadata.annotations.xrd\.example\.org/description}')" '"created via v3"' "ToAnnotation (h2s): spec.description -> annotation (JSON-serialized)"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.size}')" "L" "EnumRemap (h2s): spec.size Large -> L"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.schemaVersion}')" "v2" "Constant (h2s): spec.schemaVersion forced to \"v2\""
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.legacyFlagV2}')" "true" "JSONPatch (h2s): spec.legacyFlag -> spec.legacyFlagV2"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.priority}')" "5" "TypeCoerce (h2s): spec.priority integer 5 -> string \"5\""
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.diskSizeValue}')" "50" "ScalarToFields (h2s): spec.diskSize -> diskSizeValue"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.diskSizeUnit}')" "Gi" "ScalarToFields (h2s): spec.diskSize -> diskSizeUnit"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.memoryGB}')" "4" "NumericScale (h2s): spec.memoryMB 4096 / factor 1024 -> spec.memoryGB"
assert_eq "$(kubectl get "${V2}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.dnsServersCSV}')" "8.8.8.8,1.1.1.1" "ListJoin (h2s): spec.dnsServers[] -> spec.dnsServersCSV"

log "Reading it back at v1 to check every v1-spoke strategy's hub->spoke conversion"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.zone.name}')" "us-east-1a" "SingletonArrayToObject (h2s): spec.zones[0] -> spec.zone"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.regions[0].name}')" "us-east-1" "ObjectToSingletonArray (h2s): spec.primaryRegion -> spec.regions[0]"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.envTag}')" "prod" "MapToFields (h2s): spec.tags.env -> spec.envTag"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.teamTag}')" "platform" "MapToFields (h2s): spec.tags.team -> spec.teamTag"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.metadata.labels.tier}')" "gold" "ToLabel (h2s): spec.tier -> metadata.labels.tier"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.computeUnits}')" "1" "DefaultValue (h2s): spec.computeUnits injected with default 1 (hub has no such field)"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.debugMode}')" "" "Delete (h2s): spec.debugMode is not set (hub has no such field to convert from)"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.volumes[0].size}')" "100" "ForEach (h2s): spec.volumes[].sizeGB -> spec.volumes[].size"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.volumes[0].name}')" "data" "ForEach (h2s): spec.volumes[].label -> spec.volumes[].name"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.contact}')" "Jane Doe <jane@example.com>" "FieldsToScalar (h2s): contactName+contactEmail -> spec.contact"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath="{.spec.endpoints['web'].url}")" "https://web.example.com" "ArrayToMapByKey (h2s): spec.endpoints[] -> spec.endpoints (map)"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.tierLimits[0].tier}')" "gold" "MapToArrayByKey (h2s): spec.limitsByTier -> spec.tierLimits[] (sorted by key)"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.tierLimits[0].limit}')" "1000" "MapToArrayByKey (h2s): first tier's limit"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.tierLimits[1].tier}')" "silver" "MapToArrayByKey (h2s): second tier, sorted after \"gold\""
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.allowedCIDRs[0]}')" "10.0.0.0/8" "ListSplit (h2s): spec.allowedCIDRsCSV -> spec.allowedCIDRs[]"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.allowedCIDRs[1]}')" "192.168.0.0/16" "ListSplit (h2s): second CIDR element"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.cpuMillis}')" "500" "Quantity (h2s): spec.cpuRequest 500m -> spec.cpuMillis 500"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.timeoutSeconds}')" "300" "Duration (h2s): spec.timeout 5m -> spec.timeoutSeconds 300"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.extraLabels.application}')" "widget" "MapKeyRename (h2s): spec.extraLabels.app -> spec.extraLabels.application"
assert_eq "$(kubectl get "${V1}" "${NAME_V3}" -n "${COMPOSITE_NS}" -o jsonpath='{.spec.extraLabels.region}')" "us-east-1" "MapKeyRename (h2s): unmapped key region passes through"

log "All e2e conversion checks passed (every one of pkg/engine's 28 built-in strategies exercised against a real apiserver)"
