#!/usr/bin/env python3
"""Merge cluster-side observations into a scalegen result file.

scalegen measures the run from the client's side: latency and throughput
through the apiserver's conversion path. Two of the numbers a scale run is
supposed to publish are not visible from there —

  * cold start, i.e. how long a webhook-server replica spent compiling
    every assigned plan before it could serve, which is the term that grows
    with the fleet and the one a startupProbe has to be sized against; and
  * the loaded working set, which is what decides whether the envelope
    fits in a container limit at all. Note that this is the steady state
    after the cold start, not the transient peak during it — the peak
    happens before the replicas are Ready, where nothing is sampling, and
    is measured by the -benchmem benchmarks instead.

Both are read off the cluster here and merged into the same JSON, so the
artifact a scheduled run publishes is one file rather than three.

Individual failures are tolerated — one unscrapeable pod should not
discard a twenty-five-minute run — but collecting NOTHING is an error, so
a nightly that lost both measurements cannot report itself green.

Usage:
  hack/scale-observe.py --result FILE --namespace NS [--label SELECTOR]
"""
from __future__ import annotations

import argparse
import json
import subprocess
import sys

WEBHOOK_SELECTOR = "app.kubernetes.io/name=declarative-conversion-webhook-server"
METRICS_PORT = 8443


def kubectl(*args: str) -> str:
    return subprocess.run(
        ["kubectl", *args], check=True, capture_output=True, text=True
    ).stdout


def warn(message: str) -> None:
    print(f"scale-observe: {message}", file=sys.stderr)


def running_pods(namespace: str, selector: str) -> list[tuple[str, str]]:
    """Ready, not-terminating pods.

    Running is not enough. Immediately after a rolling restart — which is
    when this is called, so that the cold start is measured against the
    real fleet — the outgoing replicas are still Running and still
    reporting the metrics from *before* the fleet existed. Counting them
    would inflate the replica count and mix two generations' numbers.
    """
    raw = kubectl(
        "-n", namespace, "get", "pod", "-l", selector,
        "--field-selector=status.phase=Running", "-o", "json",
    )
    out = []
    for p in json.loads(raw).get("items", []):
        if p["metadata"].get("deletionTimestamp"):
            continue
        ready = any(
            c.get("type") == "Ready" and c.get("status") == "True"
            for c in p.get("status", {}).get("conditions", [])
        )
        if ready:
            out.append((p["metadata"]["name"], p["spec"]["nodeName"]))
    return out


def gauge(metrics: str, name: str) -> float | None:
    """Read an unlabelled gauge out of a Prometheus exposition body."""
    for line in metrics.splitlines():
        if line.startswith("#"):
            continue
        parts = line.split(None, 1)
        if len(parts) == 2 and parts[0] == name:
            try:
                return float(parts[1])
            except ValueError:
                return None
    return None


def working_set_bytes(node: str, namespace: str, pod: str) -> float | None:
    """Working set from the kubelet Summary API.

    The same number hack/measure-cache-memory.sh uses, and the same one a
    container memory limit is enforced against — unlike RSS, it excludes
    reclaimable page cache.

    This is a single sample taken once the replicas are Ready again, so it
    is the loaded STEADY state, not the transient peak during the cold
    start: the peak happens before readiness, where nothing is sampling.
    The peak is measured by the -benchmem benchmarks instead, and published
    in docs/operations/capacity.md.

    Matched on namespace as well as name, because a pod name is unique only
    within its namespace and the Summary API reports the whole node.
    """
    summary = json.loads(kubectl("get", "--raw", f"/api/v1/nodes/{node}/proxy/stats/summary"))
    for entry in summary.get("pods", []):
        ref = entry.get("podRef", {})
        if ref.get("name") == pod and ref.get("namespace") == namespace:
            value = entry.get("memory", {}).get("workingSetBytes")
            return float(value) if value is not None else None
    return None


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--result", required=True, help="scalegen --result-json file to merge into")
    ap.add_argument("--namespace", required=True)
    ap.add_argument("--label", default=WEBHOOK_SELECTOR)
    args = ap.parse_args()

    with open(args.result, encoding="utf-8") as fh:
        report = json.load(fh)

    observed: dict[str, float] = {}
    try:
        pods = running_pods(args.namespace, args.label)
    except subprocess.CalledProcessError as err:
        warn(f"could not list webhook-server pods: {err.stderr.strip()}")
        pods = []

    if not pods:
        warn("no running webhook-server pods; skipping cluster-side observations")

    sync_seconds: list[float] = []
    sync_targets: list[float] = []
    peak_bytes: list[float] = []
    for pod, node in pods:
        try:
            metrics = kubectl(
                "get", "--raw",
                f"/api/v1/namespaces/{args.namespace}/pods/{pod}:{METRICS_PORT}/proxy/metrics",
            )
        except subprocess.CalledProcessError as err:
            warn(f"could not scrape {pod}: {err.stderr.strip()}")
        else:
            value = gauge(metrics, "dco_webhook_initial_sync_duration_seconds")
            if value is not None:
                sync_seconds.append(value)
            value = gauge(metrics, "dco_webhook_initial_sync_targets")
            if value is not None:
                sync_targets.append(value)

        try:
            value = working_set_bytes(node, args.namespace, pod)
        except (subprocess.CalledProcessError, json.JSONDecodeError) as err:
            warn(f"could not read the kubelet summary for {pod}: {err}")
        else:
            if value is not None:
                peak_bytes.append(value)

    # The slowest replica is the one that decides the rollout, and the
    # largest is the one that decides the limit, so both are maxima rather
    # than averages.
    if sync_seconds:
        observed["webhookInitialSyncSeconds"] = max(sync_seconds)
    if sync_targets:
        observed["webhookInitialSyncTargets"] = max(sync_targets)
    if peak_bytes:
        observed["webhookWorkingSetBytes"] = max(peak_bytes)
    if pods:
        observed["webhookReplicas"] = float(len(pods))

    # Judged on the two measurement classes, not on `observed` being
    # non-empty: webhookReplicas is there whenever a Ready pod exists, so a
    # run where every metrics scrape and every kubelet read failed would
    # still look successful and publish an artifact with nothing in it for
    # the nightly diff to compare.
    if not sync_seconds and not peak_bytes:
        warn("collected no cluster observations at all; the run's cold-start and working-set numbers are missing")
        return 1

    report["observed"] = {**report.get("observed", {}), **observed}
    with open(args.result, "w", encoding="utf-8") as fh:
        json.dump(report, fh, indent=2)
        fh.write("\n")
    print(f"scale-observe: merged {len(observed)} cluster observations into {args.result}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
