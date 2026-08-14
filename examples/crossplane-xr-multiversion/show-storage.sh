#!/usr/bin/env bash
# Print how XWidgets are stored: CRD storedVersions, per-object managedFields
# apiVersions, and (on kind) each etcd value's root apiVersion.
#
# managedFields[].apiVersion is the GVK a field manager last spoke — it is
# not the etcd encoding. The etcd root apiVersion is.
set -euo pipefail

NS="${DEMO_NAMESPACE:-default}"
CRD="xwidgets.example.org"
GROUP="example.org"
PLURAL="xwidgets"

echo "== CRD storage / storedVersions =="
kubectl get crd "${CRD}" -o jsonpath='storage={.spec.versions[?(@.storage==true)].name}{"\nstoredVersions="}{.status.storedVersions}{"\n"}'

echo
echo "== managedFields apiVersion per XR (historical manager GVK, not etcd encoding) =="
names="$(kubectl get xwidgets.example.org -n "${NS}" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')"
if [[ -z "${names}" ]]; then
  echo "(no XWidgets in namespace ${NS})"
else
  while IFS= read -r name; do
    [[ -z "${name}" ]] && continue
    echo "-- ${NS}/${name} --"
    kubectl get xwidgets.example.org "${name}" -n "${NS}" \
      -o jsonpath='{range .metadata.managedFields[*]}{.manager}{"\t"}{.apiVersion}{"\n"}{end}'
  done <<< "${names}"
fi

echo
echo "== etcd root apiVersion (the stored version) =="
node="$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
etcd_pod="etcd-${node}"
if [[ -z "${node}" ]] || ! kubectl -n kube-system get pod "${etcd_pod}" >/dev/null 2>&1; then
  echo "(no etcd pod ${etcd_pod} — skip; this dump is for kind/kubeadm control-plane nodes)"
  exit 0
fi

etcdctl() {
  kubectl -n kube-system exec "${etcd_pod}" -- etcdctl \
    --cacert=/etc/kubernetes/pki/etcd/ca.crt \
    --cert=/etc/kubernetes/pki/etcd/server.crt \
    --key=/etc/kubernetes/pki/etcd/server.key \
    "$@"
}

if [[ -z "${names}" ]]; then
  exit 0
fi
while IFS= read -r name; do
  [[ -z "${name}" ]] && continue
  key="/registry/${GROUP}/${PLURAL}/${NS}/${name}"
  raw="$(etcdctl get "${key}" --print-value-only 2>/dev/null || true)"
  root="$(printf '%s' "${raw}" | grep -oE '"apiVersion":"'"${GROUP}"'/v[0-9]+"' | head -1 || true)"
  if [[ -z "${root}" ]]; then
    echo "${name}  (missing or unreadable)"
  else
    echo "${name}  ${root}"
  fi
done <<< "${names}"
