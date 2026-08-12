# RBAC blast radius

This operator installs two cluster-scoped ServiceAccounts. Both are
cluster-scoped in effect (their Roles are `ClusterRole`s) because the
CRDs they manage — `XRDConversionConfig`, `CRDConversionConfig`,
`ConversionWebhookServer`, plus target XRDs/CRDs — are cluster-scoped.

Source of truth for the chart install path:
`charts/declarative-conversion-operator/templates/rbac/clusterrole.yaml`.
The kustomize twin lives under `config/rbac/`.

## Manager ServiceAccount

Used by the operator manager Deployment. It **mutates** target XRDs/CRDs
to wire (and unwind) conversion webhook configuration — that is the
highest-privilege action this operator takes.

| API group | Resource | Verbs | Why |
|---|---|---|---|
| `""` | `events` | `create`, `patch` | Emit reconcile events. |
| `""` | `secrets` | `get`, `list`, `watch` | Read cert-manager-issued TLS Secrets so the controller can refresh XRD/CRD `caBundle`s on rotation. |
| `""` | `services` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Own the Service in front of each ConversionWebhookServer. |
| `apiextensions.crossplane.io` | `compositeresourcedefinitions` | `get`, `list`, `patch`, `watch` | Read XRD schemas for validation; **patch** `spec.conversion` to attach/detach the conversion webhook. |
| `apiextensions.k8s.io` | `customresourcedefinitions` | `get`, `list`, `patch`, `watch` | Same for native CRDs when native-CRD support is enabled. |
| `apps` | `deployments` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Own each ConversionWebhookServer's Deployment. |
| `autoscaling` | `horizontalpodautoscalers` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Own optional HPAs for ConversionWebhookServer instances. |
| `cert-manager.io` | `certificates` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Own the Certificate for each ConversionWebhookServer. |
| `policy` | `poddisruptionbudgets` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Own optional PDBs for ConversionWebhookServer instances. |
| `coordination.k8s.io` | `leases` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` | Leader election for the manager. |
| `admissionregistration.k8s.io` | `validatingwebhookconfigurations` | `get`, `list`, `watch` | Observe this operator's own admission webhook configuration. |
| `terasky.com` | `conversionwebhookservers`, `crdconversionconfigs`, `xrdconversionconfigs` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Reconcile this operator's CRDs. |
| `terasky.com` | `*/finalizers` | `update` | Safe-delete / safe-revert finalizers. |
| `terasky.com` | `*/status` | `get`, `patch`, `update` | Write status/conditions. |

### Why XRD/CRD `patch` is required

Kubernetes has no narrower verb for "set `spec.conversion` only." Patching
an XRD/CRD is therefore the blast-radius hot spot: a compromised manager
could in principle alter other fields on those resources. Mitigations in
practice:

- The controller only SSA-patches the conversion webhook fields it owns.
- Configs are admission-validated before any patch.
- Deletion is finalizer-gated when more than one version is still served.

## Webhook-server ServiceAccount

Used by every `ConversionWebhookServer` pod. **Read/watch only** — the
webhook-server binary never mutates cluster state. Each replica runs its
own informers so it can compile conversion plans without depending on the
manager at request time.

| API group | Resource | Verbs | Why |
|---|---|---|---|
| `terasky.com` | `xrdconversionconfigs`, `crdconversionconfigs`, `conversionwebhookservers` | `get`, `list`, `watch` | Discover assigned configs and the owning server. |
| `apiextensions.crossplane.io` | `compositeresourcedefinitions` | `get`, `list`, `watch` | Read live XRD schemas to (re)compile plans. |
| `apiextensions.k8s.io` | `customresourcedefinitions` | `get`, `list`, `watch` | Same for native CRDs. |

No access to Secrets, no write verbs, no ability to patch XRDs/CRDs.

## Related docs

- [SECURITY.md](../../SECURITY.md) — reporting process and high-level model
- [Pod security posture](pod-security.md)
- [Metrics trust boundary](metrics.md)
