# Helpers for --demo-mode gitops --gitops-engine flux|argo.
# Sourced from ../demo.sh. Relies on die() and the flag globals.
#
# Pins (override with env):
#   GITOPS_FLUX_VERSION          flux2 install.yaml tag
#   GITOPS_ARGO_CHART_VERSION    argo-cd Helm chart
#   GITOPS_RUNNER_IMAGE          ghcr.io/actions/actions-runner tag
#   GITOPS_CONVCTL_IMAGE         tiny convctl image loaded into kind

GITOPS_FLUX_VERSION="${GITOPS_FLUX_VERSION:-v2.9.3}"
GITOPS_ARGO_CHART_VERSION="${GITOPS_ARGO_CHART_VERSION:-10.3.0}"
GITOPS_RUNNER_IMAGE="${GITOPS_RUNNER_IMAGE:-ghcr.io/actions/actions-runner:2.336.0}"
GITOPS_CONVCTL_IMAGE="${GITOPS_CONVCTL_IMAGE:-xwidget-demo-convctl:dev}"
GITOPS_APP_NAME="${GITOPS_APP_NAME:-xwidget-demo}"
GITOPS_RUNNER_LABEL="${GITOPS_RUNNER_LABEL:-xwidget-demo}"

GITOPS_DIR="${EXAMPLE_DIR}/gitops"
GITOPS_WORKTREE="${GITOPS_WORKTREE:-${GITOPS_DIR}/.worktree}"
GITOPS_STATE_FILE="${GITOPS_DIR}/.demo-state"
GITOPS_OPEN_PR="${GITOPS_OPEN_PR:-}"
GITOPS_OPEN_BRANCH="${GITOPS_OPEN_BRANCH:-}"

# After XRD versions change, Kyverno's REST mapper can miss a newly
# served GVR (failurePolicy Fail → Flux dry-run denied). Probe every
# served version, not just storage — storage stays v2 when v3 is added.
# Restart is silent: not a demo-magic step.
kyverno_discovery_stale_msg() {
  echo "$1" | grep -qiE 'not found in group example\.org/|failed calling webhook.*kyverno|connect: connection refused|no endpoints available|service .*kyverno'
}

kyverno_served_versions() {
  local line name served
  while IFS= read -r line; do
    name="${line%%=*}"
    served="${line#*=}"
    [[ -n "${name}" ]] || continue
    if [[ -z "${served}" || "${served}" == true ]]; then
      printf '%s\n' "${name}"
    fi
  done < <(kubectl get crd xwidgets.example.org \
    -o jsonpath='{range .spec.versions[*]}{.name}={.served}{"\n"}{end}' 2>/dev/null)
}

kyverno_xr_discovery_ok() {
  kubectl get ns kyverno >/dev/null 2>&1 || return 0
  kubectl get crd xwidgets.example.org >/dev/null 2>&1 || return 0
  local group ver out
  group="$(kubectl get crd xwidgets.example.org -o jsonpath='{.spec.group}' 2>/dev/null || true)"
  [[ -n "${group}" ]] || return 0
  while IFS= read -r ver; do
    [[ -n "${ver}" ]] || continue
    out="$(kubectl apply --dry-run=server --server-side -f - 2>&1 <<EOF || true
apiVersion: ${group}/${ver}
kind: XWidget
metadata:
  name: kyverno-discovery-probe
  namespace: ${NS:-default}
spec: {}
EOF
)"
    if kyverno_discovery_stale_msg "${out}"; then
      return 1
    fi
  done < <(kyverno_served_versions)
  return 0
}

kyverno_restart_admission() {
  kubectl get deploy -n kyverno kyverno-admission-controller >/dev/null 2>&1 || return 0
  {
    kubectl rollout restart deploy/kyverno-admission-controller -n kyverno
    kubectl rollout restart deploy/kyverno-background-controller -n kyverno 2>/dev/null || true
    kubectl rollout status deploy/kyverno-admission-controller -n kyverno --timeout=120s
    kubectl rollout status deploy/kyverno-background-controller -n kyverno --timeout=120s 2>/dev/null || true
    local i
    for i in $(seq 1 40); do
      kyverno_xr_discovery_ok && return 0
      sleep 2
    done
  } >/dev/null 2>&1 || true
}

kyverno_refresh_if_stale() {
  kubectl get deploy -n kyverno kyverno-admission-controller >/dev/null 2>&1 || return 0
  kyverno_xr_discovery_ok && return 0
  kyverno_restart_admission
}

gitops_is_live() {
  [[ "${DEMO_MODE:-}" == gitops && "${GITOPS_ENGINE:-simulate}" != simulate ]]
}

gitops_manifest_root() {
  if [[ -n "${GIT_PREFIX:-}" ]]; then
    echo "${GITOPS_WORKTREE}/${GIT_PREFIX}"
  else
    echo "${GITOPS_WORKTREE}"
  fi
}

gitops_rel_prefix() {
  if [[ -n "${GIT_PREFIX:-}" ]]; then
    echo "${GIT_PREFIX}"
  else
    echo "."
  fi
}

# ---------------------------------------------------------------------------
# State
# ---------------------------------------------------------------------------

gitops_state_write() {
  (
    umask 077
    cat > "${GITOPS_STATE_FILE}" <<EOF
CREATED_REPO=${CREATED_REPO:-0}
GITHUB_REPO=${GITHUB_REPO:-}
GITOPS_ENGINE=${GITOPS_ENGINE:-}
GIT_PREFIX=${GIT_PREFIX:-}
GITOPS_BASE_BRANCH=${GITOPS_BASE_BRANCH:-main}
INSTALLED_ENGINE=${INSTALLED_ENGINE:-0}
INSTALLED_RUNNER=${INSTALLED_RUNNER:-0}
EOF
  )
}

gitops_state_load() {
  if [[ -f "${GITOPS_STATE_FILE}" ]]; then
    # shellcheck disable=SC1090
    source "${GITOPS_STATE_FILE}"
  fi
}

