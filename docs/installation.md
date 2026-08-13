# Installation

## Prerequisites

| Requirement | Why |
|---|---|
| Kubernetes 1.27+ | Baseline for the API machinery this operator depends on. |
| [cert-manager](https://cert-manager.io/docs/installation/) | Issues TLS certificates for **both** webhook surfaces: this operator's own admission webhook, and every `ConversionWebhookServer` instance's conversion webhook. |
| [Crossplane](https://docs.crossplane.io/latest/software/install/) (current major, `apiextensions.crossplane.io/v2`) — **only if you want `XRDConversionConfig` support** | Set `features.crossplane.enabled: false` (see [Feature toggles](#feature-toggles)) on clusters without Crossplane installed; native-CRD support via `CRDConversionConfig` works with no Crossplane dependency at all. |

## Install

The chart is published as an OCI artifact alongside every release:

```console
helm install declarative-conversion-operator \
  oci://ghcr.io/terasky-oss/charts/declarative-conversion-operator \
  --namespace declarative-conversion-system --create-namespace
```

Or, from a checkout of the repository:

```console
helm install declarative-conversion-operator charts/declarative-conversion-operator \
  --namespace declarative-conversion-system --create-namespace
```

By default this creates:

- The operator's `Deployment`, RBAC, and own admission webhook (validating `XRDConversionConfig`/`CRDConversionConfig`/`ConversionWebhookServer` objects at `kubectl apply` time).
- A bootstrap self-signed `ClusterIssuer`, if you didn't supply your own `issuerRef` (see [below](#bring-your-own-certificate-issuer)) — so `helm install` works zero-config as long as cert-manager itself is present.
- One `ConversionWebhookServer` named `default`, marked `spec.default: true` — the fallback target for any config that doesn't set `spec.webhookServerRef`. See [ConversionWebhookServer](configuration/conversionwebhookserver.md).
- Both `XRDConversionConfig` and `CRDConversionConfig` support active (see [Feature toggles](#feature-toggles) to disable either).

Confirm everything came up:

```console
kubectl -n declarative-conversion-system get pods
kubectl wait --for=condition=Available conversionwebhookserver/default --timeout=120s
```

## Feature toggles

Both `XRDConversionConfig` (Crossplane XRD) support and `CRDConversionConfig` (native CRD) support are on by default:

```yaml title="values.yaml"
features:
  crossplane:
    enabled: true   # requires Crossplane installed
  nativeCRD:
    enabled: true
```

**If Crossplane isn't installed on this cluster, set `features.crossplane.enabled: false`.** The manager watches Crossplane's `CompositeResourceDefinition` type as part of `XRDConversionConfig` support; establishing that watch fails fatally at startup if the type doesn't exist. `features.nativeCRD.enabled` carries no equivalent risk — `CustomResourceDefinition` is a core Kubernetes type that's always present — so disabling it is purely a matter of not wanting the feature active.

Both CRDs (`XRDConversionConfig`, `CRDConversionConfig`) are always installed regardless of these toggles — Helm's `crds/` directory doesn't support conditionals, and an unused CRD with no active controller behind it is harmless. The toggles instead control which controllers and watches the manager, and every `ConversionWebhookServer` replica, actually set up. If a toggle is off, the admission webhook for that config kind still exists but rejects creates with a clear error, rather than silently accepting an object that will never be reconciled.

## Key values

| Key | Description | Default |
|---|---|---|
| `image.manager.repository` / `image.webhookServer.repository` | Image repositories for the two binaries. | `terasky-oss/declarative-conversion-operator` / `terasky-oss/declarative-conversion-webhook-server` |
| `manager.replicaCount` | Operator replica count (leader election keeps exactly one active). | `1` |
| `manager.leaderElection.enabled` | Disable only for single-replica local/dev setups. | `true` |
| `certManager.issuerRef` | Issuer/`ClusterIssuer` used for **both** webhook surfaces unless overridden per-surface. | bootstrap self-signed `ClusterIssuer` |
| `admissionWebhook.certificate.issuerRef` | Issuer override for just the operator's own admission-webhook certificate. | inherits `certManager.issuerRef` |
| `conversionWebhookServer.replicaCount` | Replica count for the default `ConversionWebhookServer` instance. | `2` |
| `conversionWebhookServer.autoscaling.enabled` | Use an HPA instead of a fixed replica count for the default instance. | `false` |
| `conversionWebhookServer.certificate.issuerRef` | Issuer override for the default instance's conversion-webhook certificate. | inherits `certManager.issuerRef` |
| `metrics.serviceMonitor.enabled` | Create a Prometheus Operator `ServiceMonitor`. Opt-in by value, not capability-detected, so chart behavior doesn't change based on how it's rendered. | `false` |
| `metrics.prometheusRule.enabled` | Create a `PrometheusRule` with built-in alerts. | `false` |
| `dashboards.enabled` | Create Grafana sidecar ConfigMap(s) labeled `grafana_dashboard: "1"`. | `false` |
| `features.crossplane.enabled` | Enable `XRDConversionConfig` support. Requires Crossplane installed. | `true` |
| `features.nativeCRD.enabled` | Enable `CRDConversionConfig` support. | `true` |
| `crds.install` | Install the two CRDs from `crds/`. Disable if you manage CRDs separately (e.g. a dedicated CRD-management pipeline). | `true` |

See [`values.yaml`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/charts/declarative-conversion-operator/values.yaml) for the complete set, including resource requests/limits, `nodeSelector`/`tolerations`/`affinity` for both the manager and the default webhook server, and `PodDisruptionBudget` settings.

### Bring your own certificate issuer

For anything beyond a quick try-out, point both webhook surfaces at a real CA-backed `Issuer`/`ClusterIssuer` instead of the bootstrap self-signed one:

```yaml title="values.yaml"
certManager:
  issuerRef:
    name: my-ca-issuer
    kind: ClusterIssuer
  selfSigned:
    enabled: false
```

## Upgrading

CRDs live in the chart's `crds/` directory and follow [Helm's standard convention](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/): they're applied once at install time and **never** touched by `helm upgrade` or `helm uninstall` — the safest default against accidental schema-change data loss. If a chart upgrade changes the CRD schema, apply the new CRDs manually first:

```console
helm show crds oci://ghcr.io/terasky-oss/charts/declarative-conversion-operator --version <new-version> | kubectl apply -f -
helm upgrade declarative-conversion-operator \
  oci://ghcr.io/terasky-oss/charts/declarative-conversion-operator --version <new-version> \
  --namespace declarative-conversion-system
```

## Uninstalling

```console
helm uninstall declarative-conversion-operator --namespace declarative-conversion-system
```

Deleting the release does **not** remove the CRDs (consistent with the install behavior above), and the operator's own finalizers will block deletion of any `XRDConversionConfig` or `ConversionWebhookServer` that's still protecting a live XRD — see [Deletion safety](configuration/xrdconversionconfig.md#deletion-safety) before force-removing anything.

## Verifying the install offline first

Before installing against a real cluster, `helm template` and `helm lint` both work with no cluster access at all:

```console
helm lint charts/declarative-conversion-operator
helm template declarative-conversion-operator charts/declarative-conversion-operator \
  --namespace declarative-conversion-system
```
