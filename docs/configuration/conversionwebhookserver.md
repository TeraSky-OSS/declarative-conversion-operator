# ConversionWebhookServer

A `ConversionWebhookServer` is a deployable, independently scalable instance of the shared conversion webhook runtime — the thing that actually receives `ConversionReview` requests from the apiserver and converts objects. The Helm chart creates exactly one, named `default` and marked `spec.default: true`; create more directly as CRs for scale-out or tenant isolation.

It's cluster-scoped, but its owned resources (Deployment, Service, Certificate, HPA, PDB) live in a real namespace given by `spec.namespace` (defaulting to the operator's own install namespace). The Deployment itself is built by the operator from this CR — not from Helm templates — so pod knobs live here, not under a Helm `Deployment` for webhook-server pods. The chart's `conversionWebhookServer.*` values are passed through onto the default instance's spec.

## Spec

```yaml
apiVersion: terasky.com/v1alpha1
kind: ConversionWebhookServer
metadata:
  name: default
spec:
  default: true
  replicas: 2
  extraArgs:
    - --cert-reload-interval=1m
  extraEnv:
    - name: TENANT
      value: a
  podLabels:
    tenant: a
  cacheSelector:
    matchLabels:
      tenant: a
  certificate:
    issuerRef:
      name: declarative-conversion-operator-selfsigned-issuer
      kind: ClusterIssuer
  podDisruptionBudget:
    minAvailable: 1
```

