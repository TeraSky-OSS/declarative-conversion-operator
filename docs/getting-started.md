# Getting Started

This walks through installing the operator and converting your first XRD, end to end, on a scratch cluster.

## Prerequisites

- A Kubernetes cluster (1.27+).
- [cert-manager](https://cert-manager.io/docs/installation/) installed — both the operator's own admission webhook and every `ConversionWebhookServer` instance need it for TLS.
- [Crossplane](https://docs.crossplane.io/latest/software/install/) installed (the current `apiextensions.crossplane.io/v2` API — see [Limitations](limitations.md)).
- `kubectl` and `helm` on your `PATH`.

If you just want to try things out, `kind` works fine — see the [`hack/e2e-test.sh`](https://github.com/terasky-oss/declarative-conversion-operator/blob/main/hack/e2e-test.sh) script in the repository for a fully scripted example of everything below.

## 1. Install the operator

```console
helm install declarative-conversion-operator \
  oci://ghcr.io/terasky-oss/charts/declarative-conversion-operator \
  --namespace declarative-conversion-system --create-namespace
```

See [Installation](installation.md) for the full set of values and what a fresh install creates. By default this also creates one `ConversionWebhookServer` named `default`.

```console
kubectl wait --for=condition=Available conversionwebhookserver/default --timeout=120s
```

## 2. Have an XRD with more than one served version

If you don't already have one, here's a minimal two-version example. `v2` is the hub (`referenceable: true`); `v1` is a spoke whose field is simply named differently.

```yaml title="xrd.yaml"
apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: xwidgets.example.org
spec:
  scope: Namespaced
  group: example.org
  names:
    kind: XWidget
    plural: xwidgets
  versions:
    - name: v2
      served: true
      referenceable: true # this is the hub version
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                storageGB:
                  type: string
    - name: v1
      served: true
      referenceable: false
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                storageSize:
                  type: string
```

```console
kubectl apply -f xrd.yaml
kubectl wait --for=condition=Established --timeout=60s crd/xwidgets.example.org
```

## 3. Test the mapping offline, before touching the cluster

Write the config you intend to apply:

```yaml title="xrdconversionconfig.yaml"
apiVersion: terasky.com/v1alpha1
kind: XRDConversionConfig
metadata:
  name: xwidgets-conversion
spec:
  targetXRD:
    name: xwidgets.example.org
  hubVersion: v2
  spokes:
    - version: v1
      rules:
        - strategy: FieldRename
          fieldRename:
            hubPath: spec.storageGB
            spokePath: spec.storageSize
```

And a sample object or two:

```yaml title="samples/example.yaml"
apiVersion: example.org/v2
kind: XWidget
metadata:
  name: sample
spec:
  storageGB: "100"
```

Run it through the exact same engine the operator will use, entirely offline:

```console
convctl validate --config xrdconversionconfig.yaml --xrd xrd.yaml
convctl test --xrd xrd.yaml --config xrdconversionconfig.yaml --samples ./samples/
```

`convctl test` round-trips every sample through every served version and reports any unacknowledged data loss. See the [CLI Reference](cli.md) for the full command set, including `--live`, which runs the same check against every object that already exists in a real cluster.

## 4. Apply it

```console
kubectl apply -f xrdconversionconfig.yaml
kubectl get xrdconversionconfig xwidgets-conversion -o wide
```

Watch it progress through its phases:

```console
kubectl get xrdconversionconfig xwidgets-conversion -w
```

Once `PHASE` reaches `Applied`, the operator has server-side-applied `spec.conversion` onto the XRD — never before validation passed, the XRD was healthy, and the assigned `ConversionWebhookServer` was confirmed ready. If it's stuck anywhere before that, `status.conditions` and `status.spokeStatuses` explain exactly why — see [XRDConversionConfig](configuration/xrdconversionconfig.md#status) for what each field means.

## 5. Prove it actually converts

```console
kubectl apply -f - <<'EOF'
apiVersion: example.org/v1
kind: XWidget
metadata:
  name: demo
  namespace: default
spec:
  storageSize: "500"
EOF

kubectl get xwidgets.v2.example.org demo -n default -o jsonpath='{.spec.storageGB}'
# 500
```

The object was created at `v1` and read back at the hub version `v2`, converted by the webhook the operator wired up — no code written.

## Next steps

- [Configuration](configuration/index.md) — the full `XRDConversionConfig` and `ConversionWebhookServer` spec.
- [Examples](examples/index.md) — five complete, runnable conversion stories, smallest first.
- [Strategy Reference](strategies/index.md) — every built-in strategy with worked examples.
- [CLI Reference](cli.md) — `convctl validate` / `analyze` / `test`, including `--live` pre-upgrade checks.
