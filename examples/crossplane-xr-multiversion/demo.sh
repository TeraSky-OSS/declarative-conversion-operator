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
# --demo-mode gitops also needs Kyverno (installed by make dev-up unless
# DEV_KYVERNO=false). Live engines (flux|argo) need gh, git, helm, docker,
# and kind. Simulated typing requires `pv` (or pass -d).
#
# Usage:
#   ./demo.sh                              type each command; Enter to type, Enter to run
#   ./demo.sh --demo-mode patches          default: kubectl patch compositionRef
#   ./demo.sh --demo-mode gitops           Kyverno MutatingPolicies; engine defaults to simulate
#   ./demo.sh --demo-mode gitops --gitops-engine simulate
#   ./demo.sh --demo-mode gitops --gitops-engine flux --create-repo
#   ./demo.sh --demo-mode gitops --gitops-engine argo --github-repo $USER/platform --git-prefix xwidget-demo
#   ./demo.sh -n                           demo-magic: no pauses (recording / CI)
#   ./demo.sh -d                           demo-magic: print commands instantly (no typing)
#   ./demo.sh -w 3                         demo-magic: auto-advance after 3s
#   ./demo.sh --cleanup                    wipe leftover demo objects and exit
#   ./demo.sh --cleanup --delete-repo      also gh repo delete if this run created the repo
#   ./demo.sh --from-stage 6               resume after a failure (0–6; does not wipe)
#
set -u

EXAMPLE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${EXAMPLE_DIR}/../.." && pwd)"
NS="${DEMO_NAMESPACE:-default}"
CLEANUP_ONLY=0
DEMO_MODE=patches
GITOPS_ENGINE=simulate
GITHUB_REPO_FLAG=""
CREATE_REPO=0
CREATE_REPO_NAME="xwidget-lifecycle-demo"
GIT_PREFIX=""
REPO_VISIBILITY=private
DELETE_REPO=0
CREATED_REPO=0
FROM_STAGE=0

usage() { sed -n '2,30p' "$0"; }

die() { echo "error: $*" >&2; exit 1; }

# Long flags we own; everything else is forwarded to demo-magic's getopts
# (-h -d -n -c -w).
forward=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --cleanup) CLEANUP_ONLY=1; shift ;;
    --demo-mode)
      DEMO_MODE="${2:-}"
      shift 2
      ;;
    --demo-mode=*)
      DEMO_MODE="${1#*=}"
      shift
      ;;
    --gitops-engine)
      GITOPS_ENGINE="${2:-}"
      shift 2
      ;;
    --gitops-engine=*)
      GITOPS_ENGINE="${1#*=}"
      shift
      ;;
    --github-repo)
      GITHUB_REPO_FLAG="${2:-}"
      shift 2
      ;;
    --github-repo=*)
      GITHUB_REPO_FLAG="${1#*=}"
      shift
      ;;
    --create-repo)
      CREATE_REPO=1
      if [[ "${2:-}" != "" && "${2:-}" != -* ]]; then
        CREATE_REPO_NAME="$2"
        shift 2
      else
        shift
      fi
      ;;
    --create-repo=*)
      CREATE_REPO=1
      CREATE_REPO_NAME="${1#*=}"
      shift
      ;;
    --git-prefix)
      GIT_PREFIX="${2:-}"
      shift 2
      ;;
    --git-prefix=*)
      GIT_PREFIX="${1#*=}"
      shift
      ;;
    --repo-visibility)
      REPO_VISIBILITY="${2:-}"
      shift 2
      ;;
    --repo-visibility=*)
      REPO_VISIBILITY="${1#*=}"
      shift
      ;;
    --delete-repo) DELETE_REPO=1; shift ;;
    --from-stage)
      FROM_STAGE="${2:-}"
      shift 2
      ;;
    --from-stage=*)
      FROM_STAGE="${1#*=}"
      shift
      ;;
    --yes|--auto) forward+=(-n); shift ;;
    --fast) forward+=(-d); shift ;;
    --help) usage; exit 0 ;;
    *) forward+=("$1"); shift ;;
  esac
done
set -- "${forward[@]}"

