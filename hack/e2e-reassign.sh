#!/usr/bin/env bash
#
# Reassignment e2e: move a target from one ConversionWebhookServer to
# another while sustained reads and writes flow through it, and assert that
# not one of them failed or came back wrong.
#
# This is the claim automatic sharding rests on. Rebalancing a fleet means
# repointing a target's spec.conversion at a different Service, and the
# apiserver starts calling the new one the moment that write lands. If the
# new instance has not compiled the plan yet, every read and write of the
# resource fails until it has — an outage caused by a scaling decision,
# affecting resources that had nothing to do with it.
#
# Two moves are exercised, because they fail differently:
#
#   1. An explicit webhookServerRef change — the manual case, which has
#      always been possible and has always had this window.
#   2. Enabling sharding, which moves a share of the unpinned configs
#      without anyone naming a target at all.
#
# The assertion is on correctness as well as on errors, for the same reason
# the soak's is: a read that returns HTTP 200 with the wrong value is worse
# than one that fails, because nothing reports it.
#
#   hack/e2e-reassign.sh [--duration SECONDS] [--objects N]
#
# Prerequisites: docker, kind, kubectl, helm, python3.
# Set KEEP_CLUSTER=1 to skip teardown.
set -euo pipefail

# shellcheck source=hack/e2e-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/e2e-common.sh"

CLUSTER_NAME="${CLUSTER_NAME:-declarative-conversion-e2e-reassign}"
NAMESPACE="${NAMESPACE:-declarative-conversion-system}"
IMG_TAG="e2e-reassign-$(date +%s 2>/dev/null || echo local)"
MANAGER_IMG="ghcr.io/terasky-oss/declarative-conversion-operator:${IMG_TAG}"
WEBHOOK_IMG="ghcr.io/terasky-oss/declarative-conversion-webhook-server:${IMG_TAG}"
CERT_MANAGER_VERSION="v1.21.1"
RELEASE_NAME="declarative-conversion-operator"

PROXY_PORT="${PROXY_PORT:-18003}"
DURATION=240
OBJECTS=8
MOVE_TIMEOUT="${MOVE_TIMEOUT:-180}"
TARGET_CRD="gadgets.nativecrd.example.org"
CONFIG_NAME="gadgets-e2e-conversion"
RESULT_JSON="${RESULT_JSON:-/tmp/e2e-reassign-result.json}"
PROXY_PID=""
CLIENT_PID=""
STOP_FILE="$(mktemp "${TMPDIR:-/tmp}/e2e-reassign-stop.XXXXXX")"
rm -f "${STOP_FILE}"

while [ $# -gt 0 ]; do
  case "$1" in
    --duration) DURATION="$2"; shift 2 ;;
    --objects) OBJECTS="$2"; shift 2 ;;
    --move-timeout) MOVE_TIMEOUT="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

require_positive_int() {
  local name="$1" value="$2"
  case "${value}" in
    ''|*[!0-9]*) echo "FAIL: ${name} must be a positive integer, got '${value}'" >&2; exit 2 ;;
  esac
  if [ "${value}" -le 0 ]; then
    echo "FAIL: ${name} must be greater than zero, got '${value}'" >&2
    exit 2
  fi
}
require_positive_int --duration "${DURATION}"
require_positive_int --objects "${OBJECTS}"
require_positive_int --move-timeout "${MOVE_TIMEOUT}"

reassign_cleanup() {
  local code=$?
  # set +e first: e2e_cleanup has to run even when the test failed, and
  # under `set -e` a non-zero status inside the trap can end it early —
  # leaving the kind cluster behind on exactly the runs where nobody wants
  # a stray cluster.
  set +e
  [ -n "${CLIENT_PID}" ] && kill "${CLIENT_PID}" >/dev/null 2>&1
  [ -n "${PROXY_PID}" ] && kill "${PROXY_PID}" >/dev/null 2>&1
  rm -f "${STOP_FILE}"
  (exit "${code}")
  e2e_cleanup
}
trap reassign_cleanup EXIT

require_cmd docker
require_cmd kind
require_cmd kubectl
require_cmd helm
require_cmd python3

