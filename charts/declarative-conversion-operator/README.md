# declarative-conversion-operator

Declarative conversion webhooks for Crossplane XRDs and, since `CRDConversionConfig`, plain native Kubernetes CustomResourceDefinitions too.

## Prerequisites

- Kubernetes 1.27+
- [cert-manager](https://cert-manager.io/docs/installation/) installed (both this operator's own admission webhook and every `ConversionWebhookServer` instance's conversion webhook need it for TLS)
- [Crossplane](https://docs.crossplane.io/) installed, with at least one `CompositeResourceDefinition` you want to manage multi-version conversion for — **unless** you set `features.crossplane.enabled: false` (see below), in which case Crossplane isn't required at all and only `CRDConversionConfig` (native CRDs) is available

## Install

```console
helm install declarative-conversion-operator charts/declarative-conversion-operator --namespace declarative-conversion-system --create-namespace
```

By default this creates one `ConversionWebhookServer` instance named `default`, marked as the fallback target for any `XRDConversionConfig`/`CRDConversionConfig` that doesn't set `spec.webhookServerRef`.

### Feature toggles

Both `XRDConversionConfig` (Crossplane XRDs) and `CRDConversionConfig` (native CRDs) support are enabled by default. Disable whichever you don't need:

```yaml
features:
  crossplane:
    enabled: false  # set false if Crossplane isn't installed on this cluster
  nativeCRD:
    enabled: false  # set false to disable native-CRD conversion support
```

Both CRDs are always installed regardless of these toggles (an unused CRD sitting inert is harmless); the toggles instead control which controllers/watches the manager and every `ConversionWebhookServer` replica actually set up. **Important:** if Crossplane isn't installed, `features.crossplane.enabled` must be set to `false` — the manager watches Crossplane's `CompositeResourceDefinition` type, and establishing that watch fails fatally at startup if the type doesn't exist on the cluster.

## Upgrading CRDs

CRDs live in `crds/` and are only applied at install time (Helm's standard convention — safest against accidental schema-change data loss, at the cost of not auto-upgrading). When upgrading to a chart version with CRD schema changes, diff/apply them first:

```console
make helm-upgrade-crds          # kubectl diff; no write
make helm-upgrade-crds APPLY=1  # apply if they differ
helm upgrade declarative-conversion-operator charts/declarative-conversion-operator --namespace declarative-conversion-system
```

`./hack/upgrade-crds.sh --chart oci://ghcr.io/terasky-oss/charts/declarative-conversion-operator --version <new-version> --apply` is the same helper against a published chart.

## Key values

| Key | Description | Default |
|---|---|---|
| `commonLabels` | Labels merged onto every chart-templated resource | `{}` |
| `manager.priorityClassName` / `podLabels` / `extraEnv` / `extraVolumes` | Scheduling, labeling, and escape hatches on the manager Deployment | unset / `{}` / `[]` |
| `conversionWebhookServer.extraArgs` / `cacheSelector` / `extraEnv` | Passed through to the default CWS CR (the operator builds the Deployment) | `[]` / `{}` / `[]` |
| `certManager.issuerRef` | Issuer/ClusterIssuer for `ConversionWebhookServer` certificates | bootstrap self-signed `ClusterIssuer` |
| `admissionWebhook.certificate.issuerRef` | Issuer/ClusterIssuer for this operator's own admission-webhook certificate (a separate trust surface) | bootstrap self-signed `ClusterIssuer` |
| `conversionWebhookServer.autoscaling.enabled` | Use an HPA instead of a fixed replica count for the default instance | `false` |
| `metrics.serviceMonitor.enabled` | Create Prometheus Operator `ServiceMonitor`s (opt-in; not auto-detected) | `false` |
| `metrics.prometheusRule.enabled` | Create a `PrometheusRule` with built-in conversion/manager alerts | `false` |
| `dashboards.enabled` | Create Grafana sidecar dashboard ConfigMap(s) (`grafana_dashboard: "1"`) | `false` |
| `features.crossplane.enabled` | Enable `XRDConversionConfig` support for Crossplane XRDs. Requires Crossplane to be installed. | `true` |
| `features.nativeCRD.enabled` | Enable `CRDConversionConfig` support for plain native CustomResourceDefinitions. | `true` |

See `values.yaml` for the full set.

## Multiple ConversionWebhookServer instances

Create additional instances directly as `ConversionWebhookServer` custom resources (not via this chart) for scale-out or tenancy — each gets its own Deployment/Service/Certificate/HPA/PDB, reconciled by the operator. Pod-level settings such as `spec.extraArgs` are configured on the CR (the chart does not template a Deployment for webhook-server pods):

```yaml
apiVersion: terasky.com/v1alpha1
kind: ConversionWebhookServer
metadata:
  name: tenant-a
spec:
  certificate:
    issuerRef: {name: my-ca-issuer, kind: ClusterIssuer}
  extraArgs:
    - --cert-reload-interval=1m
```

Then reference it from an `XRDConversionConfig` via `spec.webhookServerRef.name: tenant-a`.