case "${DEMO_MODE}" in
  patches|gitops) ;;
  *) die "invalid --demo-mode ${DEMO_MODE} (want patches or gitops)" ;;
esac

case "${GITOPS_ENGINE}" in
  simulate|flux|argo) ;;
  *) die "invalid --gitops-engine ${GITOPS_ENGINE} (want simulate, flux, or argo)" ;;
esac

if [[ "${GITOPS_ENGINE}" != simulate && "${DEMO_MODE}" != gitops ]]; then
  die "--gitops-engine ${GITOPS_ENGINE} requires --demo-mode gitops"
fi

if [[ "${DEMO_MODE}" == patches && "${GITOPS_ENGINE}" != simulate ]]; then
  die "--gitops-engine is only valid with --demo-mode gitops"
fi

case "${REPO_VISIBILITY}" in
  public|private) ;;
  *) die "invalid --repo-visibility ${REPO_VISIBILITY} (want public or private)" ;;
esac

if [[ -n "${GIT_PREFIX}" && "${CREATE_REPO}" -eq 1 ]]; then
  die "--git-prefix is for an existing --github-repo; new repos write at /"
fi

if [[ "${GITOPS_ENGINE}" == simulate && "${CLEANUP_ONLY}" -ne 1 ]]; then
  if [[ -n "${GITHUB_REPO_FLAG}" || "${CREATE_REPO}" -eq 1 ]]; then
    die "--github-repo / --create-repo require --gitops-engine flux or argo"
  fi
fi

if ! [[ "${FROM_STAGE}" =~ ^[0-6]$ ]]; then
  die "invalid --from-stage ${FROM_STAGE} (want 0-6)"
fi

demo_stage() {
  [[ "$1" -ge "${FROM_STAGE}" ]]
}

# shellcheck source=gitops/lib.sh
source "${EXAMPLE_DIR}/gitops/lib.sh"

cleanup_demo() {
  kubectl delete xwidgets.example.org --all -n "${NS}" --wait=true --timeout=90s >/dev/null 2>&1 || true
  kubectl annotate xrdconversionconfig xwidgets-conversion \
    conversion.terasky.com/allow-unsafe-delete=true --overwrite >/dev/null 2>&1 || true
  kubectl delete xrdconversionconfig xwidgets-conversion --wait=true --timeout=60s >/dev/null 2>&1 || true
  kubectl delete composition xwidgets.example.org xwidgets-v2.example.org xwidgets-v3.example.org --wait=true --timeout=60s >/dev/null 2>&1 || true
  kubectl delete compositeresourcedefinition.apiextensions.crossplane.io/xwidgets.example.org --wait=true --timeout=90s >/dev/null 2>&1 || true
  kubectl delete mutatingpolicy.policies.kyverno.io label-compositions-xwidgets set-composition-version-selector-xwidgets migrate-xwidgets-to-v2 migrate-xwidgets-to-v3 --wait=true --timeout=60s >/dev/null 2>&1 || true
  kubectl delete clusterrole kyverno-xwidgets-view kyverno-xwidgets-mutate --wait=true --timeout=30s >/dev/null 2>&1 || true
  kubectl delete configmap -n "${NS}" demo-settings from-v2-settings from-v3-settings after-promote-settings on-v3-settings >/dev/null 2>&1 || true
}

command -v kubectl >/dev/null 2>&1 || die "kubectl not found on PATH"

if [[ "${CLEANUP_ONLY}" -eq 1 ]]; then
  cleanup_demo
  if gitops_is_live || [[ "${DELETE_REPO}" -eq 1 ]] || [[ -f "${GITOPS_STATE_FILE}" ]]; then
    gitops_cleanup_cluster
    gitops_cleanup_repo
  fi
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
if [[ "${DEMO_MODE}" == gitops ]]; then
  kubectl get crd mutatingpolicies.policies.kyverno.io >/dev/null 2>&1 \
    || die "Kyverno MutatingPolicy CRD is missing. Run 'make dev-up' (DEV_KYVERNO=true, the default) or omit --demo-mode gitops."
fi

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

