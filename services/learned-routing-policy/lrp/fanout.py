# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Protected, resumable OpenAI Chat replay with exact-model price evidence."""

from __future__ import annotations

import asyncio
import math
import os
import random
import re
import time
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

import httpx
from pydantic import BaseModel, ConfigDict, Field

from .collect import (
    MAX_ROWS,
    DataError,
    append_row,
    authorize_content,
    existing_rows,
    identity,
    journal,
    response_key,
    strict_json,
    utc_now,
    validate_record,
)

PRICING_URL = "https://openrouter.ai/api/v1/models"
MAX_RESPONSE_BYTES = 2 * 1024 * 1024
SAFE_SCALAR = re.compile(r"[A-Za-z0-9_.:/ -]{1,256}\Z")


class FanoutTarget(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)
    provider: str = Field(min_length=1, max_length=256)
    model: str = Field(min_length=1, max_length=512)
    model_ref: str = Field(default="", max_length=512)
    honors_max_tokens: bool = True
    router_group: str = ""
    # Export the actual temporary group's targets, not just an operator assertion.
    router_targets: list[dict[str, str]] = Field(default_factory=list)
    output_token_field: str = "max_tokens"

    def offline(self) -> dict[str, str]:
        return {
            "provider": self.provider,
            "model": self.model,
            "model_ref": self.model_ref,
        }


def safe_scalar(value: Any) -> str | None:
    return value if isinstance(value, str) and SAFE_SCALAR.fullmatch(value) else None


def finite_nonnegative(value: Any) -> float:
    if isinstance(value, bool) or not isinstance(value, (str, int, float)):
        raise DataError("invalid_numeric_evidence")
    try:
        number = float(value)
    except ValueError:
        raise DataError("invalid_numeric_evidence") from None
    if not math.isfinite(number) or number < 0:
        raise DataError("invalid_numeric_evidence")
    return number


async def bounded_json(
    client: httpx.AsyncClient,
    method: str,
    url: str,
    *,
    timeout_s: float,
    headers: dict[str, str] | None = None,
    body: dict[str, Any] | None = None,
    max_bytes: int = MAX_RESPONSE_BYTES,
) -> tuple[int, dict[str, Any], float, dict[str, str]]:
    """Wall timeout includes headers and body. No redirects or error-body retention."""
    start = time.monotonic()
    async with asyncio.timeout(timeout_s):
        async with client.stream(
            method,
            url,
            headers=headers,
            json=body,
            timeout=timeout_s,
            follow_redirects=False,
        ) as response:
            ttfb = (time.monotonic() - start) * 1000
            metadata = {
                "request_id": safe_scalar(response.headers.get("x-request-id")) or ""
            }
            data = bytearray()
            async for chunk in response.aiter_bytes():
                data.extend(chunk)
                if len(data) > max_bytes:
                    raise DataError("response_size_limit")
            if response.status_code != 200:
                try:
                    parsed = strict_json(bytes(data))
                except DataError:
                    parsed = {}
                # Read bounded usage evidence even on paid failed attempts; discard
                # upstream error messages and all other error-body fields.
                return (
                    response.status_code,
                    {"usage": parsed.get("usage")},
                    ttfb,
                    metadata,
                )
            return response.status_code, strict_json(bytes(data)), ttfb, metadata


async def refresh_prices(
    client: httpx.AsyncClient, models: list[str]
) -> dict[str, dict[str, Any]]:
    status, result, _, _ = await bounded_json(
        client, "GET", PRICING_URL, timeout_s=30, max_bytes=16 * 1024 * 1024
    )
    if status != 200 or not isinstance(result.get("data"), list):
        raise DataError("pricing_refresh_failed")
    prices: dict[str, dict[str, Any]] = {}
    fetched_at = utc_now()
    for row in result["data"]:
        if not isinstance(row, dict) or row.get("id") not in models:
            continue
        slug = row["id"]
        if slug in prices:
            raise DataError("duplicate_pricing_model")
        pricing = row.get("pricing", {})
        if (
            not isinstance(pricing, dict)
            or "prompt" not in pricing
            or "completion" not in pricing
        ):
            continue
        prices[slug] = {
            "input_per_m_usd": finite_nonnegative(pricing["prompt"]) * 1e6,
            "output_per_m_usd": finite_nonnegative(pricing["completion"]) * 1e6,
            "source": PRICING_URL,
            "fetched_at": fetched_at,
        }
    # Never silently use a base model price for :nitro or treat missing rates as zero.
    return prices


