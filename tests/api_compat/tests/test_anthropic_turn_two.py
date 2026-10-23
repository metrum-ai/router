# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""ANTH-01..05 (+ cheap ANTH-10): Claude turn-two fidelity via ScriptedScenario."""

from __future__ import annotations

import json

from conftest import DENIED_CALLER, PROMPT_CANARY, TOOL_CANARY
from harness.oracles import json_dumps
from harness.scripted_upstream import FakeUpstream, ScriptedScenario
from harness.sse import sse_events

BASH_TOOL = {
    "name": "Bash",
    "description": TOOL_CANARY,
    "input_schema": {
        "type": "object",
        "properties": {"command": {"type": "string"}},
        "required": ["command"],
    },
}
LOOKUP_TOOL = {
    "name": "lookup",
    "description": TOOL_CANARY,
    "input_schema": {
        "type": "object",
        "properties": {"q": {"type": "string"}},
        "required": ["q"],
    },
}
ECHO_TOOL = {
    "name": "echo",
    "description": "echo",
    "input_schema": {
        "type": "object",
        "properties": {"text": {"type": "string"}},
        "required": ["text"],
    },
}

THINKING_TEXT = "Plan the Bash call."
SIGNATURE = "sig_opaque_turn2_abcXYZ+/="
REDACTED_DATA = "redacted_thinking_opaque_bytes_9f3a"
TOOL_ID_A = "toolu_bash_1"
TOOL_ID_B = "toolu_lookup_2"
CACHE_CONTROL = {"type": "ephemeral"}


def _body(raw: bytes) -> dict:
    return json.loads(raw)


def _assistant_thinking_tool(*, tool_id: str = TOOL_ID_A, command: str = "pwd") -> list[dict]:
    return [
        {"type": "thinking", "thinking": THINKING_TEXT, "signature": SIGNATURE},
        {
            "type": "tool_use",
            "id": tool_id,
            "name": "Bash",
            "input": {"command": command},
            "cache_control": CACHE_CONTROL,
        },
    ]


def _unary_tool_message(*, msg_id: str, content: list[dict], stop_reason: str = "tool_use") -> dict:
    return {
        "id": msg_id,
        "type": "message",
        "role": "assistant",
        "model": "synthetic-messages",
        "stop_reason": stop_reason,
        "content": content,
        "usage": {"input_tokens": 11, "output_tokens": 7},
    }


def _frame(event: str, payload: dict) -> str:
    return f"event: {event}\ndata: {json_dumps(payload)}\n\n"


def _thinking_tool_stream_frames(*, msg_id: str = "msg_stream_turn1") -> list[str]:
    """ANTH-04/05: full Messages SSE with thinking, signature, tool input JSON, and ping."""
    return [
        _frame(
            "message_start",
            {
                "type": "message_start",
                "message": {
                    "id": msg_id,
                    "type": "message",
                    "role": "assistant",
                    "model": "synthetic-messages",
                    "content": [],
                    "usage": {"input_tokens": 11, "output_tokens": 0},
                },
            },
        ),
        _frame("ping", {"type": "ping"}),
        _frame(
            "content_block_start",
            {
                "type": "content_block_start",
                "index": 0,
                "content_block": {"type": "thinking", "thinking": "", "signature": ""},
            },
        ),
        _frame(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "thinking_delta", "thinking": THINKING_TEXT},
            },
        ),
        _frame(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "signature_delta", "signature": SIGNATURE},
            },
        ),
        _frame("content_block_stop", {"type": "content_block_stop", "index": 0}),
        _frame(
            "content_block_start",
            {
                "type": "content_block_start",
                "index": 1,
                "content_block": {
                    "type": "tool_use",
                    "id": TOOL_ID_A,
                    "name": "Bash",
                    "input": {},
                },
            },
        ),
        _frame(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 1,
                "delta": {"type": "input_json_delta", "partial_json": '{"command":'},
            },
        ),
        _frame(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 1,
                "delta": {"type": "input_json_delta", "partial_json": '"pwd"}'},
            },
        ),
        _frame("content_block_stop", {"type": "content_block_stop", "index": 1}),
        _frame(
            "message_delta",
            {
                "type": "message_delta",
                "delta": {"stop_reason": "tool_use"},
                "usage": {"output_tokens": 7},
            },
        ),
        _frame("message_stop", {"type": "message_stop"}),
    ]