| Field | Description |
|---|---|
| `default` | Marks this instance as the fallback target for `XRDConversionConfig`s that don't set `spec.webhookServerRef`. At most one instance may be `default` at a time — the admission webhook rejects creating a second one. |
| `namespace` | Where this instance's owned resources live. Defaults to the operator's install namespace. |
| `replicas` | Fixed replica count. Mutually exclusive with `autoscaling` — once autoscaling is set, the HPA owns the replica count and this controller stops driving it directly. |
| `autoscaling.{minReplicas,maxReplicas,targetCPUUtilizationPercentage}` | Creates a `HorizontalPodAutoscaler` for this instance instead of a fixed count. |
| `image.{repository,tag,digest,pullPolicy}` | Overrides the webhook-server image for this instance. Omit to use the operator's own default (set via Helm `image.webhookServer.*` / a manager flag). When `digest` is set it takes precedence over `tag` (`repository@digest`). |
| `resources`, `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`, `topologySpreadConstraints`, `serviceAccountName` | Standard Kubernetes pod-scheduling knobs, applied to this instance's Deployment. Setting `resources.limits.memory` also makes the operator set `GOMEMLIMIT` on the container at 90% of it, so the Go garbage collector respects the same ceiling the kernel enforces — see [Capacity planning](../operations/capacity.md#peak-versus-steady-state). Set `GOMEMLIMIT` in `extraEnv` to override. |
| `podLabels` | Merged onto the pod template. Keys the controller uses for the Deployment selector (`app.kubernetes.io/name`, `instance`, `managed-by`) are ignored so a mis-set label cannot break rolling updates. |
| `podAnnotations` | Set on the webhook-server pod template. |
| `extraArgs` | Additional container arguments appended after operator-managed flags (`--webhook-server-name`, `--tls-cert-dir`, bind addresses, feature toggles, `--cache-label-selector`). For optional webhook-server flags (e.g. `--cert-reload-interval`, `--max-request-bytes`, `--request-timeout`, `--shutdown-timeout`, zap options). Admission and reconcile reject ExtraArgs that name those managed flags. |
| `extraEnv`, `extraVolumes`, `extraVolumeMounts` | Appended after the operator-managed environment / `tls`+`tmp` volumes. Use for custom CA bundles, proxies, or tenant env. An explicit `GOMEMLIMIT` here replaces the one derived from `resources.limits.memory` rather than being appended alongside it. |
| `cacheSelector` | Optional `metav1.LabelSelector`. When set, webhook-server replicas watch only matching `XRDConversionConfig` / `CRDConversionConfig` objects **and** only matching `CustomResourceDefinition` / `CompositeResourceDefinition` objects — so the targets have to carry the label too. Unset (the default) watches everything. See [Capacity planning](../operations/capacity.md#memory-the-webhook-server). |
| `sharding.{enabled,weight}` | Opts this instance into the pool that unpinned configs are distributed across, by weighted rendezvous hashing on the target name. Off unless set. While a pool exists it, not `spec.default`, serves unpinned configs — so the default instance must be a member, which admission enforces. See [Automatic sharding](#automatic-sharding). |
| `startupProbe.{enabled,periodSeconds,failureThreshold}` | The cold-start budget: `periodSeconds × failureThreshold`, defaulting to `5 × 60` (five minutes). The probe polls `/readyz`, which stays false until the registry has compiled every assigned plan, so the budget is a deadline on the sync itself; while it is in flight the kubelet runs neither of the other two probes. See [Capacity planning](../operations/capacity.md#cold-start-how-long-before-a-replica-can-serve). |
| `rollout.{preStopSleepSeconds,terminationGracePeriodSeconds,maxUnavailable,maxSurge,defaultTopologySpread}` | How a replica leaves service. Defaults (`5`, `45`, `0`, `1`, `true`) make a rolling update cause zero failed conversions; they are one set, and admission rejects a combination where preStop + `--shutdown-timeout` exceeds the grace period. See the [HA checklist](../operations/ha-checklist.md#rolling-updates). |
| `certificate.issuerRef` | The cert-manager `Issuer`/`ClusterIssuer` for this instance's webhook TLS certificate. `certificate.dnsNames`, `.duration`, `.renewBefore` are also available. |
| `service.{type,port,annotations}` | The `Service` fronting this instance's pods. |
| `podDisruptionBudget.{minAvailable,maxUnavailable}` | Creates a `PodDisruptionBudget` for this instance. |

## Status

```yaml
status:
  observedGeneration: 2
  replicas: 2
  readyReplicas: 2
  endpoint: https://default-webhook-server.declarative-conversion-system.svc:443
  conditions:
    - type: Available
      status: "True"
      reason: DeploymentAvailable
    - type: ServiceReady
      status: "True"
    - type: CertificateReady
      status: "True"
      reason: CertificateReady
  assignedConfigs:
    - name: xwidgets-conversion
      xrdName: xwidgets.example.org
      phase: Applied
  reportingReplicas: 2
  servedTargets:
    - xwidgets.example.org
```

`status.assignedConfigs` reflects the **desired** assignment as computed by the shared resolver every reconcile — not proof that every replica has actually loaded that config.

`status.servedTargets` is the other half: what every live replica reports it can *actually* serve, as an **intersection** — a target two replicas out of three hold is a target that fails one request in three, so it does not appear here. `reportingReplicas` says how many replicas fed that intersection; a value below `readyReplicas` means at least one ready replica has not published yet, and the list is not yet a statement about the whole instance.

The gap between the two lists is exactly the window in which a target has been *given* to this instance but the instance cannot serve it yet, and it is what the [handover gate](#moving-a-target-is-gated-not-immediate) waits on.

This is still not a network call: the replicas publish it themselves into a Lease each, and the operator reads those through an informer like anything else. For a single pod's exact registry contents, its own `/debug/registry` endpoint and its `dco_webhook_registry_entry_loaded` metric remain the finer-grained answer.

### Conditions

| Condition | Meaning when `True` |
|---|---|
| `Available` | The owned Deployment reports `Available`. |
| `ServiceReady` | The owned Service has ready endpoints. |
| `CertificateReady` | The owned Certificate's Secret contains a valid TLS keypair. |
| `DefaultConflict` | More than one instance is marked `default` — shouldn't happen if the admission webhook works, but direct edits/restores can still produce it. The reconciler flags this loudly and does **not** auto-fix it. |
| `DeletionBlocked` | Deletion is being held by the finalizer — see [Deletion safety](#deletion-safety). |

The `XRDConversionConfig` / `CRDConversionConfig` side of a move carries its own `HandoverReady` condition — see [Moving a target is gated, not immediate](#moving-a-target-is-gated-not-immediate).

## Multiple instances

Create additional instances for scale-out or tenancy, then point specific configs at them:

```yaml
apiVersion: terasky.com/v1alpha1
kind: ConversionWebhookServer
metadata:
  name: tenant-a-webhook
spec:
  replicas: 3
  namespace: tenant-a
  certificate:
    issuerRef:
      name: tenant-a-issuer
      kind: ClusterIssuer
---
apiVersion: terasky.com/v1alpha1
kind: XRDConversionConfig
metadata:
  name: xdatabases-conversion
spec:
  targetXRD:
    name: xdatabases.tenant-a.example.org
  webhookServerRef:
    name: tenant-a-webhook
  # ...
```

Every replica of every instance is symmetric and self-sufficient: each runs its own lightweight controller-runtime manager watching `XRDConversionConfig`, `ConversionWebhookServer`, and the relevant XRDs directly — there's no push mechanism from the main operator, and no leader election, since there's no shared state to coordinate. A single config's compile failure only affects that config: the pod keeps serving whatever was last good for every other XRD, and never crash-loops or de-readies over one bad config.

## Automatic sharding

Hand-assigning every config with `webhookServerRef` is right for tenant
isolation and wrong as a scaling story. `spec.sharding` distributes the
configs that express no preference across every instance that opts in:

```yaml
apiVersion: terasky.com/v1alpha1
kind: ConversionWebhookServer
metadata:
  name: default
spec:
  default: true
  sharding:
    enabled: true
---
apiVersion: terasky.com/v1alpha1
kind: ConversionWebhookServer
metadata:
  name: shard-b
spec:
  sharding:
    enabled: true
    weight: 2   # twice the share of an instance at the default weight of 1
  # ...
```

Assignment is resolved in strict precedence order, by the same shared
resolver the operator and every webhook-server replica run independently:

1. **`spec.webhookServerRef`** — deliberate pinning wins over everything.
   Sharding can never move a pinned config, which is what keeps tenant
   isolation intact.
2. **The sharding pool**, if any instance opts in, picked by weighted
   rendezvous hashing on the *target resource's* name.
3. **`spec.default`**, exactly as before, when no instance opts in.

### Enable it on the default instance first

While a pool exists it, not `spec.default`, answers for unpinned configs.
The instance marked default must therefore be a pool member, and admission
rejects a state where it is not — otherwise enabling sharding on one
non-default instance would move every unpinned config onto it in a single
write.

There is always a valid ordering. Enabling sharding on the default
instance alone makes the pool that one instance, so nothing moves. Each
instance added afterwards takes a bounded share.

### Why rendezvous hashing

Adding an instance moves only the targets that instance now wins — in
expectation `1/(N+1)` of them — and moves **nothing** between the instances
that were already there. Removing one moves only its own. A hash ring
merely approximates that, with a quality that depends on a virtual-node
count somebody has to tune; rendezvous has no such knob and no shared
state, so every party computes the same answer from the same objects
without coordinating.

### Moving a target is gated, not immediate

A move — a changed `webhookServerRef`, or a rebalance after an instance is
added — means repointing the target's `spec.conversion` at a different
Service. Doing that the moment the assignment changes would open a window
in which the apiserver calls replicas that have not compiled the plan yet,
and every read and write of that resource fails until they have.

So the operator waits for the destination to confirm it can already serve
the target:

- Each replica publishes the targets it holds a compiled plan for into a
  Lease of its own. `status.servedTargets` is the **intersection** across
  live replicas — a target two replicas out of three can serve is not a
  target the instance serves — and `status.reportingReplicas` says how many
  fed it.
- The config's `HandoverReady` condition reports the verdict. `False` with
  reason `HandoverPending` means the move is deliberately being held. It is
  not cleared once the move completes: it is the verdict on the *last*
  handover, which stays true — and which is how an unverified one stays
  visible long enough to be noticed.
- Meanwhile the **source** keeps serving: a replica holds a compiled plan
  for as long as either the assignment *or* the live target points at it.
  That is what makes waiting safe rather than merely slower.
- And it keeps serving for **30 seconds after** the target stops naming it.
  The apiserver refreshes a CRD's conversion configuration asynchronously
  after the write, so for a moment it is still calling the old Service; a
  replica that dropped its plan immediately would answer those calls with a
  503. Same shape as the `preStop` sleep, same treatment.

An instance whose replicas publish nothing at all — a fleet mid-upgrade, or
one in a namespace with no Lease `Role` — cannot be verified. The move then
proceeds as every earlier release did, and says so: `HandoverReady=True`
with reason `HandoverUnverified`. See
[RBAC](../security/rbac.md) for the Role the replicas need.

## Deletion safety

Deleting a `ConversionWebhookServer` runs the same finalizer-gated safety check: the operator lists every `XRDConversionConfig`, resolves its assignment, and blocks deletion (`DeletionBlocked` condition, listing the dependent configs by name) if **any** of them resolve to this instance — explicitly via `webhookServerRef`, implicitly as the fallback `default`, or by landing here through [sharding](#automatic-sharding).

It also blocks on a config whose *target* still points its conversion webhook here, even after the resolver has moved the config elsewhere. That is the mid-handover state, and during it this instance is the one answering every `ConversionReview` for that target — so judging by assignment alone would approve deleting the instance a live target depends on. The break-glass override is the same pattern as `XRDConversionConfig`:

```console
kubectl annotate conversionwebhookserver default \
  conversion.terasky.com/allow-force-delete=true
kubectl delete conversionwebhookserver default
```

With the annotation present (checked live at the moment of the delete reconcile), the finalizer is removed and the owned Deployment/Service/Certificate/HPA/PDB garbage-collect via their owner references.
