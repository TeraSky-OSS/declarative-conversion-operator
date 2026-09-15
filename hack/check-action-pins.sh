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
unchecked=0

# resolve prints "ok", "missing", or a reason the check could not be made.
#
# Only a 404 means the reference does not exist. Anything else — a rate
# limit, a transient 5xx, a token without the scope — is a failure to look,
# and reporting that as a missing tag sends somebody chasing a reference that
# is perfectly fine. One retry, because a single blip should not do either.
resolve() {
  local endpoint="$1" out status
  for attempt in 1 2; do
    if out="$(gh api "$endpoint" --jq .ref 2>&1)" || [[ "$out" == *'"sha"'* ]]; then
      echo ok
      return
    fi
    status="$out"
    if grep -qiE '\(HTTP 404\)|Not Found' <<< "$status"; then
      echo missing
      return
    fi
    [ "$attempt" -eq 1 ] && sleep 2
  done
  # Collapse the API's message onto one line for the warning.
  tr '\n' ' ' <<< "$status" | cut -c1-120
}

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
    verdict="$(resolve "repos/$repo/commits/$tag")"
  else
    verdict="$(resolve "repos/$repo/git/ref/tags/$tag")"
    if [ "$verdict" = "missing" ]; then
      # A branch is a legitimate, if unwise, thing to pin to.
      verdict="$(resolve "repos/$repo/git/ref/heads/$tag")"
    fi
  fi

  case "$verdict" in
    ok)
      checked=$((checked + 1))
      ;;
    missing)
      echo "UNRESOLVABLE $ref (no such tag, branch or commit)" >&2
      failed=1
      ;;
    *)
      # Could not look is not the same as is not there. A rate limit or a
      # network blip reported as a missing tag would send somebody chasing
      # a reference that is perfectly fine.
      echo "WARNING  $ref could not be checked ($verdict)" >&2
      unchecked=$((unchecked + 1))
      ;;
  esac
done <<< "$refs"

if [ "$failed" -ne 0 ]; then
  echo "one or more action references do not resolve" >&2
  exit 1
fi
if [ "$unchecked" -gt 0 ]; then
  echo "all $checked action reference(s) resolve; $unchecked could not be checked"
  exit 0
fi
echo "all $checked action reference(s) resolve"
