#!/usr/bin/env python3
"""Drive sustained reads and writes of a converted resource, and check them.

Used by hack/e2e-soak.sh while the webhook-server is being rolled. Talks to
`kubectl proxy` rather than the apiserver directly, so it needs no
credentials and still exercises the real admission path: every request here
goes apiserver -> conversion webhook -> back.

Two classes of failure are counted separately, because they are not equally
bad:

  failures    an error the caller can see (HTTP 5xx, a conversion webhook
              error, a connection reset). Loud. Still unacceptable during a
              rolling update, which is the point of the soak.

  mismatches  a HTTP 200 carrying the wrong value. Silent. This is the
              failure mode the whole project exists to prevent, and a soak
              that only counted errors would pass straight through it.

Exit status is always 0: the harness reads the JSON summary and decides.
"""
from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.error
import urllib.request

TIMEOUT = 30


def request(method: str, url: str, body: dict | None = None) -> tuple[int, dict | None, str]:
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
            return resp.status, json.loads(resp.read().decode() or "null"), ""
    except urllib.error.HTTPError as e:
        raw = e.read().decode(errors="replace")
        return e.code, None, raw[:400]
    except Exception as e:  # connection reset, timeout, DNS, ...
        return 0, None, f"{type(e).__name__}: {e}"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", required=True, help="kubectl proxy base URL")
    ap.add_argument("--namespace", default="default")
    ap.add_argument("--duration", type=float, required=True, help="seconds")
    ap.add_argument("--names", required=True, help="comma-separated resource names")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    names = [n for n in args.names.split(",") if n]
    spoke = f"{args.base}/apis/nativecrd.example.org/v1/namespaces/{args.namespace}/gadgets"

    stats = {
        "reads": 0, "writes": 0,
        "read_failures": 0, "write_failures": 0,
        "mismatches": 0,
        "conflicts": 0,
        "samples": [],
    }

    def note(kind: str, detail: str) -> None:
        if len(stats["samples"]) < 25:
            stats["samples"].append({"kind": kind, "detail": detail, "at": time.time()})

    deadline = time.time() + args.duration
    round_no = 0
    while time.time() < deadline:
        round_no += 1
        for name in names:
            # READ at the non-storage version: the apiserver stores v2 and
            # must call the webhook to answer in v1.
            code, obj, err = request("GET", f"{spoke}/{name}")
            stats["reads"] += 1
            if code != 200 or obj is None:
                stats["read_failures"] += 1
                note("read", f"{name}: HTTP {code} {err}")
                continue
            # The seeded value is encoded in the name, so the expectation
            # survives the process restarting and needs no shared state.
            want = name.rsplit("-", 1)[-1]
            got = (obj.get("spec") or {}).get("storageSize")
            if got != want:
                stats["mismatches"] += 1
                note("read-mismatch", f"{name}: storageSize={got!r}, want {want!r}")

            # WRITE at the non-storage version: conversion runs on the way
            # in, too, and a rollout that breaks writes is just as bad.
            obj.setdefault("spec", {})["debugMode"] = (round_no % 2 == 0)
            code, back, err = request("PUT", f"{spoke}/{name}", obj)
            stats["writes"] += 1
            if code == 409:
                # A resourceVersion conflict is ordinary optimistic
                # concurrency, not a webhook failure. Counted so the number
                # is visible, but not fatal.
                stats["conflicts"] = stats.get("conflicts", 0) + 1
                continue
            if code != 200:
                stats["write_failures"] += 1
                note("write", f"{name}: HTTP {code} {err}")
                continue
            if back is not None:
                got = (back.get("spec") or {}).get("storageSize")
                if got != want:
                    stats["mismatches"] += 1
                    note("write-mismatch", f"{name}: storageSize={got!r} after write, want {want!r}")

    with open(args.out, "w") as f:
        json.dump(stats, f, indent=2)
    print(json.dumps({k: v for k, v in stats.items() if k != "samples"}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
