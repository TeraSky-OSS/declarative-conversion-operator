#!/usr/bin/env bash
# Check that every third-party action reference in this repository resolves
# to a tag that actually exists.
#
# actionlint validates syntax and shellcheck validates the shell, but neither
# resolves a `uses:` — so `sigstore/cosign-installer@v4` passed every local
# check and failed in CI with "unable to find version v4", because sigstore
# publishes a floating v3 but only exact v4.x tags. The failure surfaces as
# an unresolvable action rather than as anything about the thing it installs,
# which makes it slow to diagnose.
#
# Needs gh authenticated (GITHUB_TOKEN is enough).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
failed=0
checked=0

# Every `uses:` across workflows and composite actions, minus local (./) ones.
refs="$(grep -rhoE '^\s*(- )?uses: [^ ]+' \
  "$root/.github/workflows" "$root/.github/actions" "$root/docs/gitops" 2>/dev/null \
  | sed -E 's/^\s*(- )?uses: //' \
  | grep -v '^\./' \
  | sort -u)"

while IFS= read -r ref; do
  [ -n "$ref" ] || continue
  path="${ref%@*}"
  tag="${ref##*@}"
  # owner/repo/sub/action@ref — the repository is the first two segments.
  repo="$(cut -d/ -f1,2 <<< "$path")"

  # This repository's own Actions cannot be resolved by tag until a release
  # carries them, so what is checked is that the path exists here. A
  # reference to an action we do not ship is the error worth catching; the
  # tag is the release process's business.
  if [ "$repo" = "terasky-oss/declarative-conversion-operator" ]; then
    sub="${path#"$repo"/}"
    if [ -f "$root/$sub/action.yml" ] || [ -f "$root/$sub/action.yaml" ]; then
      checked=$((checked + 1))
      continue
    fi
    echo "UNRESOLVABLE $ref (this repository ships no $sub)" >&2
    failed=1
    continue
  fi
  # A 40-character hex string is a commit SHA, which needs a different API.
  if [[ "$tag" =~ ^[0-9a-f]{40}$ ]]; then
    if gh api "repos/$repo/commits/$tag" --jq .sha >/dev/null 2>&1; then
      checked=$((checked + 1))
      continue
    fi
    echo "UNRESOLVABLE $ref (no such commit)" >&2
    failed=1
    continue
  fi
  if gh api "repos/$repo/git/ref/tags/$tag" --jq .ref >/dev/null 2>&1 \
    || gh api "repos/$repo/git/ref/heads/$tag" --jq .ref >/dev/null 2>&1; then
    checked=$((checked + 1))
    continue
  fi
  echo "UNRESOLVABLE $ref (no such tag or branch)" >&2
  failed=1
done <<< "$refs"

if [ "$failed" -ne 0 ]; then
  echo "one or more action references do not resolve" >&2
  exit 1
fi
echo "all $checked action reference(s) resolve"
