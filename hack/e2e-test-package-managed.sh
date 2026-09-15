#!/usr/bin/env bash
#
# End-to-end test for the XRD conversion guard: proves that an XRD whose
# spec.conversion is stripped by an out-of-band full replace never serves a
# single unconverted read, and — critically — that this test can DETECT the
# failure when the guard is turned off.
#
# WHAT IT REPRODUCES, AND HOW FAITHFULLY
#
# Crossplane's package establisher ends in a full client.Update from the
# package contents (internal/controller/pkg/revision/establisher.go, under
# an upstream comment reading "This should be a server side apply?"). Its
# merge step preserves nothing for XRDs. A full Update is a replace: it is
# not merge-aware and it ignores Server-Side Apply field ownership, so
# spec.conversion and this operator's annotations are removed outright.
# Establish runs on every revision reconcile, with no diff check, and the
# default --sync resync is one hour.
#
# `kubectl replace -f <the package's XRD>` is that exact API call: a full,
# non-SSA Update of the whole object from the package's copy. This script
# drives it in a loop, against an XRD carrying a ConfigurationRevision
# owner reference, so the operator sees precisely the object shape and
# precisely the write it would see from a real package resync.
#
# WHAT IT DOES NOT REPRODUCE: the Crossplane package machinery around that
# write — building an xpkg, pushing it to a registry the cluster can pull
# from, and installing it as a Configuration. Doing that in CI needs an
# in-cluster registry Crossplane will pull over TLS, which is a large
# amount of infrastructure for a test whose subject is the apiserver write,
# not the package manager. The write itself, the object shape, and the
# detection loop are all real. See the tracking issue for the full
# Configuration variant.
#
# THE EXPERIMENTAL DESIGN
#
# "No errors" proves nothing here. With conversion gone the apiserver serves
# a stored v2 object at v1 by RELABELLING apiVersion and returning the
# original field layout — HTTP 200, no error, wrong data. So every read is
# checked field by field.
#
# Three claims, each with an assertion that does not depend on winning a
# race:
#
#   Phase 1 (guard on). After every full replace, the XRD ITSELF still
#   carries the conversion stanza. The guard acts inside the admission
#   request, so the stripped state is not merely short-lived — it is
#   unreachable. One API read after each replace settles it, with no timing
#   involved. The read loop must also come back clean.
#
#   Phase 2a (guard off). The same replaces must now CATCH the XRD stripped.
#   Without this, phase 1 proves nothing: a replace that never strips
#   anything would pass it trivially.
#
#   Phase 2b (guard off, manager scaled to zero). Nothing re-applies, so
#   conversion is durably gone and the generated CRD falls back to
#   strategy: None — verified before asserting anything. The read detector
#   must then report the object as wrong. This is what proves the detector
#   can fail at all, and it is also a real scenario: the guard fails OPEN by
#   design, so "operator down" is exactly the degraded mode it accepts.
#
# A guard test that cannot fail is not a test, which is why 2a and 2b are
# both here rather than trusted.
#
# docker, kind, kubectl, helm and python3 on PATH. KEEP_CLUSTER=1 skips
# teardown.
set -euo pipefail

# shellcheck source=hack/e2e-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/e2e-common.sh"

CLUSTER_NAME="${CLUSTER_NAME:-declarative-conversion-e2e-packaged}"
NAMESPACE="${NAMESPACE:-declarative-conversion-system}"
IMG_TAG="e2e-packaged-$(date +%s 2>/dev/null || echo local)"
MANAGER_IMG="ghcr.io/terasky-oss/declarative-conversion-operator:${IMG_TAG}"
WEBHOOK_IMG="ghcr.io/terasky-oss/declarative-conversion-webhook-server:${IMG_TAG}"
CERT_MANAGER_VERSION="v1.21.1"
RELEASE_NAME="declarative-conversion-operator"

