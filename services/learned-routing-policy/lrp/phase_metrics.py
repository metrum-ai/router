# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Bounded-cardinality phase timing for the LRP sidecar request path.

Histograms record wall time only. Labels are closed sets (phase + token bucket).
Prompt text is never logged or used as a label value.
"""

from __future__ import annotations

import math
import threading
import time
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass, field
from typing import Any

# Closed label sets — keep cardinality tiny for Prometheus.
PHASES = (
    "receive",
    "tokenize",
    "embed",
    "featurize",
    "predict",
    "select",
    "respond",
)
TOKEN_BUCKETS = ("0-31", "32-127", "128-511", "512-2047", "2048+", "unknown")

PHASE_LATENCY_BUCKETS = (
    0.0005,
    0.001,
    0.0025,
    0.005,
    0.01,
    0.025,
    0.05,
    0.1,
    0.25,
    0.5,
    1.0,
    2.5,
    5.0,
)

_LOCAL = threading.local()


@dataclass
class PhaseSpans:
    """Per-request phase durations (seconds) and scalar token estimate."""

    spans: dict[str, float] = field(default_factory=dict)
    token_estimate: float | None = None
    tokenizer_saturated: bool = False

    @contextmanager
    def phase(self, name: str) -> Iterator[None]:
        if name not in PHASES:
            raise ValueError(f"unknown phase: {name}")
        started = time.monotonic()
        try:
            yield
        finally:
            self.spans[name] = self.spans.get(name, 0.0) + (time.monotonic() - started)


def bind_phase_spans(spans: PhaseSpans | None) -> None:
    _LOCAL.spans = spans


def current_phase_spans() -> PhaseSpans | None:
    return getattr(_LOCAL, "spans", None)


def clear_phase_spans() -> None:
    _LOCAL.spans = None


@contextmanager
def optional_phase(spans: PhaseSpans | None, name: str) -> Iterator[None]:
    if spans is None:
        yield
        return
    with spans.phase(name):
        yield


def feature_token_bucket(estimated_tokens: float | None) -> str:
    """Map the feature-frame token estimate to a fixed bucket label."""
    if estimated_tokens is None:
        return "unknown"
    try:
        value = float(estimated_tokens)
    except (TypeError, ValueError):
        return "unknown"
    if not math.isfinite(value) or value < 0:
        return "unknown"
    n = int(value)
    if n < 32:
        return "0-31"
    if n < 128:
        return "32-127"
    if n < 512:
        return "128-511"
    if n < 2048:
        return "512-2047"
    return "2048+"


def token_estimate_from_payload(payload: Any) -> float | None:
    """Prefer router context.estimatedTokens; else a scalar text-length heuristic.

    Never returns or stores prompt content — only a non-negative float.
    """
    context = getattr(payload, "context", None)
    if isinstance(context, dict):
        raw = context.get("estimatedTokens")
        if isinstance(raw, bool):
            return None
        if isinstance(raw, (int, float)) and math.isfinite(raw) and raw >= 0:
            return float(raw)
    text = getattr(payload, "text", None)
    if isinstance(text, str) and text:
        byte_count = len(text.encode("utf-8"))
        return float(max(1, (byte_count + 3) // 4))
    return None


def mark_tokenizer_saturation(spans: PhaseSpans | None, *, ids_len: int, max_seq_len: int) -> None:
    """Count left-truncation ceiling hits without recording prompt text."""
    if spans is None:
        return
    if max_seq_len > 0 and ids_len >= max_seq_len:
        spans.tokenizer_saturated = True