def _reconstruct_stream_blocks(raw: bytes) -> tuple[list[dict], str | None]:
    """Rebuild content blocks from Anthropic SSE; ignore pings."""
    blocks: dict[int, dict] = {}
    stop_reason = None
    for name, payload in sse_events(raw):
        if name == "ping" or (isinstance(payload, dict) and payload.get("type") == "ping"):
            continue
        if not isinstance(payload, dict):
            continue
        typ = payload.get("type") or name
        if typ == "content_block_start":
            idx = int(payload["index"])
            block = dict(payload["content_block"])
            if block.get("type") == "thinking":
                block.setdefault("thinking", "")
                block.setdefault("signature", "")
            if block.get("type") == "tool_use":
                block["_json"] = ""
            if block.get("type") == "text":
                block.setdefault("text", "")
            blocks[idx] = block
        elif typ == "content_block_delta":
            idx = int(payload["index"])
            delta = payload["delta"]
            block = blocks[idx]
            dtype = delta.get("type")
            if dtype == "thinking_delta":
                block["thinking"] = block.get("thinking", "") + delta.get("thinking", "")
            elif dtype == "signature_delta":
                block["signature"] = block.get("signature", "") + delta.get("signature", "")
            elif dtype == "text_delta":
                block["text"] = block.get("text", "") + delta.get("text", "")
            elif dtype == "input_json_delta":
                block["_json"] = block.get("_json", "") + delta.get("partial_json", "")
        elif typ == "content_block_stop":
            idx = int(payload["index"])
            block = blocks[idx]
            if block.get("type") == "tool_use" and "_json" in block:
                block["input"] = json.loads(block.pop("_json") or "{}")
        elif typ == "message_delta":
            stop_reason = (payload.get("delta") or {}).get("stop_reason")
    ordered = [blocks[i] for i in sorted(blocks)]
    return ordered, stop_reason


def _assert_turn_two_upstream(call: dict, *, assistant_content: list[dict], tool_result: dict) -> None:
    body = call["body"]
    assert call["path"].endswith("/messages")
    assert body["model"] == "synthetic-messages"
    messages = body["messages"]
    assert messages[0]["role"] == "user"
    assert messages[0]["content"] == PROMPT_CANARY or (
        isinstance(messages[0]["content"], list)
        and any(part.get("text") == PROMPT_CANARY for part in messages[0]["content"] if isinstance(part, dict))
    )
    # system may be top-level or first message depending on client; accept either preserved.
    if "system" in body:
        system = body["system"]
        if isinstance(system, list):
            assert any(isinstance(b, dict) and b.get("text") == "be careful" for b in system)
        else:
            assert "be careful" in str(system)
    assistant = messages[-2]
    assert assistant["role"] == "assistant"
    assert assistant["content"] == assistant_content
    follow = messages[-1]
    assert follow["role"] == "user"
    assert follow["content"][0] == tool_result


def test_anthropic_unary_turn_two_preserves_thinking_and_tools(api, router):
    """ANTH-01/05: unary tool+thinking response survives turn-two forwarding."""
    scenario = ScriptedScenario()
    assistant_content = _assistant_thinking_tool()
    tool_result = {
        "type": "tool_result",
        "tool_use_id": TOOL_ID_A,
        "content": "wrote solver.py",
    }

    def validate_first(call: dict) -> None:
        body = call["body"]
        assert body.get("stream") in (None, False)
        assert body["tools"][0]["name"] == "Bash"
        assert body["messages"][0]["content"] == PROMPT_CANARY
        if "system" in body:
            assert body["system"]

    def validate_second(call: dict) -> None:
        _assert_turn_two_upstream(call, assistant_content=assistant_content, tool_result=tool_result)
        thinking = call["body"]["messages"][-2]["content"][0]
        assert thinking["signature"] == SIGNATURE
        assert thinking["thinking"] == THINKING_TEXT
        tool_use = call["body"]["messages"][-2]["content"][1]
        assert tool_use["cache_control"] == CACHE_CONTROL

    scenario.expect(
        path_suffix="/messages",
        validate=validate_first,
        response=_unary_tool_message(msg_id="msg_unary_1", content=assistant_content),
    )
    scenario.expect(
        path_suffix="/messages",
        validate=validate_second,
        response=_unary_tool_message(
            msg_id="msg_unary_2",
            content=[{"type": "text", "text": "done"}],
            stop_reason="end_turn",
        ),
    )
    FakeUpstream.scenario = scenario

    status, _, raw = api(
        "/anthropic/v1/messages",
        {
            "model": "messages",
            "max_tokens": 256,
            "system": [{"type": "text", "text": "be careful"}],
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
            "tools": [BASH_TOOL],
        },
    )
    assert status == 200
    first = _body(raw)
    assert first["stop_reason"] == "tool_use"
    assert first["content"] == assistant_content

    status, _, raw = api(
        "/anthropic/v1/messages",
        {
            "model": "messages",
            "max_tokens": 256,
            "system": [{"type": "text", "text": "be careful"}],
            "messages": [
                {"role": "user", "content": PROMPT_CANARY},
                {"role": "assistant", "content": first["content"]},
                {"role": "user", "content": [tool_result, {"type": "text", "text": "continue"}]},
            ],
            "tools": [BASH_TOOL],
        },
    )
    assert status == 200
    assert _body(raw)["content"] == [{"type": "text", "text": "done"}]
    scenario.assert_complete()