if [[ "${FROM_STAGE}" -gt 0 ]]; then
  note "Resuming from stage ${FROM_STAGE} (cluster and git left as-is)."
  if gitops_is_live; then
    gitops_state_load
    [[ -n "${GITHUB_REPO:-}" ]] || die "--from-stage ${FROM_STAGE} needs a previous live run (${GITOPS_STATE_FILE} missing GITHUB_REPO)"
    [[ -d "${GITOPS_WORKTREE}/.git" ]] || die "git worktree missing at ${GITOPS_WORKTREE}; cannot resume"
    note "Repo: ${GITHUB_REPO}  prefix: $(gitops_rel_prefix)  runner label: ${GITOPS_RUNNER_LABEL}"
  fi
else
section "XWidget API evolution — live demo"
cat <<EOF
This walkthrough evolves xwidgets.example.org:

  1. v1 only (XRD + Composition → ConfigMap). No conversion webhook.
  2. Add v2 as a served spoke; conversion size ↔ capacity.
  3. Promote v2 to the hub; rehub the config; new Composition; retarget; migrate-storage.
  4. Add v3 as a spoke (widgetName ↔ name).
  5. Promote v3 to the hub (rehub + migrate-storage again).
  6. Deprecate v1 (served: false), migrate-storage --prune-stored-versions, drop the v1 block.

Before each apply we run convctl against both the correct snapshot and an
intentional mistake, so you see the CLI catch a bad mapping before it hits
the cluster.

Composed resource: a native ConfigMap. Namespace: ${NS}
Retarget mode: ${DEMO_MODE}
GitOps engine: ${GITOPS_ENGINE}
EOF
note "Each command is typed. Press Enter to reveal it, Enter again to run it."

note "Wiping leftover objects from a previous run (untyped cleanup)…"
cleanup_demo

if gitops_is_live; then
  section "GitOps bootstrap (GitHub + in-cluster runner + ${GITOPS_ENGINE})"
  cat <<EOF
Live engine: commits go to GitHub as PRs. A self-hosted Actions runner
pod in this kind cluster runs convctl validate / test --samples / test --live
(GitHub-hosted runners cannot reach kind; this demo does not use ACT).
After merge, ${GITOPS_ENGINE} reconciles main.

migrate-storage stays a local command — it is cluster housekeeping, not
desired-state YAML.
EOF
  note "Creating or opening the GitHub repo, loading convctl onto the runner, installing ${GITOPS_ENGINE}…"
  gitops_bootstrap
  note "Repo: ${GITHUB_REPO}  prefix: $(gitops_rel_prefix)  runner label: ${GITOPS_RUNNER_LABEL}"
fi
fi

# ---------------------------------------------------------------------------
if demo_stage 0; then
section "0 — composition functions"
note "function-go-templating emits the ConfigMap; function-auto-ready marks the XR Ready."
if gitops_is_live; then
  note "Functions were seeded on main and synced by ${GITOPS_ENGINE} during bootstrap."
  pe "cat functions.yaml"
  pe "kubectl wait --for=condition=Healthy --timeout=180s function/function-go-templating"
  pe "kubectl wait --for=condition=Healthy --timeout=180s function/function-auto-ready"
else
  pe "kubectl apply -f functions.yaml"
  pe "kubectl wait --for=condition=Healthy --timeout=180s function/function-go-templating"
  pe "kubectl wait --for=condition=Healthy --timeout=180s function/function-auto-ready"
fi

fi
# ---------------------------------------------------------------------------
if demo_stage 1; then
section "1 — one version, no conversion"
note "A single served, referenceable v1. The Composition copies spec.widgetName / spec.size onto a ConfigMap. This operator has nothing to do yet."

pe "cat 01-v1-only/xrd.yaml"
if gitops_is_live; then
  note "Ship the v1 snapshot and Kyverno labeler through a PR. The app XR comes after the CRD exists — Flux cannot apply an XWidget before Crossplane creates the CRD."
  pe "gitops_ship --branch stage-1 --title 'stage 1: v1 only' --stage 01-v1-only --policy gitops/policies/label-compositions-xwidgets.yaml --extra gitops/policies/kyverno-rbac.yaml"
