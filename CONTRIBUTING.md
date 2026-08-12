# Contributing

Thanks for contributing to **declarative-conversion-operator**. This document
covers the day-to-day developer loop. For adding a new conversion strategy
end-to-end, see [Adding a strategy](docs/contributing/adding-a-strategy.md).

## Prerequisites

- Go matching `go.mod`
- Docker (for image builds / e2e)
- `kubectl`, `kind`, and `helm` for e2e targets

## Development loop

```console
make generate   # deepcopy for api/v1alpha1
make manifests  # CRD + RBAC into config/
make fmt
make vet
make test       # generate + manifests + fmt + vet + go test -race
```

Useful extras:

```console
make helm-sync          # copy generated CRDs into the Helm chart
make build              # manager, webhook-server, convctl binaries into bin/
make test-prometheus    # promtool unit tests for shipped alerts
```

Offline CLI checks against fixtures (no cluster required):

```console
go run ./cmd/convctl validate --config examples/field-rename/xrdconversionconfig.yaml --xrd examples/field-rename/xrd.yaml
go run ./cmd/convctl test --config examples/field-rename/xrdconversionconfig.yaml --xrd examples/field-rename/xrd.yaml --samples examples/field-rename/samples
```

## Pull requests

- Prefer small, reviewable commits that each leave `make test` green.
- Match the existing code and docs tone: concrete, conservative, no drive-by
  refactors outside the change's scope.
- Update docs when you change user-facing behavior (CLI flags, status
  semantics, chart values, strategies).
- Do not commit secrets (`.env`, kubeconfigs, credentials).

## Where to ask

- Bugs and features: [GitHub Issues](https://github.com/TeraSky-OSS/declarative-conversion-operator/issues)
- Security reports: [SECURITY.md](SECURITY.md) (private advisory — do not file a public issue)
- Conduct: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