TESTDATA="${REPO_ROOT}/test/e2e/testdata/packaged"
GROUP="e2e.example.org"
XRD_NAME="xpackagedwidgets.${GROUP}"
CONFIG_NAME="xpackagedwidgets-e2e-conversion"
REVISION_NAME="e2e-platform-abc123"
# Deliberately not a real image: the revision exists to own the XRD and to
# make PackageManaged true, not to establish anything.
REVISION_IMAGE="ghcr.io/terasky-oss/e2e-nonexistent-configuration:v0.0.0"
REVISION_UID=""
OBJ_NS="default"
OBJ_NAME="e2e-packaged-widget"
V1="xpackagedwidgets.v1.${GROUP}"

# How many replace/read rounds each phase runs. Each round is one full
# non-SSA replace plus a burst of reads at the non-storage version.
ROUNDS="${ROUNDS:-12}"
READS_PER_ROUND="${READS_PER_ROUND:-15}"

trap e2e_cleanup EXIT

require_cmd docker
require_cmd kind
require_cmd kubectl
require_cmd helm
require_cmd python3

# --- helpers --------------------------------------------------------------

# create_configuration_revision creates a REAL ConfigurationRevision to own
# the XRD.
#
# A fabricated owner reference is not an option: Kubernetes' garbage
# collector deletes any object whose owner does not exist, so an XRD
# stamped with a made-up UID is reaped within seconds and the test silently
# has nothing to work on. (Learned the hard way — the first version of this
# script sat waiting for a CRD that the GC had already removed.)
#
# The revision points at an image that does not exist, so Crossplane's
# revision controller never establishes anything from it. That is fine and
# deliberate: what this test needs from it is a real owner UID and a real
# PackageManaged signal, not a working package.
create_configuration_revision() {
  cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: pkg.crossplane.io/v1
kind: ConfigurationRevision
metadata:
  name: ${REVISION_NAME}
spec:
  desiredState: Inactive
  image: ${REVISION_IMAGE}
  revision: 1
YAML
  REVISION_UID="$(kubectl get configurationrevision "${REVISION_NAME}" -o jsonpath='{.metadata.uid}')"
  if [ -z "${REVISION_UID}" ]; then
    echo "FAIL: could not read the ConfigurationRevision's UID"
    exit 1
  fi
}

# apply_packaged_xrd writes the XRD exactly as a package carries it, owned
# by the ConfigurationRevision above, so the operator's PackageManaged
# condition reflects a genuinely package-managed object.
apply_packaged_xrd() {
  xrd_with_owner | kubectl apply -f - >/dev/null
}

# xrd_with_owner emits the packaged XRD plus the owner reference the
# establisher sets. Written as text rather than through a YAML library so
# the fixture file stays the single source of truth for the XRD's content.
# resourceVersion is included only when one is passed, since `apply` must
# not carry one and `replace` must.
xrd_with_owner() {
  DCO_RV="${1:-}" DCO_REV_NAME="${REVISION_NAME}" DCO_REV_UID="${REVISION_UID}" \
    python3 - "${TESTDATA}/xrd.yaml" <<'PY'
import os, sys
src = open(sys.argv[1]).read()
marker = "  name: xpackagedwidgets.e2e.example.org\n"
assert marker in src, "XRD fixture shape changed"
extra = ""
if os.environ.get("DCO_RV"):
    extra += '  resourceVersion: "%s"\n' % os.environ["DCO_RV"]
extra += (
    "  ownerReferences:\n"
    "    - apiVersion: pkg.crossplane.io/v1\n"
    "      kind: ConfigurationRevision\n"
    "      name: %s\n"
    "      uid: %s\n" % (os.environ["DCO_REV_NAME"], os.environ["DCO_REV_UID"])
)
print(src.replace(marker, marker + extra, 1))
PY
}

