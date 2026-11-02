# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Conservative, fail-closed per-run budget accounting for the parallel live gate.

A reservation is never released, including when a request times out or omits
usage. This limits future traffic after unknown outcomes. Usage-based amounts
are estimates from published list rates, not provider invoice totals.
"""
from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal, ROUND_UP
import json
import threading


@dataclass(frozen=True)
class Price:
    model: str
    input_per_million: Decimal
    output_per_million: Decimal
    source: str


PRICES = {
    "openai": Price("gpt-4o-mini", Decimal("0.15"), Decimal("0.60"), "https://platform.openai.com/docs/pricing"),
    "anthropic": Price("claude-haiku-4-5", Decimal("1"), Decimal("5"), "https://platform.claude.com/docs/en/about-claude/pricing"),
}
MAX_CAP = Decimal("5")
MILLION = Decimal(1_000_000)


def list_cost(provider: str, input_tokens: int, output_tokens: int) -> Decimal:
    """Calculate list-rate cost from reported tokens (not an invoice)."""
    if any(isinstance(x, bool) or not isinstance(x, int) or x < 0 for x in (input_tokens, output_tokens)):
        raise ValueError("usage must be nonnegative integer tokens")
    price = PRICES[provider]
    return (Decimal(input_tokens) * price.input_per_million + Decimal(output_tokens) * price.output_per_million) / MILLION


class Budget:
    """Reserve pessimistic input/output allowances *before* any provider request.

    This is a software guard, not a provider-side spending limit. It assumes the
    configured text-only list prices and token maxima; change in provider prices,
    billing rules, retries outside this process or non-text charges can violate
    those assumptions. The caller must disable external retries and reconcile
    usage against actual router rows and the provider invoice.
    """

    def __init__(self, cap: Decimal = MAX_CAP):
        if not isinstance(cap, Decimal) or not cap.is_finite() or cap <= 0 or cap > MAX_CAP:
            raise ValueError("cap must be between 0 and $5")
        self.cap = cap
        self.reserved = Decimal(0)
        self.list_usage_cost = Decimal(0)
        self.unknown_usage = 0
        self.blocked = False
        self.lock = threading.Lock()

    def reserve(self, provider: str, body: bytes) -> Decimal:
        """Reserve at an 8x price margin and 4 input tokens per body byte.

        Body must contain the provider's actual output bound. This intentionally
        overestimates typical English text but is not a mathematical guarantee of
        the provider's bill. Reservations remain consumed for the whole run.
        """
        if provider not in ("openai-chat", "openai-responses", "anthropic"):
            raise ValueError("unsupported provider dialect")
        payload = json.loads(body)
        if not isinstance(payload, dict):
            raise ValueError("request must be an object")
        bound = payload.get("max_output_tokens") if provider == "openai-responses" else payload.get("max_tokens")
        if not isinstance(bound, int) or isinstance(bound, bool) or not 256 <= bound <= 2048:
            raise ValueError("request must specify a 256..2048 output-token bound")
        provider = "openai" if provider in ("openai-chat", "openai-responses") else provider
        # Four tokens per UTF-8 byte plus protocol overhead, multiplied by eight
        # for safety against reasonable list-price changes and pricing details.
        input_bound = 4 * len(body) + 4096
        estimate = (list_cost(provider, input_bound, bound) * 8).quantize(Decimal("0.000001"), rounding=ROUND_UP)
        with self.lock:
            if self.blocked or self.reserved + estimate > self.cap:
                raise RuntimeError("live budget halted or exhausted before upstream dispatch")
            self.reserved += estimate
        return estimate

    def settle(self, provider: str, usage: tuple[int, int] | None, reservation: Decimal) -> Decimal | None:
        """Account for reported usage; unknown or underestimated usage halts future calls."""
        price_key = "openai" if provider in ("openai-chat", "openai-responses") else provider
        cost = list_cost(price_key, *usage) if usage is not None else None
        with self.lock:
            # Unknown usage keeps its reservation consumed (never refunded), so the
            # run still cannot exceed the cap, but it does not hide later cells.
            if cost is None:
                self.unknown_usage += 1
            elif cost > reservation:
                # Reported usage above the pessimistic reservation is an accounting
                # surprise: stop dispatching rather than continue under-counting.
                self.blocked = True
            if cost is not None:
                self.list_usage_cost += cost
        return cost


def read_usage(provider: str, body: bytes, stream: bool) -> tuple[int, int] | None:
    """Extract final provider-reported tokens from a JSON or SSE response.

    Anthropic splits input and output counts between message_start and
    message_delta. Missing either is unknown, not zero. Incomplete SSE therefore
    fails closed rather than reporting a fictitious free request.
    """
    if provider not in ("openai-chat", "openai-responses", "anthropic"):
        raise ValueError("unsupported provider dialect")
    input_tokens = output_tokens = None
    terminal = not stream
    anthropic_final_usage = not stream or provider != "anthropic"
    if stream:
        events = []
        for line in body.splitlines():
            if not line.startswith(b"data:"):
                continue
            data = line[5:].strip()
            if not data:
                continue
            if data == b"[DONE]":
                if provider == "openai-chat":
                    terminal = True
                continue
            try:
                item = json.loads(data)
            except (ValueError, UnicodeDecodeError):
                return None
            if not isinstance(item, dict):
                return None
            events.append(item)
    else:
        try:
            result = json.loads(body)
        except (ValueError, UnicodeDecodeError):
            return None
        events = [result] if isinstance(result, dict) else []
    for event in events:
        usage = event.get("usage") or {}
        if provider == "anthropic" and event.get("type") == "message_start":
            message = event.get("message")
            if not isinstance(message, dict):
                return None
            usage = message.get("usage") or {}
        if provider == "anthropic" and event.get("type") == "message_stop":
            terminal = True
        if provider == "openai-responses" and event.get("type") == "response.completed":
            response = event.get("response")
            if not isinstance(response, dict):
                return None
            terminal = True
            usage = response.get("usage") or {}
        if not isinstance(usage, dict):
            return None
        incoming = usage.get("prompt_tokens" if provider == "openai-chat" else "input_tokens")
        outgoing = usage.get("completion_tokens" if provider == "openai-chat" else "output_tokens")
        if isinstance(incoming, int) and not isinstance(incoming, bool) and incoming >= 0:
            input_tokens = incoming
        if isinstance(outgoing, int) and not isinstance(outgoing, bool) and outgoing >= 0:
            output_tokens = outgoing
            if provider == "anthropic" and event.get("type") == "message_delta":
                anthropic_final_usage = True
    if not terminal or not anthropic_final_usage or input_tokens is None or output_tokens is None:
        return None
    return input_tokens, output_tokens
