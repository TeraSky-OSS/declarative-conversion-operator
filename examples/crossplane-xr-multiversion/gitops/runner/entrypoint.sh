#!/usr/bin/env bash
# GitHub Actions runner entrypoint for the kind demo cluster.
# Writes an in-cluster kubeconfig so `convctl test --live` uses the pod SA,
# then registers with GitHub and polls for jobs.
set -euo pipefail

export PATH="/opt/convctl:${PATH}"

mkdir -p "${HOME}/.kube"
cat > "${HOME}/.kube/config" <<EOF
apiVersion: v1
kind: Config
clusters:
  - name: in-cluster
    cluster:
      certificate-authority: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
      server: https://kubernetes.default.svc
contexts:
  - name: in-cluster
    context:
      cluster: in-cluster
      user: sa
current-context: in-cluster
users:
  - name: sa
    user:
      tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
EOF
chmod 0600 "${HOME}/.kube/config"

: "${GH_REPO:?GH_REPO is required (owner/name)}"
: "${RUNNER_TOKEN:?RUNNER_TOKEN is required}"

cd /home/runner
./config.sh \
  --url "https://github.com/${GH_REPO}" \
  --token "${RUNNER_TOKEN}" \
  --name "${RUNNER_NAME:-kind-xwidget-demo}" \
  --labels "${RUNNER_LABELS:-xwidget-demo}" \
  --work _work \
  --unattended \
  --replace

exec ./run.sh
