#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""f.5 tunnel-kill drill for the d3 external policy (issue #278), Metrum AI Router.

Sends seed prompts through the router group while the SSH tunnel to d3 is up,
kills the tunnel process mid-run, and keeps sending. For every request it reads
the usage DB: HTTP status, policy outcome and error class, served model and the
number of upstream attempts. Output is scalar JSON with seed IDs only.

Copyright 2026 Metrum AI.
"""
from __future__ import annotations

import argparse
import json
import os
import signal
import sqlite3
import sys
import time
import urllib.error
import urllib.request
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parents[5]
SEED = ROOT / "docs" / "evidence" / "d3" / "seed_prompts.json"


def send(base: str, token: str, group: str, prompt: str) -> tuple[int, str, float]:
    body = {"model": group, "messages": [{"role": "user", "content": prompt}], "max_tokens": 16}
    req = urllib.request.Request(base.rstrip("/") + "/chat/completions", json.dumps(body).encode(),
                                 {"Content-Type": "application/json", "Authorization": "Bearer " + token})
    op = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    t0 = time.perf_counter()
    try:
        with op.open(req, timeout=120) as resp:
            resp.read()
            return resp.status, resp.headers.get("X-Request-Id", ""), (time.perf_counter() - t0) * 1000
    except urllib.error.HTTPError as err:
        rid = err.headers.get("X-Request-Id", "")
        body_err = err.read()
        err.close()
        kind = ""
        try:
            kind = json.loads(body_err).get("error", {}).get("type", "") if body_err else ""
        except (ValueError, AttributeError):
            kind = ""
        return err.code, rid + ("|" + kind if kind else ""), (time.perf_counter() - t0) * 1000


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--base-url", required=True)
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--usage-db", required=True)
    ap.add_argument("--group", default="d3-auto")
    ap.add_argument("--tunnel-pid", type=int, required=True)
    ap.add_argument("--on-error", required=True, choices=("fail_closed", "fallback"))
    ap.add_argument("--before", type=int, default=4)
    ap.add_argument("--after", type=int, default=6)
    ap.add_argument("--target0-model", default="accounts/fireworks/models/deepseek-v4p1-flash")
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()
    token = Path(args.token_file).read_text(encoding="utf-8").strip()
    prompts = json.loads(SEED.read_text(encoding="utf-8"))["prompts"]
    hard = [p for p in prompts if p["difficulty"] == "hard"][: args.before + args.after]
    sent = []
    for i, item in enumerate(hard):
        phase = "tunnel_up" if i < args.before else "tunnel_killed"
        if i == args.before:
            os.kill(args.tunnel_pid, signal.SIGTERM)
            time.sleep(1)
        status, rid, ms = send(args.base_url, token, args.group, item["prompt"])
        error_type = rid.split("|", 1)[1] if "|" in rid else ""
        sent.append({"id": item["id"], "phase": phase, "status": status, "error_type": error_type,
                     "request_id": rid.split("|", 1)[0], "e2e_ms": round(ms, 1)})
    time.sleep(2)
    conn = sqlite3.connect(f"file:{args.usage_db}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    for row in sent:
        rid = row.pop("request_id")
        pol = conn.execute("SELECT outcome, error_class, class_label, duration_ms FROM request_policy_executions "
                           "WHERE request_id = ? ORDER BY seq DESC LIMIT 1", (rid,)).fetchone()
        use = conn.execute("SELECT status, target_model FROM request_usage WHERE request_id = ?", (rid,)).fetchone()
        att = conn.execute("SELECT COUNT(*) FROM request_attempts WHERE request_id = ?", (rid,)).fetchone()[0]
        row.update({"policy_outcome": pol["outcome"] if pol else None,
                    "policy_error_class": pol["error_class"] if pol else None,
                    "class_label": pol["class_label"] if pol else None,
                    "policy_ms": pol["duration_ms"] if pol else None,
                    "usage_status": use["status"] if use else None,
                    "served_model": (use["target_model"] or None) if use else None,
                    "upstream_attempts": att})
    killed = [r for r in sent if r["phase"] == "tunnel_killed"]
    up = [r for r in sent if r["phase"] == "tunnel_up"]
    if args.on_error == "fail_closed":
        passed = all(r["status"] == 502 and r["error_type"] == "routing-policy-error"
                     and r["upstream_attempts"] == 0 for r in killed)
        expectation = "every request after the kill: HTTP 502 routing-policy-error, 0 upstream attempts"
    else:
        passed = all(r["status"] == 200 and r["served_model"] == args.target0_model for r in killed)
        expectation = "every request after the kill: HTTP 200 served by target 0 (" + args.target0_model + ")"
    result = {
        "schema": "metrum.d3-live.tunnel-drill.v1", "on_error": args.on_error, "expectation": expectation,
        "passed": passed and all(r["status"] == 200 for r in up),
        "tunnel_up": {"n": len(up), "status": dict(Counter(r["status"] for r in up)),
                      "policy_outcomes": dict(Counter(r["policy_outcome"] for r in up))},
        "tunnel_killed": {"n": len(killed), "status": dict(Counter(r["status"] for r in killed)),
                          "error_types": dict(Counter(r["error_type"] for r in killed)),
                          "policy_outcomes": dict(Counter(r["policy_outcome"] for r in killed)),
                          "served_models": dict(Counter(r["served_model"] for r in killed)),
                          "upstream_attempts": dict(Counter(r["upstream_attempts"] for r in killed))},
        "rows": sent,
    }
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, indent=1) + "\n", encoding="utf-8")
    print(json.dumps({k: v for k, v in result.items() if k != "rows"}, indent=1))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
