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
| `resources`, `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`, `topologySpreadConstraints`, `serviceAccountName` | Standard Kubernetes pod-scheduling knobs, applied to this instance's Deployment. |
| `podLabels` | Merged onto the pod template. Keys the controller uses for the Deployment selector (`app.kubernetes.io/name`, `instance`, `managed-by`) are ignored so a mis-set label cannot break rolling updates. |
| `podAnnotations` | Set on the webhook-server pod template. |
| `extraArgs` | Additional container arguments appended after operator-managed flags (`--webhook-server-name`, `--tls-cert-dir`, bind addresses, feature toggles, `--cache-label-selector`). For optional webhook-server flags (e.g. `--cert-reload-interval`, zap options). Admission and reconcile reject ExtraArgs that name those managed flags. |
| `extraEnv`, `extraVolumes`, `extraVolumeMounts` | Appended after the operator-managed environment / `tls`+`tmp` volumes. Use for custom CA bundles, proxies, or tenant env. |
| `cacheSelector` | Optional `metav1.LabelSelector`. When set, webhook-server replicas watch only matching `XRDConversionConfig` / `CRDConversionConfig` objects. Unset (the default) watches every config. |
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
```

`status.assignedConfigs` reflects the **desired** assignment as computed by the shared resolver every reconcile — not proof that every replica has actually loaded that config. Per-pod actual state (what's really compiled and serving right now) is deliberately kept out of this status field, to avoid the operator's own reconcile loop depending on a network call to the webhook-server pods; check each pod's own `/debug/registry` endpoint or its metrics for that.

### Conditions

| Condition | Meaning when `True` |
|---|---|
| `Available` | The owned Deployment reports `Available`. |
| `ServiceReady` | The owned Service has ready endpoints. |
| `CertificateReady` | The owned Certificate's Secret contains a valid TLS keypair. |
| `DefaultConflict` | More than one instance is marked `default` — shouldn't happen if the admission webhook works, but direct edits/restores can still produce it. The reconciler flags this loudly and does **not** auto-fix it. |
| `DeletionBlocked` | Deletion is being held by the finalizer — see [Deletion safety](#deletion-safety). |

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

## Deletion safety

Deleting a `ConversionWebhookServer` runs the same finalizer-gated safety check: the operator lists every `XRDConversionConfig`, resolves its assignment, and blocks deletion (`DeletionBlocked` condition, listing the dependent configs by name) if **any** of them resolve to this instance — explicitly via `webhookServerRef`, or implicitly as the fallback `default`. The break-glass override is the same pattern as `XRDConversionConfig`:

```console
kubectl annotate conversionwebhookserver default \
  conversion.terasky.com/allow-force-delete=true
kubectl delete conversionwebhookserver default
```

With the annotation present (checked live at the moment of the delete reconcile), the finalizer is removed and the owned Deployment/Service/Certificate/HPA/PDB garbage-collect via their owner references.
