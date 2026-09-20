# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""STREAM P0 fault-injection contracts (issue #94)."""

from __future__ import annotations

import json
import os
import subprocess
import threading
import time
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

import pytest

from conftest import (
    CALLER,
    CALLER_DIGEST,
    PROMPT_CANARY,
    assert_generated_artifacts_redacted,
    request,
    unused_port,
)
from harness.oracles import assert_chat_terminal_usage
from harness.scripted_upstream import FakeUpstream, ScriptedScenario, start_fake_upstream
from harness.sse import classify_sse_payload, parse_sse_frames, sse_events
from harness.stream_faults import (
    attach_stream_faults,
    chat_frames_until_finish,
    chat_refusal_frames,
    chat_truncated_tool_frames,
    first_chat_content_event,
    open_stream,
    read_sse_until,
    reconstruct_sse_from_byte_splits,
    responses_error_event_frames,
    responses_incomplete_frames,
    rich_chat_stream_frames,
    safe_close,
    sse_semantics_chat_frames,
    stream_fault_writes,
)


TOOL = {
    "type": "function",
    "function": {
        "name": "lookup",
        "description": "stream-fault tool",
        "parameters": {"type": "object", "properties": {"q": {"type": "string"}}},
    },
}


def _chat_stream_payload(*, model: str = "chat", tools: bool = False) -> dict:
    body: dict = {
        "model": model,
        "messages": [{"role": "user", "content": PROMPT_CANARY}],
        "stream": True,
        "stream_options": {"include_usage": True},
    }
    if tools:
        body["tools"] = [TOOL]
        body["tool_choice"] = "auto"
    return body


def test_stream_01_byte_split_reconstruction(api, router):
    """STREAM-01: same transcript reconstructs across every-byte segmentation."""
    frames = rich_chat_stream_frames()
    joined = "".join(frames).encode()

    baseline = reconstruct_sse_from_byte_splits(joined, split_bytes=len(joined) or 1)
    for size in (1, 2, 7, 64):
        assert reconstruct_sse_from_byte_splits(joined, split_bytes=size) == baseline

    scenario = ScriptedScenario()
    scenario.expect(
        path_suffix="/chat/completions",
        response=frames,
        split_sse_bytes=1,
    )
    FakeUpstream.scenario = scenario

    status, headers, raw = api("/v1/chat/completions", _chat_stream_payload())
    assert status == 200 and "text/event-stream" in headers["Content-Type"]
    assert_chat_terminal_usage(raw, expected_content='synthetic "café" line\u2014ok')
    scenario.assert_complete()


def test_stream_02_sse_field_semantics(api, router):
    """STREAM-02: LF/CRLF, comments, multiline data, optional-space; malformed ≠ unsupported."""
    # Local oracle: malformed JSON data is distinct from an unsupported event name.
    assert classify_sse_payload('event: vendor.x\ndata: {"ok":true}\n\n') == "unsupported_event"
    assert classify_sse_payload("data: {not-json\n\n") == "malformed_json"
    assert classify_sse_payload("data: [DONE]\n\n") == "ok"

    frames = sse_semantics_chat_frames(content="sse-field-ok")
    wire = "".join(frames)
    # Comments are skipped; optional-space / multiline / CRLF still yield JSON events.
    parsed = parse_sse_frames(wire)
    assert any(name == "vendor.obfuscation" for name, _ in parsed)
    assert classify_sse_payload(wire) == "unsupported_event"
    assert any(
        isinstance(payload, dict)
        and (payload.get("choices") or [{}])[0].get("delta", {}).get("content") == "sse-field-ok"
        for _, payload in sse_events(wire)
    )

    scenario = ScriptedScenario()
    scenario.expect(path_suffix="/chat/completions", response=frames)
    FakeUpstream.scenario = scenario

    status, headers, raw = api("/v1/chat/completions", _chat_stream_payload())
    assert status == 200 and "text/event-stream" in headers["Content-Type"]
    # Unsupported additive event must forward without blocking Chat reconstruction.
    assert b"vendor.obfuscation" in raw
    assert_chat_terminal_usage(raw, expected_content="sse-field-ok")
    scenario.assert_complete()