def usage_and_cost(
    body: dict[str, Any], pricing: dict[str, Any]
) -> tuple[dict[str, int] | None, float | None, float | None]:
    raw = body.get("usage")
    if not isinstance(raw, dict):
        return None, None, None
    billed = finite_nonnegative(raw["cost"]) if raw.get("cost") is not None else None
    if "prompt_tokens" not in raw or "completion_tokens" not in raw:
        return None, None, billed
    counts = [raw["prompt_tokens"], raw["completion_tokens"]]
    if any(type(n) is not int or n < 0 for n in counts):
        raise DataError("invalid_usage")
    usage = {"input_tokens": counts[0], "output_tokens": counts[1]}
    cost = (
        counts[0] * pricing["input_per_m_usd"] + counts[1] * pricing["output_per_m_usd"]
    ) / 1e6
    return usage, cost, billed


def total_spend(row: dict[str, Any]) -> float | None:
    """Cost across every attempt, unknown if any attempted call has no evidence."""
    if row.get("status") == "ineligible":
        return 0.0
    attempts = row.get("attempts") or [row]
    total = 0.0
    for attempt in attempts:
        cost = attempt.get("billed_cost_usd")
        if cost is None:
            cost = attempt.get("cost_usd")
        if cost is None:
            return None
        total += cost
    return total


def _endpoint(base_url: str) -> str:
    parsed = urlsplit(base_url)
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise DataError("invalid_endpoint")
    if parsed.scheme != "https" and not (
        parsed.scheme == "http" and parsed.hostname in ("127.0.0.1", "localhost", "::1")
    ):
        raise DataError("https_or_loopback_required")
    return base_url.rstrip("/") + "/chat/completions"