# wait_for_assignment blocks until the config reports the named server AND
# the webhook URL the operator wrote actually points at it. The status field
# alone is not enough: it is set before the gate, so it names the intended
# server while the target still points at the old one, which is exactly the
# state this test is about.
wait_for_assignment() {
  local want="$1" deadline=$(( $(date +%s) + MOVE_TIMEOUT ))
  while [ "$(date +%s)" -lt "${deadline}" ]; do
    local assigned url
    assigned="$(kubectl get crdconversionconfig "${CONFIG_NAME}" -o jsonpath='{.status.assignedWebhookServer}' 2>/dev/null || true)"
    url="$(kubectl get crdconversionconfig "${CONFIG_NAME}" -o jsonpath='{.status.webhookURL}' 2>/dev/null || true)"
    if [ "${assigned}" = "${want}" ] && case "${url}" in *"${want}-webhook-server"*) true ;; *) false ;; esac; then
      echo "OK: ${CONFIG_NAME} is served by ${want} (${url})"
      return 0
    fi
    sleep 2
  done
  echo "FAIL: ${CONFIG_NAME} did not move to ${want} within ${MOVE_TIMEOUT}s"
  kubectl get crdconversionconfig "${CONFIG_NAME}" -o yaml || true
  kubectl get conversionwebhookserver -o yaml || true
  exit 1
}

# assert_handover_verified proves the Lease-backed readiness signal is what
# allowed the move, rather than the compatibility fallback for a fleet that
# publishes nothing. Without this the test would pass just as happily with
# the whole mechanism removed.
assert_handover_verified() {
  local reason
  reason="$(kubectl get crdconversionconfig "${CONFIG_NAME}" \
    -o jsonpath='{.status.conditions[?(@.type=="HandoverReady")].reason}' 2>/dev/null || true)"
  if [ "${reason}" != "HandoverReady" ]; then
    echo "FAIL: HandoverReady reason is '${reason}', want 'HandoverReady'."
    echo "      The move went through without the destination confirming it could serve the target,"
    echo "      which means the readiness signal is not working and the window is still open."
    kubectl get crdconversionconfig "${CONFIG_NAME}" -o yaml || true
    kubectl -n "${NAMESPACE}" get leases -l conversion.terasky.com/webhook-server -o yaml || true
    exit 1
  fi
  echo "OK: the handover was verified against the destination's published served targets"
}

create_kind_cluster
build_and_load_images
install_cert_manager
# Native CRD only: the reassignment window is a property of the webhook
# Service the target names, identical whether that target is a CRD or an
# XRD, and skipping Crossplane takes several minutes off the setup.
install_operator \
  --set features.crossplane.enabled=false \
  --set features.nativeCRD.enabled=true

log "Waiting for the default ConversionWebhookServer to become Available"
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default

log "Creating a second ConversionWebhookServer"
cat <<EOF | kubectl apply -f -
apiVersion: terasky.com/v1alpha1
kind: ConversionWebhookServer
metadata:
  name: shard-b
spec:
  namespace: ${NAMESPACE}
  replicas: 2
  serviceAccountName: ${RELEASE_NAME}-webhook-server
  image:
    repository: ghcr.io/terasky-oss/declarative-conversion-webhook-server
    tag: ${IMG_TAG}
    pullPolicy: Never
  certificate:
    issuerRef:
      name: ${RELEASE_NAME}-selfsigned-issuer
      kind: ClusterIssuer
EOF
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/shard-b

# The shared kind helper reuses an existing cluster so local iteration does
# not pay for a fresh control plane every time, which means this script has
# to be idempotent against the state its own previous run left behind — a
# sharded pool and a pinned config would make the first assertion below
# fail for a reason that has nothing to do with the code.
#
# Order matters: shard-b leaves the pool first. Taking the default instance
# out while shard-b is still in it would leave a pool the default is not a
# member of, which admission correctly rejects.
log "Resetting any assignment state left by a previous run"
kubectl patch conversionwebhookserver shard-b --type=merge -p '{"spec":{"sharding":null}}' >/dev/null 2>&1 || true
kubectl patch conversionwebhookserver default --type=merge -p '{"spec":{"sharding":null}}' >/dev/null 2>&1 || true
kubectl patch crdconversionconfig "${CONFIG_NAME}" --type=json \
  -p '[{"op":"remove","path":"/spec/webhookServerRef"}]' >/dev/null 2>&1 || true

log "Applying the native Gadget CRD and CRDConversionConfig"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/crd.yaml"
kubectl wait --for=condition=Established --timeout=60s "crd/${TARGET_CRD}"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/crdconversionconfig.yaml"
kubectl wait --for=condition=Applied --timeout=120s "crdconversionconfig/${CONFIG_NAME}"
wait_for_assignment default

