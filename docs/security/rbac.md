# RBAC blast radius

This operator installs two **namespace-scoped** ServiceAccounts bound to
`ClusterRole`s (via `ClusterRoleBinding`s). Those bindings grant
cluster-wide permissions because the CRDs they manage —
`XRDConversionConfig`, `CRDConversionConfig`, `ConversionWebhookServer`,
plus target XRDs/CRDs — are cluster-scoped.

Source of truth for the chart install path:
`charts/declarative-conversion-operator/templates/rbac/clusterrole.yaml`.
The kustomize twin lives under `config/rbac/`.

## Manager ServiceAccount

Used by the operator manager Deployment. It **mutates** target XRDs/CRDs
to wire (and unwind) conversion webhook configuration — that is the
highest-privilege action this operator takes.

Unless noted, list/watch grants below apply to **all** objects of that
resource type in scope (no name/namespace restriction in the ClusterRole).

| API group | Resource | Verbs | Why |
|---|---|---|---|
| `""` | `events` | `create`, `patch` | Emit reconcile events. |
| `""` | `secrets` | `get` | Read the cert-manager-issued TLS Secret so the controller can refresh XRD/CRD `caBundle`s on rotation. **`get` only, and cluster-wide by necessity:** the namespace of a `ConversionWebhookServer`'s Secret comes from `spec.namespace` at runtime, so the ClusterRole cannot be narrowed — but the manager reads one key of one Secret per apply through an *uncached* client, never lists or watches them, and so holds no Secret contents in memory. `list`/`watch` were dropped once the informer was removed; granting them back would re-enable a cluster-wide Secret informer. See [Capacity planning](../operations/capacity.md#memory-the-manager). |
| `""` | `services` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Own the Service in front of each ConversionWebhookServer. |
| `apiextensions.crossplane.io` | `compositeresourcedefinitions` | `get`, `list`, `patch`, `watch` | Read XRD schemas for validation; **patch** `spec.conversion` to attach/detach the conversion webhook. **Only granted when `features.crossplane.enabled`** — with Crossplane support off the manager does not watch or write the type, and the chart omits the rule rather than leaving dormant privilege. |
| `apiextensions.k8s.io` | `customresourcedefinitions` | `get`, `list`, `watch` (+ `patch`) | Read is granted whenever **either** feature is enabled — an XRD's `ConversionPropagated` condition is computed by reading the CRD Crossplane *generated* from it, which is not native-CRD support. **`patch` is only granted when `features.nativeCRD.enabled`**, since only a native CRD's own `spec.conversion` is written directly. With both features off, no access at all. |
| `apps` | `deployments` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Own each ConversionWebhookServer's Deployment. |
| `autoscaling` | `horizontalpodautoscalers` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Own optional HPAs for ConversionWebhookServer instances. |
| `cert-manager.io` | `certificates` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Own the Certificate for each ConversionWebhookServer. |
| `policy` | `poddisruptionbudgets` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Own optional PDBs for ConversionWebhookServer instances. |
| `coordination.k8s.io` | `leases` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` | Leader election for the manager. |
| `admissionregistration.k8s.io` | `validatingwebhookconfigurations`, `mutatingwebhookconfigurations` | `get`, `list`, `watch` | Intended use: observe this operator's own admission webhook configurations (the validators, and the [XRD conversion guard](#the-xrd-conversion-guard-is-a-mutating-webhook-on-somebody-elses-type)). Read-only — the chart creates these objects, the manager never writes them. **Granted scope:** all such configurations cluster-wide. |
| `terasky.com` | `conversionwebhookservers`, `crdconversionconfigs`, `xrdconversionconfigs` | `create`, `delete`, `get`, `list`, `patch`, `update`, `watch` | Reconcile this operator's CRDs. |
| `terasky.com` | `*/finalizers` | `update` | Safe-delete / safe-revert finalizers. |
| `terasky.com` | `*/status` | `get`, `patch`, `update` | Write status/conditions. |

### The XRD conversion guard is a mutating webhook on somebody else's type

With `features.crossplane.conversionGuard.enabled` (default), the chart
creates a `MutatingWebhookConfiguration` that intercepts **every** CREATE and
UPDATE to `compositeresourcedefinitions` cluster-wide. That is a wide match
by design — see [Architecture](../architecture.md#the-xrd-conversion-guard)
for why an `objectSelector` cannot be used here — so it is worth being
precise about what it can and cannot do:

- **It cannot deny a write.** `failurePolicy: Ignore` is hard-coded, and the
  handler returns `Allowed` on every path including its own errors. An
  operator outage, a decode failure, or an index error all fail open.
- **It can only add `spec.conversion` and two annotations.** Nothing else in
  the incoming object is read, compared, or modified.
- **It only acts on an XRD that an already-`Applied` `XRDConversionConfig`
  targets.** Every other XRD on the cluster is returned unmodified after an
  in-memory index lookup, with no API call.
- **It will not overwrite a third-party `spec.conversion`.** An XRD wired to
  a hand-written conversion webhook is left alone.
- **It adds no RBAC.** The handler reads only `XRDConversionConfig`s, which
  the manager already watches, through the cache.

The blast radius it adds over the manager's existing `patch` on XRDs is
therefore latency (a 5-second timeout on a cluster-local in-memory lookup)
rather than authority. Turn it off with
`features.crossplane.conversionGuard.enabled=false`; the only thing that
changes is that a package-managed XRD goes back to losing its conversion
stanza on every `ConfigurationRevision` reconcile.

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

- [SECURITY.md](https://github.com/TeraSky-OSS/declarative-conversion-operator/blob/main/SECURITY.md) — reporting process and high-level model
- [Pod security posture](pod-security.md)
- [Metrics trust boundary](metrics.md)