else
  pe "kubectl apply -f 01-v1-only/xrd.yaml"
  pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale
  pe "kubectl wait --for=condition=Established --timeout=60s compositeresourcedefinition.apiextensions.crossplane.io/xwidgets.example.org"

  pe "cat 01-v1-only/composition.yaml"
  pe "kubectl apply -f 01-v1-only/composition.yaml"
  if [[ "${DEMO_MODE}" == gitops ]]; then
    note "GitOps mode: Kyverno labels Compositions for this XRD (group+kind) from compositeTypeRef."
    pe "kubectl apply -f gitops/policies/kyverno-rbac.yaml"
    pe "convctl generate kyverno --xrd 01-v1-only/xrd.yaml --to v1 | sed -n '1,42p'"
    pe "kubectl apply -f gitops/policies/label-compositions-xwidgets.yaml"
  fi

  pe "cat 01-v1-only/xr.yaml"
  pe "kubectl apply -f 01-v1-only/xr.yaml"
fi
pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale
pe "kubectl wait --for=condition=Established --timeout=60s compositeresourcedefinition.apiextensions.crossplane.io/xwidgets.example.org"
if gitops_is_live; then
  pe "gitops_ship --branch stage-1-app --title 'stage 1: demo XR' --app gitops/apps/widget.yaml"
fi
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/demo -n ${NS}"

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"
pe "kubectl get xwidgets.v1.example.org demo -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get configmap demo-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"

fi
# ---------------------------------------------------------------------------
if demo_stage 2; then
section "2 — add v2 as a spoke (hub stays v1)"
note "v2 is served so clients can use example.org/v2, but referenceable stays on v1. Conversion is written FROM THE CURRENT HUB: hubPath spec.size → spokePath spec.capacity."

note "First: the mistake. A v2 spoke with no rules. Fail-closed coverage must reject it."
pe "cat mistakes/02-missing-rename.yaml"
pe_fail "convctl validate --config mistakes/02-missing-rename.yaml --xrd 02-add-v2/xrd.yaml"

note "Same mistake against the live XR (demo), not just fixtures:"
pe_fail "convctl test --config mistakes/02-missing-rename.yaml --xrd 02-add-v2/xrd.yaml --live"

if gitops_is_live; then
  note "Same mistake as a PR — CI should fail and comment the convctl output. The PR stays open."
  pe "gitops_ship --expect-fail --branch stage-2 --title 'stage 2: add v2 spoke' --stage 02-add-v2 --config-override mistakes/02-missing-rename.yaml"
fi

note "Now the correct mapping. widgetName is identical on both sides, so it needs no rule."
pe "cat 02-add-v2/xrdconversionconfig.yaml"
pe "convctl validate --config 02-add-v2/xrdconversionconfig.yaml --xrd 02-add-v2/xrd.yaml"
pe "convctl test --config 02-add-v2/xrdconversionconfig.yaml --xrd 02-add-v2/xrd.yaml --samples 02-add-v2/samples/"
note "Pre-upgrade check: every object already in the cluster, at storage version."
pe "convctl test --config 02-add-v2/xrdconversionconfig.yaml --xrd 02-add-v2/xrd.yaml --live"

if gitops_is_live; then
  note "Push the valid config to the same PR. When CI goes green, merge. Flux applies the XRD Kustomization first, then conversion/ (admission sees the live schema)."
  pe "gitops_ship --fix-open-pr --title 'stage 2: add v2 spoke' --stage 02-add-v2"
else
  pe "kubectl apply -f 02-add-v2/xrd.yaml"
  pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale
  pe "kubectl apply -f 02-add-v2/xrdconversionconfig.yaml"
  pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"
fi

pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale
pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"
pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"

note "Create an XR at v2 (spec.capacity). Storage and the Composition still see v1 (spec.size) — the webhook converts at the apiserver."
if gitops_is_live; then
  pe "cat live/from-v2.yaml"
  pe "gitops_ship --branch stage-2-app --title 'stage 2: from-v2 XR' --app live/from-v2.yaml"
