#!/usr/bin/env python3
"""Render a scale run as markdown, and fail it on a regression.

A scale target nobody runs is a scale target nobody trusts — and a
scheduled run whose output nobody can read is the same thing with extra
steps. This turns scalegen's JSON into a job summary, and compares it
against the previous run's artifact.

The comparison gates on a RELATIVE change, never on an absolute number.
Absolute timings on a hosted runner vary by a factor of two between runs
for reasons that have nothing to do with this code, so a threshold tight
enough to catch a real regression would fire constantly, and one loose
enough not to would catch nothing. A run that is 1.5x the previous run on
the same measurement is a signal; a run that took 40 ms instead of 30 ms is
not.

Two measurements are exempt from ratio comparison and checked absolutely,
because for them zero is the only acceptable value: error counts, and the
requirement that the run did any work at all.

Usage:
  hack/scale-report.py --current cur.json [--previous prev.json]
                       [--threshold 1.5] [--summary out.md]
"""
from __future__ import annotations

import argparse
import json
import sys

# Lower is better for everything here except throughput, which the report
# carries but which is derived from p50 — so comparing both would double-
# count the same movement. Throughput is rendered, not gated.
LATENCY_FIELDS = ("p50Ms", "p99Ms")

# Cluster-side observations worth gating on, and what they mean.
#
# webhookWorkingSetBytes is a single sample taken once the replicas are
# Ready again after the restart — so it is the loaded steady state, NOT the
# transient peak during the cold start. The peak happens before readiness,
# where nothing is sampling; pkg/engine's and internal/webhookserver's
# -benchmem benchmarks are what measure that, and they are in
# docs/operations/capacity.md.
OBSERVED_GATED = {
    "webhookInitialSyncSeconds": "webhook-server cold start",
    "webhookWorkingSetBytes": "webhook-server working set after the cold start",
}

# Below this, the measurement is too small for a ratio to mean anything: a
# p50 that moved from 2 ms to 4 ms is a 2x regression by arithmetic and
# scheduler noise by every other reading.
NOISE_FLOOR_MS = 20.0
NOISE_FLOOR_SECONDS = 1.0
NOISE_FLOOR_BYTES = 32 * 1024 * 1024


def human_bytes(value: float) -> str:
    for unit in ("B", "KiB", "MiB", "GiB"):
        if abs(value) < 1024 or unit == "GiB":
            return f"{value:.1f} {unit}"
        value /= 1024
    return f"{value:.1f} GiB"


def noise_floor(key: str) -> float:
    if key.endswith("Bytes"):
        return NOISE_FLOOR_BYTES
    if key.endswith("Seconds"):
        return NOISE_FLOOR_SECONDS
    return NOISE_FLOOR_MS


def envelope_delta(previous: dict, current: dict) -> list[str]:
    """Every input the two runs disagree on.

    Targets and instances alone are not enough: the same fleet driven at 16
    workers and at 60, or at a different QPS or strategy mix, is two
    different measurements wearing the same field names. Comparing them
    produces a confident answer to a question nobody asked.
    """
    prev_env = previous.get("envelope") or {
        "targets": str(previous.get("targets")),
        "instances": str(previous.get("instances")),
    }
    cur_env = current.get("envelope") or {
        "targets": str(current.get("targets")),
        "instances": str(current.get("instances")),
    }
    out = []
    for key in sorted(set(prev_env) | set(cur_env)):
        was, now = prev_env.get(key), cur_env.get(key)
        if was != now:
            out.append(f"{key} {was} -> {now}")
    return out


def render(report: dict, previous: dict | None) -> list[str]:
    lines: list[str] = []
    lines.append("## Scale run")
    lines.append("")
    lines.append(
        f"**{report['targets']} CRDs x 3 versions**, "
        f"{report['instances']} objects each "
        f"({report['totalObjects']} objects total), "
        f"recorded {report.get('recordedAt', 'unknown')}."
    )
    lines.append("")
    lines.append(f"Fleet creation took {report['createMs'] / 1000:.1f}s.")
    lines.append("")

    lines.append("| Operation | n | errors | p50 | p99 | max | per-worker /s |")
    lines.append("|---|---:|---:|---:|---:|---:|---:|")
    for name in sorted(report.get("measurements", {})):
        m = report["measurements"][name]
        lines.append(
            f"| `{name}` | {m['n']} | {m['errors']} | "
            f"{m['p50Ms']:.1f} ms | {m['p99Ms']:.1f} ms | {m['maxMs']:.1f} ms | "
            f"{m.get('throughputPerSecond', 0):.1f} |"
        )
    lines.append("")

    observed = report.get("observed") or {}
    if not observed:
        lines.append(
            "> :warning: **No cluster-side observations were collected.** Cold start and "
            "working set are missing from this run, so neither is trended against the "
            "previous one. See the job log for what `scale-observe.py` reported."
        )
        lines.append("")
    if observed:
        lines.append("| Observed on the cluster | Value |")
        lines.append("|---|---:|")
        for key in sorted(observed):
            value = observed[key]
            shown = human_bytes(value) if key.endswith("Bytes") else f"{value:g}"
            lines.append(f"| `{key}` | {shown} |")
        lines.append("")

    if previous is None:
        lines.append("_No previous run to compare against; this run becomes the baseline._")
        lines.append("")
    return lines


