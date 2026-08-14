#!/usr/bin/env bash
#
# XWidget API lifecycle demo, driven by upstream demo-magic:
#   https://github.com/paxtonhare/demo-magic  (vendored as ./demo-magic.sh)
#
# Walks v1-only → add v2 → promote v2 → add v3 → promote v3 → deprecate v1
# → rewrite etcd at v3 and drop the v1 block from the XRD.
# Each kubectl/convctl command is typed out. Press Enter to type, Enter again
# to run (unless -n). Intentional mistakes are run through convctl *before*
# apply so you see the CLI catch them.
#
# Prerequisites: cluster with Crossplane v2 and this operator (`make dev-up`).
# Simulated typing requires `pv` (or pass -d).
#
# Usage:
#   ./demo.sh              type each command; Enter to type, Enter to run
#   ./demo.sh -n           demo-magic: no pauses (recording / CI)
#   ./demo.sh -d           demo-magic: print commands instantly (no typing)
#   ./demo.sh -w 3         demo-magic: auto-advance after 3s
#   ./demo.sh --cleanup    wipe leftover demo objects and exit
#
set -u

EXAMPLE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${EXAMPLE_DIR}/../.." && pwd)"
NS="${DEMO_NAMESPACE:-default}"
CLEANUP_ONLY=0

usage() { sed -n '2,20p' "$0"; }

die() { echo "error: $*" >&2; exit 1; }

# Long flags we own; everything else is forwarded to demo-magic's getopts
# (-h -d -n -c -w).
forward=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --cleanup) CLEANUP_ONLY=1; shift ;;
    --yes|--auto) forward+=(-n); shift ;;
    --fast) forward+=(-d); shift ;;
    --help) usage; exit 0 ;;
    *) forward+=("$1"); shift ;;
  esac
done
set -- "${forward[@]}"

cleanup_demo() {
  kubectl delete xwidgets.example.org --all -n "${NS}" --wait=true --timeout=90s >/dev/null 2>&1 || true
  kubectl annotate xrdconversionconfig xwidgets-conversion \
    conversion.terasky.com/allow-unsafe-delete=true --overwrite >/dev/null 2>&1 || true
  kubectl delete xrdconversionconfig xwidgets-conversion --wait=true --timeout=60s >/dev/null 2>&1 || true
  kubectl delete composition xwidgets.example.org xwidgets-v2.example.org xwidgets-v3.example.org --wait=true --timeout=60s >/dev/null 2>&1 || true
  kubectl delete compositeresourcedefinition.apiextensions.crossplane.io/xwidgets.example.org --wait=true --timeout=90s >/dev/null 2>&1 || true
  kubectl delete configmap -n "${NS}" demo-settings from-v2-settings from-v3-settings after-promote-settings on-v3-settings >/dev/null 2>&1 || true
}

command -v kubectl >/dev/null 2>&1 || die "kubectl not found on PATH"

if [[ "${CLEANUP_ONLY}" -eq 1 ]]; then
  cleanup_demo
  echo "demo objects removed"
  exit 0
fi

kubectl cluster-info >/dev/null 2>&1 || die "no reachable cluster. Run 'make dev-up' first."
kubectl get crd xrdconversionconfigs.terasky.com >/dev/null 2>&1 \
  || die "declarative-conversion-operator is not installed. Run 'make dev-up' first."
kubectl get crd compositeresourcedefinitions.apiextensions.crossplane.io >/dev/null 2>&1 \
  || die "Crossplane is not installed. Run 'make dev-up' first."
kubectl wait --for=condition=Available --timeout=120s conversionwebhookserver/default >/dev/null 2>&1 \
  || die "ConversionWebhookServer/default is not Available yet."

# Always use this checkout's convctl so a stale PATH binary cannot drift.
mkdir -p "${REPO_ROOT}/bin"
(cd "${REPO_ROOT}" && go build -o bin/convctl ./cmd/convctl) \
  || die "failed to build convctl"
export PATH="${REPO_ROOT}/bin:${PATH}"

# demo-magic aborts if TYPE_SPEED is set and pv is missing. -d is the
# upstream switch that unsets TYPE_SPEED before that check.
if ! command -v pv >/dev/null 2>&1; then
  case " $* " in
    *" -d "*) ;;
    *)
      echo "note: pv not found; disabling simulated typing (install pv, or this is -d)." >&2
      set -- -d "$@"
      ;;
  esac