else
  pe "cat live/from-v2.yaml"
  pe "kubectl apply -f live/from-v2.yaml"
fi
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/from-v2 -n ${NS}"

note "Same object, two API versions:"
pe "kubectl get xwidgets.v2.example.org from-v2 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v1.example.org from-v2 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get configmap from-v2-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"
note "The ConfigMap has data.size, not data.capacity — composition still targets v1."

fi
# ---------------------------------------------------------------------------
if demo_stage 3; then
section "3 — promote v2 to the hub"
note "Five steps: (1) referenceable v1→v2  (2) convctl rehub — rewrite the config from v2's POV  (3) a NEW Composition — compositeTypeRef is immutable  (4) retarget every existing XR  (5) convctl migrate-storage — rewrite etcd at the new storage version."

note "The copy-paste trap: keep hubPath spec.size after the hub is v2. size is not on the hub anymore."
pe "cat mistakes/03-copy-paste-hub-paths.yaml"
pe_fail "convctl validate --config mistakes/03-copy-paste-hub-paths.yaml --xrd 03-promote-v2/xrd.yaml"
pe_fail "convctl test --config mistakes/03-copy-paste-hub-paths.yaml --xrd 03-promote-v2/xrd.yaml --live"

note "Do not rewrite spokes by hand. rehub inverts the promoted spoke and re-expresses the rest from the new hub. Review the draft, then commit it."
pe "convctl rehub --config 02-add-v2/xrdconversionconfig.yaml --xrd 02-add-v2/xrd.yaml --to v2"
note "Reviewed snapshot in this repo (same rewrite):"
pe "cat 03-promote-v2/xrdconversionconfig.yaml"
pe "convctl validate --config 03-promote-v2/xrdconversionconfig.yaml --xrd 03-promote-v2/xrd.yaml"
pe "convctl test --config 03-promote-v2/xrdconversionconfig.yaml --xrd 03-promote-v2/xrd.yaml --samples 03-promote-v2/samples/"
pe "convctl test --config 03-promote-v2/xrdconversionconfig.yaml --xrd 03-promote-v2/xrd.yaml --live"

if gitops_is_live; then
  note "One PR: XRD + Composition + migrate policy + conversion config. Flux still applies conversion/ after the XRD is Established."
  pe "convctl generate kyverno --xrd 03-promote-v2/xrd.yaml --from v1 --to v2"
  pe "gitops_ship --branch stage-3 --title 'stage 3: promote v2' --stage 03-promote-v2 --policy gitops/policies/from-v1-to-v2.yaml"
else
  pe "kubectl apply -f 03-promote-v2/xrd.yaml"
  pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale
  pe "kubectl apply -f 03-promote-v2/xrdconversionconfig.yaml"
  pe "cat 03-promote-v2/composition.yaml"
  pe "kubectl apply -f 03-promote-v2/composition.yaml"
  pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"
fi

pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale
pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"

note "Do NOT skip the retarget. Crossplane pins compositionRef at create time. Watch existing XRs go Synced=False:"
pe "kubectl get xwidgets.example.org -n ${NS}"
pe "kubectl get xwidgets.example.org/demo -n ${NS} -o jsonpath='{.status.conditions[?(@.type==\"Synced\")].message}{\"\\n\"}'"

if gitops_is_live; then
  note "The migrate policy landed in the same PR; Kyverno admission strips pins and sets xrd-api-version=v2."
elif [[ "${DEMO_MODE}" == gitops ]]; then
  note "GitOps retarget: generate Kyverno policies, apply, admission strips pins and sets xrd-api-version=v2."
  pe "convctl generate kyverno --xrd 03-promote-v2/xrd.yaml --from v1 --to v2"
  pe "kubectl apply -f gitops/policies/from-v1-to-v2.yaml"
else
  note "Retarget every XR onto xwidgets-v2.example.org and drop the pinned revision:"
  pe "cat patches/retarget-v2.json"
  pe 'for xr in $(kubectl get xwidgets.example.org -n '"${NS}"' -o name); do kubectl patch "$xr" -n '"${NS}"' --type=json --patch-file patches/retarget-v2.json; done'
