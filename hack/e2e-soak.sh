#!/usr/bin/env bash
#
# Soak e2e: roll the webhook-server repeatedly while sustained reads and
# writes flow through it, and assert that not one of them failed or came
# back wrong.
#
# A conversion webhook sits in the apiserver's admission path, so a replica
# that stops listening before the apiserver stops being routed to it fails
# every write to every target it serves — for reasons that have nothing to
# do with the deployment. Nothing in a unit test can see that race, and
# nothing in the ordinary e2e suite rolls anything.
#
# The assertion is deliberately on correctness as well as on errors. A read
# that returns HTTP 200 with the wrong value is worse than one that fails,
# because nothing anywhere reports it.
#
# Slow by design (default ~8 minutes of traffic). Run it on a schedule, not
# on every PR:  hack/e2e-soak.sh [--duration SECONDS] [--restarts N]
#
# Prerequisites: docker, kind, kubectl, helm, python3.
# Set KEEP_CLUSTER=1 to skip teardown.
set -euo pipefail

# shellcheck source=hack/e2e-common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/e2e-common.sh"

CLUSTER_NAME="${CLUSTER_NAME:-declarative-conversion-e2e-soak}"
NAMESPACE="${NAMESPACE:-declarative-conversion-system}"
IMG_TAG="e2e-soak-$(date +%s 2>/dev/null || echo local)"
MANAGER_IMG="ghcr.io/terasky-oss/declarative-conversion-operator:${IMG_TAG}"
WEBHOOK_IMG="ghcr.io/terasky-oss/declarative-conversion-webhook-server:${IMG_TAG}"
CERT_MANAGER_VERSION="v1.21.1"
RELEASE_NAME="declarative-conversion-operator"

PROXY_PORT="${PROXY_PORT:-18001}"
# How long a single `rollout status` is allowed to take. Used both for the
# wait itself and to size the traffic driver's backstop, so the two cannot
# disagree.
ROLLOUT_TIMEOUT="${ROLLOUT_TIMEOUT:-300}"
DURATION=480
RESTARTS=4
OBJECTS=8
RESULT_JSON="${RESULT_JSON:-/tmp/e2e-soak-result.json}"
PROXY_PID=""
CLIENT_PID=""
STOP_FILE="$(mktemp "${TMPDIR:-/tmp}/e2e-soak-stop.XXXXXX")"
rm -f "${STOP_FILE}"

while [ $# -gt 0 ]; do
  case "$1" in
    --duration) DURATION="$2"; shift 2 ;;
    --restarts) RESTARTS="$2"; shift 2 ;;
    --objects) OBJECTS="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

soak_cleanup() {
  local code=$?
  [ -n "${CLIENT_PID}" ] && kill "${CLIENT_PID}" >/dev/null 2>&1 || true
  [ -n "${PROXY_PID}" ] && kill "${PROXY_PID}" >/dev/null 2>&1 || true
  rm -f "${STOP_FILE}"
  (exit "${code}")
  e2e_cleanup
}
trap soak_cleanup EXIT

require_cmd docker
require_cmd kind
require_cmd kubectl
require_cmd helm
require_cmd python3

create_kind_cluster
build_and_load_images
install_cert_manager
# Native CRD only: the rollout race is a property of the webhook Service and
# its Endpoints, identical whether the target is a CRD or an XRD, and
# skipping Crossplane takes several minutes off the setup.
install_operator \
  --set features.crossplane.enabled=false \
  --set features.nativeCRD.enabled=true

log "Waiting for the default ConversionWebhookServer to become Available"
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default

log "Applying the native Gadget CRD and CRDConversionConfig"
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/crd.yaml"
kubectl wait --for=condition=Established --timeout=60s crd/gadgets.nativecrd.example.org
kubectl apply -f "${REPO_ROOT}/test/e2e/testdata/crdconversionconfig.yaml"
kubectl wait --for=condition=Applied --timeout=120s crdconversionconfig/gadgets-e2e-conversion

# Seed at the STORAGE version (v2), read and write at the non-storage
# version (v1), so every request in the soak has to go through conversion.
# The expected value is encoded in the object's own name, which means the
# checker needs no state that could itself go stale.
log "Seeding ${OBJECTS} Gadgets at the storage version"
names=""
for i in $(seq 1 "${OBJECTS}"); do
  name="soak-${i}"
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
# "zero mismatches" for eight minutes. A checker that silently passed
# everything would make this whole test a no-op.
log "Self-check: one clean pass before any rollout"
python3 "${REPO_ROOT}/hack/e2e-soak-client.py" \
  --base "http://127.0.0.1:${PROXY_PORT}" --duration 5 \
  --names "${names}" --out "${RESULT_JSON}.presoak"