def test_anthropic_streamed_turn_two_preserves_thinking_and_tools(api, router):
    """ANTH-01/04/05: streamed thinking+tool fragments reconstruct and continue."""
    scenario = ScriptedScenario()
    reconstructed_holder: dict[str, list[dict]] = {}
    tool_result = {
        "type": "tool_result",
        "tool_use_id": TOOL_ID_A,
        "content": [{"type": "text", "text": "ok"}],
    }

    def validate_first(call: dict) -> None:
        assert call["body"].get("stream") is True
        assert call["body"]["tools"][0]["name"] == "Bash"

    def validate_second(call: dict) -> None:
        _assert_turn_two_upstream(
            call,
            assistant_content=reconstructed_holder["blocks"],
            tool_result=tool_result,
        )

    scenario.expect(
        path_suffix="/messages",
        validate=validate_first,
        response=_thinking_tool_stream_frames(),
    )
    scenario.expect(
        path_suffix="/messages",
        validate=validate_second,
        response=_unary_tool_message(
            msg_id="msg_stream_turn2",
            content=[{"type": "text", "text": "streamed-final"}],
            stop_reason="end_turn",
        ),
    )
    FakeUpstream.scenario = scenario

    status, headers, raw = api(
        "/anthropic/v1/messages",
        {
            "model": "messages",
            "max_tokens": 256,
            "stream": True,
            "system": [{"type": "text", "text": "be careful"}],
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
            "tools": [BASH_TOOL],
        },
    )
    assert status == 200 and "text/event-stream" in headers["Content-Type"]
    names = [name for name, _ in sse_events(raw)]
    assert "ping" in names
    assert names.count("content_block_start") == 2
    assert names[-1] == "message_stop"
    # Pings must not become text content.
    text_deltas = [
        payload["delta"].get("text")
        for name, payload in sse_events(raw)
        if isinstance(payload, dict)
        and payload.get("type") == "content_block_delta"
        and payload.get("delta", {}).get("type") == "text_delta"
    ]
    assert text_deltas == []

    blocks, stop_reason = _reconstruct_stream_blocks(raw)
    assert stop_reason == "tool_use"
    assert blocks[0]["type"] == "thinking"
    assert blocks[0]["thinking"] == THINKING_TEXT
    assert blocks[0]["signature"] == SIGNATURE
    assert blocks[1]["type"] == "tool_use"
    assert blocks[1]["id"] == TOOL_ID_A
    assert blocks[1]["input"] == {"command": "pwd"}
    # Streamed tool_use start does not include cache_control; preserve reconstructed shape.
    reconstructed_holder["blocks"] = [
        {"type": "thinking", "thinking": blocks[0]["thinking"], "signature": blocks[0]["signature"]},
        {
            "type": "tool_use",
            "id": blocks[1]["id"],
            "name": blocks[1]["name"],
            "input": blocks[1]["input"],
            "cache_control": CACHE_CONTROL,
        },
    ]

    status, _, raw = api(
        "/anthropic/v1/messages",
        {
            "model": "messages",
            "max_tokens": 256,
            "system": [{"type": "text", "text": "be careful"}],
            "messages": [
                {"role": "user", "content": PROMPT_CANARY},
                {"role": "assistant", "content": reconstructed_holder["blocks"]},
                {"role": "user", "content": [tool_result, {"type": "text", "text": "continue"}]},
            ],
            "tools": [BASH_TOOL],
        },
    )
    assert status == 200
    assert _body(raw)["content"][0]["text"] == "streamed-final"
    scenario.assert_complete()


