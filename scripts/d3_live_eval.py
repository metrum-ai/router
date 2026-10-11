#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Live evidence runner for the d3 external routing policy (issue #278).

Subcommands (all read the fixed seed set docs/evidence/d3/seed_prompts.json):

  direct  POST every seed prompt straight to a d3 System One server with the
          adapter's questions. Records client latency, the server-timing
          header, and answer distributions per labelled cell (f.1).
  router  Send every seed prompt through a router group, then read the usage
          DB: policy outcome, class label, policy duration, served target,
          shadow recommendation, tier split and upstream cost (f.2, f.3).
          --mode-label always-strong / always-cheap runs the same prompts
          through a single-target group (local_pi_instance.py
          --d3-tier-groups) as a measured cost baseline (#287).
  compare Read an enforce run and the always-strong / always-cheap runs and
          report cost per run and per request, the saving against
          always-strong, the extra cost against always-cheap, the strong share
          and latency (#287).

Outputs are scalar JSON: seed prompt IDs, labels, answers, timings, models and
costs. Prompt text, tokens and keys never appear in the output. Bearer keys
are read from environment variables or files and are never printed.

Copyright 2026 Metrum AI.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import math
import os
import sqlite3
import statistics
import sys
import time
import urllib.error
import urllib.request
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
SEED = ROOT / "docs" / "evidence" / "d3" / "seed_prompts.json"


def load_policy() -> Any:
    spec = importlib.util.spec_from_file_location("d3_policy", ROOT / "examples/external-routing-policy/d3_policy.py")
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def pct(values: list[float], p: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    return round(ordered[max(0, math.ceil(p / 100 * len(ordered)) - 1)], 2)


def latency_summary(values: list[float]) -> dict[str, Any]:
    return {"n": len(values), "p50": pct(values, 50), "p95": pct(values, 95), "p99": pct(values, 99),
            "mean": round(statistics.fmean(values), 2) if values else None,
            "max": round(max(values), 2) if values else None}


def label_tier(label: str | None) -> str | None:
    """Tier the default d3_policy rules pick, re-derived from a classLabel like d3:hard:code:r0.71."""
    parts = (label or "").split(":")
    if len(parts) != 4 or parts[0] != "d3" or not parts[3].startswith("r"):
        return None
    try:
        reasoning = float(parts[3][1:])
    except ValueError:
        return None
    return "strong" if parts[1] in ("hard", "expert") or reasoning >= 0.6 else "cheap"


def opener() -> urllib.request.OpenerDirector:
    return urllib.request.build_opener(urllib.request.ProxyHandler({}))


def seed_prompts(limit: int | None, per_cell: int | None = None) -> list[dict[str, Any]]:
    items = json.loads(SEED.read_text(encoding="utf-8"))["prompts"]
    if per_cell:
        seen: Counter = Counter()
        picked = []
        for item in items:
            cell = (item["difficulty"], item["domain"])
            if seen[cell] < per_cell:
                seen[cell] += 1
                picked.append(item)
        items = picked
    return items[:limit] if limit else items


def direct(args: argparse.Namespace) -> dict[str, Any]:
    d3p = load_policy()
    cfg = d3p.DEFAULT_CONFIG
    if args.config:
        cfg = d3p.validate_config(d3p._merge(cfg, d3p.load_config_file(Path(args.config))))  # noqa: SLF001
    questions = d3p.d3_questions(cfg)
    key = os.environ.get(args.api_key_env, "") if args.api_key_env else ""
    headers = {"Content-Type": "application/json"}
    if key:
        headers["Authorization"] = "Bearer " + key
    op = opener()
    health_raw = op.open(args.url.rstrip("/") + "/health", timeout=30).read()
    health = json.loads(health_raw)
    rows: list[dict[str, Any]] = []
    client_ms: list[float] = []
    server_ms: list[float] = []
    errors = Counter()
    prompts = seed_prompts(args.limit, args.per_cell)
    for _ in range(args.warmup):
        body = {"state": "user: warmup", "questions": questions}
        op.open(urllib.request.Request(args.url.rstrip("/") + "/v1/systemone", json.dumps(body).encode(), headers),
                timeout=args.timeout).read()
    for item in prompts:
        body = {"state": "user: " + item["prompt"], "questions": questions}
        if args.model:
            body["model"] = args.model
        req = urllib.request.Request(args.url.rstrip("/") + "/v1/systemone", json.dumps(body).encode(), headers)
        started = time.perf_counter()
        try:
            with op.open(req, timeout=args.timeout) as resp:
                raw = resp.read()
                timing = resp.headers.get("server-timing", "")
        except urllib.error.HTTPError as err:
            errors[f"http_{err.code}"] += 1
            err.close()
            continue
        except OSError:
            errors["unreachable_or_timeout"] += 1
            continue
        elapsed = (time.perf_counter() - started) * 1000
        client_ms.append(elapsed)
        if "dur=" in timing:
            try:
                server_ms.append(float(timing.split("dur=")[1].split(",")[0].split(";")[0]))
            except ValueError:
                pass
        try:
            answers = d3p.parse_answers(cfg, json.loads(raw))
        except d3p.D3Error as err:
            errors[err.kind] += 1
            continue
        decision_rule = d3p.choose_target(cfg["rules"], answers, {}, [
            {"tier": "cheap", "keyConfigured": True}, {"tier": "strong", "keyConfigured": True}])
        rows.append({
            "id": item["id"], "difficulty_label": item["difficulty"], "domain_label": item["domain"],
            "d3_difficulty": answers.get("difficulty", {}).get("value"),
            "d3_difficulty_expected": round(answers.get("difficulty", {}).get("expected", float("nan")), 3),
            "d3_reasoning": round(answers.get("reasoning", {}).get("p", float("nan")), 3),
            "d3_kind": answers.get("kind", {}).get("value"),
            "tier": "strong" if decision_rule[0] == 1 else "cheap",
            "label": d3p.build_label(cfg, answers), "client_ms": round(elapsed, 2),
            "input_tokens": (json.loads(raw).get("usage") or {}).get("input_tokens"),
        })
    by_cell: dict[str, Counter] = defaultdict(Counter)
    for r in rows:
        cell = f"{r['difficulty_label']}-{r['domain_label']}"
        by_cell[cell][f"tier:{r['tier']}"] += 1
        by_cell[cell][f"difficulty:{r['d3_difficulty']}"] += 1
        by_cell[cell][f"kind:{r['d3_kind']}"] += 1
    hard_ok = sum(1 for r in rows if r["difficulty_label"] == "hard" and r["tier"] == "strong")
    easy_ok = sum(1 for r in rows if r["difficulty_label"] == "easy" and r["tier"] == "cheap")
    n_hard = sum(1 for r in rows if r["difficulty_label"] == "hard")
    n_easy = sum(1 for r in rows if r["difficulty_label"] == "easy")
    kind_map = {"code": "code", "math": "math", "chat": "chat"}
    kind_ok = sum(1 for r in rows if kind_map[r["domain_label"]] == r["d3_kind"])
    return {
        "schema": "metrum.d3-live.direct.v1", "model_label": args.model_label, "d3_model": args.model,
        "revision": args.revision, "device": args.device, "health": {k: health.get(k) for k in (
            "status", "model", "max_input_tokens", "modalities")},
        "prompts": len(prompts), "answered": len(rows), "errors": dict(errors), "warmup": args.warmup,
        "client_latency_ms": latency_summary(client_ms), "server_timing_ms": latency_summary(server_ms),
        "routing_vs_labels": {
            "hard_to_strong": f"{hard_ok}/{n_hard}", "easy_to_cheap": f"{easy_ok}/{n_easy}",
            "agreement": round((hard_ok + easy_ok) / max(1, len(rows)), 3),
            "kind_matches_domain": f"{kind_ok}/{len(rows)}",
        },
        "by_cell": {cell: dict(sorted(c.items())) for cell, c in sorted(by_cell.items())},
        "rows": rows,
    }


def finish_reason(raw: bytes) -> str | None:
    """choices[0].finish_reason of a chat completion; the only response field kept."""
    try:
        choices = json.loads(raw).get("choices") or [{}]
    except (ValueError, AttributeError):
        return None
    reason = choices[0].get("finish_reason") if isinstance(choices[0], dict) else None
    return reason if isinstance(reason, str) and len(reason) <= 32 else None


def strong_upstream(model_ref: str) -> str:
    """Usage rows keep the upstream model; Fireworks refs carry the accounts/ prefix."""
    return model_ref if "/" in model_ref or model_ref.startswith("gpt-") else f"accounts/fireworks/models/{model_ref}"


def cost_summary(sent: list[dict[str, Any]]) -> dict[str, Any]:
    """Measured upstream cost and cheap quality signals (output cap hits) from usage rows."""
    ok = [r for r in sent if r.get("served_model")]
    total = sum(float(r.get("cost_usd") or 0) for r in ok)
    out: dict[str, Any] = {
        "served": len(ok), "actual_cost_usd": round(total, 6),
        "cost_per_request_usd": round(total / max(1, len(ok)), 8),
        "input_tokens": sum(r.get("input_tokens") or 0 for r in ok),
        "cached_input_tokens": sum(r.get("cached_input_tokens") or 0 for r in ok),
        "output_tokens": sum(r.get("output_tokens") or 0 for r in ok),
        "reasoning_tokens": sum(r.get("reasoning_tokens") or 0 for r in ok),
        "finish_reasons": dict(Counter(r.get("finish_reason") or "none" for r in sent)),
        "by_difficulty": {},
    }
    for label in ("easy", "hard"):
        rows = [r for r in ok if r["difficulty_label"] == label]
        cost = sum(float(r.get("cost_usd") or 0) for r in rows)
        out["by_difficulty"][label] = {
            "served": len(rows), "cost_usd": round(cost, 6),
            "output_tokens": sum(r.get("output_tokens") or 0 for r in rows),
            "hit_max_tokens": sum(1 for r in rows if r.get("finish_reason") == "length"),
            "served_models": dict(Counter(r["served_model"] for r in rows)),
        }
    return out


def compare(args: argparse.Namespace) -> dict[str, Any]:
    runs = {name: json.loads(Path(path).read_text(encoding="utf-8"))
            for name, path in (("enforce", args.enforce), ("always_strong", args.always_strong),
                               ("always_cheap", args.always_cheap))}
    table: dict[str, Any] = {}
    for name, run in runs.items():
        cost = run["cost"]
        table[name] = {
            "group": run["group"], "prompts": run["prompts"], "http_status": run["http_status"],
            "served_models": run["served_models"], "cost_usd": cost["actual_cost_usd"],
            "cost_per_request_usd": cost["cost_per_request_usd"],
            "output_tokens": cost["output_tokens"], "reasoning_tokens": cost["reasoning_tokens"],
            "hit_max_tokens": {k: v["hit_max_tokens"] for k, v in cost["by_difficulty"].items()},
            "e2e_ms": run["e2e_ms"], "policy_duration_ms": run["policy_duration_ms"],
        }
    enforce, strong, cheap = (table[k]["cost_usd"] for k in ("enforce", "always_strong", "always_cheap"))
    return {
        "schema": "metrum.d3-live.compare.v1", "runs": table,
        "strong_share": runs["enforce"].get("enforce", {}).get("strong_share"),
        "hard_prompts_to_strong": runs["enforce"].get("enforce", {}).get("hard_prompts_to_strong"),
        "easy_prompts_to_cheap": runs["enforce"].get("enforce", {}).get("easy_prompts_to_cheap"),
        "saving_vs_always_strong_usd": round(strong - enforce, 6),
        "saving_vs_always_strong_pct": round(100 * (strong - enforce) / strong, 1) if strong else None,
        "extra_vs_always_cheap_usd": round(enforce - cheap, 6),
        "extra_vs_always_cheap_x": round(enforce / cheap, 2) if cheap else None,
        "note": "each run is a separate live pass over the same 204 prompts; output lengths differ per run, "
                "so the comparison is measured spend, not the same tokens repriced",
    }


def router(args: argparse.Namespace) -> dict[str, Any]:
    token = Path(args.token_file).read_text(encoding="utf-8").strip()
    op = opener()
    prompts = seed_prompts(args.limit, args.per_cell)
    sent: list[dict[str, Any]] = []
    started_all = time.time()
    for item in prompts:
        content = item["prompt"] + (f"\n[run {args.run_marker}]" if args.run_marker else "")
        body = {"model": args.group, "messages": [{"role": "user", "content": content}],
                "max_tokens": args.max_tokens}
        req = urllib.request.Request(args.base_url.rstrip("/") + "/chat/completions", json.dumps(body).encode(),
                                     {"Content-Type": "application/json", "Authorization": "Bearer " + token})
        finish = None
        t0 = time.perf_counter()
        try:
            with op.open(req, timeout=args.timeout) as resp:
                status, rid = resp.status, resp.headers.get("X-Request-Id", "")
                finish = finish_reason(resp.read())
        except urllib.error.HTTPError as err:
            status, rid = err.code, err.headers.get("X-Request-Id", "")
            err.close()
        except OSError:
            status, rid = 0, ""
        sent.append({"id": item["id"], "difficulty_label": item["difficulty"], "domain_label": item["domain"],
                     "status": status, "request_id": rid, "e2e_ms": round((time.perf_counter() - t0) * 1000, 1),
                     "finish_reason": finish})
    time.sleep(2)
    conn = sqlite3.connect(f"file:{args.usage_db}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    for row in sent:
        rid = row["request_id"]
        if not rid:
            continue
        pol = conn.execute("SELECT outcome, class_label, duration_ms, selected_candidate_index, error_class "
                           "FROM request_policy_executions WHERE request_id = ? ORDER BY seq DESC LIMIT 1",
                           (rid,)).fetchone()
        use = conn.execute("SELECT target_provider, target_model, input_tokens, output_tokens, total_cost_usd, "
                           "status, reasoning_tokens, cached_input_tokens, upstream_duration_ms "
                           "FROM request_usage WHERE request_id = ?", (rid,)).fetchone()
        rec = conn.execute("SELECT s.candidate_index, c.model FROM request_routing_signals s "
                           "LEFT JOIN request_target_candidates c "
                           "ON c.request_id = s.request_id AND c.candidate_index = s.candidate_index "
                           "WHERE s.request_id = ? AND s.signal_name = 'shadow_recommended_candidate'",
                           (rid,)).fetchone()
        if pol:
            row.update({"policy_outcome": pol["outcome"], "class_label": pol["class_label"],
                        "policy_ms": pol["duration_ms"], "policy_error": pol["error_class"]})
        if use:
            row.update({"served_model": use["target_model"], "input_tokens": use["input_tokens"],
                        "output_tokens": use["output_tokens"], "cost_usd": use["total_cost_usd"],
                        "reasoning_tokens": use["reasoning_tokens"], "cached_input_tokens": use["cached_input_tokens"],
                        "upstream_ms": use["upstream_duration_ms"]})
        if pol:
            row["label_tier"] = label_tier(pol["class_label"])
        if rec:
            row["shadow_signal_candidate_index"] = rec["candidate_index"]
            if rec["model"]:
                row["shadow_recommended_model"] = rec["model"]
    statuses = Counter(r["status"] for r in sent)
    policy_ms = [float(r["policy_ms"]) for r in sent if r.get("policy_ms") is not None]
    served = Counter(r.get("served_model") for r in sent)
    outcome = Counter(r.get("policy_outcome") for r in sent)
    result: dict[str, Any] = {
        "schema": "metrum.d3-live.router.v1", "group": args.group, "mode_label": args.mode_label,
        "prompts": len(prompts), "started_unix": int(started_all), "http_status": dict(statuses),
        "policy_outcomes": dict(outcome), "served_models": dict(served),
        "policy_duration_ms": latency_summary(policy_ms),
        "e2e_ms": latency_summary([float(r["e2e_ms"]) for r in sent]),
        "requests_failed_due_to_policy": sum(1 for r in sent if r["status"] == 502),
        "max_tokens": args.max_tokens,
        "cost": cost_summary(sent),
    }
    if args.mode_label == "shadow":
        recs = [r for r in sent if r.get("shadow_recommended_model")]
        tiers = [r for r in sent if r.get("label_tier")]
        result["shadow_by_label_tier"] = {
            "decisions": len(tiers),
            "recommended_tier": dict(Counter(r["label_tier"] for r in tiers)),
            "hard_to_strong": f"{sum(1 for r in tiers if r['difficulty_label'] == 'hard' and r['label_tier'] == 'strong')}/"
                              f"{sum(1 for r in tiers if r['difficulty_label'] == 'hard')}",
            "easy_to_cheap": f"{sum(1 for r in tiers if r['difficulty_label'] == 'easy' and r['label_tier'] == 'cheap')}/"
                             f"{sum(1 for r in tiers if r['difficulty_label'] == 'easy')}",
            "agreement_with_labels": round(sum(1 for r in tiers if (r["difficulty_label"] == "hard")
                                               == (r["label_tier"] == "strong")) / max(1, len(tiers)), 3),
            "recommended_equals_served": sum(1 for r in tiers if r["label_tier"] == "cheap"),
            "unresolved_signal_rows": sum(1 for r in sent if r.get("shadow_signal_candidate_index") == -1),
            "note": "tier re-derived from the scalar class label with the adapter's default rules (difficulty "
                    "hard|expert or reasoning >= 0.6 -> strong); target 0 (served) is the cheap tier",
        }
        result["shadow"] = {
            "recommendations": len(recs),
            "recommended_equals_served": sum(1 for r in recs if r["shadow_recommended_model"] == r.get("served_model")),
            "recommended_models": dict(Counter(r["shadow_recommended_model"] for r in recs)),
            "recommended_strong_for_hard": sum(1 for r in recs if r["difficulty_label"] == "hard"
                                               and r["shadow_recommended_model"] == args.strong_model),
            "recommended_cheap_for_easy": sum(1 for r in recs if r["difficulty_label"] == "easy"
                                              and r["shadow_recommended_model"] != args.strong_model),
        }
    if args.mode_label == "enforce":
        ok = [r for r in sent if r.get("served_model")]
        strong = [r for r in ok if r["served_model"] in (args.strong_model, strong_upstream(args.strong_model))]
        actual = sum(float(r.get("cost_usd") or 0) for r in ok)
        always_strong = sum((r.get("input_tokens") or 0) * args.strong_input_price / 1e6
                            + (r.get("output_tokens") or 0) * args.strong_output_price / 1e6 for r in ok)
        always_cheap = sum((r.get("input_tokens") or 0) * args.cheap_input_price / 1e6
                           + (r.get("output_tokens") or 0) * args.cheap_output_price / 1e6 for r in ok)
        result["enforce"] = {
            "served": len(ok), "strong_share": round(len(strong) / max(1, len(ok)), 3),
            "hard_prompts_to_strong": f"{sum(1 for r in strong if r['difficulty_label'] == 'hard')}/"
                                      f"{sum(1 for r in ok if r['difficulty_label'] == 'hard')}",
            "easy_prompts_to_cheap": f"{sum(1 for r in ok if r['difficulty_label'] == 'easy' and r not in strong)}/"
                                     f"{sum(1 for r in ok if r['difficulty_label'] == 'easy')}",
            "actual_cost_usd": round(actual, 6), "always_strong_cost_usd_estimate": round(always_strong, 6),
            "always_cheap_cost_usd_estimate": round(always_cheap, 6),
            "note": "always-strong and always-cheap estimates price the same token counts at that target's list "
                    "price; check which tier is cheaper per token before reading the delta",
        }
    for r in sent:
        r.pop("request_id", None)
    result["rows"] = sent
    return result


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    d = sub.add_parser("direct")
    d.add_argument("--url", required=True)
    d.add_argument("--api-key-env", default="D3_API_KEY")
    d.add_argument("--model", default="")
    d.add_argument("--model-label", required=True)
    d.add_argument("--revision", default="")
    d.add_argument("--device", default="")
    d.add_argument("--config")
    d.add_argument("--warmup", type=int, default=3)
    d.add_argument("--timeout", type=float, default=60)
    d.add_argument("--limit", type=int)
    r = sub.add_parser("router")
    r.add_argument("--base-url", required=True)
    r.add_argument("--token-file", required=True)
    r.add_argument("--usage-db", required=True)
    r.add_argument("--group", default="d3-auto")
    r.add_argument("--mode-label", required=True,
                   choices=("shadow", "enforce", "baseline", "always-strong", "always-cheap"))
    r.add_argument("--strong-model", default="gpt-6-luna")
    r.add_argument("--strong-input-price", type=float, default=0.10)
    r.add_argument("--strong-output-price", type=float, default=0.50)
    r.add_argument("--cheap-input-price", type=float, default=0.22)
    r.add_argument("--cheap-output-price", type=float, default=0.66)
    r.add_argument("--max-tokens", type=int, default=32)
    r.add_argument("--run-marker", default="")
    r.add_argument("--timeout", type=float, default=120)
    r.add_argument("--limit", type=int)
    for p in (d, r):
        p.add_argument("--per-cell", type=int, help="first N prompts of each difficulty x domain cell")
    c = sub.add_parser("compare")
    c.add_argument("--enforce", required=True)
    c.add_argument("--always-strong", required=True)
    c.add_argument("--always-cheap", required=True)
    for p in (d, r, c):
        p.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()
    result = {"direct": direct, "router": router, "compare": compare}[args.cmd](args)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(result, indent=1) + "\n", encoding="utf-8")
    summary = {k: v for k, v in result.items() if k != "rows"}
    print(json.dumps(summary, indent=1))
    return 0


if __name__ == "__main__":
    sys.exit(main())