presoak_bad="$(python3 -c "import json,sys; d=json.load(open(sys.argv[1])); print(d['read_failures']+d['write_failures']+d['mismatches'])" "${RESULT_JSON}.presoak")"
if [ "${presoak_bad}" != "0" ]; then
  echo "FAIL: the cluster was already unhealthy before the soak started:"
  cat "${RESULT_JSON}.presoak"
  exit 1
fi
log "Self-check passed"

log "Driving reads and writes while restarting the webhook-server ${RESTARTS} times"
# The driver's lifetime is tied to the rollouts finishing, not to a guessed
# number of seconds. Sizing it as a fixed budget was wrong: `gap` accounts
# only for the sleeps, while each `rollout status` wait also consumes it, so a
# slow rollout pushed the later restarts past the driver's exit -- and a soak
# that is not driving traffic during a rollout proves nothing about it while
# still reporting a pass. --duration is now only a backstop against a rollout
# that hangs forever.
# The backstop has to cover the worst case the loop below permits: every
# sleep plus every rollout taking its full timeout. Sizing it as a multiple
# of DURATION was wrong in the other direction -- at DURATION=60 the loop can
# legitimately spend 300s sleeping and 1200s waiting, against a 240s backstop,
# so the driver would exit early and the run would fail for a reason that has
# nothing to do with the conversion path.
gap=$(( DURATION / (RESTARTS + 1) ))
MAX_DURATION=$(( gap * (RESTARTS + 1) + RESTARTS * ROLLOUT_TIMEOUT + 120 ))
python3 "${REPO_ROOT}/hack/e2e-soak-client.py" \
  --base "http://127.0.0.1:${PROXY_PORT}" --duration "${MAX_DURATION}" \
  --stop-file "${STOP_FILE}" \
  --names "${names}" --out "${RESULT_JSON}" &
CLIENT_PID=$!

dep="$(kubectl -n "${NAMESPACE}" get deploy -l app.kubernetes.io/name=declarative-conversion-webhook-server -o jsonpath='{.items[0].metadata.name}')"
for i in $(seq 1 "${RESTARTS}"); do
  sleep "${gap}"
  if ! kill -0 "${CLIENT_PID}" 2>/dev/null; then
    echo "FAIL: the traffic driver exited before rollout ${i}/${RESTARTS}; the remaining rollouts would have been unobserved"
    exit 1
  fi
  log "Rollout ${i}/${RESTARTS} of deployment/${dep}"
  kubectl -n "${NAMESPACE}" rollout restart "deployment/${dep}"
  kubectl -n "${NAMESPACE}" rollout status "deployment/${dep}" --timeout="${ROLLOUT_TIMEOUT}s"
done

# Keep driving briefly after the last rollout: the Endpoints of the final new
# pods settle after `rollout status` returns, and that tail is exactly where a
# missing preStop hook shows up.
sleep "${gap}"
if ! kill -0 "${CLIENT_PID}" 2>/dev/null; then
  echo "FAIL: the traffic driver exited before the final rollout had settled"
  exit 1
fi

log "Rollouts complete; asking the traffic driver to stop"
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

# A soak that did not actually generate traffic would report zero of
# everything and look like a pass.
# A driver that stopped because it hit its backstop rather than because the
# rollouts finished means a rollout hung; the traffic numbers below would then
# describe a run that never completed.
if d.get("stopped_by") == "duration":
    print("FAIL: the traffic driver hit its maximum duration instead of being stopped after the rollouts; a rollout did not complete")
    ok = False

if d["reads"] < 100 or d["writes"] < 100:
    print(f"FAIL: too little traffic to prove anything: {d['reads']} reads, {d['writes']} writes")
    ok = False

if d["read_failures"] or d["write_failures"]:
    print(f"FAIL: {d['read_failures']} failed reads and {d['write_failures']} failed writes during the rollout; "
          "a rolling update of the conversion webhook must not fail a single request")
    ok = False

# The one that matters most: HTTP 200 with the wrong value.
if d["mismatches"]:
    print(f"FAIL: {d['mismatches']} requests returned successfully with the WRONG converted value")
    ok = False

if not ok:
    for s in d.get("samples", []):
        print(f"  [{s['kind']}] {s['detail']}")
    sys.exit(1)

print(f"OK: {d['reads']} reads and {d['writes']} writes across the rollouts, "
      f"0 failures, 0 wrong values ({d.get('conflicts', 0)} benign write conflicts)")
PYCHK

log "All soak assertions passed"
