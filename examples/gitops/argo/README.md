# Argo CD: operator then configs

Two `Application`s. The operator Application is sync-wave `0`; configs
are wave `1` so Argo does not apply the `XRDConversionConfig` until the
chart (CRDs + `ConversionWebhookServer/default`) has been synced.

```text
examples/gitops/argo/operator-application.yaml
examples/gitops/argo/configs-application.yaml   → examples/gitops/configs
```

## Apply

```console
kubectl apply -f examples/gitops/argo/operator-application.yaml
# Wait for the chart, then:
kubectl wait --for=condition=Available --timeout=180s conversionwebhookserver/default
kubectl apply -f examples/gitops/argo/configs-application.yaml
kubectl get xrdconversionconfig xbuckets-conversion
```

With automated sync on both Applications, the wave annotation on the
config Application still orders the first sync. After that, Argo health
on `ConversionWebhookServer` (`Available`) is what keeps a config from
looking "stuck" while the controller health-gates.

The operator Application uses the OCI Helm chart
(`ghcr.io/terasky-oss/charts` / `declarative-conversion-operator`).
Enable Helm CRD install/upgrade in the Application so chart CRDs are not
install-once.

## driftPolicy

Same rule as Flux: `KeepServingStale` on every GitOps-managed config.
`FailClosed` plus Argo auto-sync on a hub flip is an outage — see the
[parent README](../README.md#driftpolicy-in-continuous-delivery).