fi

# shellcheck source=demo-magic.sh
source "${EXAMPLE_DIR}/demo-magic.sh"

# demo-magic's wait() uses `read -t`, which exits 1 on timeout. Combined with
# `set -e` that aborts the demo instead of auto-advancing. Ignore the timeout
# status; Enter still proceeds immediately.
wait() {
  if [[ "${PROMPT_TIMEOUT}" == "0" ]]; then
    read -rs || true
  else
    read -rst "${PROMPT_TIMEOUT}" || true
  fi
}

# Upstream run_cmd does `eval $@`, which splits jsonpath/for-loops. Keep the
# rest of demo-magic; only quote the command string.
run_cmd() {
  trap '' SIGINT
  stty -echoctl
  eval "$1"
  stty echoctl
  trap - SIGINT
}

pe_fail() {
  p "$1"
  if eval "$1"; then
    echo -e "${RED}error:${COLOR_RESET} that command succeeded — it should have been rejected" >&2
    exit 1
  fi
  echo
  echo -e "${CYAN}${2:-# ↑ rejected before apply. That is why we run convctl first.}${COLOR_RESET}"
}

DEMO_PROMPT="${GREEN}➜ ${CYAN}demo ${COLOR_RESET}${BOLD}\$ ${COLOR_RESET}"

section() {
  echo
  echo -e "${BOLD}${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${COLOR_RESET}"
  echo -e "${BOLD}$*${COLOR_RESET}"
  echo -e "${BOLD}${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${COLOR_RESET}"
  echo
  if [[ "${NO_WAIT}" != "true" ]]; then
    echo -e "${GREY}Press Enter to start this section…${COLOR_RESET}"
    wait
  fi
}

note() {
  echo
  echo -e "${CYAN}$*${COLOR_RESET}"
}

cd "${EXAMPLE_DIR}"
set -e


section "XWidget API evolution — live demo"
cat <<EOF
This walkthrough evolves xwidgets.example.org:

  1. v1 only (XRD + Composition → ConfigMap). No conversion webhook.
  2. Add v2 as a served spoke; conversion size ↔ capacity.
  3. Promote v2 to the hub; new Composition; retarget existing XRs.
  4. Add v3 as a spoke (widgetName ↔ name).
  5. Promote v3 to the hub (the new standard).
  6. Deprecate v1 (served: false), rewrite etcd at v3, drop the v1 block.

Before each apply we run convctl against both the correct snapshot and an
intentional mistake, so you see the CLI catch a bad mapping before it hits
the cluster.

Composed resource: a native ConfigMap. Namespace: ${NS}
EOF
note "Each command is typed. Press Enter to reveal it, Enter again to run it."

note "Wiping leftover objects from a previous run (untyped cleanup)…"
cleanup_demo

section "0 — composition functions"
note "function-go-templating emits the ConfigMap; function-auto-ready marks the XR Ready."
pe "kubectl apply -f functions.yaml"
pe "kubectl wait --for=condition=Healthy --timeout=180s function/function-go-templating"
pe "kubectl wait --for=condition=Healthy --timeout=180s function/function-auto-ready"

# ---------------------------------------------------------------------------
section "1 — one version, no conversion"
note "A single served, referenceable v1. The Composition copies spec.widgetName / spec.size onto a ConfigMap. This operator has nothing to do yet."

pe "cat 01-v1-only/xrd.yaml"
pe "kubectl apply -f 01-v1-only/xrd.yaml"
pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
pe "kubectl wait --for=condition=Established --timeout=60s compositeresourcedefinition.apiextensions.crossplane.io/xwidgets.example.org"

pe "cat 01-v1-only/composition.yaml"
pe "kubectl apply -f 01-v1-only/composition.yaml"

pe "cat 01-v1-only/xr.yaml"
pe "kubectl apply -f 01-v1-only/xr.yaml"
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/demo -n ${NS}"

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"
pe "kubectl get xwidgets.v1.example.org demo -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get configmap demo-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"

# ---------------------------------------------------------------------------
section "2 — add v2 as a spoke (hub stays v1)"
note "v2 is served so clients can use example.org/v2, but referenceable stays on v1. Conversion is written FROM THE CURRENT HUB: hubPath spec.size → spokePath spec.capacity."