# Both instances must be publishing before the first move, or the move would
# take the unverified fallback path and prove nothing.
log "Waiting for both instances to publish their served targets"
deadline=$(( $(date +%s) + MOVE_TIMEOUT ))
while [ "$(date +%s)" -lt "${deadline}" ]; do
  a="$(kubectl get conversionwebhookserver default -o jsonpath='{.status.reportingReplicas}' 2>/dev/null || echo 0)"
  b="$(kubectl get conversionwebhookserver shard-b -o jsonpath='{.status.reportingReplicas}' 2>/dev/null || echo 0)"
  if [ "${a:-0}" -ge 1 ] && [ "${b:-0}" -ge 1 ]; then
    echo "OK: default reports ${a} replicas, shard-b reports ${b}"
    break
  fi
  sleep 2
done
if [ "${a:-0}" -lt 1 ] || [ "${b:-0}" -lt 1 ]; then
  echo "FAIL: replicas never published their served targets (default=${a:-0}, shard-b=${b:-0});"
  echo "      check the webhook-server Lease RBAC in namespace ${NAMESPACE}"
  kubectl -n "${NAMESPACE}" get leases || true
  exit 1
fi

log "Seeding ${OBJECTS} Gadgets at the storage version"
names=""
for i in $(seq 1 "${OBJECTS}"); do
  name="move-${i}"
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: nativecrd.example.org/v2
kind: Gadget
metadata:
  name: ${name}
  namespace: default
spec:
  storageGB: "${i}"
  replicas: 1
EOF
  names="${names}${names:+,}${name}"
done

log "Starting kubectl proxy on 127.0.0.1:${PROXY_PORT}"
kubectl proxy --port="${PROXY_PORT}" >/dev/null 2>&1 &
PROXY_PID=$!
for _ in $(seq 1 30); do
  if curl -sf "http://127.0.0.1:${PROXY_PORT}/healthz" >/dev/null 2>&1; then break; fi
  sleep 1
done

# Prove the checker can tell right from wrong before trusting it to say
# "zero mismatches". A checker that silently passed everything would make
# this whole test a no-op.
log "Self-check: one clean pass before any move"
python3 "${REPO_ROOT}/hack/e2e-soak-client.py" \
  --base "http://127.0.0.1:${PROXY_PORT}" --duration 5 \
  --names "${names}" --out "${RESULT_JSON}.pre"
pre_bad="$(python3 -c "import json,sys; d=json.load(open(sys.argv[1])); print(d['read_failures']+d['write_failures']+d['mismatches'])" "${RESULT_JSON}.pre")"
if [ "${pre_bad}" != "0" ]; then
  echo "FAIL: the cluster was already unhealthy before the first move:"
  cat "${RESULT_JSON}.pre"
  exit 1
fi
log "Self-check passed"

log "Driving reads and writes across three reassignments"
python3 "${REPO_ROOT}/hack/e2e-soak-client.py" \
  --base "http://127.0.0.1:${PROXY_PORT}" --duration "$(( DURATION + 4 * MOVE_TIMEOUT ))" \
  --stop-file "${STOP_FILE}" \
  --names "${names}" --out "${RESULT_JSON}" &
CLIENT_PID=$!

driver_alive() {
  if ! kill -0 "${CLIENT_PID}" 2>/dev/null; then
    echo "FAIL: the traffic driver exited before $1; the move would have been unobserved"
    exit 1
  fi
}

gap=$(( DURATION / 4 ))
sleep "${gap}"
driver_alive "the explicit move to shard-b"

log "Move 1/3: pinning the config to shard-b with an explicit webhookServerRef"
kubectl patch crdconversionconfig "${CONFIG_NAME}" --type=merge \
  -p '{"spec":{"webhookServerRef":{"name":"shard-b"}}}'
wait_for_assignment shard-b
assert_handover_verified

sleep "${gap}"
driver_alive "the move back to default"

log "Move 2/3: unpinning, which returns the config to the default instance"
kubectl patch crdconversionconfig "${CONFIG_NAME}" --type=json \
  -p '[{"op":"remove","path":"/spec/webhookServerRef"}]'
wait_for_assignment default
assert_handover_verified

sleep "${gap}"
driver_alive "the sharding move"

# Enabling sharding on a non-default instance while the default sits outside
# the pool would move every unpinned config at once. Admission rejects it,
# and the rejection is worth asserting: it is the invariant that makes the
# pool safe to prefer over spec.default.
log "Admission must reject a pool the default instance is not in"
if kubectl patch conversionwebhookserver shard-b --type=merge \
    -p '{"spec":{"sharding":{"enabled":true}}}' 2>/dev/null; then
  echo "FAIL: enabling sharding on shard-b alone was accepted; that moves every unpinned config off the default instance in one write"
  exit 1