fi
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/demo -n ${NS}"
pe "kubectl wait --for=condition=Synced --timeout=90s xwidgets.example.org/demo -n ${NS}"
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/from-v2 -n ${NS}"
pe "kubectl wait --for=condition=Synced --timeout=90s xwidgets.example.org/from-v2 -n ${NS}"
note "Storage is now v2. migrate-storage empty-SSA-patches every XR at the storage GVK. The retarget usually already rewrote etcd; this catches stragglers. Not a GitOps file — cluster housekeeping."
pe "convctl migrate-storage --xrd xwidgets.example.org"
pe "kubectl get xwidgets.example.org -n ${NS}"

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"
pe "kubectl get xwidgets.v1.example.org from-v2 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v2.example.org from-v2 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get configmap demo-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"
note "demo-settings now has data.capacity — the v2 Composition template replaced data.size."

note "A new XR created after the hub flip picks defaultCompositionRef (v2):"
if gitops_is_live; then
  pe "cat live/after-promote.yaml"
  pe "gitops_ship --branch stage-3-app --title 'stage 3: after-promote XR' --app live/after-promote.yaml"
else
  pe "cat live/after-promote.yaml"
  pe "kubectl apply -f live/after-promote.yaml"
fi
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/after-promote -n ${NS}"
pe "kubectl get configmap after-promote-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"

fi
# ---------------------------------------------------------------------------
if demo_stage 4; then
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

if gitops_is_live; then
  pe "gitops_ship --branch stage-4 --title 'stage 4: add v3 spoke' --stage 04-add-v3"
else
  pe "kubectl apply -f 04-add-v3/xrd.yaml"
  pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale
  pe "kubectl apply -f 04-add-v3/xrdconversionconfig.yaml"
  pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"
fi

pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale
pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"
pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"

note "Create at v3 (spec.name). Read back at v2 and v1 — spoke-to-spoke hops through the hub."
if gitops_is_live; then
  pe "cat live/from-v3.yaml"
  pe "gitops_ship --branch stage-4-app --title 'stage 4: from-v3 XR' --app live/from-v3.yaml"
else
  pe "cat live/from-v3.yaml"
  pe "kubectl apply -f live/from-v3.yaml"
fi
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/from-v3 -n ${NS}"
pe "kubectl get xwidgets.v3.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v2.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v1.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get configmap from-v3-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"
note "Composition still targets the hub (v2), so the ConfigMap has widgetName + capacity."

fi
# ---------------------------------------------------------------------------
if demo_stage 5; then
section "5 — promote v3 to the hub (the new standard)"
note "Same five steps as stage 3, destination v3. rehub again: invert the v3 spoke, compose v1 through the new hub. Then migrate-storage at the new storage version."

pe "convctl rehub --config 04-add-v3/xrdconversionconfig.yaml --xrd 04-add-v3/xrd.yaml --to v3"
note "Reviewed snapshot in this repo (same rewrite):"
pe "cat 05-promote-v3/xrdconversionconfig.yaml"
pe "convctl validate --config 05-promote-v3/xrdconversionconfig.yaml --xrd 05-promote-v3/xrd.yaml"
pe "convctl test --config 05-promote-v3/xrdconversionconfig.yaml --xrd 05-promote-v3/xrd.yaml --samples 05-promote-v3/samples/"
pe "convctl test --config 05-promote-v3/xrdconversionconfig.yaml --xrd 05-promote-v3/xrd.yaml --live"

if gitops_is_live; then
  pe "convctl generate kyverno --xrd 05-promote-v3/xrd.yaml --from v2 --to v3"
  pe "gitops_ship --branch stage-5 --title 'stage 5: promote v3' --stage 05-promote-v3 --policy gitops/policies/from-v2-to-v3.yaml"
else
  pe "kubectl apply -f 05-promote-v3/xrd.yaml"
  pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale
  pe "kubectl apply -f 05-promote-v3/xrdconversionconfig.yaml"
  pe "cat 05-promote-v3/composition.yaml"
  pe "kubectl apply -f 05-promote-v3/composition.yaml"
  pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"
fi

pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale
pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"