note "First: the mistake. A v2 spoke with no rules. Fail-closed coverage must reject it."
pe "cat mistakes/02-missing-rename.yaml"
pe_fail "convctl validate --config mistakes/02-missing-rename.yaml --xrd 02-add-v2/xrd.yaml"

note "Same mistake against the live XR (demo), not just fixtures:"
pe_fail "convctl test --config mistakes/02-missing-rename.yaml --xrd 02-add-v2/xrd.yaml --live"

note "Now the correct mapping. widgetName is identical on both sides, so it needs no rule."
pe "cat 02-add-v2/xrdconversionconfig.yaml"
pe "convctl validate --config 02-add-v2/xrdconversionconfig.yaml --xrd 02-add-v2/xrd.yaml"
pe "convctl test --config 02-add-v2/xrdconversionconfig.yaml --xrd 02-add-v2/xrd.yaml --samples 02-add-v2/samples/"
note "Pre-upgrade check: every object already in the cluster, at storage version."
pe "convctl test --config 02-add-v2/xrdconversionconfig.yaml --xrd 02-add-v2/xrd.yaml --live"

pe "kubectl apply -f 02-add-v2/xrd.yaml"
pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
pe "kubectl apply -f 02-add-v2/xrdconversionconfig.yaml"
pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"

note "Create an XR at v2 (spec.capacity). Storage and the Composition still see v1 (spec.size) — the webhook converts at the apiserver."
pe "cat live/from-v2.yaml"
pe "kubectl apply -f live/from-v2.yaml"
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/from-v2 -n ${NS}"

note "Same object, two API versions:"
pe "kubectl get xwidgets.v2.example.org from-v2 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v1.example.org from-v2 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get configmap from-v2-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"
note "The ConfigMap has data.size, not data.capacity — composition still targets v1."

# ---------------------------------------------------------------------------
section "3 — promote v2 to the hub"
note "Four edits: (1) referenceable v1→v2  (2) hubVersion v2, rules rewritten from v2's POV  (3) a NEW Composition — compositeTypeRef is immutable  (4) patch compositionRef on every existing XR."

note "The copy-paste trap: keep hubPath spec.size after the hub is v2. size is not on the hub anymore."
pe "cat mistakes/03-copy-paste-hub-paths.yaml"
pe_fail "convctl validate --config mistakes/03-copy-paste-hub-paths.yaml --xrd 03-promote-v2/xrd.yaml"
pe_fail "convctl test --config mistakes/03-copy-paste-hub-paths.yaml --xrd 03-promote-v2/xrd.yaml --live"

note "Correct rules: hubPath spec.capacity → spokePath spec.size (roles flipped, not a mechanical swap)."
pe "cat 03-promote-v2/xrdconversionconfig.yaml"
pe "convctl validate --config 03-promote-v2/xrdconversionconfig.yaml --xrd 03-promote-v2/xrd.yaml"
pe "convctl test --config 03-promote-v2/xrdconversionconfig.yaml --xrd 03-promote-v2/xrd.yaml --samples 03-promote-v2/samples/"
pe "convctl test --config 03-promote-v2/xrdconversionconfig.yaml --xrd 03-promote-v2/xrd.yaml --live"

pe "kubectl apply -f 03-promote-v2/xrd.yaml"
pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
pe "kubectl apply -f 03-promote-v2/xrdconversionconfig.yaml"
pe "cat 03-promote-v2/composition.yaml"
pe "kubectl apply -f 03-promote-v2/composition.yaml"
pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"

note "Do NOT skip the compositionRef patch. Crossplane pins it at create time. Watch existing XRs go Synced=False:"
pe "kubectl get xwidgets.example.org -n ${NS}"
pe "kubectl get xwidgets.example.org/demo -n ${NS} -o jsonpath='{.status.conditions[?(@.type==\"Synced\")].message}{\"\\n\"}'"

note "Retarget every XR onto xwidgets-v2.example.org and drop the pinned revision:"
pe "cat patches/retarget-v2.json"
pe 'for xr in $(kubectl get xwidgets.example.org -n '"${NS}"' -o name); do kubectl patch "$xr" -n '"${NS}"' --type=json --patch-file patches/retarget-v2.json; done'
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/demo -n ${NS}"
pe "kubectl wait --for=condition=Synced --timeout=90s xwidgets.example.org/demo -n ${NS}"
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/from-v2 -n ${NS}"
pe "kubectl wait --for=condition=Synced --timeout=90s xwidgets.example.org/from-v2 -n ${NS}"
pe "kubectl get xwidgets.example.org -n ${NS}"

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"
pe "kubectl get xwidgets.v1.example.org from-v2 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v2.example.org from-v2 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get configmap demo-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"
note "demo-settings now has data.capacity — the v2 Composition template replaced data.size."

