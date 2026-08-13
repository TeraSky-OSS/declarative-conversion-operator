# HPA on conversion QPS

CPU-based HPA (chart `conversionWebhookServer.autoscaling` /
`ConversionWebhookServer.spec.autoscaling`) is the supported default.
Conversion cost is often dominated by schema-tree walking rather than raw
CPU, so scaling on **ConversionReview QPS** can track load more closely.

This page is a copy-paste example using
[prometheus-adapter](https://github.com/kubernetes-sigs/prometheus-adapter)
`Pods` metrics. The operator does **not** template this HPA; apply it
yourself (or extend your GitOps) against the Deployment the controller
owns for a given ConversionWebhookServer.

## Prerequisites

1. Prometheus scrapes webhook-server `/metrics` (chart
   `metrics.serviceMonitor.enabled=true`, or equivalent).
2. prometheus-adapter is installed and configured to discover custom
   metrics from Prometheus.
3. You accept that this HPA may fight the operator-managed CPU HPA — use
   **either** chart/CR autoscaling **or** this custom-metric HPA, not both
   on the same Deployment.

## prometheus-adapter rules snippet

Map the request counter to a per-pod QPS custom metric (adjust the series
relabel/`namespace` matchers to your scrape labels):

```yaml
rules:
  - seriesQuery: 'dco_webhook_conversion_review_requests_total{namespace!="",pod!=""}'
    resources:
      overrides:
        namespace: { resource: "namespace" }
        pod: { resource: "pod" }
    name:
      matches: "^dco_webhook_conversion_review_requests_total$"
      as: "dco_webhook_conversion_qps"
    metricsQuery: 'sum by (<<.GroupBy>>) (rate(<<.Series>>{<<.LabelMatchers>>}[1m]))'
```

After reload, confirm discovery:

```console
kubectl get --raw "/apis/custom.metrics.k8s.io/v1beta1/namespaces/<ns>/pods/*/dco_webhook_conversion_qps" | jq .
```

## Example HorizontalPodAutoscaler

Replace `<namespace>` and the Deployment name (default chart install:
`default-webhook-server` in the release namespace):

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: default-webhook-server-qps
  namespace: <namespace>
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: default-webhook-server
  minReplicas: 2
  maxReplicas: 10
  metrics:
    - type: Pods
      pods:
        metric:
          name: dco_webhook_conversion_qps
        target:
          type: AverageValue
          averageValue: "20"   # scale out when average pod QPS exceeds 20
```

Tune `averageValue` from observed
`rate(dco_webhook_conversion_review_requests_total[5m])` under normal
load. Latency-based scaling is possible the same way by exposing a
prometheus-adapter rule on
`dco_webhook_conversion_review_duration_seconds` (histogram →
summary/quantile in the adapter `metricsQuery`).

## Related

- Metric catalog: [../observability.md](../observability.md)
- ConversionWebhookServer autoscaling fields: [../configuration/conversionwebhookserver.md](../configuration/conversionwebhookserver.md)
