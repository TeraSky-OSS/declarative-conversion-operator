# Security Policy

## Supported versions

This project is currently in **alpha** (`v1alpha1` APIs). Security fixes
are applied to the latest published release on the default branch; older
alpha tags are not backported unless explicitly called out in a release
advisory.

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security reports.

Report vulnerabilities privately via GitHub Security Advisories for this
repository:

https://github.com/TeraSky-OSS/declarative-conversion-operator/security/advisories/new

Include a description of the issue, steps to reproduce, affected versions
(or commit), and any suggested fix if you have one. We will acknowledge
receipt and work with you on a coordinated disclosure timeline.

## Security model (summary)

- **Conversion webhook TLS.** Each `ConversionWebhookServer` gets a
  cert-manager `Certificate`; the operator mounts the issued Secret into
  webhook-server pods and refreshes the target XRD/CRD `caBundle` when the
  Secret rotates. cert-manager's CA injector is not used for XRD/CRD
  conversion webhook config (those resources are not supported injection
  targets).
- **Admission webhook TLS.** The manager's own validating webhook (for
  this operator's CRDs) uses a separate cert-manager Certificate templated
  by the Helm chart.
- **Pod security.** Manager and webhook-server pods run under Pod Security
  Standards `restricted`-compatible settings — see
  [Pod security posture](docs/security/pod-security.md).
- **Network.** Opt-in NetworkPolicies can lock metrics scrape sources and
  deny non-webhook ingress — see [Metrics trust boundary](docs/security/metrics.md).
- **RBAC.** The manager can patch XRDs/CRDs (to wire conversion webhooks);
  webhook-server ServiceAccounts are read/watch-only. Full verb/resource
  justifications: [RBAC blast radius](docs/security/rbac.md).
- **Supply chain.** Release images are signed (cosign) with SBOMs. Prefer
  pinning images by digest — see chart `image.*.digest` values.
