# Roadmap

This project designed several of its interfaces with specific extension points already seamed in, so it's worth being explicit about what's a natural next step versus what would require rethinking the architecture. None of the below is a committed timeline — it's a statement of intent and an invitation to [open an issue or PR](https://github.com/terasky-oss/declarative-conversion-operator/issues) if one of these matters to you sooner rather than later.

## More conversion strategies

The current 16 strategy families (23 named strategies, counting mirror pairs) cover the common field-reshaping patterns seen in real API evolutions. As new patterns come up against real XRDs, expect the list to grow — the `Strategy` enum and `ConversionRule`'s discriminated-union shape were built to make adding one a contained, additive change (a new `*Params` type, a new `Op`, a new `compile.go` resolver, matching webhook validation and CLI test coverage).

## Deeper observability

- Richer per-pod webhook-server state exposed through `/debug/registry` today; a future iteration may add a lightweight way to query aggregate per-pod state across a `ConversionWebhookServer`'s replicas without going through raw `kubectl exec`/`port-forward`.
- More built-in Grafana dashboards/alerts shipped alongside the chart's optional `PrometheusRule`.

## CLI ergonomics

- A `convctl diff` style command to preview exactly what an `XRDConversionConfig`/`CRDConversionConfig` update would change about an existing plan's coverage, before applying it.

## Multi-cluster / GitOps workflows

Nothing today coordinates `ConversionWebhookServer` state across clusters — each cluster's operator and webhook-server pods are entirely self-contained. Fleet-wide config validation (e.g. `convctl test --live` run against many clusters from CI before a config lands anywhere) is achievable today with existing tooling, but isn't a first-class feature of the operator itself.

---

Have a use case that doesn't fit any of the above? [Open an issue](https://github.com/terasky-oss/declarative-conversion-operator/issues) — real-world XRD migration patterns are exactly what shapes which strategy gets built next.