fi
echo "OK: rejected, as it must be"

log "Move 3/3: enabling sharding on the default instance, then on shard-b"
kubectl patch conversionwebhookserver default --type=merge \
  -p '{"spec":{"sharding":{"enabled":true}}}'
# A pool of one is still the whole pool, so nothing may move here.
wait_for_assignment default
kubectl patch conversionwebhookserver shard-b --type=merge \
  -p '{"spec":{"sharding":{"enabled":true}}}'

# Where the target lands is decided by rendezvous hashing, so the test must
# not assume an answer — it asserts only that it lands somewhere, that the
# choice is stable, and that the traffic never broke.
log "Waiting for the sharded assignment to settle"
deadline=$(( $(date +%s) + MOVE_TIMEOUT ))
settled=""
while [ "$(date +%s)" -lt "${deadline}" ]; do
  assigned="$(kubectl get crdconversionconfig "${CONFIG_NAME}" -o jsonpath='{.status.assignedWebhookServer}' 2>/dev/null || true)"
  url="$(kubectl get crdconversionconfig "${CONFIG_NAME}" -o jsonpath='{.status.webhookURL}' 2>/dev/null || true)"
  case "${url}" in *"${assigned}-webhook-server"*) settled="${assigned}"; break ;; esac
  sleep 2
done
if [ -z "${settled}" ]; then
  echo "FAIL: the sharded assignment never settled"
  kubectl get crdconversionconfig "${CONFIG_NAME}" -o yaml || true
  exit 1
fi
echo "OK: sharding placed ${TARGET_CRD} on ${settled}"

# Rendezvous hashing is deterministic in the target name and the pool, both
# of which are fixed here, so this fixture lands on shard-b. Asserting that
# rather than accepting whatever came out is the point: if it ever resolved
# to `default`, no move would have happened and the run would pass without
# having exercised a sharded handover at all — the one thing this step is
# for. A hash change should fail here and prompt a new fixture, loudly.
assert_eq "${settled}" "shard-b" "sharding moved the target off the default instance"
assert_handover_verified

# Force a reconcile and re-read, rather than re-reading the status the poll
# above already saw. An assignment that is unstable across reconciles —
# flapping between pool members — would otherwise pass this.
kubectl annotate crdconversionconfig "${CONFIG_NAME}" \
  "e2e.terasky.com/poke=$(date +%s)" --overwrite >/dev/null
for _ in $(seq 1 30); do
  again="$(kubectl get crdconversionconfig "${CONFIG_NAME}" -o jsonpath='{.status.assignedWebhookServer}')"
  [ -n "${again}" ] && break
  sleep 1
done
assert_eq "${again}" "${settled}" "the sharded assignment is stable across a fresh reconcile"

sleep "${gap}"
driver_alive "the run finishing"

log "Reassignments complete; asking the traffic driver to stop"
touch "${STOP_FILE}"
wait "${CLIENT_PID}"
CLIENT_PID=""

log "Result:"
cat "${RESULT_JSON}"
echo

python3 - "${RESULT_JSON}" <<'PYCHK'
import json, sys
d = json.load(open(sys.argv[1]))
ok = True

if d.get("stopped_by") == "duration":
    print("FAIL: the traffic driver hit its maximum duration instead of being stopped after the moves; a move did not complete")
    ok = False

if d["reads"] < 100 or d["writes"] < 100:
    print(f"FAIL: too little traffic to prove anything: {d['reads']} reads, {d['writes']} writes")
    ok = False

if d["read_failures"] or d["write_failures"]:
    print(f"FAIL: {d['read_failures']} failed reads and {d['write_failures']} failed writes while the target was being "
          "moved between webhook servers; no target may be unserved during a move")
    ok = False

if d["mismatches"]:
    print(f"FAIL: {d['mismatches']} requests returned successfully with the WRONG converted value")
    ok = False

if not ok:
    for s in d.get("samples", []):
        print(f"  [{s['kind']}] {s['detail']}")
    sys.exit(1)

print(f"OK: {d['reads']} reads and {d['writes']} writes across three reassignments, "
      f"0 failures, 0 wrong values ({d.get('conflicts', 0)} benign write conflicts)")
PYCHK

log "All reassignment assertions passed"
