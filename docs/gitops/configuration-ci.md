# The Configuration repository's CI gate

For a platform shipped as a Crossplane `Configuration`, the unit of API change
is a **package version**. Not a commit, and not the live cluster — so a gate
built on either checks something adjacent to what actually ships.

This is the end-to-end gate for a repository that builds a `Configuration`.

## The shape of it

1. Build the package from the repository's XRDs.
2. Lint every conversion config in the tree **against that package**.
3. Test the conversions against fixtures.
4. Before a release, test them against the objects already in the cluster that
   will receive the upgrade.

Steps 1–3 need no cluster at all.

## The workflow

```yaml
name: Configuration
on: pull_request

permissions:
  contents: read

jobs:
  conversion:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          persist-credentials: false

      # A pinned release, with its checksum verified. Piping install.sh from
      # `main` into a shell executes whatever that branch holds at the
      # moment the job runs, which makes the gate unreproducible and trusts
      # a mutable reference with the runner's privileges.
      - name: Install the crossplane CLI
        env:
          CROSSPLANE_VERSION: v2.0.2
        run: |
          set -euo pipefail
          url="https://releases.crossplane.io/stable/${CROSSPLANE_VERSION}/bin/linux_amd64/crank"
          curl -fsSL -o crossplane "$url"
          curl -fsSL -o crossplane.sha256 "${url}.sha256"
          echo "$(cat crossplane.sha256)  crossplane" | sha256sum -c -
          chmod +x crossplane
          sudo mv crossplane /usr/local/bin/

      # setup-convctl installs a pinned release and verifies the cosign
      # signature on its checksums. `go install ...@latest` does neither: it
      # resolves to whatever is newest at the moment the job runs, and
      # nothing checks what it got.
      - uses: terasky-oss/declarative-conversion-operator/.github/actions/setup-convctl@v1
        with:
          version: v0.5.0

      - name: Build the package
        run: crossplane xpkg build --package-root=./apis --package-file=platform.xpkg

      # Every conversion config in the tree, against the XRDs the package
      # actually ships — not against whatever XRD files happen to sit beside
      # them, which is the thing that drifts.
      - name: Lint conversions against the package
        run: convctl lint ./apis/ --package platform.xpkg -o github

      - name: Test conversions against fixtures
        run: |
          convctl test --package platform.xpkg --target xwidgets.example.org \
            --config apis/widgets/conversion.yaml \
            --samples apis/widgets/samples/ \
            --validate-output -o github
```

`-o github` puts each finding on the line of the config that produced it, so
a failure lands on the diff rather than in a log —
see [CI-native formats](../cli.md#ci-native-formats-github-sarif-markdown).

## Before the upgrade lands

The composition that matters most pairs the **new** schemas with the **existing**
objects:

```console
convctl test --package ./platform-v1.4.0.xpkg --config config.yaml --live \
  --max-samples 500 --sample-strategy random --seed 1
```

*"If I bump this Configuration, do my 4,000 existing composites still
convert?"* `--package` is a schema source and `--live` is a sample source, so
they compose without anything new. On a large cluster,
[bounded sampling](../cli.md#bounded-sampling-on-a-large-cluster) keeps it
runnable — and the report says plainly that it sampled.

## Package-managed XRDs need the conversion config applied first

An XRD shipped in a package has its `spec.conversion` stripped on every
package resync unless the [conversion guard](../architecture.md#the-xrd-conversion-guard)
is on. That also changes the **order** of a version migration: the conversion
config must be applied *before* the package revision that serves the new
version, or the version is served with no conversion at all.

`convctl plan` detects a package-managed XRD and orders the steps accordingly:

```console
convctl plan --xrd xrd.yaml --config config.yaml --to v2 --package-managed
```

See [`convctl plan`](../cli.md#package-managed-xrds-are-ordered-differently).

## Related

- [`convctl lint`](../cli.md#convctl-lint) — the offline check that runs on every commit
- [Schema sources](../cli.md#schema-sources) — `--xrd`, `--crd`, `--package`
- [Fleet CI](fleet-ci.md) — the same checks across many clusters