def _read_stream_or_error(base: str, path: str, payload: dict) -> tuple[int, str, bytes]:
    """Return status, content-type, body for both success streams and JSON errors."""
    headers = {
        "Authorization": f"Bearer {CALLER}",
        "Content-Type": "application/json",
        "Accept": "text/event-stream",
    }
    data = json.dumps(payload).encode()
    try:
        with urlopen(Request(base + path, data=data, headers=headers), timeout=5) as resp:
            return resp.status, resp.headers.get("Content-Type", ""), resp.read()
    except HTTPError as error:
        return error.code, error.headers.get("Content-Type", ""), error.read()
    except (URLError, TimeoutError, ConnectionError) as error:
        return 0, "", str(error).encode()


def test_stream_06_eof_class_classification(api, router):
    """STREAM-06: EOF before first / partial / after finish / after terminal classify correctly."""
    base = router["base"]

    # --- Before first frame: empty SSE body → caller-visible failure, no invented content ---
    FakeUpstream.reset_state()
    empty = ScriptedScenario()
    empty.expect(path_suffix="/chat/completions", response=[])
    attach_stream_faults(empty, close_after_frames=0)
    FakeUpstream.scenario = empty
    with stream_fault_writes():
        status, content_type, body = _read_stream_or_error(
            base, "/v1/chat/completions", _chat_stream_payload()
        )
    assert status != 200 or "event-stream" not in content_type, body
    assert b'"error_class":"empty_stream"' in body or b"empty_stream" in body
    # No invented SSE transcript — JSON error body only.
    assert b"data:" not in body and b"[DONE]" not in body
    empty.assert_complete()

    # --- After partial frame: committed interruption; no finish/[DONE]/invented usage ---
    FakeUpstream.reset_state()
    partial_frames = chat_frames_until_finish(content="partial-eof", include_done=True)
    first = partial_frames[0].encode()
    cut = max(16, len(first) // 2)
    partial = ScriptedScenario()
    partial.expect(path_suffix="/chat/completions", response=partial_frames)
    attach_stream_faults(partial, truncate_bytes=cut)
    FakeUpstream.scenario = partial
    with stream_fault_writes():
        status, content_type, body = _read_stream_or_error(
            base, "/v1/chat/completions", _chat_stream_payload()
        )
    assert status == 200
    assert "event-stream" in content_type
    assert b"[DONE]" not in body
    assert b'"finish_reason":"stop"' not in body
    # Must not synthesize a full successful terminal usage settlement for the client.
    assert b'"prompt_tokens":3' not in body
    partial.assert_complete()

    # --- After finish reason before [DONE] sentinel: Chat success without inventing content ---
    FakeUpstream.reset_state()
    pre_done = ScriptedScenario()
    pre_done.expect(
        path_suffix="/chat/completions",
        response=chat_frames_until_finish(content="finish-no-done", include_done=False),
    )
    FakeUpstream.scenario = pre_done
    status, headers, raw = api("/v1/chat/completions", _chat_stream_payload())
    assert status == 200 and "text/event-stream" in headers["Content-Type"]
    assert b"[DONE]" not in raw
    assert_chat_terminal_usage(raw, expected_content="finish-no-done")
    pre_done.assert_complete()

    # --- After terminal (with sentinel): full success ---
    FakeUpstream.reset_state()
    terminal = ScriptedScenario()
    terminal.expect(
        path_suffix="/chat/completions",
        response=chat_frames_until_finish(content="after-terminal", include_done=True),
    )
    FakeUpstream.scenario = terminal
    status, headers, raw = api("/v1/chat/completions", _chat_stream_payload())
    assert status == 200 and "text/event-stream" in headers["Content-Type"]
    assert b"[DONE]" in raw
    assert_chat_terminal_usage(raw, expected_content="after-terminal")
    terminal.assert_complete()


def test_stream_07_error_refusal_incomplete_never_success(api, router):
    """STREAM-07: error-in-200, truncated tools, refusal, incomplete Responses ≠ success."""

    def accumulate_tool_args(raw: bytes) -> tuple[str, str | None]:
        args = ""
        finish = None
        for _, payload in sse_events(raw):
            if payload == "[DONE]" or not isinstance(payload, dict):
                continue
            choices = payload.get("choices") or []
            if not choices:
                continue
            choice = choices[0]
            if choice.get("finish_reason"):
                finish = choice["finish_reason"]
            for tool in (choice.get("delta") or {}).get("tool_calls") or []:
                fragment = (tool.get("function") or {}).get("arguments")
                if isinstance(fragment, str):
                    args += fragment
        return args, finish

    # --- Truncated tool arguments: partial JSON must not execute ---
    FakeUpstream.reset_state()
    trunc = ScriptedScenario()
    trunc.expect(path_suffix="/chat/completions", response=chat_truncated_tool_frames())
    attach_stream_faults(trunc, close_after_frames=2)
    FakeUpstream.scenario = trunc
    with stream_fault_writes():
        status, content_type, body = _read_stream_or_error(
            router["base"], "/v1/chat/completions", _chat_stream_payload(tools=True)
        )
    assert status == 200 and "event-stream" in content_type
    args, finish = accumulate_tool_args(body)
    assert args == '{"q":"par'
    assert finish is None
    with pytest.raises(json.JSONDecodeError):
        json.loads(args)
    trunc.assert_complete()

    # --- Refusal / content_filter midstream: distinguishable from stop success ---
    FakeUpstream.reset_state()
    refusal = ScriptedScenario()
    refusal.expect(path_suffix="/chat/completions", response=chat_refusal_frames())
    FakeUpstream.scenario = refusal
    status, _, raw = api("/v1/chat/completions", _chat_stream_payload())
    assert status == 200
    events = sse_events(raw)
    finish_reasons = [
        (p.get("choices") or [{}])[0].get("finish_reason")
        for _, p in events
        if isinstance(p, dict) and (p.get("choices") or [{}])[0].get("finish_reason")
    ]
    assert "content_filter" in finish_reasons
    assert "stop" not in finish_reasons
    refusal.assert_complete()

    # --- Responses error event inside HTTP 200: never synthesize completed ---
    FakeUpstream.reset_state()
    err_sc = ScriptedScenario()
    err_sc.expect(path_suffix="/responses", response=responses_error_event_frames())
    FakeUpstream.scenario = err_sc
    status, headers, raw = api(
        "/v1/responses",
        {"model": "responses", "stream": True, "input": PROMPT_CANARY},
    )
    assert status == 200 and "text/event-stream" in headers["Content-Type"]
    names = [n for n, _ in sse_events(raw)]
    assert "error" in names
    assert "response.completed" not in names
    err_sc.assert_complete()

    # --- Incomplete Responses terminal: incomplete ≠ completed ---
    FakeUpstream.reset_state()
    incomplete = ScriptedScenario()
    incomplete.expect(path_suffix="/responses", response=responses_incomplete_frames())
    FakeUpstream.scenario = incomplete
    status, _, raw = api(
        "/v1/responses",
        {"model": "responses", "stream": True, "input": PROMPT_CANARY},
    )
    assert status == 200
    events = sse_events(raw)
    names = [n for n, _ in events]
    assert "response.incomplete" in names
    assert "response.completed" not in names
    payload = next(p for n, p in events if n == "response.incomplete")
    assert payload["response"]["status"] == "incomplete"
    incomplete.assert_complete()


def test_stream_03_gated_incremental_delivery(api, router):
    """STREAM-03: first event is observable before later frames are released."""
    frames = rich_chat_stream_frames(content="gated-first")
    release_rest = threading.Event()
    first_written = threading.Event()
    # Frame 0 writes immediately; frames 1.. wait on release_rest.
    gates = [None] + [release_rest] * (len(frames) - 1)
    signals = [first_written] + [None] * (len(frames) - 1)

    scenario = ScriptedScenario()
    scenario.expect(path_suffix="/chat/completions", response=frames)
    attach_stream_faults(scenario, frame_gates=gates, frame_signals=signals)
    FakeUpstream.scenario = scenario

    with stream_fault_writes():
        resp = open_stream(router["base"], "/v1/chat/completions", _chat_stream_payload(), token=CALLER)
        try:
            assert first_written.wait(timeout=5), "upstream never signaled first frame"
            partial = read_sse_until(resp, predicate=first_chat_content_event, timeout_s=5)
            events = sse_events(partial)
            assert any(
                isinstance(p, dict)
                and (p.get("choices") or [{}])[0].get("delta", {}).get("content") == "gated-first"
                for _, p in events
            ), partial
            # Later terminal frames must not be present until the barrier is released.
            assert not any(p == "[DONE]" for _, p in events)
            release_rest.set()
            remainder = resp.read()
        finally:
            safe_close(resp)

    full = partial + remainder
    assert_chat_terminal_usage(full, expected_content="gated-first")
    scenario.assert_complete()


def test_stream_04_cancel_mid_stream_then_healthy(api, router):
    """STREAM-04: cancel after first event; healthy follow-up stream still works."""
    frames = rich_chat_stream_frames(content="cancel-me")
    release_rest = threading.Event()
    first_written = threading.Event()
    gates = [None] + [release_rest] * (len(frames) - 1)
    signals = [first_written] + [None] * (len(frames) - 1)

    scenario = ScriptedScenario()
    scenario.expect(path_suffix="/chat/completions", response=frames)
    attach_stream_faults(scenario, frame_gates=gates, frame_signals=signals)
    FakeUpstream.scenario = scenario

    with stream_fault_writes():
        resp = open_stream(router["base"], "/v1/chat/completions", _chat_stream_payload(), token=CALLER)
        try:
            assert first_written.wait(timeout=5)
            partial = read_sse_until(resp, predicate=first_chat_content_event, timeout_s=5)
            assert b"cancel-me" in partial
        finally:
            # Client cancel: drop the body without consuming the remainder.
            safe_close(resp)
            release_rest.set()
            # Allow the upstream writer thread to observe the closed socket.
            time.sleep(0.05)

    # Follow-up healthy request must succeed (reservations/session not wedged).
    healthy = ScriptedScenario()
    healthy.expect(
        path_suffix="/chat/completions",
        response=rich_chat_stream_frames(content="after-cancel"),
    )
    FakeUpstream.scenario = healthy
    status, headers, raw = api("/v1/chat/completions", _chat_stream_payload())
    assert status == 200 and "text/event-stream" in headers["Content-Type"]
    assert_chat_terminal_usage(raw, expected_content="after-cancel")
    healthy.assert_complete()


class _FallbackUpstream(FakeUpstream):
    """Isolated class state so STREAM-05 does not share the session FakeUpstream counters."""

    calls: list = []
    scenario: ScriptedScenario | None = None
    chat_include_usage_empty_choices: bool = False
    _attempt_counter: int = 0
    _counter_lock = threading.Lock()


@pytest.fixture
def fallback_router(router, tmp_path_factory):
    """Router with primary+fallback Chat targets sharing one scripted upstream."""
    work = tmp_path_factory.mktemp("stream-fallback")
    _FallbackUpstream.reset_state()
    upstream, thread, upstream_url = start_fake_upstream(_FallbackUpstream)
    port = unused_port()
    config = f'''server:
  listen: "127.0.0.1:{port}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat-fb
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  primary: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
  fallback: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
models:
  chat-fb:
    strategy: static
    targets:
      - {{provider: primary, model: synthetic-primary, tool_support: {{openai_chat: [tools, tool_choice]}}}}
      - {{provider: fallback, model: synthetic-fallback, tool_support: {{openai_chat: [tools, tool_choice]}}}}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [chat-fb]
'''
    config_path = work / "config.yaml"
    config_path.write_text(config)
    binary = Path(router["work"]) / "router"
    assert binary.is_file(), "session fixture must have built the router binary"
    build_env = os.environ | {"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
    process = subprocess.Popen(
        [str(binary), "-config", "config.yaml"],
        cwd=work,
        env=build_env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    base = f"http://127.0.0.1:{port}"
    for _ in range(100):
        try:
            if request(base, "/readyz", token=CALLER)[0] == 200:
                break
        except OSError:
            time.sleep(0.05)
    else:
        process.terminate()
        raise RuntimeError(process.stderr.read())
    config_path.unlink()
    try:
        yield {
            "base": base,
            "work": work,
            "upstream": _FallbackUpstream,
            "process": process,
            "server": upstream,
        }
    finally:
        process.terminate()
        process.wait(timeout=5)
        upstream.shutdown()
        thread.join(timeout=5)
        assert_generated_artifacts_redacted({"work": work})


def test_stream_05_pre_commit_fallback_vs_post_commit_no_replay(fallback_router):
    """STREAM-05: 5xx before commitment may fallback; post-commit must not replay."""
    base = fallback_router["base"]
    Upstream = fallback_router["upstream"]

    # --- Pre-commit: primary 503 with empty body → fallback succeeds ---
    Upstream.reset_state()
    pre = ScriptedScenario()
    pre.expect(path_suffix="/chat/completions", status=503, response={"error": "primary-down"})
    pre.expect(
        path_suffix="/chat/completions",
        response=rich_chat_stream_frames(content="fallback-ok"),
    )
    Upstream.scenario = pre

    status, headers, raw = request(base, "/v1/chat/completions", _chat_stream_payload(model="chat-fb"))
    assert status == 200 and "text/event-stream" in headers["Content-Type"]
    assert_chat_terminal_usage(raw, expected_content="fallback-ok")
    assert len(Upstream.calls) == 2, f"expected primary+fallback, got {len(Upstream.calls)}"
    pre.assert_complete()

    # --- Post-commit: primary emits a tool chunk then ends; fallback must stay idle ---
    Upstream.reset_state()
    tool_frame = (
        'data: {"id":"chatcmpl_commit","object":"chat.completion.chunk",'
        '"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function",'
        '"function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":null}]}\n\n'
    )
    post = ScriptedScenario()
    post.expect(path_suffix="/chat/completions", response=[tool_frame])
    attach_stream_faults(post, close_after_frames=1)
    Upstream.scenario = post

    with stream_fault_writes(Upstream):
        headers = {
            "Authorization": f"Bearer {CALLER}",
            "Content-Type": "application/json",
            "Accept": "text/event-stream",
        }
        data = json.dumps(_chat_stream_payload(model="chat-fb", tools=True)).encode()
        try:
            with urlopen(Request(base + "/v1/chat/completions", data=data, headers=headers), timeout=5) as resp:
                body = resp.read()
                status = resp.status
                content_type = resp.headers.get("Content-Type", "")
        except (HTTPError, URLError, TimeoutError) as exc:
            # Committed streams may surface as incomplete reads; still assert no fallback.
            status = getattr(exc, "code", 200)
            body = getattr(exc, "read", lambda: b"")() if hasattr(exc, "read") else b""
            content_type = "text/event-stream"

    assert status == 200 or b"call_1" in body or b"lookup" in body
    assert "event-stream" in content_type or b"call_1" in body
    assert len(Upstream.calls) == 1, (
        f"post-commit must not fallback/replay; calls={len(Upstream.calls)} body={body!r}"
    )
    assert b"call_1" in body or b"lookup" in body
