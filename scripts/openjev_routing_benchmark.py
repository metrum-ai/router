#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Sanitized OpenJev routing benchmark against fake OpenJev + labelled prompts.

Optional --live-openai runs a tiny cost sample through real OpenAI chat for the
selected tiers (requires OPENAI_API_KEY). Does not download OpenJev weights.
"""
from __future__ import annotations

import argparse
import json
import os
import statistics
import subprocess
import sys
import time
import urllib.request
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PROMPTS = ROOT / "examples" / "external-routing-policy" / "openjev_prompts.json"


def load_key() -> str | None:
    v = os.environ.get("OPENAI_API_KEY")
    if v:
        return v
    for name in ("ops.env.json", "env.json"):
        path = ROOT / name
        if path.is_file():
            try:
                data = json.loads(path.read_text())
            except json.JSONDecodeError:
                continue
            if data.get("OPENAI_API_KEY"):
                return str(data["OPENAI_API_KEY"])
    return None


def wait_health(url: str, timeout_s: float = 10.0) -> None:
    deadline = time.time() + timeout_s
    last = None
    while time.time() < deadline:
        try:
            urllib.request.urlopen(url + "/health", timeout=0.5).read()
            return
        except Exception as err:  # noqa: BLE001
            last = err
            time.sleep(0.05)
    raise RuntimeError(f"policy not ready: {last}")


def post_json(url: str, body: dict, timeout: float = 10.0) -> dict:
    req = urllib.request.Request(
        url,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read())


TARGETS = [
    {
        "provider": "openai",
        "model": "gpt-5.6-luna",
        "modelRef": "luna",
        "tier": "cheap",
        "weight": 50,
        "inputPricePerMillionUsd": 0.20,
        "outputPricePerMillionUsd": 1.20,
        "keyConfigured": True,
    },
    {
        "provider": "openai",
        "model": "gpt-5.6-sol",
        "modelRef": "sol",
        "tier": "medium",
        "weight": 30,
        "inputPricePerMillionUsd": 4.00,
        "outputPricePerMillionUsd": 20.00,
        "keyConfigured": True,
    },
    {
        "provider": "openai",
        "model": "gpt-6-astra",
        "modelRef": "astra",
        "tier": "advanced",
        "weight": 20,
        "inputPricePerMillionUsd": 10.00,
        "outputPricePerMillionUsd": 50.00,
        "keyConfigured": True,
    },
]
LABEL_TO_IDX = {"simple": 0, "medium": 1, "advanced": 2}


def route_case(policy_url: str, prompt: str) -> dict:
    started = time.perf_counter()
    body = post_json(
        policy_url + "/route",
        {
            "group": "openjev-routing-demo",
            "context": {"textChars": len(prompt), "messageCount": 1},
            "targets": TARGETS,
            "request": {"messages": [{"role": "user", "content": prompt}]},
            "text": prompt,
        },
    )
    body["_latency_ms"] = int((time.perf_counter() - started) * 1000)
    return body


def openai_chat(key: str, model: str, prompt: str) -> dict:
    started = time.perf_counter()
    req = urllib.request.Request(
        "https://api.openai.com/v1/chat/completions",
        data=json.dumps(
            {
                "model": model,
                "messages": [{"role": "user", "content": prompt[:800]}],
                "max_completion_tokens": 64,
            }
        ).encode(),
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {key}",
        },
    )
    with urllib.request.urlopen(req, timeout=180) as resp:
        data = json.loads(resp.read())
    usage = data.get("usage") or {}
    tin = int(usage.get("prompt_tokens") or 0)
    tout = int(usage.get("completion_tokens") or 0)
    prices = {t["model"]: t for t in TARGETS}
    p = prices[model]
    cost = (tin * p["inputPricePerMillionUsd"] + tout * p["outputPricePerMillionUsd"]) / 1e6
    return {
        "model": model,
        "latency_ms": int((time.perf_counter() - started) * 1000),
        "input_tokens": tin,
        "output_tokens": tout,
        "cost_usd": round(cost, 6),
        "finish_reason": (data.get("choices") or [{}])[0].get("finish_reason"),
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", type=Path, default=ROOT / "docs" / "evidence" / "openjev-routing")
    ap.add_argument("--live-openai", action="store_true")
    ap.add_argument("--live-limit", type=int, default=6, help="cases per strategy for live OpenAI")
    args = ap.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)

    fake = ROOT / "examples" / "external-routing-policy" / "fake_openjev.py"
    policy = ROOT / "examples" / "external-routing-policy" / "openjev_policy.py"
    cases = json.loads(PROMPTS.read_text())["cases"]

    procs = [
        subprocess.Popen(
            [sys.executable, str(fake), "--port", "3011"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        ),
        subprocess.Popen(
            [
                sys.executable,
                str(policy),
                "--port",
                "18111",
                "--openjev-url",
                "http://127.0.0.1:3011",
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        ),
    ]
    policy_url = "http://127.0.0.1:18111"
    try:
        wait_health(policy_url)
        rows = []
        for case in cases:
            d = route_case(policy_url, case["prompt"])
            want = LABEL_TO_IDX[case["label"]]
            rows.append(
                {
                    "id": case["id"],
                    "label": case["label"],
                    "predicted_task": d["metadata"]["task"],
                    "target_index": d["targetIndex"],
                    "correct": d["targetIndex"] == want and d["metadata"]["task"] == case["label"],
                    "escalated": d["metadata"]["escalated"],
                    "latency_ms": d["_latency_ms"],
                    "model": TARGETS[d["targetIndex"]]["model"],
                }
            )
        accuracy = sum(1 for r in rows if r["correct"]) / len(rows)
        by_label = {}
        for label in ("simple", "medium", "advanced"):
            subset = [r for r in rows if r["label"] == label]
            by_label[label] = {
                "n": len(subset),
                "accuracy": sum(1 for r in subset if r["correct"]) / len(subset),
            }
        latencies = [r["latency_ms"] for r in rows]
        tier_mix = Counter(r["model"] for r in rows)

        live = None
        key = load_key() if args.live_openai else None
        if args.live_openai and key:
            sample = []
            for label, model in (
                ("simple", "gpt-5.6-luna"),
                ("medium", "gpt-5.6-sol"),
                ("advanced", "gpt-6-astra"),
            ):
                for case in [c for c in cases if c["label"] == label][: max(1, args.live_limit // 3)]:
                    try:
                        sample.append(
                            {
                                "id": case["id"],
                                "label": label,
                                **openai_chat(key, model, case["prompt"]),
                            }
                        )
                    except Exception as err:  # noqa: BLE001
                        sample.append({"id": case["id"], "label": label, "error": type(err).__name__})
            live = {
                "n": len(sample),
                "ok": sum(1 for s in sample if "cost_usd" in s),
                "total_cost_usd": round(sum(s.get("cost_usd", 0) for s in sample), 6),
                "by_model": {},
            }
            for model in ("gpt-5.6-luna", "gpt-5.6-sol", "gpt-6-astra"):
                ms = [s for s in sample if s.get("model") == model]
                live["by_model"][model] = {
                    "n": len(ms),
                    "avg_latency_ms": (
                        int(statistics.mean(s["latency_ms"] for s in ms)) if ms else None
                    ),
                    "cost_usd": round(sum(s.get("cost_usd", 0) for s in ms), 6),
                }
            # Do not commit raw sample prompts/responses.
            (args.out / "live-openai-sample.local.json").write_text(
                json.dumps({"note": "local only", "sample": sample}, indent=2)
            )

        commit = (
            subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT).decode().strip()
        )
        evidence = {
            "demo": "openjev-task-class-routing",
            "generated_at": datetime.now(timezone.utc).isoformat(),
            "git_commit": commit,
            "openjev": {
                "mode": "fake_systemone_shim",
                "note": (
                    "Sanitized wiring + labelled-prompt accuracy against deterministic "
                    "fake OpenJev. Real weights are CC BY-NC 4.0; serve via Shadeform "
                    "runbook scripts/openjev_shadeform.py."
                ),
                "license": "CC BY-NC 4.0 (weights); Apache-2.0 (this example code)",
            },
            "models": {
                "cheap": {"id": "gpt-5.6-luna", "input": 0.20, "output": 1.20},
                "medium": {"id": "gpt-5.6-sol", "input": 4.00, "output": 20.00},
                "advanced": {"id": "gpt-6-astra", "input": 10.00, "output": 50.00},
                "selection_note": (
                    "Live OpenAI key listed gpt-6-astra but not gpt-6-luna/sol; "
                    "used gpt-5.6-luna and gpt-5.6-sol as cheap/mid substitutes."
                ),
            },
            "shadeform": {
                "preferred_sku": "RTXPro6000",
                "preferred_cloud": "massedcompute",
                "note": "Discovered available at plan time; serve OpenJev on localhost only.",
            },
            "results": {
                "n": len(rows),
                "routing_accuracy": round(accuracy, 4),
                "by_label": by_label,
                "tier_mix": dict(tier_mix),
                "decision_latency_ms": {
                    "p50": int(statistics.median(latencies)),
                    "mean": int(statistics.mean(latencies)),
                    "max": max(latencies),
                },
                "escalated_count": sum(1 for r in rows if r["escalated"]),
            },
            "live_openai": live,
            "limitations": [
                "Fake OpenJev classifier is keyword-based; not a claim about openjev/openjev weights.",
                "No cherry-picked quality claims; accuracy is against human labels for this fixture.",
                "Commercial marketing use of OpenJev weights requires author permission (CC BY-NC).",
            ],
        }
        (args.out / "benchmark.json").write_text(json.dumps(evidence, indent=2) + "\n")
        md = [
            "# OpenJev routing evidence",
            "",
            f"Generated: `{evidence['generated_at']}`",
            f"Commit: `{commit}`",
            "",
            "## Ladder (live key)",
            "",
            "| tier | model | $/1M in | $/1M out |",
            "|---|---|---:|---:|",
            "| cheap | gpt-5.6-luna | 0.20 | 1.20 |",
            "| medium | gpt-5.6-sol | 4.00 | 20.00 |",
            "| advanced | gpt-6-astra | 10.00 | 50.00 |",
            "",
            "## Synthetic routing (fake OpenJev)",
            "",
            f"- cases: **{len(rows)}**",
            f"- label accuracy: **{accuracy:.1%}**",
            f"- decision latency p50: **{evidence['results']['decision_latency_ms']['p50']} ms**",
            f"- tier mix: `{dict(tier_mix)}`",
            "",
            "This is wiring evidence with a deterministic shim. For GPU OpenJev, use",
            "`scripts/openjev_shadeform.py` and point `OPENJEV_URL` at the tunneled shim.",
            "",
            "## License",
            "",
            "OpenJev weights are CC BY-NC 4.0. This repository example code is Apache-2.0.",
            "",
        ]
        if live:
            md.extend(
                [
                    "## Live OpenAI cost sample",
                    "",
                    f"- requests ok: {live['ok']}/{live['n']}",
                    f"- total cost USD: **{live['total_cost_usd']}**",
                    f"- by model: `{live['by_model']}`",
                    "",
                ]
            )
        (args.out / "README.md").write_text("\n".join(md) + "\n")
        print(json.dumps({"ok": True, "accuracy": accuracy, "out": str(args.out)}, indent=2))
        return 0
    finally:
        for p in procs:
            p.terminate()
        for p in procs:
            try:
                p.wait(timeout=2)
            except subprocess.TimeoutExpired:
                p.kill()


if __name__ == "__main__":
    raise SystemExit(main())
