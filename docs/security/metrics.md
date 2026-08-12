# Metrics trust boundary

## Default exposure

Both the **manager** and each **ConversionWebhookServer** replica expose an
HTTP metrics port in-cluster:

| Component | Port | Service |
|---|---|---|
| Manager | `8080` | `*-manager-metrics` |
| Webhook-server | `8443` | `*-webhook-server` (`metrics` port) |

With no NetworkPolicy (the chart default), any pod that can reach the
install namespace can scrape these endpoints. That is fine for many
clusters, but it is **not** a documented trust boundary — it only happens
to be true when your CNI/default-deny posture already blocks cross-pod
traffic.

## Locking metrics down (NetworkPolicy)

Set `networkPolicy.enabled=true` to deny ingress to manager and default
CWS pods except:

1. **Webhook ports** (`9443`) — left without a `from` selector so the
   apiserver can always reach conversion/admission webhooks.
2. **Probe port** on the manager (`8081`) — same rationale for kubelet.
3. **Metrics ports** — optionally restricted via
   `networkPolicy.metrics.allowedPeers` (a list of
   [`NetworkPolicyPeer`](https://kubernetes.io/docs/reference/kubernetes-api/policy-resources/network-policy-v1/#NetworkPolicyPeer)
   objects).

Example — only Prometheus in the `monitoring` namespace may scrape:

```yaml
networkPolicy:
  enabled: true
  metrics:
    allowedPeers:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: monitoring
        podSelector:
          matchLabels:
            app.kubernetes.io/name: prometheus
```

With `allowedPeers: []` (the default when NetworkPolicy is on), metrics
remain reachable from any in-cluster source while non-metrics ports stay
locked to the webhook/probe rules above.

## Future hardening

Authenticating scrapers with `kube-rbac-proxy` or controller-runtime's
secure-metrics serving is a useful next step for clusters that cannot
rely on NetworkPolicy alone. File an issue if your environment requires it.
