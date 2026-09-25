#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Live OpenJev routing evaluation against a real System One shim.

Requires OPENJEV_URL (tunneled shim). Optionally OPENAI_API_KEY for upstream
cost/latency baselines. Writes sanitized aggregates plus a local raw journal
under --out (gitignored *.local.json).
"""
from __future__ import annotations

import argparse
import json
import os
import statistics
import subprocess
import sys
import time
import urllib.error
import urllib.request
from collections import Counter, defaultdict
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PROMPTS = ROOT / "examples" / "external-routing-policy" / "openjev_prompts.json"
POLICY = ROOT / "examples" / "external-routing-policy" / "openjev_policy.py"

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
IDX_TO_LABEL = {0: "simple", 1: "medium", 2: "advanced"}


def load_openai_key() -> str | None:
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


def post_json(url: str, body: dict, *, headers: dict | None = None, timeout: float = 60.0) -> dict:
    req = urllib.request.Request(
        url,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", **(headers or {})},
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read())


def wait_health(url: str, timeout_s: float = 30.0) -> None:
    deadline = time.time() + timeout_s
    last = None
    while time.time() < deadline:
        try:
            urllib.request.urlopen(url + "/health", timeout=1).read()
            return
        except Exception as err:  # noqa: BLE001
            last = err
            time.sleep(0.2)
    raise RuntimeError(f"policy not ready: {last}")


def probe_openjev(base: str, timeout_s: float = 120.0) -> dict:
    """One warm-up System One call; returns latency and parsed choice."""
    body = {
        "model": "openjev",
        "state": "Summarize this sentence: the launch succeeded.",
        "questions": {
            "task": {
                "type": "choice",
                "instructions": "simple, medium, or advanced?",
                "criteria": {"simple": None, "medium": None, "advanced": None},
            }
        },
    }
    started = time.perf_counter()
    resp = post_json(base.rstrip("/") + "/v1/systemone", body, timeout=timeout_s)
    return {"latency_ms": int((time.perf_counter() - started) * 1000), "raw_keys": sorted(resp.keys()), "sample": resp}


def route(policy_url: str, prompt: str, timeout: float = 60.0) -> dict:
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
        timeout=timeout,
    )
    body["_e2e_ms"] = int((time.perf_counter() - started) * 1000)
    return body


def openai_chat(key: str, model: str, prompt: str) -> dict:
    started = time.perf_counter()
    req = urllib.request.Request(
        "https://api.openai.com/v1/chat/completions",
        data=json.dumps(
            {
                "model": model,
                "messages": [{"role": "user", "content": prompt[:1200]}],
                "max_completion_tokens": 96,
            }
        ).encode(),
        headers={"Content-Type": "application/json", "Authorization": f"Bearer {key}"},
    )
    with urllib.request.urlopen(req, timeout=180) as resp:
        data = json.loads(resp.read())
    usage = data.get("usage") or {}
    tin = int(usage.get("prompt_tokens") or 0)
    tout = int(usage.get("completion_tokens") or 0)
    price = next(t for t in TARGETS if t["model"] == model)
    cost = (tin * price["inputPricePerMillionUsd"] + tout * price["outputPricePerMillionUsd"]) / 1e6
    text = ((data.get("choices") or [{}])[0].get("message") or {}).get("content") or ""
    return {
        "model": model,
        "latency_ms": int((time.perf_counter() - started) * 1000),
        "input_tokens": tin,
        "output_tokens": tout,
        "cost_usd": round(cost, 6),
        "chars_out": len(text),
        "finish_reason": ((data.get("choices") or [{}])[0].get("finish_reason")),
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--openjev-url", default=os.environ.get("OPENJEV_URL", "http://127.0.0.1:3000"))
    ap.add_argument("--policy-port", type=int, default=18093)
    ap.add_argument("--out", type=Path, default=ROOT / "docs" / "evidence" / "openjev-routing")
    ap.add_argument("--limit", type=int, default=0, help="0 = all prompts")
    ap.add_argument("--live-openai", action="store_true")
    ap.add_argument("--openai-per-tier", type=int, default=5)
    ap.add_argument("--shuffle-repeat", type=int, default=1, help="repeat route N times (stability)")
    ap.add_argument("--hardware-note", default="")
    args = ap.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)

    # Warm OpenJev
    try:
        warm = probe_openjev(args.openjev_url)
    except Exception as err:  # noqa: BLE001
        print(f"openjev probe failed at {args.openjev_url}: {err}", file=sys.stderr)
        return 1

    proc = subprocess.Popen(
        [
            sys.executable,
            str(POLICY),
            "--host",
            "127.0.0.1",
            "--port",
            str(args.policy_port),
            "--openjev-url",
            args.openjev_url,
            "--timeout-s",
            "90",
        ],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    policy_url = f"http://127.0.0.1:{args.policy_port}"
    journal: list[dict] = []
    try:
        wait_health(policy_url, timeout_s=30)
        cases = json.loads(PROMPTS.read_text())["cases"]
        if args.limit:
            # keep balance across labels
            by = defaultdict(list)
            for c in cases:
                by[c["label"]].append(c)
            per = max(1, args.limit // 3)
            cases = []
            for label in ("simple", "medium", "advanced"):
                cases.extend(by[label][:per])

        routed = []
        flips = 0
        for case in cases:
            decisions = []
            for _ in range(max(1, args.shuffle_repeat)):
                d = route(policy_url, case["prompt"])
                decisions.append(d)
                journal.append(
                    {
                        "id": case["id"],
                        "label": case["label"],
                        "targetIndex": d.get("targetIndex"),
                        "classLabel": d.get("classLabel"),
                        "metadata": d.get("metadata"),
                        "e2e_ms": d.get("_e2e_ms"),
                    }
                )
            primary = decisions[0]
            if any(x.get("targetIndex") != primary.get("targetIndex") for x in decisions[1:]):
                flips += 1
            want = LABEL_TO_IDX[case["label"]]
            routed.append(
                {
                    "id": case["id"],
                    "label": case["label"],
                    "predicted": primary["metadata"]["task"],
                    "target_index": primary["targetIndex"],
                    "model": TARGETS[primary["targetIndex"]]["model"],
                    "correct": primary["targetIndex"] == want,
                    "escalated": primary["metadata"].get("escalated"),
                    "confidenceBand": primary["metadata"].get("confidenceBand"),
                    "openjevLatencyMs": primary["metadata"].get("openjevLatencyMs"),
                    "e2e_ms": primary["_e2e_ms"],
                }
            )

        accuracy = sum(1 for r in routed if r["correct"]) / len(routed)
        by_label = {}
        for label in ("simple", "medium", "advanced"):
            subset = [r for r in routed if r["label"] == label]
            by_label[label] = {
                "n": len(subset),
                "accuracy": (sum(1 for r in subset if r["correct"]) / len(subset)) if subset else None,
            }

        # Baselines: always fixed tier cost estimate using live OpenAI samples
        live = None
        key = load_openai_key() if args.live_openai else None
        if key:
            samples = []
            for label, model in (
                ("simple", "gpt-5.6-luna"),
                ("medium", "gpt-5.6-sol"),
                ("advanced", "gpt-6-astra"),
            ):
                for case in [c for c in cases if c["label"] == label][: args.openai_per_tier]:
                    try:
                        samples.append({"id": case["id"], "label": label, **openai_chat(key, model, case["prompt"])})
                    except Exception as err:  # noqa: BLE001
                        samples.append({"id": case["id"], "label": label, "model": model, "error": type(err).__name__})
            # Routed cost: for each routed case, use mean cost of that model from samples when available
            cost_by_model = defaultdict(list)
            for s in samples:
                if "cost_usd" in s:
                    cost_by_model[s["model"]].append(s["cost_usd"])
            mean_cost = {m: statistics.mean(v) for m, v in cost_by_model.items() if v}
            routed_cost = sum(mean_cost.get(r["model"], 0.0) for r in routed)
            always = {
                "always_cheap": mean_cost.get("gpt-5.6-luna", 0.0) * len(routed),
                "always_medium": mean_cost.get("gpt-5.6-sol", 0.0) * len(routed),
                "always_advanced": mean_cost.get("gpt-6-astra", 0.0) * len(routed),
                "openjev_routed_est": routed_cost,
            }
            live = {
                "n": len(samples),
                "ok": sum(1 for s in samples if "cost_usd" in s),
                "total_cost_usd": round(sum(s.get("cost_usd", 0) for s in samples), 6),
                "by_model": {
                    m: {
                        "n": len([s for s in samples if s.get("model") == m and "cost_usd" in s]),
                        "avg_latency_ms": int(
                            statistics.mean(s["latency_ms"] for s in samples if s.get("model") == m and "latency_ms" in s)
                        )
                        if any("latency_ms" in s and s.get("model") == m for s in samples)
                        else None,
                        "cost_usd": round(sum(s.get("cost_usd", 0) for s in samples if s.get("model") == m), 6),
                    }
                    for m in ("gpt-5.6-luna", "gpt-5.6-sol", "gpt-6-astra")
                },
                "projected_full_set_cost_usd": {k: round(v, 6) for k, v in always.items()},
            }
            (args.out / "live-openai-sample.local.json").write_text(
                json.dumps({"sample": samples}, indent=2)
            )

        oj_lat = [r["openjevLatencyMs"] for r in routed if r.get("openjevLatencyMs") is not None]
        e2e = [r["e2e_ms"] for r in routed]
        commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        evidence = {
            "demo": "openjev-task-class-routing-live",
            "generated_at": datetime.now(timezone.utc).isoformat(),
            "git_commit": commit,
            "openjev": {
                "mode": "real_systemone_shim",
                "url_host": "127.0.0.1:3000_via_ssh_tunnel",
                "warmup_latency_ms": warm["latency_ms"],
                "license": "CC BY-NC 4.0 (weights); Apache-2.0 (example code)",
            },
            "hardware": {
                "note": args.hardware_note
                or "Shadeform massedcompute RTXPro6000 (NVIDIA RTX PRO 6000 Blackwell Server Edition, 96GB)",
                "sku": "RTXPro6000",
                "cloud": "massedcompute",
            },
            "models": {
                "cheap": {"id": "gpt-5.6-luna", "input": 0.20, "output": 1.20},
                "medium": {"id": "gpt-5.6-sol", "input": 4.00, "output": 20.00},
                "advanced": {"id": "gpt-6-astra", "input": 10.00, "output": 50.00},
            },
            "results": {
                "n": len(routed),
                "routing_accuracy": round(accuracy, 4),
                "by_label": by_label,
                "tier_mix": dict(Counter(r["model"] for r in routed)),
                "openjev_latency_ms": {
                    "p50": int(statistics.median(oj_lat)) if oj_lat else None,
                    "mean": int(statistics.mean(oj_lat)) if oj_lat else None,
                    "max": max(oj_lat) if oj_lat else None,
                },
                "policy_e2e_ms": {
                    "p50": int(statistics.median(e2e)),
                    "mean": int(statistics.mean(e2e)),
                    "max": max(e2e),
                },
                "escalated_count": sum(1 for r in routed if r["escalated"]),
                "option_repeat_flips": flips,
                "shuffle_repeat": args.shuffle_repeat,
            },
            "live_openai": live,
            "limitations": [
                "Quality is routing-label accuracy vs human fixture labels, not answer grading.",
                "OpenJev weights CC BY-NC 4.0; commercial marketing needs author permission.",
                "Projected always-* costs extrapolate mean per-model sample cost across the prompt set.",
            ],
            "observed_trace_examples": routed[:6],
        }
        (args.out / "benchmark.live.json").write_text(json.dumps(evidence, indent=2) + "\n")
        (args.out / "routing-journal.local.json").write_text(json.dumps(journal, indent=2))
        md = [
            "# OpenJev live routing evidence",
            "",
            f"Generated: `{evidence['generated_at']}`",
            f"Commit: `{commit}`",
            f"Hardware: `{evidence['hardware']['note']}`",
            "",
            "## Routing (real OpenJev)",
            "",
            f"- cases: **{len(routed)}**",
            f"- label accuracy: **{accuracy:.1%}**",
            f"- OpenJev latency p50: **{evidence['results']['openjev_latency_ms']['p50']} ms**",
            f"- policy e2e p50: **{evidence['results']['policy_e2e_ms']['p50']} ms**",
            f"- tier mix: `{dict(Counter(r['model'] for r in routed))}`",
            f"- repeat flips: **{flips}** / {len(cases)} (repeat={args.shuffle_repeat})",
            "",
        ]
        if live and live.get("projected_full_set_cost_usd"):
            md.extend(
                [
                    "## Cost projection (from live OpenAI samples)",
                    "",
                    f"- sample total USD: **{live['total_cost_usd']}** ({live['ok']}/{live['n']} ok)",
                    f"- projected: `{live['projected_full_set_cost_usd']}`",
                    "",
                ]
            )
        (args.out / "README.live.md").write_text("\n".join(md) + "\n")
        print(json.dumps({"ok": True, "accuracy": accuracy, "out": str(args.out)}, indent=2))
        return 0
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except urllib.error.URLError as err:
        print(f"live eval failed: {err}", file=sys.stderr)
        raise SystemExit(1)