def test_anthropic_parallel_tools_and_result_shapes(api, router):
    """ANTH-02: parallel tools and varied tool_result shapes preserve IDs/flags."""
    scenario = ScriptedScenario()
    parallel = [
        {
            "type": "tool_use",
            "id": TOOL_ID_A,
            "name": "Bash",
            "input": {"command": "ls"},
        },
        {
            "type": "tool_use",
            "id": TOOL_ID_B,
            "name": "lookup",
            "input": {"q": "alpha"},
        },
        {
            "type": "tool_use",
            "id": "toolu_echo_3",
            "name": "echo",
            "input": {"text": "hi"},
        },
    ]
    results = [
        {"type": "tool_result", "tool_use_id": TOOL_ID_B, "content": ""},
        {
            "type": "tool_result",
            "tool_use_id": TOOL_ID_A,
            "content": [{"type": "text", "text": "files"}],
        },
        {
            "type": "tool_result",
            "tool_use_id": "toolu_echo_3",
            "content": "boom",
            "is_error": True,
        },
    ]

    def validate_second(call: dict) -> None:
        messages = call["body"]["messages"]
        assert [b["id"] for b in messages[-2]["content"]] == [TOOL_ID_A, TOOL_ID_B, "toolu_echo_3"]
        got = messages[-1]["content"]
        assert got == results
        assert got[2]["is_error"] is True

    scenario.expect(
        path_suffix="/messages",
        response=_unary_tool_message(msg_id="msg_par_1", content=parallel),
    )
    scenario.expect(
        path_suffix="/messages",
        validate=validate_second,
        response=_unary_tool_message(
            msg_id="msg_par_2",
            content=[{"type": "text", "text": "parallel-ok"}],
            stop_reason="end_turn",
        ),
    )
    FakeUpstream.scenario = scenario

    status, _, raw = api(
        "/anthropic/v1/messages",
        {
            "model": "messages",
            "max_tokens": 128,
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
            "tools": [BASH_TOOL, LOOKUP_TOOL, ECHO_TOOL],
        },
    )
    assert status == 200
    first = _body(raw)["content"]
    assert [b["id"] for b in first] == [TOOL_ID_A, TOOL_ID_B, "toolu_echo_3"]

    status, _, raw = api(
        "/anthropic/v1/messages",
        {
            "model": "messages",
            "max_tokens": 128,
            "messages": [
                {"role": "user", "content": PROMPT_CANARY},
                {"role": "assistant", "content": first},
                {"role": "user", "content": results},
            ],
            "tools": [BASH_TOOL, LOOKUP_TOOL, ECHO_TOOL],
        },
    )
    assert status == 200
    assert _body(raw)["content"][0]["text"] == "parallel-ok"
    scenario.assert_complete()


def test_anthropic_tool_result_ordering_preserved(api, router):
    """ANTH-03: valid adjacency preserved; mismatched ID order not silently fixed."""
    scenario = ScriptedScenario()
    assistant = [
        {"type": "tool_use", "id": "toolu_x", "name": "lookup", "input": {"q": "1"}},
        {"type": "tool_use", "id": "toolu_y", "name": "lookup", "input": {"q": "2"}},
    ]
    # Deliberately reverse result order relative to tool_use emission.
    wrong_order = [
        {"type": "tool_result", "tool_use_id": "toolu_y", "content": "second"},
        {"type": "tool_result", "tool_use_id": "toolu_x", "content": "first"},
        {"type": "text", "text": "extra"},
    ]

    def validate_second(call: dict) -> None:
        content = call["body"]["messages"][-1]["content"]
        assert [c.get("tool_use_id") for c in content if c.get("type") == "tool_result"] == [
            "toolu_y",
            "toolu_x",
        ]
        assert content == wrong_order

    scenario.expect(
        path_suffix="/messages",
        response=_unary_tool_message(msg_id="msg_ord_1", content=assistant),
    )
    scenario.expect(
        path_suffix="/messages",
        validate=validate_second,
        response=_unary_tool_message(
            msg_id="msg_ord_2",
            content=[{"type": "text", "text": "order-ok"}],
            stop_reason="end_turn",
        ),
    )
    FakeUpstream.scenario = scenario

    status, _, raw = api(
        "/anthropic/v1/messages",
        {
            "model": "messages",
            "max_tokens": 64,
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
            "tools": [LOOKUP_TOOL],
        },
    )
    assert status == 200
    first = _body(raw)["content"]
    status, _, raw = api(
        "/anthropic/v1/messages",
        {
            "model": "messages",
            "max_tokens": 64,
            "messages": [
                {"role": "user", "content": PROMPT_CANARY},
                {"role": "assistant", "content": first},
                {"role": "user", "content": wrong_order},
            ],
            "tools": [LOOKUP_TOOL],
        },
    )
    assert status == 200
    scenario.assert_complete()