note "A new XR created after the hub flip picks defaultCompositionRef (v2):"
pe "cat live/after-promote.yaml"
pe "kubectl apply -f live/after-promote.yaml"
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/after-promote -n ${NS}"
pe "kubectl get configmap after-promote-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"

# ---------------------------------------------------------------------------
section "4 — add v3 as a spoke (hub stays v2)"
note "Same pattern as stage 2. v3 is served, not referenceable. spec.capacity is identical on v2 and v3 (no rule). spec.widgetName ↔ spec.name needs FieldRename."

note "The mistake: serve v3 with an empty rule list."
pe "cat mistakes/04-v3-without-rename.yaml"
pe_fail "convctl validate --config mistakes/04-v3-without-rename.yaml --xrd 04-add-v3/xrd.yaml"
pe_fail "convctl test --config mistakes/04-v3-without-rename.yaml --xrd 04-add-v3/xrd.yaml --live"

note "Correct config: v1 spoke unchanged, v3 spoke adds widgetName ↔ name."
pe "cat 04-add-v3/xrdconversionconfig.yaml"
pe "convctl validate --config 04-add-v3/xrdconversionconfig.yaml --xrd 04-add-v3/xrd.yaml"
pe "convctl test --config 04-add-v3/xrdconversionconfig.yaml --xrd 04-add-v3/xrd.yaml --samples 04-add-v3/samples/"
pe "convctl test --config 04-add-v3/xrdconversionconfig.yaml --xrd 04-add-v3/xrd.yaml --live"

pe "kubectl apply -f 04-add-v3/xrd.yaml"
pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
pe "kubectl apply -f 04-add-v3/xrdconversionconfig.yaml"
pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"

note "Create at v3 (spec.name). Read back at v2 and v1 — spoke-to-spoke hops through the hub."
pe "cat live/from-v3.yaml"
pe "kubectl apply -f live/from-v3.yaml"
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/from-v3 -n ${NS}"
pe "kubectl get xwidgets.v3.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v2.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v1.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get configmap from-v3-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"
note "Composition still targets the hub (v2), so the ConfigMap has widgetName + capacity."

# ---------------------------------------------------------------------------
section "5 — promote v3 to the hub (the new standard)"
note "Same four edits as stage 3, destination v3. From v3's POV: v2 needs spec.name ↔ spec.widgetName; v1 needs that plus spec.capacity ↔ spec.size."

pe "cat 05-promote-v3/xrdconversionconfig.yaml"
pe "convctl validate --config 05-promote-v3/xrdconversionconfig.yaml --xrd 05-promote-v3/xrd.yaml"
pe "convctl test --config 05-promote-v3/xrdconversionconfig.yaml --xrd 05-promote-v3/xrd.yaml --samples 05-promote-v3/samples/"
pe "convctl test --config 05-promote-v3/xrdconversionconfig.yaml --xrd 05-promote-v3/xrd.yaml --live"

pe "kubectl apply -f 05-promote-v3/xrd.yaml"
pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
pe "kubectl apply -f 05-promote-v3/xrdconversionconfig.yaml"
pe "cat 05-promote-v3/composition.yaml"
pe "kubectl apply -f 05-promote-v3/composition.yaml"
pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"

note "Existing XRs are still pinned to xwidgets-v2.example.org — incompatible once v3 is referenceable."
pe "kubectl get xwidgets.example.org -n ${NS}"
pe "cat patches/retarget-v3.json"
pe 'for xr in $(kubectl get xwidgets.example.org -n '"${NS}"' -o name); do kubectl patch "$xr" -n '"${NS}"' --type=json --patch-file patches/retarget-v3.json; done'
pe 'for xr in $(kubectl get xwidgets.example.org -n '"${NS}"' -o name); do kubectl wait --for=condition=Ready --timeout=90s -n '"${NS}"' "$xr"; kubectl wait --for=condition=Synced --timeout=90s -n '"${NS}"' "$xr"; done'
pe "kubectl get xwidgets.example.org -n ${NS}"

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"
pe "kubectl get xwidgets.v3.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v2.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get configmap from-v3-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"
note "ConfigMap now has data.name + data.capacity — v3 is the standard."

