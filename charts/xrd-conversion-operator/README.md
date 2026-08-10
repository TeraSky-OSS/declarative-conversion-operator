# xrd-conversion-operator

Declarative conversion webhooks for Crossplane XRDs.

## Prerequisites

- Kubernetes 1.27+
- [cert-manager](https://cert-manager.io/docs/installation/) installed (both this operator's own admission webhook and every `ConversionWebhookServer` instance's conversion webhook need it for TLS)
- [Crossplane](https://docs.crossplane.io/) installed, with at least one `CompositeResourceDefinition` you want to manage multi-version conversion for

## Install

```console
helm install xrd-conversion-operator charts/xrd-conversion-operator --namespace xrd-conversion-system --create-namespace
```

By default this creates one `ConversionWebhookServer` instance named `default`, marked as the fallback target for any `XRDConversionConfig` that doesn't set `spec.webhookServerRef`.

## Upgrading CRDs

CRDs live in `crds/` and are only applied at install time (Helm's standard convention — safest against accidental schema-change data loss, at the cost of not auto-upgrading). When upgrading to a chart version with CRD schema changes, apply them manually first:

```console
helm show crds charts/xrd-conversion-operator | kubectl apply -f -
helm upgrade xrd-conversion-operator charts/xrd-conversion-operator --namespace xrd-conversion-system
```

## Key values

| Key | Description | Default |
|---|---|---|
| `certManager.issuerRef` | Issuer/ClusterIssuer for `ConversionWebhookServer` certificates | bootstrap self-signed `ClusterIssuer` |
| `admissionWebhook.certificate.issuerRef` | Issuer/ClusterIssuer for this operator's own admission-webhook certificate (a separate trust surface) | bootstrap self-signed `ClusterIssuer` |
| `conversionWebhookServer.autoscaling.enabled` | Use an HPA instead of a fixed replica count for the default instance | `false` |
| `metrics.serviceMonitor.enabled` | Create Prometheus Operator `ServiceMonitor`s (opt-in; not auto-detected) | `false` |

See `values.yaml` for the full set.

## Multiple ConversionWebhookServer instances

Create additional instances directly as `ConversionWebhookServer` custom resources (not via this chart) for scale-out or tenancy — each gets its own Deployment/Service/Certificate/HPA/PDB, reconciled by the operator:

```yaml
apiVersion: terasky.com/v1alpha1
kind: ConversionWebhookServer
metadata:
  name: tenant-a
spec:
  certificate:
    issuerRef: {name: my-ca-issuer, kind: ClusterIssuer}
```

Then reference it from an `XRDConversionConfig` via `spec.webhookServerRef.name: tenant-a`.
