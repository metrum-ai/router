# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Unit tests for bounded LRP phase timing helpers."""

from __future__ import annotations

import time

from lrp.phase_metrics import (
    PHASES,
    PhaseSpans,
    bind_phase_spans,
    clear_phase_spans,
    current_phase_spans,
    feature_token_bucket,
    mark_tokenizer_saturation,
    optional_phase,
    token_estimate_from_payload,
)


def test_feature_token_bucket_closed_set():
    assert feature_token_bucket(0) == "0-31"
    assert feature_token_bucket(31) == "0-31"
    assert feature_token_bucket(32) == "32-127"
    assert feature_token_bucket(127) == "32-127"
    assert feature_token_bucket(128) == "128-511"
    assert feature_token_bucket(511) == "128-511"
    assert feature_token_bucket(512) == "512-2047"
    assert feature_token_bucket(2047) == "512-2047"
    assert feature_token_bucket(2048) == "2048+"
    assert feature_token_bucket(None) == "unknown"
    assert feature_token_bucket(float("nan")) == "unknown"
    assert feature_token_bucket(-1) == "unknown"


def test_phase_spans_accumulate_and_optional_noop():
    spans = PhaseSpans()
    with spans.phase("receive"):
        time.sleep(0.01)
    with optional_phase(None, "tokenize"):
        pass
    with optional_phase(spans, "tokenize"):
        time.sleep(0.005)
    assert spans.spans["receive"] >= 0.01
    assert spans.spans["tokenize"] >= 0.005
    assert set(spans.spans) <= set(PHASES)


def test_bind_phase_spans_thread_local():
    spans = PhaseSpans()
    bind_phase_spans(spans)
    try:
        assert current_phase_spans() is spans
        mark_tokenizer_saturation(spans, ids_len=512, max_seq_len=512)
        assert spans.tokenizer_saturated is True
        short = PhaseSpans()
        mark_tokenizer_saturation(short, ids_len=128, max_seq_len=512)
        assert short.tokenizer_saturated is False
    finally:
        clear_phase_spans()
        assert current_phase_spans() is None


def test_token_estimate_from_payload_prefers_context():
    payload = type("Payload", (), {"context": {"estimatedTokens": 640}, "text": "x" * 100})()
    assert token_estimate_from_payload(payload) == 640.0

    text_only = type("TextOnly", (), {"context": {}, "text": "abcd"})()
    # 4 bytes → max(1, (4+3)//4) = 1
    assert token_estimate_from_payload(text_only) == 1.0