note "Existing XRs are still pinned to xwidgets-v2.example.org — incompatible once v3 is referenceable."
pe "kubectl get xwidgets.example.org -n ${NS}"
if gitops_is_live; then
  note "The v3 migrate policy landed in the same PR; Kyverno admission retargets existing XRs."
elif [[ "${DEMO_MODE}" == gitops ]]; then
  note "GitOps retarget to v3 — same policy name, new --from/--to:"
  pe "convctl generate kyverno --xrd 05-promote-v3/xrd.yaml --from v2 --to v3"
  pe "kubectl apply -f gitops/policies/from-v2-to-v3.yaml"
else
  pe "cat patches/retarget-v3.json"
  pe 'for xr in $(kubectl get xwidgets.example.org -n '"${NS}"' -o name); do kubectl patch "$xr" -n '"${NS}"' --type=json --patch-file patches/retarget-v3.json; done'
fi
pe 'for xr in $(kubectl get xwidgets.example.org -n '"${NS}"' -o name); do kubectl wait --for=condition=Ready --timeout=90s -n '"${NS}"' "$xr"; kubectl wait --for=condition=Synced --timeout=90s -n '"${NS}"' "$xr"; done'
note "Storage is now v3. Same migrate-storage step as after the v2 promote."
pe "convctl migrate-storage --xrd xwidgets.example.org"
pe "kubectl get xwidgets.example.org -n ${NS}"

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"
pe "kubectl get xwidgets.v3.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v2.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get configmap from-v3-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"
note "ConfigMap now has data.name + data.capacity — v3 is the standard."

if gitops_is_live; then
  pe "cat live/on-v3.yaml"
  pe "gitops_ship --branch stage-5-app --title 'stage 5: on-v3 XR' --app live/on-v3.yaml"
else
  pe "cat live/on-v3.yaml"
  pe "kubectl apply -f live/on-v3.yaml"
fi
pe "kubectl wait --for=condition=Ready --timeout=90s xwidgets.example.org/on-v3 -n ${NS}"
pe "kubectl get configmap on-v3-settings -n ${NS} -o jsonpath='{.data}{\"\\n\"}'"

fi
# ---------------------------------------------------------------------------
if demo_stage 6; then
section "6 — deprecate v1"
note "Order: drop v1 from XRDConversionConfig BEFORE setting served: false. A spoke whose version is not served fails validation."

note "The mistake: XRD already has served:false on v1, but the config still lists v1 as a spoke."
pe "cat mistakes/06-spoke-not-served.yaml"
pe_fail "convctl validate --config mistakes/06-spoke-not-served.yaml --xrd 06-deprecate-v1/xrd.yaml"

note "Correct order: conversion config without v1, then XRD with served: false."
pe "cat 06-deprecate-v1/xrdconversionconfig.yaml"

note "App git still has demo at v1. Dropping the spoke while that object stays on v1 fails convctl test."
pe "cat gitops/apps/widget.yaml"
pe_fail "convctl test --config 06-deprecate-v1/xrdconversionconfig.yaml --xrd 05-promote-v3/xrd.yaml --samples gitops/apps/"

note "Bump the app XR to v2 (still served, still a spoke), then drop v1 from the config."
pe "cat 06-deprecate-v1/widget.yaml"
pe "convctl test --config 06-deprecate-v1/xrdconversionconfig.yaml --xrd 05-promote-v3/xrd.yaml --samples 06-deprecate-v1/"
pe "convctl validate --config 06-deprecate-v1/xrdconversionconfig.yaml --xrd 06-deprecate-v1/xrd.yaml"
pe "convctl test --config 06-deprecate-v1/xrdconversionconfig.yaml --xrd 06-deprecate-v1/xrd.yaml --samples 06-deprecate-v1/samples/"
pe "convctl test --config 06-deprecate-v1/xrdconversionconfig.yaml --xrd 06-deprecate-v1/xrd.yaml --live"