# replace_from_package is the establisher's write: a full, non-SSA Update of
# the whole object from the package's copy. Everything the operator added —
# spec.conversion, both annotations — is absent from that copy, so a plain
# apiserver would drop it.
#
# `kubectl replace` needs the current resourceVersion, which is exactly what
# the establisher supplies too (it copies resourceVersion and
# ownerReferences from the live object and nothing else).
replace_from_package() {
  local rv
  rv="$(kubectl get compositeresourcedefinition "${XRD_NAME}" -o jsonpath='{.metadata.resourceVersion}')"
  xrd_with_owner "${rv}" | kubectl replace -f - >/dev/null
}

# read_v1_is_correct reads the stored v2 object back at v1 and checks the
# CONVERTED field values. It deliberately does not look at exit codes: the
# failure this exists to catch is a successful read of wrong data.
#
# Correct v1 shape (stored v2 is storageGB "200Gi", memoryMB 8192):
#   spec.storageSize == "200Gi"   (FieldRename)
#   spec.memoryGB    == 8         (NumericScale, 8192 / 1024)
#   spec.storageGB   absent       (the hub-side name must not leak through)
read_v1_is_correct() {
  local out
  if ! out="$(kubectl get "${V1}" "${OBJ_NAME}" -n "${OBJ_NS}" -o json 2>/dev/null)"; then
    # A hard error is a different failure from silent corruption, but it is
    # still a failed read, so report it as one.
    echo "read-error"
    return 0
  fi
  # The object goes in through the environment, not stdin: `python3 -` reads
  # its program from stdin, so a second stdin redirection would replace the
  # program with the JSON.
  DCO_READ_JSON="${out}" python3 "${CHECKER}"
}

# write_checker emits the field-level correctness check as a standalone
# script. Stored shape is v2 (storageGB "200Gi", memoryMB 8192), so a
# correct v1 read is storageSize "200Gi" and memoryGB 8 — and, just as
# importantly, carries NEITHER hub-side name. With conversion gone the
# apiserver relabels the stored object and returns the v2 fields verbatim,
# which is what makes their presence the unambiguous tell.
write_checker() {
  CHECKER="$(mktemp)"
  cat >"${CHECKER}" <<'PY'
import json, os
spec = json.loads(os.environ["DCO_READ_JSON"]).get("spec", {})
problems = []
if spec.get("storageSize") != "200Gi":
    problems.append("storageSize=%r" % (spec.get("storageSize"),))
if spec.get("memoryGB") != 8:
    problems.append("memoryGB=%r" % (spec.get("memoryGB"),))
if "storageGB" in spec:
    problems.append("hub-side spec.storageGB leaked through unconverted")
if "memoryMB" in spec:
    problems.append("hub-side spec.memoryMB leaked through unconverted")
print("ok" if not problems else "BAD: " + ", ".join(problems))
PY
}

# xrd_conversion_state reports what the XRD's spec.conversion looks like
# right now: "ours" when it carries a Webhook strategy stamped with this
# config's managed-by annotation, "stripped" when the stanza is gone or
# reset, "other" when it points somewhere else.
#
# This is the RACE-FREE observation. The guard acts inside the admission
# request, so with it enabled the object is never persisted in a stripped
# state at all — there is no window to catch, and no timing to get lucky
# with. With the guard disabled the stripped state is persisted and the
# operator has to notice and re-apply, which is a race.
xrd_conversion_state() {
  local strategy managed
  strategy="$(kubectl get compositeresourcedefinition "${XRD_NAME}" -o jsonpath='{.spec.conversion.strategy}' 2>/dev/null || true)"
  managed="$(kubectl get compositeresourcedefinition "${XRD_NAME}" -o jsonpath='{.metadata.annotations.conversion\.terasky\.com/managed-by}' 2>/dev/null || true)"
  case "${strategy}" in
    Webhook)
      if [ "${managed}" = "${CONFIG_NAME}" ]; then echo "ours"; else echo "other"; fi ;;
    *) echo "stripped" ;;
  esac
}

