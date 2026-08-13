# HA checklist

The two components have very different availability requirements, and conflating
them leads to over-provisioning the wrong one.

| Component | On the request path? | What its outage breaks |
|---|---|---|
| **Webhook-server pods** (a `ConversionWebhookServer`) | **Yes** — the apiserver calls them on every read/write of an object at a non-storage version. | Reads and writes of converted resources fail. This is the one to make highly available. |
| **Manager** | No | Reconciling configs, and applying new `XRDConversionConfig`/`ConversionWebhookServer` objects (its admission webhook is `failurePolicy: Fail`). Conversions already applied keep working. |

## Webhook server

- [ ] **At least 2 replicas.** `conversionWebhookServer.replicaCount` defaults to
      `2`. Every replica is symmetric and self-sufficient — its own informers, its
      own in-memory registry, no leader election and no shared state — so replicas
      add availability with no coordination cost.
- [ ] **A PodDisruptionBudget.** `conversionWebhookServer.podDisruptionBudget` is
      enabled by default with `minAvailable: 1`. On a cluster where node drains
      are routine, prefer `minAvailable: 2` with 3 replicas: `minAvailable: 1`
      permits draining down to a single replica, which then has no headroom.
- [ ] **Spread across nodes.** Use
      `conversionWebhookServer.affinity` for anti-affinity across nodes (or zones)
      so a single node loss can't take every replica.
- [ ] **Autoscale with a floor of 2.** `conversionWebhookServer.autoscaling`
      (CPU-based) is off by default; when you enable it, keep `minReplicas: 2` or
      higher. `replicas` and `autoscaling` are mutually exclusive — once
      autoscaling is set, the HPA owns the count. For scaling on conversion QPS
      instead of CPU, see [HPA on conversion QPS](hpa-custom-metrics.md).
- [ ] **Certificates renew well ahead of expiry.**
      `conversionWebhookServer.certificate.duration` / `renewBefore` default to
      `2160h` / `360h` (90 and 15 days). The operator refreshes the target's
      `caBundle` when the Secret rotates, so rotation is not a manual step — but
      cert-manager itself needs to be healthy for it to happen.
- [ ] **Verify readiness per pod, not per CWS.**
      `ConversionWebhookServer.status.assignedConfigs` is the *desired* assignment
      computed by the resolver, not confirmation that each replica compiled it.
      After any scale-out, restart, or node replacement:

      ```promql
      (dco_webhook_ready == 1)
        unless on (pod)
      (dco_webhook_registry_entry_loaded{target="xwidgets.example.org"} == 1)
      ```

      An empty result means every ready replica can serve that target.

## Manager

- [ ] **1 replica is a legitimate choice.** `manager.replicaCount` defaults to
      `1` with leader election on. A manager restart delays reconciles; it does
      not interrupt conversions.
- [ ] **2+ replicas only buy faster failover.** Leader election keeps exactly one
      active, so extra replicas reduce the gap after a node loss rather than
      adding throughput. If you run more than one, enable
      `manager.podDisruptionBudget` (off by default, `minAvailable: 1`) — a PDB
      with a single replica just blocks drains.
- [ ] **Keep leader election on** for anything but a single-node dev cluster.
      Two active managers would both try to patch the same targets.
- [ ] **Remember the admission webhook.** With `admissionWebhook.failurePolicy:
      Fail` (the default), no manager means `kubectl apply` of this operator's own
      CRs is rejected. That is the intended trade — accepting configs nobody can
      validate is worse — but it's the practical reason to give the manager a
      second replica on a cluster where configs are applied by an unattended
      GitOps loop.

## Multiple webhook-server instances

Additional `ConversionWebhookServer` instances buy blast-radius isolation, not
just throughput: a compile failure for one config never affects another config on
the same pod, but a bad *rollout* or a resource-exhausted node does. Splitting
tenants or high-traffic targets onto their own instance keeps them independent —
see [Multiple instances](../configuration/conversionwebhookserver.md#multiple-instances).

Each instance needs its own certificate and its own replica count and PDB; none
of it is shared.

## Alerting

Enable the chart's `PrometheusRule` (`metrics.prometheusRule.enabled=true`) — the
alerts most relevant to availability are `ConversionWebhookNotReady`,
`ConversionWebhookReplicaNotReady`, `ConversionWebhookRegistryCompileErrors`, and
`ConversionWebhookErrorRatio`. Full list and expressions:
[Observability](../observability.md#alerts-and-dashboards).

## Known gaps

- **No cross-cluster coordination.** Every instance and every replica assumes a
  single cluster; there is no failover between clusters, by design. See
  [Limitations](../limitations.md).
- **Scheduling knobs are partial.** `nodeSelector`, `tolerations`, `affinity`,
  and `priorityClassName` are available; `topologySpreadConstraints` is not yet
  wired through the chart — tracked in
  [issue #53](https://github.com/terasky-oss/declarative-conversion-operator/issues/53).
- **Per-pod registry state isn't in `status`.** The PromQL check above exists
  because that's deliberately not a status field — see
  [Limitations](../limitations.md).

## Related

- [Capacity planning](capacity.md) — how many replicas of what size.
- [Upgrade runbook](upgrade-runbook.md) — staying available through a rollout.
- [ConversionWebhookServer reference](../configuration/conversionwebhookserver.md) — every field named above.