def test_anthropic_sse_lifecycle_and_pings(api, router):
    """ANTH-04: indexed lifecycle + ping + split text/json fragments."""
    scenario = ScriptedScenario()
    frames = [
        _frame(
            "message_start",
            {
                "type": "message_start",
                "message": {
                    "id": "msg_life",
                    "type": "message",
                    "role": "assistant",
                    "model": "synthetic-messages",
                    "content": [],
                    "usage": {"input_tokens": 3, "output_tokens": 0},
                },
            },
        ),
        _frame("ping", {"type": "ping"}),
        _frame(
            "content_block_start",
            {
                "type": "content_block_start",
                "index": 0,
                "content_block": {"type": "text", "text": ""},
            },
        ),
        _frame(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "text_delta", "text": "hel"},
            },
        ),
        _frame(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "text_delta", "text": "lo"},
            },
        ),
        _frame("content_block_stop", {"type": "content_block_stop", "index": 0}),
        _frame(
            "message_delta",
            {
                "type": "message_delta",
                "delta": {"stop_reason": "end_turn"},
                "usage": {"output_tokens": 2},
            },
        ),
        _frame("message_stop", {"type": "message_stop"}),
    ]
    scenario.expect(path_suffix="/messages", response=frames)
    FakeUpstream.scenario = scenario

    status, headers, raw = api(
        "/anthropic/v1/messages",
        {
            "model": "messages",
            "max_tokens": 32,
            "stream": True,
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
        },
    )
    assert status == 200 and "text/event-stream" in headers["Content-Type"]
    events = sse_events(raw)
    names = [name for name, _ in events]
    assert names[0] == "message_start"
    assert "ping" in names
    assert names.index("content_block_delta") < names.index("message_delta") < names.index("message_stop")
    blocks, stop = _reconstruct_stream_blocks(raw)
    assert stop == "end_turn"
    assert blocks == [{"type": "text", "text": "hello"}]
    scenario.assert_complete()