# hammer runs ROUNDS full replaces. After each one it samples the XRD's own
# conversion state immediately (the race-free signal) and then reads the
# object at the non-storage version a few times (the user-visible
# consequence). It prints "<strippedCount>/<rounds> <badReads>/<totalReads>".
hammer() {
  local stripped=0 bad=0 reads=0 state verdict
  for _ in $(seq 1 "${ROUNDS}"); do
    replace_from_package
    state="$(xrd_conversion_state)"
    if [ "${state}" != "ours" ]; then
      stripped=$((stripped + 1))
      echo "    XRD observed ${state} immediately after a replace" >&2
    fi
    for _ in $(seq 1 "${READS_PER_ROUND}"); do
      verdict="$(read_v1_is_correct)"
      reads=$((reads + 1))
      if [ "${verdict}" != "ok" ]; then
        bad=$((bad + 1))
        echo "    bad read: ${verdict}" >&2
      fi
    done
  done
  echo "${stripped}/${ROUNDS} ${bad}/${reads}"
}

# metric_value sums every series of a manager metric. The manager image is
# distroless, so there is no shell to exec into — scrape the metrics Service
# from a throwaway pod on the cluster network instead.
metric_value() {
  local metric="$1"
  kubectl -n "${NAMESPACE}" run "metric-scrape-${RANDOM}" --rm -i --restart=Never --quiet \
    --image=curlimages/curl:8.11.1 --command -- \
    curl -fsS "http://${RELEASE_NAME}-manager-metrics.${NAMESPACE}.svc:8080/metrics" 2>/dev/null \
    | awk -v m="^${metric}" '$1 ~ m {sum += $2} END {printf "%d", sum+0}'
}

# --- setup ----------------------------------------------------------------

write_checker

create_kind_cluster
build_and_load_images
install_cert_manager
install_crossplane

install_operator
log "Waiting for the default ConversionWebhookServer to become Available"
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default

log "Creating the ConfigurationRevision that will own the XRD"
create_configuration_revision
echo "OK: ConfigurationRevision ${REVISION_NAME} exists (uid ${REVISION_UID})"

log "Applying the packaged XRD (no spec.conversion, owned by that ConfigurationRevision)"
apply_packaged_xrd
kubectl wait --for=create --timeout=120s "crd/${XRD_NAME}"
kubectl wait --for=condition=Established --timeout=60s "crd/${XRD_NAME}"

log "Applying the XRDConversionConfig and waiting for Applied + ConversionPropagated"
kubectl apply -f "${TESTDATA}/xrdconversionconfig.yaml"
kubectl wait --for=condition=Applied --timeout=120s "xrdconversionconfig/${CONFIG_NAME}"
kubectl wait --for=condition=ConversionPropagated --timeout=120s "xrdconversionconfig/${CONFIG_NAME}"

log "Asserting the PackageManaged condition sees the ConfigurationRevision owner"
assert_eq "$(kubectl get "xrdconversionconfig/${CONFIG_NAME}" -o jsonpath='{.status.conditions[?(@.type=="PackageManaged")].status}')" \
  "True" "PackageManaged condition"
PKG_MSG="$(kubectl get "xrdconversionconfig/${CONFIG_NAME}" -o jsonpath='{.status.conditions[?(@.type=="PackageManaged")].message}')"
case "${PKG_MSG}" in
  *e2e-platform-abc123*) echo "OK: the PackageManaged message names the owning revision" ;;
  *) echo "FAIL: PackageManaged message does not name the revision: ${PKG_MSG}"; exit 1 ;;
esac

log "Creating a composite at the hub (v2), so every read at v1 must go through conversion"
kubectl apply -f "${TESTDATA}/composite-v2.yaml"
assert_eq "$(read_v1_is_correct)" "ok" "baseline: a v1 read converts correctly before any replace"

# --- phase 1: guard ENABLED -----------------------------------------------