pe "cat live/on-v3.yaml"
pe "kubectl apply -f live/on-v3.yaml"
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/on-v3 -n ${NS}"
pe "kubectl get configmap on-v3-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"

# ---------------------------------------------------------------------------
section "6 — deprecate v1"
note "Order: drop v1 from XRDConversionConfig BEFORE setting served: false. A spoke whose version is not served fails validation."

note "The mistake: XRD already has served:false on v1, but the config still lists v1 as a spoke."
pe "cat mistakes/06-spoke-not-served.yaml"
pe_fail "convctl validate --config mistakes/06-spoke-not-served.yaml --xrd 06-deprecate-v1/xrd.yaml"

note "Correct order: conversion config without v1, then XRD with served: false."
pe "cat 06-deprecate-v1/xrdconversionconfig.yaml"
pe "convctl validate --config 06-deprecate-v1/xrdconversionconfig.yaml --xrd 06-deprecate-v1/xrd.yaml"
pe "convctl test --config 06-deprecate-v1/xrdconversionconfig.yaml --xrd 06-deprecate-v1/xrd.yaml --samples 06-deprecate-v1/samples/"
pe "convctl test --config 06-deprecate-v1/xrdconversionconfig.yaml --xrd 06-deprecate-v1/xrd.yaml --live"

pe "kubectl apply -f 06-deprecate-v1/xrdconversionconfig.yaml"
pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"
pe "kubectl apply -f 06-deprecate-v1/xrd.yaml"
pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"
pe "kubectl get xwidgets.v2.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v3.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"

note "v1 is no longer served:"
pe_fail "kubectl get xwidgets.v1.example.org from-v2 -n ${NS}" "# ↑ expected: the v1 API is gone"

note "The v1 version *block* is still on the XRD. Kubernetes will not let us delete it while the generated CRD's status.storedVersions still lists v1 — even if etcd is already at v3."
pe "kubectl get crd xwidgets.example.org -o jsonpath='storage={.spec.versions[?(@.storage==true)].name}{\"\\nstoredVersions=\"}{.status.storedVersions}{\"\\n\"}'"

note "Unlike a native CRD, promoting an XRD hub already wrote every XR (compositionRef retarget — compositeTypeRef is immutable). etcd root apiVersion is usually already v3. storedVersions still has to be pruned. managedFields can still mention old GVKs; that is not the stored version."
pe "./show-storage.sh"

note "migrate-storage anyway (no-op on already-v3 objects; catches stragglers), then prune storedVersions. For native CRDs the empty SSA pass is the actual rewrite — here the prune is what unblocks dropping v1."
pe "convctl migrate-storage --xrd xwidgets.example.org --prune-stored-versions"

note "After: etcd root apiVersion v3, storedVersions [v3]. Leftover v2 in managedFields is normal."
pe "./show-storage.sh"
pe "kubectl get crd xwidgets.example.org -o jsonpath='{.status.storedVersions}{\"\\n\"}'"

note "Now the v1 block can actually leave the XRD:"
pe "cat 06-deprecate-v1/xrd-drop-v1.yaml"
pe "kubectl apply -f 06-deprecate-v1/xrd-drop-v1.yaml"
pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
pe "kubectl wait --for=condition=Established --timeout=60s compositeresourcedefinition.apiextensions.crossplane.io/xwidgets.example.org"

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"
pe "kubectl get crd xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  storage={.storage}{\"\\n\"}{end}'"
note "v1 is gone from both the XRD and the generated CRD. v2 is still a served spoke."

pe "kubectl get xwidgets.v2.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v3.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"

section "Done"
cat <<EOF
Hub is v3 (the standard), spoke is v2, Composition is xwidgets-v3.example.org.
v1 has been un-served, storedVersions pruned, and removed from the XRD
(etcd was already at v3 from the compositionRef retarget; migrate-storage
was belt-and-suspenders plus the prune).
Deprecating v2 later is the same sequence: drop-from-config, served:false,
convctl migrate-storage --prune-stored-versions, then drop the version block.

Objects left in ${NS}: demo, from-v2, after-promote, from-v3, on-v3.

Wipe them with:
  ${EXAMPLE_DIR}/demo.sh --cleanup
EOF
