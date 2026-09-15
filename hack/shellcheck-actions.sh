#!/usr/bin/env bash
# Shellcheck the scripts inside this repository's composite Actions.
#
# actionlint runs shellcheck over workflow `run:` steps, but reads a
# composite action's action.yml as a malformed workflow and skips it — so the
# Actions this project publishes, which contain most of its shell, would
# otherwise never be checked. The class of bug this catches is real: a
# `[ "$X" -lt 1 ]` against an unset value is a bash error rather than a
# failed comparison.
#
# Usage: hack/shellcheck-actions.sh [severity]
set -euo pipefail

severity="${1:-style}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

extracted=0
for action in "$root"/.github/actions/*/action.yml; do
  name="$(basename "$(dirname "$action")")"
  mkdir -p "$workdir/$name"
  # GitHub substitutes ${{ }} before bash ever sees the script, so they are
  # replaced with a literal rather than left for shellcheck to choke on.
  python3 - "$action" "$workdir/$name" <<'PY'
import re, sys, pathlib

src, outdir = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
text = src.read_text()
for i, m in enumerate(re.finditer(r'\n(\s+)run: \|\n((?:\1  .*\n|\n)+)', text)):
    indent = len(m.group(1)) + 2
    body = '\n'.join(l[indent:] if len(l) > indent else '' for l in m.group(2).split('\n'))
    body = re.sub(r'\$\{\{[^}]*\}\}', 'SUBSTITUTED', body)
    (outdir / f'step{i}.sh').write_text('#!/usr/bin/env bash\n' + body)
PY
  count="$(find "$workdir/$name" -name '*.sh' | wc -l)"
  extracted=$((extracted + count))
  echo "extracted $count script(s) from $name"
done

if [ "$extracted" -eq 0 ]; then
  echo "no scripts extracted; the extraction is broken, not the Actions" >&2
  exit 1
fi

shellcheck -S "$severity" "$workdir"/*/*.sh
echo "shellcheck clean across $extracted script(s)"
