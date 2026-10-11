#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Co-located d3 policy latency for Metrum AI Router (issue #287, item 1).

Runs on the GPU host next to a d3 server that listens on loopback. It starts
a loopback mock upstream (from scripts/run_d3_policy_e2e.py), the d3 adapter
(examples/external-routing-policy/d3_policy.py) and a router built from this
tree, all on 127.0.0.1, then sends the 204 seed prompts through one enforce
group. It reports the router's policy `duration_ms` (request_policy_executions),
the adapter's d3 round trip (`d3Ms` from its scalar log) and end-to-end client
latency. No provider keys are used: both tiers are the mock upstream.

    python3 colocated_policy_latency.py --repo <tree> --router-binary <bin> \
        --d3-url http://127.0.0.1:8000 --d3-key-file ~/d3.key --d3-model d3 --out out.json

Output is scalar JSON only (seed IDs, labels, tiers, timings).

Copyright 2026 Metrum AI.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import os
import secrets
import hashlib
import sys
import tempfile
import time
from contextlib import ExitStack
from pathlib import Path
from typing import Any


def load(path: Path, name: str) -> Any:
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--repo", type=Path, required=True)
    ap.add_argument("--router-binary", type=Path, required=True)
    ap.add_argument("--d3-url", default="http://127.0.0.1:8000")
    ap.add_argument("--d3-key-file", type=Path, required=True)
    ap.add_argument("--d3-model", default="d3")
    ap.add_argument("--d3-revision", default="")
    ap.add_argument("--policy-timeout-ms", type=int, default=1500)
    ap.add_argument("--adapter-timeout-ms", type=int, default=1000)
    ap.add_argument("--warmup", type=int, default=3)
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()

    e2e = load(args.repo / "scripts" / "run_d3_policy_e2e.py", "run_d3_policy_e2e")
    ev = load(args.repo / "scripts" / "d3_live_eval.py", "d3_live_eval")
    prompts = json.loads((args.repo / "docs/evidence/d3/seed_prompts.json").read_text(encoding="utf-8"))["prompts"]
    d3_key = args.d3_key_file.read_text(encoding="utf-8").strip()

    with tempfile.TemporaryDirectory(prefix="metrum-d3-colo-") as temp_name, ExitStack() as stack:
        temp = Path(temp_name)
        upstream = e2e.upstream_server(stack)
        env = {k: os.environ[k] for k in ("PATH", "HOME", "TMPDIR", "LD_LIBRARY_PATH") if k in os.environ}
        env.update({"PYTHONUNBUFFERED": "1", "D3_COLO_KEY": d3_key})
        adapter_port = e2e.free_port()
        adapter_log = temp / "adapter.log"
        cmd = [sys.executable, str(args.repo / "examples/external-routing-policy/d3_policy.py"),
               "--port", str(adapter_port), "--url", args.d3_url, "--timeout-ms", str(args.adapter_timeout_ms),
               "--api-key-env", "D3_COLO_KEY", "--model", args.d3_model]
        if args.d3_revision:
            cmd += ["--revision", args.d3_revision]
        adapter = e2e.start(stack, cmd, adapter_log, env)
        e2e.ready(f"http://127.0.0.1:{adapter_port}/health", adapter, "adapter")

        token = secrets.token_urlsafe(32)
        provider_key = secrets.token_urlsafe(16)
        router_port = e2e.free_port()
        db = temp / "usage.sqlite"
        base_url = f"http://127.0.0.1:{upstream.server_port}/v1"
        config = {
            "server": {
                "listen": f"127.0.0.1:{router_port}", "default_model_group": "d3-colo",
                "logging": {"path": str(temp / "requests.jsonl")}, "cache": {"enabled": False},
                "decision_telemetry": {"enabled": True}, "upstream": {"timeout_ms": 5000},
                "identifiers": {"mode": "passthrough"},
                "usage_db": {"enabled": True, "driver": "sqlite", "path": str(db), "migration_policy": "auto-safe"},
            },
            "state_path": str(temp / "state.json"),
            "providers": {
                "cheapco": {"base_url": base_url, "dialect": "openai-chat", "api_key": provider_key},
                "strongco": {"base_url": base_url, "dialect": "openai-chat", "api_key": provider_key},
            },
            "models": {"d3-colo": {
                "strategy": "external",
                "targets": [{"provider": "cheapco", "model": "mock-cheap-1", "tier": "cheap"},
                            {"provider": "strongco", "model": "mock-strong-1", "tier": "strong"}],
                "external_policy": {
                    "url": f"http://127.0.0.1:{adapter_port}/route", "allow_hosts": ["127.0.0.1"],
                    "timeout_ms": args.policy_timeout_ms, "max_response_bytes": 65536,
                    "include_request": True, "mode": "enforce", "on_error": "fail_closed"}}},
            "callers": [{"id": "d3-colo-caller", "user": "test-user", "project": "test-project",
                         "environment": "test", "status": "active",
                         "token_sha256": hashlib.sha256(token.encode()).hexdigest(),
                         "token_id": "rtr_metrum_d3_colo_test", "allow": ["d3-colo"],
                         "rate": {"rpm": 100000, "tpm": 100000000, "concurrent": 8}}],
        }
        cfg_path = temp / "router.json"
        cfg_path.write_text(json.dumps(config), encoding="utf-8")
        cfg_path.chmod(0o600)
        router = e2e.start(stack, [str(args.router_binary), "-config", str(cfg_path)], temp / "router.log", env)
        base = f"http://127.0.0.1:{router_port}"
        e2e.ready(base + "/readyz", router, "router")
        headers = {"Authorization": "Bearer " + token}

        for _ in range(args.warmup):
            e2e.http(base + "/v1/chat/completions", {"model": "d3-colo", "max_tokens": 8,
                                                     "messages": [{"role": "user", "content": "warmup"}]}, headers)
        rows: list[dict[str, Any]] = []
        for item in prompts:
            t0 = time.perf_counter()
            status, _, rid = e2e.http(base + "/v1/chat/completions", {
                "model": "d3-colo", "max_tokens": 8, "messages": [{"role": "user", "content": item["prompt"]}]},
                headers, timeout=30)
            e2e_ms = (time.perf_counter() - t0) * 1000
            pol = e2e.policy_row(db, rid) if status == 200 else {}
            rows.append({"id": item["id"], "difficulty_label": item["difficulty"], "status": status,
                         "e2e_ms": round(e2e_ms, 2), "policy_ms": pol.get("duration_ms"),
                         "class_label": pol.get("class_label"), "label_tier": ev.label_tier(pol.get("class_label")),
                         "served_model": e2e.served_model(db, rid) if status == 200 else ""})
        d3_ms = []
        for line in adapter_log.read_text(errors="replace").splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if event.get("event") == "route" and isinstance(event.get("d3Ms"), (int, float)):
                d3_ms.append(float(event["d3Ms"]))
        d3_ms = d3_ms[args.warmup:]

    policy = [float(r["policy_ms"]) for r in rows if r["policy_ms"] is not None]
    strong = [r for r in rows if r["served_model"] == "mock-strong-1"]
    hard = [r for r in rows if r["difficulty_label"] == "hard"]
    easy = [r for r in rows if r["difficulty_label"] == "easy"]
    result = {
        "schema": "metrum.d3-live.colocated.v1", "d3_model": args.d3_model, "d3_revision": args.d3_revision,
        "prompts": len(rows), "http_status": {str(s): sum(1 for r in rows if r["status"] == s)
                                              for s in sorted({r["status"] for r in rows})},
        "policy_duration_ms": ev.latency_summary(policy),
        "adapter_d3_ms": ev.latency_summary(d3_ms),
        "e2e_ms_mock_upstream": ev.latency_summary([r["e2e_ms"] for r in rows]),
        "strong_share": round(len(strong) / max(1, len(rows)), 3),
        "hard_to_strong": f"{sum(1 for r in hard if r in strong)}/{len(hard)}",
        "easy_to_cheap": f"{sum(1 for r in easy if r not in strong)}/{len(easy)}",
        "note": "router, adapter, d3 and a mock upstream on one host over 127.0.0.1; policy_duration_ms is the "
                "router's request_policy_executions.duration_ms",
        "rows": rows,
    }
    args.out.write_text(json.dumps(result, indent=1) + "\n", encoding="utf-8")
    print(json.dumps({k: v for k, v in result.items() if k != "rows"}, indent=1))
    return 0


if __name__ == "__main__":
    sys.exit(main())
