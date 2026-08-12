# Pod security posture

Both Deployments this operator runs — the **manager** (Helm-templated) and
each **ConversionWebhookServer** webhook-server replica (built in Go by the
controller) — use the same Pod Security Standards `restricted`-compatible
`securityContext`. The images are `gcr.io/distroless/static:nonroot`
(`USER 65532:65532`).

| Setting | Manager | Webhook-server (CWS) |
|---|---|---|
| Pod `runAsNonRoot` | `true` | `true` |
| Pod `runAsUser` / `runAsGroup` | `65532` | `65532` |
| Pod `seccompProfile.type` | `RuntimeDefault` | `RuntimeDefault` |
| Container `allowPrivilegeEscalation` | `false` | `false` |
| Container `capabilities.drop` | `[ALL]` | `[ALL]` |
| Container `readOnlyRootFilesystem` | `true` | `true` |
| Container `runAsUser` / `runAsGroup` | `65532` | `65532` |
| Scratch volume | `emptyDir` at `/tmp` | `emptyDir` at `/tmp` |
| TLS certs | Secret mount under `/tmp/k8s-webhook-server/serving-certs` | Secret mount at `/tls` (read-only) |

The only intentional difference is **where TLS material is mounted**: the
manager's kubebuilder admission webhook expects certs under
`/tmp/k8s-webhook-server/serving-certs`; the conversion webhook-server
binary is passed `--tls-cert-dir=/tls`.

## Where this is set

- Manager: `charts/declarative-conversion-operator/templates/manager/deployment.yaml` (and the kustomize twin in `config/manager/deployment.yaml`)
- Webhook-server: `internal/controller/conversionwebhookserver_controller.go` (`reconcileDeployment`)