async def run_fanout(
    *,
    requests: Path,
    out: Path,
    targets: list[dict[str, Any]],
    via: str = "openrouter",
    base_url: str | None = None,
    refresh_pricing: bool = True,
    approved_content: bool = False,
    client: httpx.AsyncClient | None = None,
    concurrency: int = 4,
    retries: int = 2,
    timeout_s: float = 120,
    allow_uncapped: bool = False,
    max_total_cost_usd: float | None = None,
    seed: int | None = None,
) -> dict[str, int]:
    if (
        via not in ("router", "openrouter")
        or not 1 <= concurrency <= 4
        or not 0 <= retries <= 2
        or not 0 < timeout_s <= 120
    ):
        raise DataError("invalid_fanout_limits")
    if not refresh_pricing:
        raise DataError("fresh_pricing_required")
    if max_total_cost_usd is not None and finite_nonnegative(max_total_cost_usd) <= 0:
        raise DataError("positive_spend_limit_required")
    if allow_uncapped and (max_total_cost_usd is None or concurrency != 1):
        raise DataError("uncapped_requires_serial_spend_guard")
    try:
        configured = [FanoutTarget.model_validate(target) for target in targets]
    except ValueError:
        raise DataError("invalid_fanout_target") from None
    if not 1 <= len(configured) <= 128:
        raise DataError("target_limit")
    keys = [(t.provider, t.model) for t in configured]
    if len(set(keys)) != len(keys):
        raise DataError("duplicate_target_identity")
    for target in configured:
        if target.output_token_field not in ("max_tokens", "max_completion_tokens"):
            raise DataError("invalid_output_token_field")
        if via == "openrouter" and target.provider != "openrouter":
            raise DataError("direct_provider_must_be_openrouter")
        if via == "router" and (
            not target.router_group or target.router_targets != [target.offline()]
        ):
            raise DataError("router_single_target_mapping_required")
    url = _endpoint(
        base_url or ("https://openrouter.ai/api/v1" if via == "openrouter" else "")
    )
    rows = list(
        existing_rows(requests, "request", lambda row: row["request_id"]).values()
    )
    authorize_content(rows, approved_content)
    if len(rows) * len(configured) > MAX_ROWS:
        raise DataError("dataset_limit")
    config_hash = identity(
        {
            "targets": [t.model_dump() for t in configured],
            "via": via,
            "url": url,
            "timeout_s": timeout_s,
            "retries": retries,
            "allow_uncapped": allow_uncapped,
            "max_total_cost_usd": max_total_cost_usd,
            "encoder": "chat-v1",
        }
    )
    headers: dict[str, str] = {}
    owned_client = client is None
    if owned_client:
        token = os.environ.get(
            "OPENROUTER_API_KEY" if via == "openrouter" else "LRP_ROUTER_TOKEN", ""
        )
        if not token:
            raise DataError("credential_required")
        headers["Authorization"] = "Bearer " + token
        client = httpx.AsyncClient(trust_env=False)
    assert client is not None
    stats = {"written": 0, "skipped": 0}
    rng = random.Random(seed)
    try:
        with journal(out) as handle:
            done = existing_rows(out, "response", response_key)
            work: list[tuple[dict[str, Any], FanoutTarget]] = []
            for row in rows:
                for target in configured:
                    key = response_key(
                        {"request_id": row["request_id"], "target": target.offline()}
                    )
                    old = done.get(key)
                    if old:
                        if (
                            old.get("request_hash") != identity(row)
                            or old.get("config_hash") != config_hash
                        ):
                            raise DataError("resume_input_changed")
                        stats["skipped"] += 1
                    else:
                        work.append((row, target))
            if len(done) + len(work) > MAX_ROWS:
                raise DataError("dataset_limit")
            if not work:
                return stats
            prices = await refresh_prices(client, [t.model for t in configured])
            prior_spend = [total_spend(r) for r in done.values()]
            unknown_spend = any(cost is None for cost in prior_spend)
            spend = sum(cost for cost in prior_spend if cost is not None)
            # A global serial guard for explicit uncapped experiments; a stop-before-next
            # request budget is not a hard upstream billing cap.
            serial = asyncio.Semaphore(
                1 if allow_uncapped or max_total_cost_usd else 128 * concurrency
            )
            semaphores = {
                (t.provider, t.model): asyncio.Semaphore(concurrency)
                for t in configured
            }

            async def execute(row: dict[str, Any], target: FanoutTarget) -> None:
                nonlocal spend, unknown_spend
                async with serial, semaphores[(target.provider, target.model)]:
                    pricing = prices.get(target.model)
                    result: dict[str, Any] = {
                        "schema_version": "lrp.response.v1",
                        "request_id": row["request_id"],
                        "target": target.offline(),
                        "source": row["source"],
                        "started_at": utc_now(),
                        "duration_ms": 0.0,
                        "status": "ineligible",
                        "content": "",
                        "tool_calls": [],
                        "request_hash": identity(row),
                        "config_hash": config_hash,
                        "attempts": [],
                    }
                    if pricing:
                        result["pricing"] = pricing
                    cap = row.get("max_tokens") or 0
                    reason = None
                    if pricing is None:
                        reason = "pricing_unavailable"
                    elif row.get("dialect") != "openai-chat":
                        reason = "dialect_not_supported"
                    elif cap and not target.honors_max_tokens:
                        reason = "output_cap_not_honored"
                    elif not cap and not allow_uncapped:
                        reason = "uncapped_not_authorized"
                    elif max_total_cost_usd and unknown_spend:
                        reason = "spend_evidence_unavailable"
                    elif max_total_cost_usd and spend >= max_total_cost_usd:
                        reason = "spend_limit_reached"
                    if reason:
                        result["error_class"] = reason
                    else:
                        assert pricing is not None
                        messages = list(row.get("messages", []))
                        if row.get("system"):
                            messages.insert(
                                0, {"role": "system", "content": row["system"]}
                            )
                        if row.get("input"):
                            messages.append({"role": "user", "content": row["input"]})
                        body: dict[str, Any] = {
                            "model": target.router_group
                            if via == "router"
                            else target.model,
                            "messages": messages,
                            "stream": False,
                        }
                        if cap:
                            body[
                                target.output_token_field
                                if via == "openrouter"
                                else "max_tokens"
                            ] = cap
                        for name in ("tools", "response_format"):
                            if row.get(name):
                                body[name] = row[name]
                        # Provider-scoped extras (for example OpenAI cache controls)
                        # must not leak to other providers in a mixed portfolio.
                        extras = row.get("provider_request_fields")
                        if isinstance(extras, dict):
                            scoped = extras.get(target.provider)
                            if isinstance(scoped, dict):
                                for key, value in scoped.items():
                                    if isinstance(key, str) and key not in body:
                                        body[key] = value
                        start = time.monotonic()
                        for attempt_index in range(retries + 1):
                            for terminal_field in (
                                "usage",
                                "cost_usd",
                                "billed_cost_usd",
                            ):
                                result.pop(terminal_field, None)
                            attempt_start = time.monotonic()
                            attempt: dict[str, Any] = {
                                "sequence": attempt_index + 1,
                                "status": "upstream_error",
                                "duration_ms": 0.0,
                            }
                            retry = False
                            try:
                                status, response, ttfb, metadata = await bounded_json(
                                    client,
                                    "POST",
                                    url,
                                    timeout_s=timeout_s,
                                    headers=headers,
                                    body=body,
                                )
                                attempt["http_status"] = status
                                attempt["ttfb_ms"] = ttfb
                                result["ttfb_ms"] = ttfb
                                if metadata["request_id"] and via == "router":
                                    result["router_request_id"] = metadata["request_id"]
                                usage, cost, billed = usage_and_cost(response, pricing)
                                for name, value in (
                                    ("usage", usage),
                                    ("cost_usd", cost),
                                    ("billed_cost_usd", billed),
                                ):
                                    if value is not None:
                                        result[name] = value
                                        attempt[name] = value
                                if status != 200:
                                    attempt["error_class"] = "http_" + str(status)
                                    retry = status == 429 or status >= 500
                                else:
                                    serving = safe_scalar(response.get("provider"))
                                    if serving:
                                        result["serving_provider"] = serving
                                    choices = response.get("choices")
                                    if (
                                        not isinstance(choices, list)
                                        or len(choices) != 1
                                        or not isinstance(choices[0], dict)
                                    ):
                                        raise DataError("invalid_completion")
                                    choice = choices[0]
                                    message = choice.get("message", {})
                                    content = message.get("content") or ""
                                    if not isinstance(content, str):
                                        raise DataError("invalid_completion")
                                    result["content"] = content
                                    # Explicit projection: no extra provider metadata survives.
                                    tool_calls = message.get("tool_calls") or []
                                    if (
                                        not isinstance(tool_calls, list)
                                        or len(tool_calls) > 128
                                    ):
                                        raise DataError("invalid_tool_calls")
                                    result["tool_calls"] = [
                                        {
                                            "id": c.get("id", ""),
                                            "type": c.get("type"),
                                            "function": {
                                                "name": c.get("function", {}).get(
                                                    "name"
                                                ),
                                                "arguments": c.get("function", {}).get(
                                                    "arguments"
                                                ),
                                            },
                                        }
                                        for c in tool_calls
                                    ]
                                    result["finish_reason"] = (
                                        safe_scalar(choice.get("finish_reason")) or ""
                                    )
                                    attempt["status"] = (
                                        "refused"
                                        if message.get("refusal")
                                        or choice.get("finish_reason")
                                        == "content_filter"
                                        else "ok"
                                    )
                            except (TimeoutError, httpx.TimeoutException):
                                attempt.update(
                                    status="timeout", error_class="upstream_timeout"
                                )
                                retry = True
                            except httpx.HTTPError:
                                attempt["error_class"] = "transport_error"
                                retry = True
                            except (DataError, TypeError, AttributeError):
                                attempt["error_class"] = "invalid_upstream_response"
                                result["content"], result["tool_calls"] = "", []
                            attempt["duration_ms"] = (
                                time.monotonic() - attempt_start
                            ) * 1000
                            result["attempts"].append(attempt)
                            result["status"] = attempt["status"]
                            result["error_class"] = attempt.get("error_class")
                            if not retry or attempt_index == retries:
                                break
                            await asyncio.sleep(
                                rng.uniform(0, min(8, 2**attempt_index))
                            )
                        result["duration_ms"] = (time.monotonic() - start) * 1000
                    validated = validate_record(result, "response")
                    append_row(handle, validated)
                    stats["written"] += 1
                    cost = total_spend(validated)
                    unknown_spend = unknown_spend or cost is None
                    if cost is not None:
                        spend += cost

            # Bounded batches prevent one task per row growing without bound.
            for offset in range(0, len(work), 128):
                try:
                    async with asyncio.TaskGroup() as group:
                        for row, target in work[offset : offset + 128]:
                            group.create_task(execute(row, target))
                except ExceptionGroup as errors:
                    error = errors.exceptions[0]
                    if isinstance(error, DataError):
                        raise error from None
                    raise DataError("fanout_batch_failed") from None
        return stats
    finally:
        if owned_client:
            await client.aclose()