def compare(current: dict, previous: dict, threshold: float) -> tuple[list[str], list[str]]:
    """Return (rendered rows, regression messages)."""
    lines = ["| Measurement | previous | current | change |", "|---|---:|---:|---:|"]
    regressions: list[str] = []

    def row(label: str, key: str, was: float, now: float) -> None:
        if key.endswith("Bytes"):
            shown_was, shown_now = human_bytes(was), human_bytes(now)
        elif key.endswith("Seconds"):
            shown_was, shown_now = f"{was:.2f}s", f"{now:.2f}s"
        elif key == "createMs":
            # Fleet creation is minutes at any interesting envelope, and
            # "120000.0 ms" is not a number anyone reads at a glance.
            shown_was, shown_now = f"{was / 1000:.1f}s", f"{now / 1000:.1f}s"
        else:
            shown_was, shown_now = f"{was:.1f} ms", f"{now:.1f} ms"

        if was <= 0:
            lines.append(f"| {label} | — | {shown_now} | new |")
            return
        ratio = now / was
        marker = ""
        if ratio > threshold and now > noise_floor(key):
            marker = " **REGRESSED**"
            regressions.append(
                f"{label}: {shown_was} -> {shown_now} ({ratio:.2f}x, threshold {threshold:.2f}x)"
            )
        lines.append(f"| {label} | {shown_was} | {shown_now} | {ratio:.2f}x{marker} |")

    for name in sorted(current.get("measurements", {})):
        prev_m = previous.get("measurements", {}).get(name)
        if not prev_m:
            continue
        cur_m = current["measurements"][name]
        for field in LATENCY_FIELDS:
            row(f"`{name}` {field[:-2]}", field, prev_m.get(field, 0.0), cur_m.get(field, 0.0))

    row("fleet creation", "createMs", previous.get("createMs", 0.0), current.get("createMs", 0.0))

    cur_obs, prev_obs = current.get("observed") or {}, previous.get("observed") or {}
    for key, label in OBSERVED_GATED.items():
        if key in cur_obs and key in prev_obs:
            row(label, key, prev_obs[key], cur_obs[key])

    return lines, regressions


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--current", required=True)
    ap.add_argument("--previous", default="")
    ap.add_argument("--threshold", type=float, default=1.5,
                    help="fail when a measurement exceeds this multiple of the previous run")
    ap.add_argument("--summary", default="", help="write the rendered markdown here as well as to stdout")
    args = ap.parse_args()

    if args.threshold <= 1.0:
        print("scale-report: --threshold must be greater than 1.0", file=sys.stderr)
        return 2

    with open(args.current, encoding="utf-8") as fh:
        current = json.load(fh)

    previous = None
    lines = []
    if args.previous:
        try:
            with open(args.previous, encoding="utf-8") as fh:
                previous = json.load(fh)
        except (OSError, json.JSONDecodeError) as err:
            lines.append(f"_Previous run could not be read ({err}); treating this run as the baseline._")
            lines.append("")

    failures: list[str] = []

    # Errors are not a trend. Any is a failure, whatever the last run did.
    for name, m in sorted(current.get("measurements", {}).items()):
        if m.get("errors"):
            failures.append(f"{name}: {m['errors']} of {m['n']} requests failed")
    total_n = sum(m.get("n", 0) for m in current.get("measurements", {}).values())
    if total_n == 0:
        failures.append("the run issued no requests at all, so it measured nothing")

    body = render(current, previous)
    lines = body + lines

    if previous is not None:
        if previous.get("schemaVersion") != current.get("schemaVersion"):
            lines.append(
                f"_Previous run used report schema v{previous.get('schemaVersion')} and this one "
                f"v{current.get('schemaVersion')}; the fields may not mean the same thing, so no "
                "comparison was made._"
            )
            lines.append("")
        elif envelope_delta(previous, current):
            lines.append(
                "_Previous run used a different envelope ("
                + ", ".join(envelope_delta(previous, current))
                + "); no comparison was made. A run at a different parallelism, QPS or "
                "strategy mix measures a different thing._"
            )
            lines.append("")
        else:
            lines.append(f"### Against the previous run (threshold {args.threshold:.2f}x)")
            lines.append("")
            rows, regressions = compare(current, previous, args.threshold)
            lines.extend(rows)
            lines.append("")
            failures.extend(regressions)

    if failures:
        lines.append("### :x: This run failed")
        lines.append("")
        for f in failures:
            lines.append(f"- {f}")
        lines.append("")
    else:
        lines.append("### :white_check_mark: No regression")
        lines.append("")

    text = "\n".join(lines) + "\n"
    sys.stdout.write(text)
    if args.summary:
        with open(args.summary, "a", encoding="utf-8") as fh:
            fh.write(text)

    if failures:
        for f in failures:
            print(f"FAIL: {f}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
