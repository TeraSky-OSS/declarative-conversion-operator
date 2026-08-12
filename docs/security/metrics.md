# Metrics trust boundary

## Default exposure

Both the **manager** and each **ConversionWebhookServer** replica expose an
HTTP metrics port in-cluster:

| Component | Port | Service |
|---|---|---|
| Manager | `8080` | `*-manager-metrics` |
| Webhook-server | `8443` | `*-webhook-server` (`metrics` port) |

With no NetworkPolicy (the chart default), any pod that can reach the
component's namespace can scrape these endpoints. The manager uses the
Helm release namespace; a ConversionWebhookServer may use a different
namespace via `conversionWebhookServer.namespace` /
`spec.namespace`. That is fine for many clusters, but it is **not** a
documented trust boundary — it only happens to be true when your
CNI/default-deny posture already blocks cross-pod traffic.

## Locking metrics down (NetworkPolicy)

Set `networkPolicy.enabled=true` to deny ingress to manager and default
CWS pods except:

1. **Webhook ports** (`9443`) — left without a `from` selector so the
   apiserver can always reach conversion/admission webhooks.
2. **Probe port** on the manager (`8081`) — same rationale for kubelet.
3. **Manager metrics** (`8080`) — optionally restricted via
   `networkPolicy.metrics.allowedPeers` (a list of
   [`NetworkPolicyPeer`](https://kubernetes.io/docs/reference/kubernetes-api/policy-resources/network-policy-v1/#NetworkPolicyPeer)
   objects).
4. **Webhook-server plain-HTTP port** (`8443`) — always admitted without a
   `from` selector. Probes (`/healthz`, `/readyz`) and metrics share this
   port, so peer-restricting it would break kubelet health checks on CNIs
   that enforce NetworkPolicy for node→pod traffic.

Example — only Prometheus in the `monitoring` namespace may scrape **manager**
metrics:

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

With `allowedPeers: []` (the default when NetworkPolicy is on), manager
metrics remain reachable from any in-cluster source while non-metrics ports
stay locked to the webhook/probe rules above. CWS `8443` stays open either
way for the probe reason above.

## Future hardening

Serving webhook-server probes on a dedicated port (mirroring the manager's
`8081` / `8080` split) would let NetworkPolicy peer-restrict CWS metrics.
Authenticating scrapers with `kube-rbac-proxy` or controller-runtime's
secure-metrics serving is another useful next step for clusters that cannot
rely on NetworkPolicy alone. File an issue if your environment requires it.