# ---------------------------------------------------------------------------
# Prerequisites
# ---------------------------------------------------------------------------

gitops_require_tools() {
  command -v gh >/dev/null 2>&1 || die "gh not found on PATH (required for --gitops-engine ${GITOPS_ENGINE})"
  command -v git >/dev/null 2>&1 || die "git not found on PATH (required for --gitops-engine ${GITOPS_ENGINE})"
  command -v helm >/dev/null 2>&1 || die "helm not found on PATH (required for --gitops-engine ${GITOPS_ENGINE})"
  command -v docker >/dev/null 2>&1 || die "docker not found on PATH (required to build/load the convctl runner image)"
  command -v kind >/dev/null 2>&1 || die "kind not found on PATH (required to load the convctl runner image)"
  gh auth status >/dev/null 2>&1 || die "gh is not authenticated. Run 'gh auth login' (repo + workflow scopes)."
}

gitops_resolve_kind_cluster() {
  if [[ -n "${KIND_CLUSTER_NAME:-${DEV_CLUSTER_NAME:-}}" ]]; then
    KIND_CLUSTER="${KIND_CLUSTER_NAME:-${DEV_CLUSTER_NAME}}"
    return
  fi
  local ctx
  ctx="$(kubectl config current-context 2>/dev/null || true)"
  if [[ "${ctx}" == kind-* ]]; then
    KIND_CLUSTER="${ctx#kind-}"
    return
  fi
  KIND_CLUSTER="declarative-conversion-dev"
}

