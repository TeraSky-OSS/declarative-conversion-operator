#!/usr/bin/env python3
"""POST synthetic ConversionReview batches and print latency/throughput stats.

Used by hack/e2e-load.sh against a live webhook-server. Not imported by Go.
"""
from __future__ import annotations

import argparse
import json
import ssl
import statistics
import sys
import time
import urllib.error
import urllib.request


def review_body(n_objects: int, pad: int) -> bytes:
    pad_s = "x" * pad
    objects = []
    for i in range(n_objects):
        objects.append(
            {
                "apiVersion": "nativecrd.example.org/v1",
                "kind": "Gadget",
                "metadata": {
                    "name": f"load-{i}",
                    "namespace": "default",
                    "annotations": {"load.e2e/pad": pad_s} if pad else {},
                },
                "spec": {"storageSize": "50", "debugMode": False},
            }
        )
    review = {
        "apiVersion": "apiextensions.k8s.io/v1",
        "kind": "ConversionReview",
        "request": {
            "uid": "load-test",
            "desiredAPIVersion": "nativecrd.example.org/v2",
            "objects": objects,
        },
    }
    return json.dumps(review).encode()


def post(url: str, body: bytes, ctx: ssl.SSLContext) -> tuple[int, dict]:
    req = urllib.request.Request(url, data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=30) as resp:
            raw = resp.read()
            return resp.status, json.loads(raw.decode())
    except urllib.error.HTTPError as e:
        return e.code, {"error": e.read().decode(errors="replace")}


def percentile(sorted_ms: list[float], p: float) -> float:
    if not sorted_ms:
        return 0.0
    k = (len(sorted_ms) - 1) * p
    lo = int(k)
    hi = min(lo + 1, len(sorted_ms) - 1)
    frac = k - lo
    return sorted_ms[lo] * (1 - frac) + sorted_ms[hi] * frac


def run_case(url: str, n_objects: int, pad: int, iters: int, warmup: int, ctx: ssl.SSLContext) -> dict:
    body = review_body(n_objects, pad)
    for _ in range(warmup):
        post(url, body, ctx)
    samples_ms: list[float] = []
    errors = 0
    for _ in range(iters):
        t0 = time.perf_counter()
        status, got = post(url, body, ctx)
        dt = (time.perf_counter() - t0) * 1000.0
        samples_ms.append(dt)
        result = ""
        if isinstance(got, dict):
            result = ((got.get("response") or {}).get("result") or {}).get("status") or ""
        if status != 200 or result != "Success":
            errors += 1
    samples_ms.sort()
    total_s = sum(samples_ms) / 1000.0
    return {
        "objects": n_objects,
        "pad_bytes": pad,
        "iters": iters,
        "errors": errors,
        "error_rate": errors / iters if iters else 0.0,
        "p50_ms": percentile(samples_ms, 0.50),
        "p99_ms": percentile(samples_ms, 0.99),
        "avg_ms": statistics.mean(samples_ms) if samples_ms else 0.0,
        "throughput_rps": iters / total_s if total_s else 0.0,
        "obj_per_sec": (iters * n_objects) / total_s if total_s else 0.0,
    }


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--url", required=True)
    p.add_argument("--warmup", type=int, default=3)
    args = p.parse_args()
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    cases = [
        (1, 0, 30),
        (10, 0, 30),
        (50, 0, 20),
        (10, 8192, 20),
    ]
    rows = [run_case(args.url, n, pad, iters, args.warmup, ctx) for n, pad, iters in cases]
    print("objects\tpad_bytes\titers\terrors\terror_rate\tp50_ms\tp99_ms\tavg_ms\tthroughput_rps\tobj_per_sec")
    for r in rows:
        print(
            f"{r['objects']}\t{r['pad_bytes']}\t{r['iters']}\t{r['errors']}\t"
            f"{r['error_rate']:.3f}\t{r['p50_ms']:.2f}\t{r['p99_ms']:.2f}\t{r['avg_ms']:.2f}\t"
            f"{r['throughput_rps']:.1f}\t{r['obj_per_sec']:.1f}"
        )
    if any(r["errors"] for r in rows):
        print("FAIL: at least one ConversionReview returned an error", file=sys.stderr)
        return 1
    print("OK: all ConversionReview batches succeeded")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