def test_anthropic_thinking_deltas_and_redacted_blocks(api, router):
    """ANTH-05: thinking/signature deltas and redacted_thinking opaque data."""
    scenario = ScriptedScenario()
    frames = [
        _frame(
            "message_start",
            {
                "type": "message_start",
                "message": {
                    "id": "msg_think",
                    "type": "message",
                    "role": "assistant",
                    "model": "synthetic-messages",
                    "content": [],
                    "usage": {"input_tokens": 5, "output_tokens": 0},
                },
            },
        ),
        _frame(
            "content_block_start",
            {
                "type": "content_block_start",
                "index": 0,
                "content_block": {"type": "thinking", "thinking": "", "signature": ""},
            },
        ),
        _frame(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "thinking_delta", "thinking": "part-a-"},
            },
        ),
        _frame(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "thinking_delta", "thinking": "part-b"},
            },
        ),
        _frame(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "signature_delta", "signature": SIGNATURE},
            },
        ),
        _frame("content_block_stop", {"type": "content_block_stop", "index": 0}),
        _frame(
            "content_block_start",
            {
                "type": "content_block_start",
                "index": 1,
                "content_block": {"type": "redacted_thinking", "data": REDACTED_DATA},
            },
        ),
        _frame("content_block_stop", {"type": "content_block_stop", "index": 1}),
        _frame(
            "content_block_start",
            {
                "type": "content_block_start",
                "index": 2,
                "content_block": {"type": "text", "text": ""},
            },
        ),
        _frame(
            "content_block_delta",
            {
                "type": "content_block_delta",
                "index": 2,
                "delta": {"type": "text_delta", "text": "visible"},
            },
        ),
        _frame("content_block_stop", {"type": "content_block_stop", "index": 2}),
        _frame(
            "message_delta",
            {
                "type": "message_delta",
                "delta": {"stop_reason": "end_turn"},
                "usage": {"output_tokens": 4},
            },
        ),
        _frame("message_stop", {"type": "message_stop"}),
    ]

    def validate_continuation(call: dict) -> None:
        assistant = call["body"]["messages"][-2]["content"]
        assert assistant[0] == {
            "type": "thinking",
            "thinking": "part-a-part-b",
            "signature": SIGNATURE,
        }
        assert assistant[1] == {"type": "redacted_thinking", "data": REDACTED_DATA}
        assert assistant[2] == {"type": "text", "text": "visible"}

    scenario.expect(path_suffix="/messages", response=frames)
    scenario.expect(
        path_suffix="/messages",
        validate=validate_continuation,
        response=_unary_tool_message(
            msg_id="msg_think2",
            content=[{"type": "text", "text": "continued"}],
            stop_reason="end_turn",
        ),
    )
    FakeUpstream.scenario = scenario

    status, _, raw = api(
        "/anthropic/v1/messages",
        {
            "model": "messages",
            "max_tokens": 64,
            "stream": True,
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
        },
    )
    assert status == 200
    blocks, stop = _reconstruct_stream_blocks(raw)
    assert stop == "end_turn"
    assert blocks[0]["thinking"] == "part-a-part-b"
    assert blocks[0]["signature"] == SIGNATURE
    assert blocks[1] == {"type": "redacted_thinking", "data": REDACTED_DATA}
    assert blocks[2]["text"] == "visible"
    # Opaque values must appear verbatim in the forwarded SSE (no truncation/synthesis).
    assert SIGNATURE.encode() in raw
    assert REDACTED_DATA.encode() in raw

    status, _, raw = api(
        "/anthropic/v1/messages",
        {
            "model": "messages",
            "max_tokens": 64,
            # Tools keep native Anthropic passthrough so thinking/redacted blocks
            # are not collapsed through IR text encoding.
            "tools": [BASH_TOOL],
            "messages": [
                {"role": "user", "content": PROMPT_CANARY},
                {
                    "role": "assistant",
                    "content": [
                        {
                            "type": "thinking",
                            "thinking": "part-a-part-b",
                            "signature": SIGNATURE,
                        },
                        {"type": "redacted_thinking", "data": REDACTED_DATA},
                        {"type": "text", "text": "visible"},
                    ],
                },
                {"role": "user", "content": "continue"},
            ],
        },
    )
    assert status == 200
    assert _body(raw)["content"][0]["text"] == "continued"
    scenario.assert_complete()


def test_anthropic_count_tokens_aliases(api, router):
    """ANTH-10: native exact count aliases proxy; denied callers never reach upstream."""
    # Counting must preserve token-bearing tool schemas and cache controls too.
    messages = [{"role": "user", "content": [{
        "type": "text", "text": PROMPT_CANARY,
        "cache_control": {"type": "ephemeral"},
    }]}]
    for path in ("/anthropic/v1/messages/count_tokens", "/v1/messages/count_tokens"):
        FakeUpstream.reset_state()
        scenario = ScriptedScenario().expect(
            path_suffix="/v1/messages/count_tokens", response={"input_tokens": 12345}
        )
        FakeUpstream.scenario = scenario
        status, headers, raw = api(
            path,
            {"model": "messages", "messages": messages, "tools": [BASH_TOOL]},
        )
        assert status == 200, path
        assert _body(raw)["input_tokens"] == 12345
        assert headers["X-Metrum-Usage-Source"] == "reported"
        scenario.assert_complete()
        assert len(router["upstream"].calls) == 1
        forwarded = router["upstream"].calls[0]["body"]
        assert forwarded["model"] == "synthetic-messages"
        assert forwarded["messages"] == messages
        assert forwarded["tools"] == [BASH_TOOL]

    FakeUpstream.reset_state()
    status, _, raw = api(
        "/anthropic/v1/messages/count_tokens",
        {"model": "messages", "messages": [{"role": "user", "content": PROMPT_CANARY}]},
        token=DENIED_CALLER,
    )
    assert status == 403
    assert b"model-not-allowed" in raw
    assert router["upstream"].calls == []
