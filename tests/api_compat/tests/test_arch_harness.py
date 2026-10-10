# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from harness.manifest_loader import load_manifests
from harness.mutations import run_mutation_self_checks
from harness.oracles import (
    assert_chat_terminal_usage,
    assert_responses_terminal_usage,
    chat_include_usage_frames,
    responses_completed_frames_without_done,
)
from harness.scripted_upstream import ScriptedScenario, ScriptedUpstreamError


def test_manifest_loads_arch_cases():
    cases = load_manifests()
    by_id = {c["id"]: c for c in cases}
    assert "ARCH-01" in by_id
    assert "CHAT-USAGE-01" in by_id
    assert "RESP-DONE-01" in by_id
    assert "RESP-01" in by_id
    assert "RESP-02" in by_id
    assert "RESP-06" in by_id
    assert by_id["HARBOR-REAL-AGENT"]["disposition"] == "blocked"
    assert all(c["disposition"] for c in cases)


def test_chat_include_usage_empty_choices_oracle():
    raw = "".join(chat_include_usage_frames()).encode()
    assert_chat_terminal_usage(raw)
    # Strict empty-choices presence check:
    assert any(b'"choices":[]' in frame.encode() for frame in chat_include_usage_frames())


def test_chat_legacy_usage_on_finish_chunk_oracle():
    raw = (
        b'data: {"id":"chatcmpl_synthetic","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"synthetic chat"},"finish_reason":null}]}\n\n'
        b'data: {"id":"chatcmpl_synthetic","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}\n\n'
        b"data: [DONE]\n\n"
    )
    assert_chat_terminal_usage(raw)


def test_responses_oracle_without_done_sentinel():
    raw = "".join(responses_completed_frames_without_done()).encode()
    assert_responses_terminal_usage(raw)


def test_responses_oracle_tolerates_optional_done():
    raw = "".join(responses_completed_frames_without_done()).encode() + b"data: [DONE]\n\n"
    assert_responses_terminal_usage(raw)


def test_scripted_upstream_rejects_unexpected_calls():
    scenario = ScriptedScenario()
    scenario.expect(path_suffix="/chat/completions")
    call = {"path": "/v1/chat/completions", "body": {}, "headers": {}, "attempt_id": "a1"}
    scenario.next_step(call)
    try:
        scenario.next_step({"path": "/extra", "body": {}, "headers": {}, "attempt_id": "a2"})
        raised = False
    except ScriptedUpstreamError:
        raised = True
    assert raised


def test_mutations_are_detected():
    run_mutation_self_checks()
