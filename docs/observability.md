# Observability

How to tell whether conversion configs are actually loaded and serving —
without `kubectl exec` into a webhook-server pod.

## Registry readiness (per replica)

Each ConversionWebhookServer replica owns an in-memory registry of
compiled plans. Desired assignment on the CWS object
(`status.assignedConfigs`) is **not** proof that a given replica has
loaded those configs — that state is local to each pod.

Scraped Prometheus metrics on the webhook-server metrics port (`8443`,
path `/metrics`) are the supported signal:

| Metric | Meaning |
|---|---|
| `xrdconv_webhook_ready` | `1` after initial sync completed on this replica |
| `xrdconv_webhook_registry_size` | Count of registry entries on this replica (includes error-only placeholders) |
| `xrdconv_webhook_registry_entry_loaded{xrd="<target>"}` | `1` if this replica has a compiled, servable plan for that XRD/CRD target name; `0` if the entry exists but is error-only |
| `xrdconv_webhook_registry_last_reload_timestamp_seconds{xrd=...}` | Last successful compile time |
| `xrdconv_webhook_registry_compile_errors_total{xrd=...,reason=...}` | Compile failures that left stale-or-absent plans in place |

Replica identity comes from the Prometheus scrape target (pod / instance
label). Example: confirm every **ready** webhook-server pod has loaded
target `xfoos.example.org` (empty result = healthy):

```promql
(xrdconv_webhook_ready == 1)
  unless on (pod)
(xrdconv_webhook_registry_entry_loaded{xrd="xfoos.example.org"} == 1)
```

Use `instance` instead of `pod` if that is the stable scrape identity in
your Prometheus config.

Compare desired assignment count on the CWS status with live load:

```promql
# per-replica loaded (servable) entries
count by (pod) (xrdconv_webhook_registry_entry_loaded == 1)
```

`ConversionWebhookServer.status.assignedConfigs` remains the cluster-level
**desired** set computed by the shared resolver. Use it together with the
per-pod gauges above — not as a substitute for them.

## Debug endpoint (break-glass)

`GET /debug/registry` on the plain-HTTP port still returns a JSON snapshot
of the local registry. Prefer metrics for alerting and routine checks;
use the debug endpoint only when you already have pod exec / port-forward
access.

## Related

- Metrics trust boundary and NetworkPolicy: [security/metrics.md](security/metrics.md)
- ConversionWebhookServer status fields: [configuration/conversionwebhookserver.md](configuration/conversionwebhookserver.md)

## Controller watch-mapping errors

When a secondary watch map function fails to `List` related configs (API
timeout, etc.), the failure is logged and counted as
`xrdconv_watch_map_list_errors_total{map_func=...}` instead of looking like
a legitimate empty result. Configs still periodically self-reconcile
(`RequeueAfter`), so a transient List miss recovers without pod restart.
