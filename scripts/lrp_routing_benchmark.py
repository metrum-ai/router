#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline LRP routing-benchmark harness (issue #159).

Builds A/B/C config patches, seed and Harbor load plans, metric aggregators,
and routing-benchmark.json writers. Does not run live or paid benchmarks.
Configuration D / Shadeform GPU LRP topology belongs to issue #162.
"""

from __future__ import annotations

import argparse
import json
import math
import statistics
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
EVIDENCE_DIR = ROOT / "docs" / "evidence" / "learned-routing-policy"
SCHEMA_NAME = "routing-benchmark.schema.json"
RESULT_NAME = "routing-benchmark.json"
MANIFEST_NAME = "routing-benchmark-manifest.json"
SUMMARY_NAME = "routing-benchmark.md"

SCHEMA_ID = "metrum.lrp.routing-benchmark/v1"
SEED_CONCURRENCY = (1, 8, 32, 128)
HARBOR_CONCURRENCY = (1, 4, 16)
RUNS_PER_CELL = 3

# Configurations A/B/C for issue #159. D is a stub pointer to #162.
CONFIGS: dict[str, dict[str, Any]] = {
    "A": {
        "id": "A",
        "label": "static_no_lrp",
        "strategy": "static",
        "external_policy_mode": None,
        "role": "overhead_baseline",
        "notes": "Static eligible order. GPT-5.6-only remains the primary usefulness baseline.",
    },
    "B": {
        "id": "B",
        "label": "lrp_external_shadow_cpu",
        "strategy": "external",
        "external_policy_mode": "shadow",
        "role": "overhead_shadow",
        "notes": "CPU sidecar ONNX int8 embedder + LightGBM. Shadow is not an enforce quality claim.",
    },
    "C": {
        "id": "C",
        "label": "lrp_external_enforce_cpu",
        "strategy": "external",
        "external_policy_mode": "enforce",
        "role": "overhead_and_effectiveness",
        "notes": "Same sidecar as B in enforce mode.",
    },
    "D": {
        "id": "D",
        "label": "lrp_gpu_enforce_stub",
        "strategy": "external",
        "external_policy_mode": "enforce",
        "role": "deferred_to_162",
        "notes": "GPU embedder / Shadeform topology moved to issue #162. Stub only.",
        "issue": 162,
        "stub": True,
    },
}

TOKEN_CATEGORIES = (
    "input",
    "cached_input",
    "cache_write",
    "reasoning",
    "output",
)

# Router-added latency source (issue #159 acceptance).
ROUTER_ADDED_LATENCY = {
    "metric": "router_added_latency_ms",
    "definition": "Monotonic milliseconds from router receive to outbound httptrace.WroteRequest.",
    "receive_event": "router_receive",
    "wrote_request_event": "router_upstream_wrote_request",
    "duration_field": "duration_ms",
    "source_file": "internal/router/service.go",
    "receive_lines": "1318-1352",
    "wrote_request_lines": "2209-2230",
    "join": (
        "request_trace_events rows where event=router_upstream_wrote_request; "
        "duration_ms is receive_to_wrote_request."
    ),
}


class HarnessError(Exception):
    """Scalar harness failure without nested provider output."""


def percentile(sorted_values: list[float], pct: float) -> float | None:
    if not sorted_values:
        return None
    if len(sorted_values) == 1:
        return float(sorted_values[0])
    rank = (len(sorted_values) - 1) * pct
    low = math.floor(rank)
    high = math.ceil(rank)
    if low == high:
        return float(sorted_values[low])
    weight = rank - low
    return float(sorted_values[low] * (1.0 - weight) + sorted_values[high] * weight)


def summarize_latencies(values: list[float]) -> dict[str, float | None]:
    ordered = sorted(float(v) for v in values)
    return {
        "count": float(len(ordered)),
        "p50_ms": percentile(ordered, 0.50),
        "p95_ms": percentile(ordered, 0.95),
        "p99_ms": percentile(ordered, 0.99),
        "max_ms": float(ordered[-1]) if ordered else None,
    }


def median_and_spread(values: list[float]) -> dict[str, float | None]:
    if not values:
        return {"median": None, "spread": None, "min": None, "max": None, "n": 0.0}
    ordered = sorted(float(v) for v in values)
    med = float(statistics.median(ordered))
    return {
        "median": med,
        "spread": float(ordered[-1] - ordered[0]),
        "min": float(ordered[0]),
        "max": float(ordered[-1]),
        "n": float(len(ordered)),
    }


def config_patch(config_id: str, *, group: str = "lrp-bench", policy_url: str = "http://127.0.0.1:18093/route") -> dict[str, Any]:
    cfg = CONFIGS.get(config_id)
    if cfg is None:
        raise HarnessError(f"unknown_config:{config_id}")
    if cfg.get("stub"):
        return {
            "config_id": config_id,
            "stub": True,
            "issue": cfg["issue"],
            "message": "Configuration D belongs to issue #162. Do not apply from #159 harness.",
        }
    model: dict[str, Any] = {"strategy": cfg["strategy"]}
    if cfg["strategy"] == "external":
        model["external_policy"] = {
            "url": policy_url,
            "allow_hosts": ["127.0.0.1", "localhost"],
            "allow_http": True,
            "mode": cfg["external_policy_mode"],
            "timeout_ms": 500,
            "max_response_bytes": 65536,
            "include_request": True,
            "on_error": "fail_closed",
        }
    return {
        "config_id": config_id,
        "label": cfg["label"],
        "role": cfg["role"],
        "stub": False,
        "models": {group: model},
        "notes": cfg["notes"],
    }


def seed_load_plan(*, turn_ids: list[str], concurrency: int, synthetic_upstream: bool = True) -> dict[str, Any]:
    if concurrency not in SEED_CONCURRENCY:
        raise HarnessError(f"invalid_seed_concurrency:{concurrency}")
    if not turn_ids:
        raise HarnessError("empty_seed_turns")
    return {
        "driver": "seed_replay",
        "concurrency": concurrency,
        "turn_count": len(turn_ids),
        "turn_ids_digest": _digest(turn_ids),
        "synthetic_upstream": synthetic_upstream,
        "synthetic_upstream_mark": "synthetic_upstream" if synthetic_upstream else None,
        "notes": (
            "Throughput uses a deterministic local upstream sink so LRP req/sec "
            "is not provider-bound. Mark those rows synthetic_upstream."
            if synthetic_upstream
            else "Paid effectiveness traffic reuses the #157 candidate table."
        ),
    }


def harbor_load_plan(
    *,
    task_set: str,
    task_revision: str,
    concurrency: int,
    agent: str = "codex",
) -> dict[str, Any]:
    if concurrency not in HARBOR_CONCURRENCY:
        raise HarnessError(f"invalid_harbor_concurrency:{concurrency}")
    if not task_set.strip() or not task_revision.strip():
        raise HarnessError("harbor_task_set_and_revision_required")
    return {
        "driver": "harbor",
        "concurrency": concurrency,
        "task_set": task_set,
        "task_revision": task_revision,
        "agent": agent,
        "harbor_pass_rate_role": "load_shape_only",
        "notes": "Harbor pass rate is load shape metadata, not routing quality.",
    }


def run_matrix(*, include_harbor: bool = True, include_d_stub: bool = True) -> list[dict[str, Any]]:
    cells: list[dict[str, Any]] = []
    for config_id in ("A", "B", "C"):
        for concurrency in SEED_CONCURRENCY:
            cells.append(
                {
                    "config_id": config_id,
                    "driver": "seed_replay",
                    "concurrency": concurrency,
                    "runs": RUNS_PER_CELL,
                    "synthetic_upstream": True,
                }
            )
        if include_harbor:
            for concurrency in HARBOR_CONCURRENCY:
                cells.append(
                    {
                        "config_id": config_id,
                        "driver": "harbor",
                        "concurrency": concurrency,
                        "runs": RUNS_PER_CELL,
                        "synthetic_upstream": False,
                    }
                )
    if include_d_stub:
        cells.append(
            {
                "config_id": "D",
                "driver": "seed_replay",
                "concurrency": 1,
                "runs": 0,
                "stub": True,
                "issue": 162,
            }
        )
    return cells


def aggregate_three_runs(run_metrics: list[dict[str, Any]]) -> dict[str, Any]:
    if len(run_metrics) != RUNS_PER_CELL:
        raise HarnessError(f"expected_{RUNS_PER_CELL}_runs_got_{len(run_metrics)}")
    keys = (
        "lrp_decisions_per_sec",
        "router_added_latency_p50_ms",
        "router_added_latency_p95_ms",
        "router_added_latency_p99_ms",
        "sidecar_p50_ms",
        "sidecar_p95_ms",
        "sidecar_p99_ms",
        "timeout_rate",
        "abstention_rate",
        "verifier_pass_rate",
        "absolute_usd_vs_gpt56",
    )
    out: dict[str, Any] = {"runs": RUNS_PER_CELL, "metrics": {}}
    for key in keys:
        values = [float(run[key]) for run in run_metrics if key in run and run[key] is not None]
        if values:
            out["metrics"][key] = median_and_spread(values)
    return out


def overhead_metrics(
    *,
    decisions_per_sec: float,
    router_added_ms: list[float],
    sidecar_ms: list[float],
    timeout_rate: float,
    error_rate_by_class: dict[str, float] | None = None,
    e2e_ttfb_ms: list[float] | None = None,
    e2e_total_ms: list[float] | None = None,
    router_cpu_pct: float | None = None,
    router_rss_mb: float | None = None,
    sidecar_cpu_pct: float | None = None,
    sidecar_rss_mb: float | None = None,
    synthetic_upstream: bool = True,
) -> dict[str, Any]:
    return {
        "family": "overhead",
        "synthetic_upstream": synthetic_upstream,
        "lrp_decisions_per_sec": decisions_per_sec,
        "router_added_latency": summarize_latencies(router_added_ms),
        "sidecar_latency": summarize_latencies(sidecar_ms),
        "timeout_rate": timeout_rate,
        "error_rate_by_class": dict(error_rate_by_class or {}),
        "e2e_ttfb": {
            **summarize_latencies(e2e_ttfb_ms or []),
            "upstream_dependent": True,
        },
        "e2e_total": {
            **summarize_latencies(e2e_total_ms or []),
            "upstream_dependent": True,
        },
        "router_cpu_pct": router_cpu_pct,
        "router_rss_mb": router_rss_mb,
        "sidecar_cpu_pct": sidecar_cpu_pct,
        "sidecar_rss_mb": sidecar_rss_mb,
        "router_added_latency_source": ROUTER_ADDED_LATENCY,
    }


def overhead_deltas(
    *,
    baseline_a: dict[str, Any],
    candidate: dict[str, Any],
    candidate_config_id: str,
) -> dict[str, Any]:
    """Report B/C overhead deltas against static A (not a savings claim)."""
    if candidate_config_id not in ("B", "C"):
        raise HarnessError(f"overhead_delta_config:{candidate_config_id}")
    if baseline_a.get("family") != "overhead" or candidate.get("family") != "overhead":
        raise HarnessError("overhead_delta_requires_overhead_family")

    def _scalar(block: dict[str, Any], key: str) -> float | None:
        value = block.get(key)
        return float(value) if isinstance(value, (int, float)) else None

    def _p50(block: dict[str, Any], key: str) -> float | None:
        nested = block.get(key)
        if not isinstance(nested, dict):
            return None
        value = nested.get("p50_ms")
        return float(value) if isinstance(value, (int, float)) else None

    keys = (
        ("lrp_decisions_per_sec", _scalar),
        ("timeout_rate", _scalar),
        ("router_added_latency", _p50),
        ("sidecar_latency", _p50),
    )
    deltas: dict[str, float | None] = {}
    for key, reader in keys:
        base = reader(baseline_a, key)
        cand = reader(candidate, key)
        if base is None or cand is None:
            deltas[f"{key}_delta"] = None
        else:
            deltas[f"{key}_delta"] = float(cand - base)
    return {
        "family": "overhead_delta",
        "baseline_config_id": "A",
        "candidate_config_id": candidate_config_id,
        "deltas": deltas,
        "notes": "Overhead deltas of B/C versus static A. Not a cost savings claim.",
    }


def effectiveness_metrics(
    *,
    verifier_pass_rate: float,
    verifier_column_rates: dict[str, float],
    target_mix: dict[str, float],
    abstention_rate: float,
    unlabeled_count: int = 0,
    ineligible_count: int = 0,
    primary_baseline: str = "gpt-5.6-only",
) -> dict[str, Any]:
    return {
        "family": "effectiveness",
        "primary_baseline": primary_baseline,
        "verifier_pass_rate": verifier_pass_rate,
        "verifier_column_rates": dict(verifier_column_rates),
        "target_mix": dict(target_mix),
        "abstention_rate": abstention_rate,
        "unlabeled_count": unlabeled_count,
        "ineligible_count": ineligible_count,
        "notes": "Primary compare is LRP versus GPT-5.6-only on identical common-eligible turn IDs.",
    }


def cost_token_metrics(
    *,
    tokens_by_category: dict[str, float],
    usd_by_category: dict[str, float],
    total_usd: float,
    gpt56_total_usd: float,
    volume_effect_usd: dict[str, float],
    mix_effect_usd: dict[str, float],
) -> dict[str, Any]:
    for category in TOKEN_CATEGORIES:
        if category not in tokens_by_category:
            raise HarnessError(f"missing_token_category:{category}")
    absolute_usd_vs_gpt56 = float(gpt56_total_usd) - float(total_usd)
    return {
        "family": "cost_tokens",
        "token_categories": TOKEN_CATEGORIES,
        "tokens_by_category": {k: float(tokens_by_category[k]) for k in TOKEN_CATEGORIES},
        "usd_by_category": {k: float(usd_by_category.get(k, 0.0)) for k in TOKEN_CATEGORIES},
        "total_usd": float(total_usd),
        "gpt56_total_usd": float(gpt56_total_usd),
        "absolute_usd_vs_gpt56": absolute_usd_vs_gpt56,
        "volume_effect_usd": dict(volume_effect_usd),
        "mix_effect_usd": dict(mix_effect_usd),
        "forbidden": ["savings_percent", "percent_saved", "pct_savings"],
        "notes": (
            "Report absolute USD as GPT-5.6 baseline USD minus selected USD. "
            "Do not write savings percentages into docs/evidence/."
        ),
    }


def volume_mix_decomposition(
    *,
    baseline_tokens: float,
    selected_tokens: float,
    baseline_rate_per_token: float,
    selected_rate_per_token: float,
) -> dict[str, float]:
    volume = (selected_tokens - baseline_tokens) * baseline_rate_per_token
    mix = selected_tokens * (selected_rate_per_token - baseline_rate_per_token)
    return {
        "volume_effect_usd": float(volume),
        "mix_effect_usd": float(mix),
        "sum_usd": float(volume + mix),
    }


def build_manifest(
    *,
    router_commit: str,
    sidecar_commit: str,
    bundle_fingerprint: str,
    vllm_version: str | None = None,
    instance_types: dict[str, str] | None = None,
    max_decision_ms: int | None = None,
    external_policy_timeout_ms: int | None = None,
    sidecar_deadline_ms: int | None = None,
    harbor_task_set: str | None = None,
    harbor_task_revision: str | None = None,
) -> dict[str, Any]:
    return {
        "schema": SCHEMA_ID,
        "live_runs_executed": False,
        "router_commit": router_commit,
        "sidecar_commit": sidecar_commit,
        "bundle_fingerprint": bundle_fingerprint,
        "vllm_version": vllm_version,
        "instance_types": instance_types or {},
        "max_decision_ms": max_decision_ms,
        "external_policy_timeout_ms": external_policy_timeout_ms,
        "sidecar_deadline_ms": sidecar_deadline_ms,
        "max_decision_ms_definition": (
            "Report sidecar deadline_ms as max_decision_ms when both are set; "
            "also record external_policy.timeout_ms."
        ),
        "harbor_task_set": harbor_task_set,
        "harbor_task_revision": harbor_task_revision,
        "warmup": "Documented warm-up; steady-state windows only.",
        "runs_per_cell": RUNS_PER_CELL,
        "seed_concurrency": list(SEED_CONCURRENCY),
        "harbor_concurrency": list(HARBOR_CONCURRENCY),
        "router_added_latency_source": ROUTER_ADDED_LATENCY,
        "configuration_d": {"status": "deferred", "issue": 162},
        "notes": "Harness-only artifact. No live or paid benchmark executed in issue #159 PR.",
    }


def empty_results_document(manifest: dict[str, Any]) -> dict[str, Any]:
    return {
        "schema": SCHEMA_ID,
        "status": "harness_ready_no_live_runs",
        "promotable": False,
        "live_runs_executed": False,
        "issue": 159,
        "configuration_d_issue": 162,
        "manifest": manifest,
        "cells": [],
        "router_added_latency_source": ROUTER_ADDED_LATENCY,
        "public_reporting_rules": [
            "No savings percentages in docs/evidence/.",
            "Primary usefulness baseline is GPT-5.6-only.",
            "Static A remains the overhead baseline.",
            "Harbor pass rate is load shape only.",
            "Synthetic throughput rows must be marked synthetic_upstream.",
        ],
    }


def load_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, allow_nan=False, sort_keys=True) + "\n", encoding="utf-8")


def validate_against_schema(document: dict[str, Any], schema: dict[str, Any]) -> list[str]:
    """Minimal required-field validator (stdlib only; no jsonschema dependency)."""
    errors: list[str] = []
    required = schema.get("required", [])
    if not isinstance(required, list):
        return ["schema.required_not_list"]
    for key in required:
        if key not in document:
            errors.append(f"missing:{key}")
    props = schema.get("properties", {})
    if document.get("schema") != schema.get("properties", {}).get("schema", {}).get("const", SCHEMA_ID):
        const = props.get("schema", {}).get("const")
        if const is not None and document.get("schema") != const:
            errors.append("schema_const_mismatch")
    if document.get("live_runs_executed") is True:
        errors.append("live_runs_not_allowed_in_harness_pr")
    if "savings_percent" in json.dumps(document):
        errors.append("forbidden_savings_percent")
    cells = document.get("cells", [])
    if not isinstance(cells, list):
        errors.append("cells_not_list")
    return errors


def _digest(values: list[str]) -> str:
    import hashlib

    h = hashlib.sha256()
    for value in values:
        h.update(value.encode("utf-8"))
        h.update(b"\n")
    return h.hexdigest()


def write_placeholder_artifacts(out_dir: Path, *, router_commit: str = "UNVERIFIED") -> dict[str, Path]:
    manifest = build_manifest(
        router_commit=router_commit,
        sidecar_commit="UNVERIFIED",
        bundle_fingerprint="UNVERIFIED",
        max_decision_ms=None,
        external_policy_timeout_ms=500,
        sidecar_deadline_ms=200,
        harbor_task_set="aider/polyglot_python_two-bucket",
        harbor_task_revision="pin-record-at-run-time",
    )
    doc = empty_results_document(manifest)
    paths = {
        "json": out_dir / RESULT_NAME,
        "manifest": out_dir / MANIFEST_NAME,
        "summary": out_dir / SUMMARY_NAME,
    }
    write_json(paths["json"], doc)
    write_json(paths["manifest"], manifest)
    paths["summary"].write_text(_summary_markdown(doc), encoding="utf-8")
    return paths


def _summary_markdown(doc: dict[str, Any]) -> str:
    src = doc.get("router_added_latency_source", ROUTER_ADDED_LATENCY)
    return "\n".join(
        [
            "# Routing benchmark (issue #159 harness)",
            "",
            "Status: harness ready. No live or paid runs executed in this PR.",
            "",
            "Configurations A/B/C are supported. Configuration D is deferred to issue #162.",
            "",
            "## Router-added latency",
            "",
            f"- Definition: {src['definition']}",
            f"- Receive event: `{src['receive_event']}` at `{src['source_file']}:{src['receive_lines']}`",
            f"- WroteRequest event: `{src['wrote_request_event']}` at `{src['source_file']}:{src['wrote_request_lines']}`",
            f"- Join: {src['join']}",
            "",
            "## Metric families",
            "",
            "1. Overhead: LRP decisions/sec, sidecar p50/p95/p99, router-added latency,",
            "   CPU/memory, timeouts, error rate by class; B/C deltas versus static A.",
            "   E2E TTFB/total are recorded and flagged upstream_dependent.",
            "2. Effectiveness: verifier rates, target mix, abstention; primary compare versus GPT-5.6-only.",
            "3. Cost/tokens: token categories, absolute USD versus GPT-5.6, volume versus mix decomposition.",
            "",
            "No savings percentages.",
            "",
            "## Drivers",
            "",
            f"- Seed replay concurrency: {', '.join(str(x) for x in SEED_CONCURRENCY)}",
            f"- Harbor concurrency: {', '.join(str(x) for x in HARBOR_CONCURRENCY)} (task set + revision recorded)",
            f"- Runs per cell: {RUNS_PER_CELL} (median and spread)",
            "",
            "Throughput cells use a deterministic local upstream sink and mark rows `synthetic_upstream`.",
            "",
        ]
    )


def schema_document() -> dict[str, Any]:
    return {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "$id": "https://metrum.ai/schemas/lrp-routing-benchmark/v1.json",
        "title": "LRP routing benchmark evidence",
        "type": "object",
        "required": [
            "schema",
            "status",
            "promotable",
            "live_runs_executed",
            "issue",
            "manifest",
            "cells",
            "router_added_latency_source",
        ],
        "properties": {
            "schema": {"const": SCHEMA_ID},
            "status": {"type": "string"},
            "promotable": {"type": "boolean"},
            "live_runs_executed": {"type": "boolean"},
            "issue": {"type": "integer"},
            "configuration_d_issue": {"type": "integer"},
            "manifest": {"type": "object"},
            "cells": {"type": "array"},
            "router_added_latency_source": {"type": "object"},
            "public_reporting_rules": {"type": "array", "items": {"type": "string"}},
        },
        "additionalProperties": True,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    p_schema = sub.add_parser("write-schema", help="Write routing-benchmark.schema.json")
    p_schema.add_argument("--out", type=Path, default=EVIDENCE_DIR / SCHEMA_NAME)

    p_placeholder = sub.add_parser("write-placeholder", help="Write empty evidence artifacts")
    p_placeholder.add_argument("--out-dir", type=Path, default=EVIDENCE_DIR)
    p_placeholder.add_argument("--router-commit", default="UNVERIFIED")

    p_validate = sub.add_parser("validate", help="Validate a results document against the schema")
    p_validate.add_argument("--document", type=Path, required=True)
    p_validate.add_argument("--schema", type=Path, default=EVIDENCE_DIR / SCHEMA_NAME)

    p_plan = sub.add_parser("plan", help="Print the offline run matrix as JSON")
    p_plan.add_argument("--no-harbor", action="store_true")

    p_config = sub.add_parser("config-patch", help="Emit a config patch for A/B/C (D is stub)")
    p_config.add_argument("--config", required=True, choices=sorted(CONFIGS))
    p_config.add_argument("--group", default="lrp-bench")

    args = parser.parse_args(argv)
    if args.command == "write-schema":
        write_json(args.out, schema_document())
        print(args.out)
        return 0
    if args.command == "write-placeholder":
        paths = write_placeholder_artifacts(args.out_dir, router_commit=args.router_commit)
        for path in paths.values():
            print(path)
        return 0
    if args.command == "validate":
        document = load_json(args.document)
        schema = load_json(args.schema)
        errors = validate_against_schema(document, schema)
        if errors:
            print("FAIL " + ",".join(errors))
            return 1
        print("OK")
        return 0
    if args.command == "plan":
        print(json.dumps(run_matrix(include_harbor=not args.no_harbor), indent=2, sort_keys=True))
        return 0
    if args.command == "config-patch":
        print(json.dumps(config_patch(args.config, group=args.group), indent=2, sort_keys=True))
        return 0
    raise HarnessError(f"unknown_command:{args.command}")


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except HarnessError as exc:
        print(f"ERROR {exc}", file=sys.stderr)
        raise SystemExit(2) from exc
