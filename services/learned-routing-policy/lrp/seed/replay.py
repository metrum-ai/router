# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Router fanout wrapper for seed replay. Does not run paid traffic by default."""

from __future__ import annotations

from pathlib import Path
from typing import Any

import httpx

from ..collect import DataError, utc_now
from ..fanout import FanoutTarget, run_fanout
from . import SOURCE_DATASET, SPEND_ABORT_USD
from .targets import fanout_target_dicts, iteration1_targets


def openai_provider_request_fields() -> dict[str, dict[str, Any]]:
    return {"openai": {"prompt_cache_options": {"mode": "explicit"}}}


def build_replay_request(turn: dict[str, Any], *, max_tokens: int = 512) -> dict[str, Any]:
    request: dict[str, Any] = {
        "schema_version": "lrp.request.v1",
        "request_id": turn["turn_id"],
        "captured_at": utc_now(),
        "source": f"dataset:{SOURCE_DATASET}",
        "group": "lrp-seed",
        "dialect": "openai-chat",
        "caller": {"project": "lrp-seed", "environment": "offline"},
        "session_key": turn["session_key"],
        "messages": turn["messages"],
        "max_tokens": max_tokens,
        "provider_request_fields": openai_provider_request_fields(),
    }
    tools = turn.get("tools")
    if tools:
        request["tools"] = tools
    return request


def estimate_replay_spend_usd(
    turns: list[dict[str, Any]],
    targets: list[dict[str, Any]] | None = None,
    *,
    assumed_output_tokens: int = 512,
) -> dict[str, Any]:
    """Printable spend estimate hook before any paid fanout call."""
    portfolio = targets if targets is not None else iteration1_targets()
    per_target: list[dict[str, Any]] = []
    total = 0.0
    for target in portfolio:
        pricing = target.get("pricing") or {}
        input_rate = float(pricing["input_per_m_usd"])
        output_rate = float(pricing["output_per_m_usd"])
        target_cost = 0.0
        for turn in turns:
            tokens = int(turn.get("prompt_tokens_cl100k") or 0)
            target_cost += (
                tokens * input_rate + assumed_output_tokens * output_rate
            ) / 1e6
        per_target.append(
            {
                "target_id": target.get("target_id"),
                "model": target["model"],
                "estimate_usd": target_cost,
            }
        )
        total += target_cost
    return {
        "schema_version": "lrp.seed.spend_estimate.v1",
        "assumed_output_tokens": assumed_output_tokens,
        "turn_count": len(turns),
        "target_count": len(portfolio),
        "estimate_usd": total,
        "abort_threshold_usd": SPEND_ABORT_USD,
        "per_target": per_target,
    }


def guard_spend_estimate(
    estimate: dict[str, Any], *, approve_over_threshold: bool = False
) -> None:
    value = float(estimate["estimate_usd"])
    threshold = float(estimate.get("abort_threshold_usd", SPEND_ABORT_USD))
    if value > threshold and not approve_over_threshold:
        raise DataError("spend_estimate_requires_approval")


async def run_seed_replay(
    *,
    requests: Path,
    out: Path,
    targets: list[dict[str, Any]] | None = None,
    base_url: str,
    client: httpx.AsyncClient | None = None,
    approve_spend_over_threshold: bool = False,
    spend_estimate: dict[str, Any] | None = None,
    execute: bool = False,
    concurrency: int = 1,
    max_total_cost_usd: float | None = None,
) -> dict[str, Any]:
    """Wrap ``run_fanout`` via router with one group per target.

    Set ``execute=True`` only for an approved paid run. Unit tests keep
    ``execute=False`` or inject a mock client without live credentials.
    """
    portfolio = targets if targets is not None else iteration1_targets()
    if spend_estimate is not None:
        guard_spend_estimate(
            spend_estimate, approve_over_threshold=approve_spend_over_threshold
        )
    configured = fanout_target_dicts(portfolio)
    for target in configured:
        FanoutTarget.model_validate(target)

    result: dict[str, Any] = {
        "schema_version": "lrp.seed.replay.v1",
        "via": "router",
        "execute": execute,
        "targets": [row.get("target_id") for row in portfolio],
        "written": 0,
        "skipped": 0,
        "router_request_ids_recorded": True,
        "idempotent": True,
    }
    if not execute:
        result["status"] = "dry_run"
        result["note"] = "paid replay not executed"
        return result

    stats = await run_fanout(
        requests=requests,
        out=out,
        targets=configured,
        via="router",
        base_url=base_url,
        refresh_pricing=True,
        approved_content=True,
        client=client,
        concurrency=concurrency,
        max_total_cost_usd=max_total_cost_usd,
    )
    result.update(stats)
    result["status"] = "completed"
    return result