gitops_resolve_repo() {
  if [[ "${CREATE_REPO:-0}" -eq 1 && -n "${GITHUB_REPO_FLAG:-}" ]]; then
    die "--create-repo and --github-repo are mutually exclusive"
  fi
  if [[ "${CREATE_REPO:-0}" -eq 1 ]]; then
    local name="${CREATE_REPO_NAME:-xwidget-lifecycle-demo}"
    if [[ "${name}" == */* ]]; then
      GITHUB_REPO="${name}"
    else
      local owner
      owner="$(gh api user --jq .login)"
      GITHUB_REPO="${owner}/${name}"
    fi
    return
  fi
  if [[ -n "${GITHUB_REPO_FLAG:-}" ]]; then
    GITHUB_REPO="${GITHUB_REPO_FLAG}"
    return
  fi
  die "--gitops-engine ${GITOPS_ENGINE} requires --github-repo owner/name or --create-repo"
}

# ---------------------------------------------------------------------------
# Repo + worktree
# ---------------------------------------------------------------------------

gitops_set_push_url() {
  # Do not embed gh auth token in origin — git stores remotes in plaintext
  # .git/config. The worktree uses gh's credential helper instead.
  git -C "${GITOPS_WORKTREE}" config --local credential.https://github.com.helper ""
  git -C "${GITOPS_WORKTREE}" config --local --add credential.https://github.com.helper "!gh auth git-credential"
  git -C "${GITOPS_WORKTREE}" remote set-url origin "https://github.com/${GITHUB_REPO}.git"
}

gitops_create_or_use_repo() {
  if [[ "${CREATE_REPO:-0}" -eq 1 ]]; then
    if gh repo view "${GITHUB_REPO}" >/dev/null 2>&1; then
      echo "Reusing existing ${GITHUB_REPO} (previous --create-repo run). --delete-repo will still remove it."
    else
      echo "Creating GitHub repo ${GITHUB_REPO} (${REPO_VISIBILITY:-private})…"
      gh repo create "${GITHUB_REPO}" --"${REPO_VISIBILITY:-private}" --clone=false
    fi
    CREATED_REPO=1
    gitops_state_write
  else
    gh repo view "${GITHUB_REPO}" >/dev/null 2>&1 \
      || die "GitHub repo ${GITHUB_REPO} not found (or no access). Check --github-repo."
    CREATED_REPO=0
    if [[ -z "${GIT_PREFIX:-}" ]]; then
      echo "note: writing at the repo root of ${GITHUB_REPO}. Pass --git-prefix PATH to keep existing files." >&2
    fi
  fi
}

gitops_clone_worktree() {
  rm -rf "${GITOPS_WORKTREE}"
  mkdir -p "${GITOPS_WORKTREE}"
  local default=""
  default="$(gh repo view "${GITHUB_REPO}" --jq '.defaultBranchRef.name // empty' 2>/dev/null || true)"
  if [[ -n "${default}" ]]; then
    gh repo clone "${GITHUB_REPO}" "${GITOPS_WORKTREE}"
    GITOPS_BASE_BRANCH="${default}"
    git -C "${GITOPS_WORKTREE}" checkout "${GITOPS_BASE_BRANCH}"
  else
    git -C "${GITOPS_WORKTREE}" init -b main
    git -C "${GITOPS_WORKTREE}" remote add origin "https://github.com/${GITHUB_REPO}.git"
    GITOPS_BASE_BRANCH=main
  fi
  gitops_set_push_url
}

gitops_git() {
  git -C "${GITOPS_WORKTREE}" \
    -c user.email=xwidget-demo@users.noreply.github.com \
    -c user.name=xwidget-demo \
    "$@"
}

gitops_write_kustomization() {
  local dir="$1"
  mkdir -p "${dir}"
  {
    echo "apiVersion: kustomize.config.k8s.io/v1beta1"
    echo "kind: Kustomization"
    echo "resources:"
    local found=0
    local f rel
    while IFS= read -r f; do
      rel="${f#"${dir}"/}"
      echo "  - ${rel}"
      found=1
    done < <(find "${dir}" \( -name samples -o -name .git \) -prune -o \
      -type f \( -name '*.yaml' -o -name '*.yml' \) ! -name 'kustomization.yaml' -print | sort)
    if [[ "${found}" -eq 0 ]]; then
      echo "  []"
    fi
  } > "${dir}/kustomization.yaml"
  # "resources: / []" is invalid; rewrite empty as resources: []
  if grep -q '^resources:$' "${dir}/kustomization.yaml" && grep -q '^  \[\]$' "${dir}/kustomization.yaml"; then
    printf '%s\n' \
      "apiVersion: kustomize.config.k8s.io/v1beta1" \
      "kind: Kustomization" \
      "resources: []" > "${dir}/kustomization.yaml"
  fi
}

gitops_refresh_kustomizations() {
  local root
  root="$(gitops_manifest_root)"
  mkdir -p "${root}/platform" "${root}/apps" "${root}/conversion"
  # Conversion config is a sibling tree so Flux can apply the XRD first
  # (dependsOn) while CI still sees both files in the same commit.
  rm -f "${root}/platform/xrdconversionconfig.yaml"
  gitops_write_kustomization "${root}/platform"
  gitops_write_kustomization "${root}/apps"
  gitops_write_kustomization "${root}/conversion"
  cat > "${root}/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - platform
  - apps
EOF
}

gitops_yaml_name() {
  awk 'BEGIN{m=0} /^metadata:/{m=1} m && /^  name:/{print $2; exit}' "$1"
}

gitops_write_workflow() {
  mkdir -p "${GITOPS_WORKTREE}/.github/workflows"
  sed -e "s|__CONVCTL_ROOT__|$(gitops_rel_prefix)|g" \
      -e "s|__DEFAULT_BRANCH__|${GITOPS_BASE_BRANCH:-main}|g" \
    "${GITOPS_DIR}/workflow/convctl.yaml" \
    > "${GITOPS_WORKTREE}/.github/workflows/convctl.yaml"
}

gitops_seed_main() {
  local root
  root="$(gitops_manifest_root)"
  mkdir -p "${root}/platform" \
    "${root}/apps" \
    "${root}/conversion"
  gitops_write_workflow

  if [[ "${CREATED_REPO}" -eq 1 ]]; then
    cp "${GITOPS_DIR}/repo-README.md" "${GITOPS_WORKTREE}/README.md"
  elif [[ -n "${GIT_PREFIX:-}" ]]; then
    cp "${GITOPS_DIR}/repo-README.md" "${root}/README.md"
  fi

  cp "${EXAMPLE_DIR}/functions.yaml" "${root}/platform/functions.yaml"
  gitops_refresh_kustomizations

  gitops_git add -A
  if gitops_git diff --cached --quiet; then
    echo "Seed already present on main."
    return
  fi
  gitops_git commit -m "seed: workflow, functions, empty apps"
  gitops_git push -u origin "${GITOPS_BASE_BRANCH:-main}"
}

# ---------------------------------------------------------------------------
# convctl image + in-cluster runner
# ---------------------------------------------------------------------------

# kind load docker-image / image-archive both call
# `ctr images import --all-platforms`, which fails on Buildx attestation
# indexes ("content digest … not found"). Import one platform ourselves.
gitops_docker_platform() {
  case "$(uname -m)" in
    aarch64|arm64) echo linux/arm64 ;;
    *) echo linux/amd64 ;;
  esac
}

gitops_kind_node() {
  kind get nodes --name "${KIND_CLUSTER}" | head -n1
}

gitops_kind_import_image() {
  local image="$1"
  local node platform
  node="$(gitops_kind_node)"
  [[ -n "${node}" ]] || die "no kind nodes for cluster ${KIND_CLUSTER}"
  platform="$(gitops_docker_platform)"
  echo "Importing ${image} into ${node} (${platform})…"
  docker save "${image}" | docker exec -i "${node}" \
    ctr --namespace=k8s.io images import --platform "${platform}" --digests --snapshotter=overlayfs -
}

gitops_build_convctl_image() {
  gitops_resolve_kind_cluster
  local platform runner_flat
  platform="$(gitops_docker_platform)"
  runner_flat="xwidget-demo-actions-runner:dev"
  mkdir -p "${GITOPS_DIR}/runner"
  [[ -x "${REPO_ROOT}/bin/convctl" ]] || die "bin/convctl missing; demo.sh should have built it"
  cp "${REPO_ROOT}/bin/convctl" "${GITOPS_DIR}/runner/convctl"
  docker build --platform "${platform}" --provenance=false --sbom=false \
    -t "${GITOPS_CONVCTL_IMAGE}" -f "${GITOPS_DIR}/runner/Dockerfile" "${GITOPS_DIR}/runner"
  rm -f "${GITOPS_DIR}/runner/convctl"

  echo "Pulling ${GITOPS_RUNNER_IMAGE} and flattening to ${runner_flat} (no attestations)…"
  docker pull --platform "${platform}" "${GITOPS_RUNNER_IMAGE}"
  docker build --platform "${platform}" --provenance=false --sbom=false \
    -t "${runner_flat}" - <<EOF
FROM ${GITOPS_RUNNER_IMAGE}
EOF
  GITOPS_RUNNER_IMAGE="${runner_flat}"

  echo "Loading images into kind cluster ${KIND_CLUSTER}…"
  gitops_kind_import_image "${GITOPS_CONVCTL_IMAGE}"
  gitops_kind_import_image "${GITOPS_RUNNER_IMAGE}"
}

gitops_install_runner() {
  local token name
  # Stop any leftover pod first so it cannot hold a GitHub session, then
  # delete every demo runner registration. Reusing kind-xwidget-demo after a
  # restart hits TaskAgentSessionConflictException ("session already exists").
  kubectl delete deployment xwidget-demo-runner -n actions-runner --ignore-not-found --wait=true --timeout=120s >/dev/null 2>&1 || true
  kubectl delete pod -n actions-runner -l app.kubernetes.io/name=xwidget-demo-runner --wait=true --timeout=120s >/dev/null 2>&1 || true
  gitops_remove_github_runner
  # Broker sessions linger a few seconds after DELETE.
  sleep 5

  name="kind-xwidget-demo-$(date +%s)"
  echo "Requesting Actions runner registration token for ${GITHUB_REPO} (name ${name})…"
  token="$(gh api -X POST "repos/${GITHUB_REPO}/actions/runners/registration-token" --jq .token)"
  [[ -n "${token}" ]] || die "failed to get Actions runner registration token for ${GITHUB_REPO}"

  kubectl create namespace actions-runner --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n actions-runner delete configmap runner-entrypoint --ignore-not-found
  kubectl -n actions-runner create configmap runner-entrypoint \
    --from-file=entrypoint.sh="${GITOPS_DIR}/runner/entrypoint.sh"
  kubectl -n actions-runner delete secret runner-auth --ignore-not-found
  kubectl -n actions-runner create secret generic runner-auth \
    --from-literal=repo="${GITHUB_REPO}" \
    --from-literal=token="${token}" \
    --from-literal=name="${name}"
  sed -e "s|xwidget-demo-convctl:dev|${GITOPS_CONVCTL_IMAGE}|g" \
      -e "s|ghcr.io/actions/actions-runner:2.336.0|${GITOPS_RUNNER_IMAGE}|g" \
    "${GITOPS_DIR}/runner/manifests.yaml" | kubectl apply -f -

  kubectl -n actions-runner rollout status deployment/xwidget-demo-runner --timeout=180s
  INSTALLED_RUNNER=1
}

gitops_wait_runner() {
  echo "Waiting for GitHub to see runner label ${GITOPS_RUNNER_LABEL} online…"
  local i status
  for i in $(seq 1 60); do
    status="$(gh api "repos/${GITHUB_REPO}/actions/runners" \
      --jq ".runners[] | select(.labels[]?.name==\"${GITOPS_RUNNER_LABEL}\") | .status" \
      2>/dev/null | head -n1 || true)"
    if [[ "${status}" == online ]]; then
      echo "Runner is online."
      return 0
    fi
    sleep 5
  done
  die "self-hosted runner did not come online (check kubectl -n actions-runner logs)"
}

# ---------------------------------------------------------------------------
# Flux / Argo
# ---------------------------------------------------------------------------

gitops_git_secret_token() {
  gh auth token
}

gitops_install_flux() {
  echo "Installing Flux ${GITOPS_FLUX_VERSION}…"
  kubectl apply --server-side --force-conflicts \
    -f "https://github.com/fluxcd/flux2/releases/download/${GITOPS_FLUX_VERSION}/install.yaml"
  kubectl -n flux-system wait --for=condition=Available --timeout=300s deployment --all

  local url="https://github.com/${GITHUB_REPO}"
  kubectl -n flux-system delete secret gitops-auth --ignore-not-found
  kubectl -n flux-system create secret generic gitops-auth \
    --from-literal=username=git \
    --from-literal=password="$(gitops_git_secret_token)"

  kubectl apply -f - <<EOF
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: ${GITOPS_APP_NAME}
  namespace: flux-system
spec:
  interval: 30s
  url: ${url}
  secretRef:
    name: gitops-auth
  ref:
    branch: ${GITOPS_BASE_BRANCH:-main}
EOF
  gitops_apply_flux_kustomizations
  INSTALLED_ENGINE=1
}

# Platform (XRD, Compositions, policies) is wait:true.
# Apps are a sibling Kustomization with wait:false — Flux kstatus reports
# XWidgets as NotFound across conversion GVKs, which used to block platform
# Ready and therefore conversion/.
gitops_apply_flux_kustomizations() {
  local prefix="./"
  if [[ "$(gitops_rel_prefix)" != . ]]; then
    prefix="./$(gitops_rel_prefix)/"
  fi
  kubectl apply -f - <<EOF
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: ${GITOPS_APP_NAME}
  namespace: flux-system
spec:
  interval: 30s
  prune: true
  wait: true
  timeout: 3m
  path: ${prefix}platform
  sourceRef:
    kind: GitRepository
    name: ${GITOPS_APP_NAME}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: ${GITOPS_APP_NAME}-conversion
  namespace: flux-system
spec:
  interval: 15s
  retryInterval: 10s
  prune: true
  wait: true
  timeout: 3m
  path: ${prefix}conversion
  dependsOn:
    - name: ${GITOPS_APP_NAME}
  sourceRef:
    kind: GitRepository
    name: ${GITOPS_APP_NAME}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: ${GITOPS_APP_NAME}-apps
  namespace: flux-system
spec:
  interval: 30s
  prune: true
  wait: false
  timeout: 3m
  path: ${prefix}apps
  dependsOn:
    - name: ${GITOPS_APP_NAME}
  sourceRef:
    kind: GitRepository
    name: ${GITOPS_APP_NAME}
EOF
}

gitops_install_argo() {
  echo "Installing Argo CD chart ${GITOPS_ARGO_CHART_VERSION}…"
  helm repo add argo https://argoproj.github.io/argo-helm >/dev/null
  helm repo update argo >/dev/null
  helm upgrade --install argocd argo/argo-cd \
    --namespace argocd --create-namespace \
    --version "${GITOPS_ARGO_CHART_VERSION}" \
    --set configs.cm."timeout\.reconciliation"=30s \
    --wait --timeout 10m

  kubectl -n argocd delete secret "repo-${GITOPS_APP_NAME}" --ignore-not-found
  kubectl -n argocd create secret generic "repo-${GITOPS_APP_NAME}" \
    --from-literal=type=git \
    --from-literal=url="https://github.com/${GITHUB_REPO}" \
    --from-literal=username=git \
    --from-literal=password="$(gitops_git_secret_token)"
  kubectl -n argocd label secret "repo-${GITOPS_APP_NAME}" \
    argocd.argoproj.io/secret-type=repository --overwrite

  local path
  path="$(gitops_rel_prefix)"
  kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${GITOPS_APP_NAME}
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/${GITHUB_REPO}.git
    targetRevision: ${GITOPS_BASE_BRANCH:-main}
    path: ${path}
  destination:
    server: https://kubernetes.default.svc
    namespace: default
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
  ignoreDifferences:
    - group: example.org
      kind: XWidget
      jsonPointers:
        - /spec/crossplane/compositionRef
        - /spec/crossplane/compositionRevisionRef
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${GITOPS_APP_NAME}-conversion
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "1"
spec:
  project: default
  source:
    repoURL: https://github.com/${GITHUB_REPO}.git
    targetRevision: ${GITOPS_BASE_BRANCH:-main}
    path: ${path}/conversion
  destination:
    server: https://kubernetes.default.svc
    namespace: default
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    retry:
      limit: 10
      backoff:
        duration: 10s
        factor: 1
        maxDuration: 1m
EOF
  INSTALLED_ENGINE=1
}

gitops_head_sha() {
  if [[ -d "${GITOPS_WORKTREE}/.git" ]]; then
    git -C "${GITOPS_WORKTREE}" rev-parse HEAD
    return
  fi
  gh api "repos/${GITHUB_REPO}/commits/${GITOPS_BASE_BRANCH:-main}" --jq .sha
}

gitops_request_reconcile() {
  case "${GITOPS_ENGINE}" in
    flux)
      local now
      now="$(date +%s)"
      kubectl -n flux-system annotate gitrepository "${GITOPS_APP_NAME}" \
        "reconcile.fluxcd.io/requestedAt=${now}" --overwrite >/dev/null
      kubectl -n flux-system annotate kustomization "${GITOPS_APP_NAME}" \
        "reconcile.fluxcd.io/requestedAt=${now}" --overwrite >/dev/null
      kubectl -n flux-system annotate kustomization "${GITOPS_APP_NAME}-conversion" \
        "reconcile.fluxcd.io/requestedAt=${now}" --overwrite >/dev/null 2>/dev/null || true
      kubectl -n flux-system annotate kustomization "${GITOPS_APP_NAME}-apps" \
        "reconcile.fluxcd.io/requestedAt=${now}" --overwrite >/dev/null 2>/dev/null || true
      ;;
    argo)
      kubectl -n argocd patch application "${GITOPS_APP_NAME}" --type merge \
        -p '{"operation":{"initiatedBy":{"username":"xwidget-demo"},"sync":{"revision":"HEAD"}}}' \
        >/dev/null 2>&1 || true
      ;;
  esac
}

# kubectl wait --for=condition=Ready returns immediately if the Kustomization
# is still Ready from the *previous* commit (seed, last stage). Wait until
# Flux/Argo has attempted the SHA we just merged.
gitops_wait_sync() {
  local want
  want="$(gitops_head_sha)"
  echo "Waiting for ${GITOPS_ENGINE} to reconcile ${want:0:12}…"
  case "${GITOPS_ENGINE}" in
    flux) gitops_wait_flux "${want}" ;;
    argo) gitops_wait_argo "${want}" ;;
    *) die "gitops_wait_sync: unknown engine ${GITOPS_ENGINE}" ;;
  esac
}

gitops_flux_revision() {
  kubectl -n flux-system get kustomization "$1" \
    -o jsonpath='{.status.lastAppliedRevision}' 2>/dev/null || true
}

gitops_wait_flux() {
  local want="$1" i src attempted applied ready msg conv apps
  local kyverno_kicked=0
  local have_apps=0
  kubectl -n flux-system get kustomization "${GITOPS_APP_NAME}-apps" >/dev/null 2>&1 && have_apps=1
  for i in $(seq 1 72); do
    if [[ $((i % 6)) -eq 1 ]]; then
      gitops_request_reconcile
    fi
    src="$(kubectl -n flux-system get gitrepository "${GITOPS_APP_NAME}" \
      -o jsonpath='{.status.artifact.revision}' 2>/dev/null || true)"
    attempted="$(kubectl -n flux-system get kustomization "${GITOPS_APP_NAME}" \
      -o jsonpath='{.status.lastAttemptedRevision}' 2>/dev/null || true)"
    applied="$(gitops_flux_revision "${GITOPS_APP_NAME}")"
    conv="$(gitops_flux_revision "${GITOPS_APP_NAME}-conversion")"
    apps="$(gitops_flux_revision "${GITOPS_APP_NAME}-apps")"
    ready="$(kubectl -n flux-system get kustomization "${GITOPS_APP_NAME}" \
      -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)"
    msg="$(kubectl -n flux-system get kustomization "${GITOPS_APP_NAME}" \
      -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' 2>/dev/null || true)"
    if [[ "${applied}" == *"${want}"* && "${conv}" == *"${want}"* ]]; then
      if [[ "${have_apps}" -eq 0 || "${apps}" == *"${want}"* ]]; then
        echo "Flux applied ${want:0:12} (platform + conversion${have_apps:+ + apps})."
        kyverno_refresh_if_stale
        return 0
      fi
    fi
    # Flux is already failing on a GVR Kyverno has not discovered. Restart
    # now — waiting until Ready would never call the post-success refresh.
    if [[ "${kyverno_kicked}" -eq 0 ]] && kyverno_discovery_stale_msg "${msg}"; then
      kyverno_restart_admission
      gitops_request_reconcile
      kyverno_kicked=1
    fi
    if [[ "${attempted}" == *"${want}"* && "${ready}" != True ]]; then
      echo "Flux is applying ${want:0:12}: ${msg:-not Ready yet}"
    elif [[ "${applied}" == *"${want}"* && "${conv}" != *"${want}"* ]]; then
      echo "Flux platform applied ${want:0:12}; waiting for conversion Kustomization…"
    fi
    sleep 5
  done
  echo "Flux GitRepository revision: ${src:-?}" >&2
  echo "Flux lastAttemptedRevision: ${attempted:-?}" >&2
  echo "Flux lastAppliedRevision: ${applied:-?}" >&2
  echo "Flux conversion lastAppliedRevision: ${conv:-?}" >&2
  echo "Flux Ready: ${ready:-?} ${msg}" >&2
  die "Flux did not reconcile ${want:0:12}"
}

gitops_wait_argo() {
  local want="$1" i sync health rev
  for i in $(seq 1 60); do
    if [[ $((i % 6)) -eq 1 ]]; then
      gitops_request_reconcile
    fi
    sync="$(kubectl -n argocd get application "${GITOPS_APP_NAME}" \
      -o jsonpath='{.status.sync.status}' 2>/dev/null || true)"
    health="$(kubectl -n argocd get application "${GITOPS_APP_NAME}" \
      -o jsonpath='{.status.health.status}' 2>/dev/null || true)"
    rev="$(kubectl -n argocd get application "${GITOPS_APP_NAME}" \
      -o jsonpath='{.status.sync.revision}' 2>/dev/null || true)"
    if [[ "${rev}" == "${want}"* && "${sync}" == Synced ]]; then
      echo "Argo synced ${want:0:12} (health=${health:-?})."
      kyverno_refresh_if_stale
      return 0
    fi
    sleep 5
  done
  die "Argo Application ${GITOPS_APP_NAME} did not sync ${want:0:12} (sync=${sync:-?} rev=${rev:-?})"
}

# ---------------------------------------------------------------------------
# Ship a stage as a PR
# ---------------------------------------------------------------------------

gitops_copy_named_yaml() {
  local src="$1" dest_dir="$2"
  local name
  name="$(gitops_yaml_name "${src}")"
  [[ -n "${name}" ]] || name="$(basename "${src}" .yaml)"
  mkdir -p "${dest_dir}"
  cp "${src}" "${dest_dir}/${name}.yaml"
}

# convctl generate kyverno emits labeler + migrate in one file. Kustomize
# rejects duplicate GVKs, so live ship keeps the standing labeler and
# writes the migrate document to metadata.name.yaml. A hub flip updates
# --from/--to on the same MutatingPolicy; do not mint a second object.
gitops_copy_policy() {
  local src="$1" dest_dir="$2"
  mkdir -p "${dest_dir}"
  if grep -qE '^  name: (set-composition-version-selector-|migrate-)' "${src}"; then
    local tmp name
    tmp="$(mktemp)"
    awk '
      BEGIN { doc = ""; keep = 0 }
      /^---[[:space:]]*$/ {
        if (keep && doc != "") printf "%s", doc
        doc = ""; keep = 0
        next
      }
      { doc = doc $0 "\n" }
      /^  name: set-composition-version-selector-/ { keep = 1 }
      /^  name: migrate-/ { keep = 1 }
      END { if (keep && doc != "") printf "%s", doc }
    ' "${src}" > "${tmp}"
    name="$(awk '/^metadata:/{m=1} m && /^  name:/{print $2; exit}' "${tmp}")"
    [[ -n "${name}" ]] || name="$(basename "${src}" .yaml)"
    # Drop leftover versioned generate dumps so kustomize sees one migrate doc.
    rm -f "${dest_dir}"/from-v*-to-v*.yaml "${dest_dir}"/migrate-*.yaml
    mv "${tmp}" "${dest_dir}/${name}.yaml"
  else
    cp "${src}" "${dest_dir}/$(basename "${src}")"
  fi
}

gitops_copy_stage() {
  local stage="$1"
  local skip_xrd="${2:-0}"
  local skip_config="${3:-0}"
  local xrd_override="${4:-}"
  local config_override="${5:-}"
  local omit_config="${6:-0}"
  local root
  root="$(gitops_manifest_root)"

  [[ -d "${EXAMPLE_DIR}/${stage}" ]] || die "stage directory not found: ${stage}"

  if [[ -n "${xrd_override}" ]]; then
    cp "${EXAMPLE_DIR}/${xrd_override}" "${root}/platform/xrd.yaml"
  elif [[ "${skip_xrd}" -eq 0 && -f "${EXAMPLE_DIR}/${stage}/xrd.yaml" ]]; then
    cp "${EXAMPLE_DIR}/${stage}/xrd.yaml" "${root}/platform/xrd.yaml"
  fi

  mkdir -p "${root}/conversion"
  rm -f "${root}/platform/xrdconversionconfig.yaml"
  if [[ "${omit_config:-0}" -eq 1 ]]; then
    rm -f "${root}/conversion/xrdconversionconfig.yaml"
  elif [[ -n "${config_override}" ]]; then
    cp "${EXAMPLE_DIR}/${config_override}" "${root}/conversion/xrdconversionconfig.yaml"
  elif [[ "${skip_config}" -eq 0 && -f "${EXAMPLE_DIR}/${stage}/xrdconversionconfig.yaml" ]]; then
    cp "${EXAMPLE_DIR}/${stage}/xrdconversionconfig.yaml" "${root}/conversion/xrdconversionconfig.yaml"
  fi

  if [[ -f "${EXAMPLE_DIR}/${stage}/composition.yaml" ]]; then
    gitops_copy_named_yaml \
      "${EXAMPLE_DIR}/${stage}/composition.yaml" \
      "${root}/platform/compositions"
  fi

  if [[ -d "${EXAMPLE_DIR}/${stage}/samples" ]]; then
    rm -rf "${root}/platform/samples"
    cp -a "${EXAMPLE_DIR}/${stage}/samples" "${root}/platform/samples"
  fi
}

gitops_wait_pr_checks() {
  local pr="$1"
  local want_sha="${2:-}"
  if [[ -z "${want_sha}" ]]; then
    want_sha="$(gh pr view "${pr}" --repo "${GITHUB_REPO}" --json headRefOid --jq .headRefOid 2>/dev/null || true)"
  fi
  echo "Waiting for checks on ${pr}${want_sha:+ at ${want_sha:0:7}}…"
  local i n
  for i in $(seq 1 60); do
    if [[ -n "${want_sha}" ]]; then
      n="$(gh api "repos/${GITHUB_REPO}/commits/${want_sha}/check-runs" --jq '.total_count' 2>/dev/null || echo 0)"
    else
      n="$(gh pr view "${pr}" --repo "${GITHUB_REPO}" --json statusCheckRollup \
        --jq '.statusCheckRollup | length' 2>/dev/null || echo 0)"
    fi
    if [[ "${n}" -gt 0 ]]; then
      break
    fi
    sleep 5
  done
  gh pr checks "${pr}" --repo "${GITHUB_REPO}" --watch
}

gitops_ship() {
  local branch="" title="" body="" stage=""
  local skip_xrd=0 skip_config=0 omit_config=0 expect_fail=0 fix_open_pr=0
  local xrd_override="" config_override=""
  local apps=() policies=() extras=()

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --branch) branch="$2"; shift 2 ;;
      --title) title="$2"; shift 2 ;;
      --body) body="$2"; shift 2 ;;
      --stage) stage="$2"; shift 2 ;;
      --skip-xrd) skip_xrd=1; shift ;;
      --skip-config) skip_config=1; shift ;;
      --omit-config) omit_config=1; skip_config=1; shift ;;
      --xrd) xrd_override="$2"; shift 2 ;;
      --config-override) config_override="$2"; shift 2 ;;
      --app) apps+=("$2"); shift 2 ;;
      --policy) policies+=("$2"); shift 2 ;;
      --extra) extras+=("$2"); shift 2 ;;
      --expect-fail) expect_fail=1; shift ;;
      --fix-open-pr) fix_open_pr=1; shift ;;
      *) die "gitops_ship: unknown flag $1" ;;
    esac
  done
  [[ -n "${title}" ]] || die "gitops_ship: --title is required"
  local base="${GITOPS_BASE_BRANCH:-main}"
  local pr=""
  if [[ "${fix_open_pr}" -eq 1 ]]; then
    [[ -n "${GITOPS_OPEN_PR:-}" && -n "${GITOPS_OPEN_BRANCH:-}" ]] \
      || die "gitops_ship --fix-open-pr: no open expect-fail PR (run --expect-fail first)"
    [[ -n "${stage}" ]] || die "gitops_ship --fix-open-pr: --stage is required"
    branch="${GITOPS_OPEN_BRANCH}"
    pr="${GITOPS_OPEN_PR}"
    gitops_git checkout "${branch}"
    config_override=""
  else
    [[ -n "${branch}" ]] || die "gitops_ship: --branch is required"
    gitops_git checkout "${base}"
    gitops_git pull --ff-only origin "${base}" || true
    if gitops_git show-ref --verify --quiet "refs/heads/${branch}"; then
      branch="${branch}-$(date +%s)"
    fi
    gitops_git checkout -b "${branch}"
  fi

  local root
  root="$(gitops_manifest_root)"
  mkdir -p "${root}/platform" "${root}/apps"

  if [[ -n "${stage}" ]]; then
    gitops_copy_stage "${stage}" "${skip_xrd}" "${skip_config}" "${xrd_override}" "${config_override}" "${omit_config}"
  elif [[ -n "${xrd_override}" || -n "${config_override}" ]]; then
    gitops_copy_stage "06-deprecate-v1" "${skip_xrd}" "${skip_config}" "${xrd_override}" "${config_override}" "${omit_config}"
  fi

  local f
  for f in "${apps[@]+"${apps[@]}"}"; do
    cp "${EXAMPLE_DIR}/${f}" "${root}/apps/$(basename "${f}")"
  done
  for f in "${policies[@]+"${policies[@]}"}"; do
    gitops_copy_policy "${EXAMPLE_DIR}/${f}" "${root}/platform/policies"
  done
  for f in "${extras[@]+"${extras[@]}"}"; do
    gitops_copy_named_yaml "${EXAMPLE_DIR}/${f}" "${root}/platform/extras"
  done

  gitops_refresh_kustomizations
  gitops_write_workflow

  gitops_git add -A
  if gitops_git diff --cached --quiet; then
    echo "No changes to ship for ${title}; already on ${base}."
    gitops_git checkout "${base}"
    gitops_wait_sync
    return 0
  fi
  gitops_git commit -m "${title}"
  gitops_git push -u origin "${branch}"

  if [[ "${fix_open_pr}" -eq 1 ]]; then
    echo "Pushed fix to ${pr}"
    gh pr edit "${pr}" --repo "${GITHUB_REPO}" --title "${title}" || true
    local sha rc=0
    sha="$(gitops_git rev-parse HEAD)"
    gitops_wait_pr_checks "${pr}" "${sha}" || rc=$?
    if [[ "${rc}" -ne 0 ]]; then
      gitops_git checkout "${base}"
      die "CI failed for fix-up on ${pr}"
    fi
    gh pr merge "${pr}" --repo "${GITHUB_REPO}" --squash --delete-branch
    GITOPS_OPEN_PR=""
    GITOPS_OPEN_BRANCH=""
    gitops_git checkout "${base}"
    gitops_git pull --ff-only origin "${base}"
    gitops_wait_sync
    return 0
  fi

  [[ -n "${body}" ]] || body="Shipped by examples/crossplane-xr-multiversion/demo.sh (${title})."
  pr="$(gh pr create --repo "${GITHUB_REPO}" \
    --base "${base}" --head "${branch}" \
    --title "${title}" \
    --body "${body}")"
  echo "Opened ${pr}"

  local sha rc=0
  sha="$(gitops_git rev-parse HEAD)"
  gitops_wait_pr_checks "${pr}" "${sha}" || rc=$?

  if [[ "${expect_fail}" -eq 1 ]]; then
    if [[ "${rc}" -eq 0 ]]; then
      gh pr close "${pr}" --repo "${GITHUB_REPO}" --delete-branch \
        --comment "demo: expected convctl CI to fail, but it passed" || true
      gitops_git checkout "${base}"
      die "expected CI to fail for ${title}"
    fi
    echo "CI failed as expected. Leaving ${pr} open for a fix-up push."
    GITOPS_OPEN_PR="${pr}"
    GITOPS_OPEN_BRANCH="${branch}"
    return 0
  fi

  if [[ "${rc}" -ne 0 ]]; then
    gitops_git checkout "${base}"
    die "CI failed for ${pr}"
  fi

  gh pr merge "${pr}" --repo "${GITHUB_REPO}" --squash --delete-branch
  gitops_git checkout "${base}"
  gitops_git pull --ff-only origin "${base}"
  gitops_wait_sync
}

# ---------------------------------------------------------------------------
# Bootstrap / cleanup
# ---------------------------------------------------------------------------

gitops_bootstrap() {
  gitops_require_tools
  gitops_resolve_repo
  gitops_create_or_use_repo
  gitops_clone_worktree
  gitops_seed_main
  gitops_build_convctl_image
  gitops_install_runner
  gitops_wait_runner
  case "${GITOPS_ENGINE}" in
    flux) gitops_install_flux ;;
    argo) gitops_install_argo ;;
    *) die "unknown --gitops-engine ${GITOPS_ENGINE}" ;;
  esac
  gitops_state_write
  gitops_wait_sync
  echo "Waiting for composition functions from git…"
  kubectl wait --for=condition=Healthy --timeout=180s function/function-go-templating
  kubectl wait --for=condition=Healthy --timeout=180s function/function-auto-ready
}

gitops_remove_github_runner() {
  local id
  # Remove every demo runner (fixed name or kind-xwidget-demo-<timestamp>).
  while read -r id; do
    [[ -z "${id}" ]] && continue
    gh api -X DELETE "repos/${GITHUB_REPO}/actions/runners/${id}" >/dev/null 2>&1 || true
  done < <(gh api "repos/${GITHUB_REPO}/actions/runners" --jq \
    '.runners[] | select((.name | startswith("kind-xwidget-demo")) or ([.labels[].name] | index("xwidget-demo"))) | .id' \
    2>/dev/null || true)
}

# Delete demo Flux/Argo apps so they stop recreating objects. Safe when
# those engines are not installed.
gitops_stop_reconcile() {
  kubectl delete kustomization "${GITOPS_APP_NAME}" "${GITOPS_APP_NAME}-conversion" "${GITOPS_APP_NAME}-apps" -n flux-system --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete application "${GITOPS_APP_NAME}" "${GITOPS_APP_NAME}-conversion" -n argocd --ignore-not-found >/dev/null 2>&1 || true
}

gitops_cleanup_cluster() {
  gitops_state_load
  if [[ "${INSTALLED_RUNNER:-0}" -eq 1 || "${GITOPS_ENGINE:-}" == flux || "${GITOPS_ENGINE:-}" == argo ]]; then
    if [[ -n "${GITHUB_REPO:-}" ]]; then
      gitops_remove_github_runner
    fi
    kubectl delete -f "${GITOPS_DIR}/runner/manifests.yaml" --wait=true --timeout=120s >/dev/null 2>&1 || true
    kubectl delete namespace actions-runner --wait=true --timeout=120s >/dev/null 2>&1 || true
    kubectl delete clusterrole xwidget-demo-runner --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete clusterrolebinding xwidget-demo-runner --ignore-not-found >/dev/null 2>&1 || true
  fi
  if [[ "${INSTALLED_ENGINE:-0}" -eq 1 || "${GITOPS_ENGINE:-}" == flux ]]; then
    kubectl delete kustomization "${GITOPS_APP_NAME}" "${GITOPS_APP_NAME}-conversion" "${GITOPS_APP_NAME}-apps" -n flux-system --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete gitrepository "${GITOPS_APP_NAME}" -n flux-system --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete namespace flux-system --wait=true --timeout=180s >/dev/null 2>&1 || true
  fi
  if [[ "${INSTALLED_ENGINE:-0}" -eq 1 || "${GITOPS_ENGINE:-}" == argo ]]; then
    kubectl delete application "${GITOPS_APP_NAME}" "${GITOPS_APP_NAME}-conversion" -n argocd --ignore-not-found >/dev/null 2>&1 || true
    helm uninstall argocd -n argocd >/dev/null 2>&1 || true
    kubectl delete namespace argocd --wait=true --timeout=180s >/dev/null 2>&1 || true
  fi
}

gitops_cleanup_repo() {
  gitops_state_load
  if [[ "${DELETE_REPO:-0}" -eq 1 ]]; then
    if [[ -n "${GITHUB_REPO_FLAG:-}" ]]; then
      echo "note: --delete-repo ignored (never deleting a --github-repo you passed in)." >&2
    else
      # --create-repo on this command names the demo-owned repo, even when
      # .demo-state is missing (bootstrap died after gh repo create).
      if [[ "${CREATE_REPO:-0}" -eq 1 ]]; then
        gitops_resolve_repo
        CREATED_REPO=1
      fi
      if [[ "${CREATED_REPO:-0}" -ne 1 ]]; then
        echo "note: --delete-repo ignored (no demo-created repo recorded). Re-run with --create-repo --cleanup --delete-repo." >&2
      elif [[ -z "${GITHUB_REPO:-}" ]]; then
        echo "note: --delete-repo set but no GITHUB_REPO in ${GITOPS_STATE_FILE}." >&2
      else
        echo "Deleting GitHub repo ${GITHUB_REPO} (created by this demo)…"
        gh repo delete "${GITHUB_REPO}" --yes
      fi
    fi
  fi
  rm -rf "${GITOPS_WORKTREE}"
  # Keep .demo-state when we created a repo but did not delete it, so a later
  # --cleanup --delete-repo can still see CREATED_REPO=1.
  if [[ "${DELETE_REPO:-0}" -eq 1 || "${CREATED_REPO:-0}" -ne 1 ]]; then
    rm -f "${GITOPS_STATE_FILE}"
  fi
}
