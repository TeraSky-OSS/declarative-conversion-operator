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
- [ ] **Spread across nodes.** A soft (`ScheduleAnyway`) spread across
      `kubernetes.io/hostname` is applied by default, so replicas do not all land
      on one node and a single node drain is not a full conversion outage. It is
      soft on purpose: a hard constraint makes a single-node cluster's Deployment
      unschedulable, and an outage caused by the anti-outage setting is the worse
      failure. For zone spreading, or to require rather than prefer it, set
      `conversionWebhookServer.topologySpreadConstraints` (which replaces the
      default entirely) or `conversionWebhookServer.affinity`. Turn the default
      off with `conversionWebhookServer.rollout.defaultTopologySpread: false`.
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
- [ ] **Know your cold-start budget.** A replica compiles every assigned plan
      before it can serve. The health endpoint (`/healthz`, `/readyz`,
      `/metrics`) listens from the start, so the replica is visibly alive
      throughout; `/readyz` stays `503` and the *conversion* endpoint does
      not listen at all until the registry is populated. The `startupProbe`
      (`conversionWebhookServer.startupProbe`, five minutes by default)
      polls `/readyz`, so its `periodSeconds × failureThreshold` is a
      deadline on the sync — and while it is in flight the kubelet runs
      neither of the other two probes, so a slow sync is not also fighting
      the liveness probe's 3 × 10 s. Size it from
      `dco_webhook_initial_sync_duration_seconds`: too tight turns a slow
      start into a crash loop, too loose only delays the restart of a pod
      that is not taking traffic anyway.
      Check the measured figure after a scale-out and raise
      `failureThreshold` if it is close:

      ```promql
      max(dco_webhook_initial_sync_duration_seconds)
      ```

      The compile itself is under a second for a thousand targets; what
      makes a cold start slow is the informer cache sync in front of it,
      which `spec.cacheSelector` is the lever for. See
      [Capacity planning](capacity.md#cold-start-how-long-before-a-replica-can-serve).

## Rolling updates

A conversion webhook is on the apiserver's admission path. A replica that
stops listening before the apiserver stops being routed to it fails **every
write** to **every target it serves**, for reasons that have nothing to do
with the deployment being rolled. The defaults below make that not happen;
they are a set, not four independent knobs.

- [ ] **Leave the `preStop` sleep on.** `rollout.preStopSleepSeconds` defaults
      to `5`. It makes the container outlive its own Endpoints removal, so the
      apiserver stops being routed here before the listener goes away. This is
      the single highest-value setting on this page. Setting it to `0` disables
      the hook and is only correct if something else already guarantees the
      ordering.
- [ ] **Keep the three timeouts consistent.** The sequence a terminating pod
      goes through is:

      ```text
      preStop sleep (5s)  →  SIGTERM  →  graceful drain (--shutdown-timeout, 30s)
                                              →  (grace period ends) SIGKILL
      ```

      so `rollout.terminationGracePeriodSeconds` (default `45`) must exceed
      preStop + drain. Raising one in isolation is how a rollout that looked
      safe starts dropping requests; the ConversionWebhookServer validating
      webhook rejects a combination that does not add up, including a
      `--shutdown-timeout` set through `extraArgs`.
- [ ] **Do not lower `maxSurge` or raise `maxUnavailable` casually.**
      `rollout.maxUnavailable` defaults to `0` and `rollout.maxSurge` to `1`, so
      a replacement is Ready before its predecessor goes away. This is stricter
      than Kubernetes' own 25% default on purpose: at two replicas, taking one
      out first halves the capacity of an admission-path dependency. With
      `maxUnavailable: 0`, a `maxSurge` of at least 1 is required or the rollout
      cannot progress at all.
- [ ] **Check the PodDisruptionBudget agrees.** `minAvailable: 1` with two
      replicas and `maxUnavailable: 0` is consistent. `minAvailable` equal to
      the replica count is not: voluntary eviction is then impossible and node
      drains hang.

The nightly [soak workflow](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/.github/workflows/soak.yml)
(`hack/e2e-soak.sh`) rolls the webhook-server four times under sustained reads
and writes at a non-storage version and asserts **zero** failed requests and
**zero** requests that returned successfully with the wrong converted value.
Run it locally against a change to any of the above:

```bash
hack/e2e-soak.sh --duration 300 --restarts 3
```

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
- **Zone spreading is not defaulted.** The default spread constraint is across
  `kubernetes.io/hostname` only. Spreading across `topology.kubernetes.io/zone`
  needs an explicit `conversionWebhookServer.topologySpreadConstraints`, because
  a default zone constraint is wrong on any single-zone cluster.
- **Per-pod registry state isn't in `status`.** The PromQL check above exists
  because that's deliberately not a status field — see
  [Limitations](../limitations.md).

## Related

- [Capacity planning](capacity.md) — how many replicas of what size.
- [Upgrade runbook](upgrade-runbook.md) — staying available through a rollout.
- [ConversionWebhookServer reference](../configuration/conversionwebhookserver.md) — every field named above.