log "Phase 1 (guard ENABLED): ${ROUNDS} full non-SSA replaces, ${READS_PER_ROUND} v1 reads each"
GUARDED="$(hammer)"
GUARDED_STRIPPED="${GUARDED%% *}"
GUARDED_READS="${GUARDED##* }"
log "guard enabled: XRD stripped ${GUARDED_STRIPPED} times, ${GUARDED_READS} reads wrong"

# THE assertion. The guard restores the stanza inside the same admission
# request, so the XRD is never persisted without it — not briefly, not
# once. This does not depend on the operator winning any race, which is
# precisely what makes it a test of the guard rather than of scheduling luck.
if [ "${GUARDED_STRIPPED%%/*}" != "0" ]; then
  echo "FAIL: with the guard enabled the XRD was observed without its conversion stanza ${GUARDED_STRIPPED} times;"
  echo "      the guard is meant to make that state unreachable, not merely short-lived."
  exit 1
fi
echo "OK: guard enabled — the XRD carried its conversion stanza after every one of ${ROUNDS} full replaces"

if [ "${GUARDED_READS%%/*}" != "0" ]; then
  echo "FAIL: with the guard enabled, ${GUARDED_READS} reads returned unconverted data"
  exit 1
fi
echo "OK: guard enabled — every one of ${GUARDED_READS##*/} reads converted correctly"

assert_eq "$(kubectl get compositeresourcedefinition "${XRD_NAME}" -o jsonpath='{.metadata.annotations.conversion\.terasky\.com/managed-by}')" \
  "${CONFIG_NAME}" "the managed-by annotation was restored too"

# --- phase 2: guard DISABLED ----------------------------------------------
#
# Two things have to be shown here, and they are different claims.
#
#   (a) The guard is what prevented phase 1's stripped state — so without
#       it, the stripped state must actually occur. Observed on the XRD
#       directly, which is one API read after the replace.
#   (b) The read loop can detect the user-visible consequence. Proven with
#       the manager scaled to zero, so nothing re-applies and conversion is
#       genuinely, durably gone — no race to win, and also a real scenario:
#       the guard fails OPEN by design, so "operator down" is exactly the
#       degraded mode it accepts.

log "Phase 2 (guard DISABLED): re-installing with features.crossplane.conversionGuard.enabled=false"
install_operator --set features.crossplane.conversionGuard.enabled=false
kubectl -n "${NAMESPACE}" rollout status deploy/"${RELEASE_NAME}"-manager --timeout=180s
# The MutatingWebhookConfiguration must actually be gone, or phase 2 would
# be testing the guard against itself.
if kubectl get mutatingwebhookconfiguration "${RELEASE_NAME}-mutating-webhook-configuration" >/dev/null 2>&1; then
  echo "FAIL: the guard's MutatingWebhookConfiguration still exists after disabling it"
  exit 1
fi
echo "OK: the guard's MutatingWebhookConfiguration is gone"

kubectl wait --for=condition=Applied --timeout=120s "xrdconversionconfig/${CONFIG_NAME}"
assert_eq "$(read_v1_is_correct)" "ok" "baseline: reads are correct again before phase 2 starts"

log "Phase 2a: the same ${ROUNDS} replaces, expecting the strip to become observable"
UNGUARDED="$(hammer)"
UNGUARDED_STRIPPED="${UNGUARDED%% *}"
UNGUARDED_READS="${UNGUARDED##* }"
log "guard disabled: XRD stripped ${UNGUARDED_STRIPPED} times, ${UNGUARDED_READS} reads wrong"

