# `setup-convctl`

Installs a **verified** `convctl` onto `PATH`.

```yaml
- uses: terasky-oss/declarative-conversion-operator/.github/actions/setup-convctl@v1
  with:
    version: v0.5.0   # or "latest" (the default)
- run: convctl lint ./platform/
```

## Inputs

| Input | Default | Meaning |
|---|---|---|
| `version` | `latest` | a release tag, or `latest` |
| `verify` | `true` | verify the cosign signature on `checksums.txt`, then the archive's checksum against it |
| `token` | `${{ github.token }}` | for the releases API, to avoid anonymous rate limits |
| `cache` | `true` | restore and save through `actions/cache` |
| `binary` | — | path to a `convctl` that already exists, used instead of downloading one |

## Outputs

| Output | Meaning |
|---|---|
| `version` | the resolved tag — `latest` is pinned in the step summary, so a re-run is explainable |
| `path` | the installed binary |
| `cache-hit` | whether it came from the runner cache |

## Verification

On by default. That is the point: the release already produces a cosign-signed
`checksums.txt`, per-archive SBOMs and build-provenance attestations, and
almost nobody benefits from them because verifying by hand means reading the
release notes and writing eight lines of `cosign verify-blob`.

The certificate identity is pinned to **this repository's release workflow at
a tag**, not to a wildcard — a signature from any other workflow in any other
repository is exactly what this rejects. The OIDC issuer is pinned too.

The owner is matched case-insensitively: Fulcio records the casing GitHub
renders (`TeraSky-OSS`), and a lowercase pattern fails with *"none of the
expected identities matched"*, which reads like a bad signature rather than a
typo.

**A cache hit still verifies.** The cached branch is the one that runs in
practice, and a poisoned cache that a cache hit could launder would make the
whole thing decorative.

Set `verify: false` only if you have a reason; cosign is not installed at all
in that case, so it costs nothing to leave on.

## Using a binary you already have

`binary: /path/to/convctl` skips resolving, downloading and verifying
entirely — there is no artifact whose provenance could be in question — and
just puts it on `PATH`. For a runner that vendors the binary, one with no
egress to the releases API, and for this repository's own workflows, which
have to exercise the Actions against the build under test rather than against
the last release.

Every Action that composes this one accepts it too.

## Platforms

`ubuntu-latest`, `macos-latest` and `windows-latest`, on amd64 and arm64.
`shell: bash` throughout, so the same script runs on all three rather than a
Windows variant nobody exercises. Nothing needs `sudo`.