if gitops_is_live; then
  note "Two PRs so the config lands before served:false (same order as kubectl apply). The first PR also moves the v1 app XR to v2."
  pe "gitops_ship --branch stage-6-config --title 'stage 6: drop v1 from conversion config' --stage 06-deprecate-v1 --skip-xrd --app 06-deprecate-v1/widget.yaml"
  pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"
  pe "gitops_ship --branch stage-6-unserve --title 'stage 6: un-serve v1' --stage 06-deprecate-v1 --skip-config"
else
  pe "kubectl apply -f 06-deprecate-v1/xrdconversionconfig.yaml"
  pe "kubectl wait --for=condition=Applied --timeout=120s xrdconversionconfig/xwidgets-conversion"
  pe "kubectl apply -f 06-deprecate-v1/xrd.yaml"
fi
pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"
pe "kubectl get xwidgets.v2.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v3.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"

note "v1 is no longer served:"
pe_fail "kubectl get xwidgets.v1.example.org from-v2 -n ${NS}" "# ↑ expected: the v1 API is gone"

note "The v1 version *block* is still on the XRD. Kubernetes will not let us delete it while the generated CRD's status.storedVersions still lists v1 — even if etcd is already at v3."
pe "kubectl get crd xwidgets.example.org -o jsonpath='storage={.spec.versions[?(@.storage==true)].name}{\"\\nstoredVersions=\"}{.status.storedVersions}{\"\\n\"}'"

note "Required file-order step before dropping the block: migrate-storage --prune-stored-versions. The empty SSA pass rewrites any XR still encoded at an old version; --prune-stored-versions is what unblocks deleting v1. Not desired-state YAML — run it locally even under Flux/Argo."
pe "./show-storage.sh"
pe "convctl migrate-storage --xrd xwidgets.example.org --prune-stored-versions"

note "After: etcd root apiVersion v3, storedVersions [v3]. Leftover v2 in managedFields is normal."
pe "./show-storage.sh"
pe "kubectl get crd xwidgets.example.org -o jsonpath='{.status.storedVersions}{\"\\n\"}'"

note "Now the v1 block can actually leave the XRD:"
pe "cat 06-deprecate-v1/xrd-drop-v1.yaml"
if gitops_is_live; then
  pe "gitops_ship --branch stage-6-drop --title 'stage 6: drop v1 block from XRD' --stage 06-deprecate-v1 --xrd 06-deprecate-v1/xrd-drop-v1.yaml --skip-config"
else
  pe "kubectl apply -f 06-deprecate-v1/xrd-drop-v1.yaml"
fi
pe "kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org"
kyverno_refresh_if_stale
pe "kubectl wait --for=condition=Established --timeout=60s compositeresourcedefinition.apiextensions.crossplane.io/xwidgets.example.org"

pe "kubectl get compositeresourcedefinition xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  referenceable={.referenceable}{\"\\n\"}{end}'"
pe "kubectl get crd xwidgets.example.org -o jsonpath='{range .spec.versions[*]}{.name}  served={.served}  storage={.storage}{\"\\n\"}{end}'"
note "v1 is gone from both the XRD and the generated CRD. v2 is still a served spoke."

pe "kubectl get xwidgets.v2.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"
pe "kubectl get xwidgets.v3.example.org from-v3 -n ${NS} -o yaml | sed -n '/^spec:/,/^status:/p'"

fi
section "Done"
cat <<EOF
Hub is v3 (the standard), spoke is v2, Composition is xwidgets-v3.example.org.
v1 has been un-served, storedVersions pruned, and removed from the XRD.
Hub flips used convctl rehub (config) and convctl migrate-storage (etcd).
Deprecating v2 later is the same sequence: drop-from-config, served:false,
convctl migrate-storage --prune-stored-versions, then drop the version block.

Objects left in ${NS}: demo, from-v2, after-promote, from-v3, on-v3.

Wipe them with:
  ${EXAMPLE_DIR}/demo.sh --cleanup
EOF
if gitops_is_live; then
  cat <<EOF

This run also installed ${GITOPS_ENGINE} and an Actions runner.
If this process created ${GITHUB_REPO:-the demo repo}:
  ${EXAMPLE_DIR}/demo.sh --cleanup --delete-repo
--delete-repo never deletes a --github-repo you passed in.
EOF
fi