# Either signal proves the point, and accepting either is what keeps this
# off a race. The XRD-stripped count depends on the operator not winning
# the re-apply race before one API read — likely, but not guaranteed on a
# fast runner. The bad-read count comes from the generated CRD falling back
# to strategy: None, which persists for as long as Crossplane takes to
# re-render it in BOTH directions, so it is the wider window of the two.
# Requiring both would be stricter than the claim being made, which is
# simply: without the guard, the failure is observable.
if [ "${UNGUARDED_STRIPPED%%/*}" = "0" ] && [ "${UNGUARDED_READS%%/*}" = "0" ]; then
  echo "FAIL: with the guard DISABLED, neither the XRD's stripped state nor a single wrong read"
  echo "      was ever observed. Either the replace is not actually stripping anything — in which"
  echo "      case phase 1 proves nothing — or the operator re-applied faster than an API read"
  echo "      every single time. Raise ROUNDS/READS_PER_ROUND, or check that replace_from_package"
  echo "      is really sending a full non-SSA Update."
  exit 1
fi
echo "OK: guard disabled — caught stripped ${UNGUARDED_STRIPPED}, wrong reads ${UNGUARDED_READS}; phase 1's green is the guard's doing"

log "Phase 2b: scaling the manager to zero so nothing re-applies, and proving the read detector fires"
# With the guard off AND nothing re-applying, conversion is durably gone:
# Crossplane re-renders the generated CRD without it and the apiserver falls
# back to relabelling. No race, no timing.
kubectl -n "${NAMESPACE}" scale deploy/"${RELEASE_NAME}"-manager --replicas=0
kubectl -n "${NAMESPACE}" wait --for=delete pod -l "app.kubernetes.io/instance=${RELEASE_NAME},control-plane=controller-manager" --timeout=120s || true
replace_from_package
assert_eq "$(xrd_conversion_state)" "stripped" "with nothing re-applying, the XRD stays stripped"

log "Waiting for Crossplane to re-render the generated CRD without conversion"
for _ in $(seq 1 60); do
  CRD_STRATEGY="$(kubectl get "crd/${XRD_NAME}" -o jsonpath='{.spec.conversion.strategy}' 2>/dev/null || echo None)"
  [ "${CRD_STRATEGY}" != "Webhook" ] && break
  sleep 2
done
assert_eq "${CRD_STRATEGY:-Webhook}" "None" "the generated CRD fell back to strategy: None"

# The moment of truth for the detector: this read MUST come back wrong, with
# HTTP 200 and no error, which is the entire failure mode.
DETECTED="$(read_v1_is_correct)"
if [ "${DETECTED}" = "ok" ]; then
  echo "FAIL: conversion is provably gone (the generated CRD is strategy: None) and the read STILL"
  echo "      looked correct. read_v1_is_correct is not checking what it claims to check, which"
  echo "      would make phase 1's green meaningless."
  exit 1
fi
echo "OK: with conversion provably gone the read came back wrong as expected: ${DETECTED}"

log "Scaling the manager back up"
kubectl -n "${NAMESPACE}" scale deploy/"${RELEASE_NAME}"-manager --replicas=1
kubectl -n "${NAMESPACE}" rollout status deploy/"${RELEASE_NAME}"-manager --timeout=180s

# --- the revert counter ---------------------------------------------------

log "Asserting dco_manager_conversion_reverts_total moved"
REVERTS="$(metric_value "dco_manager_conversion_reverts_total")"
if [ "${REVERTS:-0}" -lt 1 ]; then
  echo "FAIL: dco_manager_conversion_reverts_total is ${REVERTS:-0}; the operator never observed a revert"
  exit 1
fi
echo "OK: dco_manager_conversion_reverts_total = ${REVERTS}"

# --- restore the guard ----------------------------------------------------

log "Re-enabling the guard and confirming the config recovers"
install_operator
kubectl -n "${NAMESPACE}" rollout status deploy/"${RELEASE_NAME}"-manager --timeout=180s
kubectl wait --for=condition=Applied --timeout=120s "xrdconversionconfig/${CONFIG_NAME}"
kubectl wait --for=condition=ConversionPropagated --timeout=180s "xrdconversionconfig/${CONFIG_NAME}"
assert_eq "$(read_v1_is_correct)" "ok" "reads are correct again with the guard restored"

log "All package-managed e2e checks passed"
