# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""ANTH-06..09, ANTH-11, ANTH-12: Anthropic capability-profile contracts (issue #94).

Expected behavior is taken from docs-site/docs/reference/api-compatibility.md and
the Anthropic Messages reference, not from router-encoded goldens. Metrum AI.
"""

from __future__ import annotations

import base64
import hashlib
import json
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import pytest

from conftest import CALLER, CALLER_DIGEST, PROMPT_CANARY, TOOL_CANARY, spawn_router, stop_router
from harness.oracles import json_dumps
from harness.scripted_upstream import FakeUpstream, ScriptedScenario
from harness.sse import sse_events


@pytest.fixture(scope="module")
def anth_router(router, tmp_path_factory):
    upstream_port = router["server"].server_address[1]
    upstream_url = f"http://127.0.0.1:{upstream_port}"
    work = tmp_path_factory.mktemp("api-compat-anth-capabilities")
    config = f'''server:
  listen: "127.0.0.1:{{port}}"
  identifiers: {{mode: passthrough}}
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  messages: {{base_url: "{upstream_url}/anthropic", dialect: anthropic}}
  chat: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
models:
  anth-plain:
    strategy: static
    targets:
      - {{provider: messages, model: synthetic-plain, tool_support: {{anthropic_messages: [client_tools]}}}}
  anth-think:
    strategy: static
    targets:
      - provider: messages
        model: synthetic-think
        tool_support: {{anthropic_messages: [client_tools]}}
        reasoning: {{supported: true, mode: opt_in, control: token_budget, min_budget_tokens: 1024, max_budget_tokens: 4096, budget_must_be_less_than_max_tokens: true}}
  anth-vision:
    strategy: static
    targets:
      - {{provider: messages, model: synthetic-text-only, tool_support: {{anthropic_messages: [client_tools]}}}}
      - {{provider: messages, model: synthetic-vision, input_modalities: [text, image], tool_support: {{anthropic_messages: [client_tools]}}}}
  anth-text-only:
    strategy: static
    targets:
      - {{provider: messages, model: synthetic-text-only, tool_support: {{anthropic_messages: [client_tools]}}}}
  anth-bridge:
    strategy: static
    targets:
      - provider: chat
        model: synthetic-anth-bridge-chat
        request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [anth-plain, anth-think, anth-vision, anth-text-only, anth-bridge]
'''
    handle = spawn_router(router, work, config)
    yield handle
    stop_router(handle)


# Independent Anthropic fixtures (shapes from the Messages reference, not router output).
_PNG_BYTES = b"\x89PNG\r\n\x1a\n" + b"anth-11-synthetic-image"
_PNG_B64 = base64.b64encode(_PNG_BYTES).decode("ascii")
_PNG_SHA = hashlib.sha256(_PNG_BYTES).hexdigest()
_PUBLIC_IMAGE_URL = "https://8.8.8.8/anth-11-pixel.png"
LOOKUP_TOOL = {
    "name": "lookup",
    "description": TOOL_CANARY,
    "input_schema": {"type": "object", "properties": {"q": {"type": "string"}}, "required": ["q"]},
}


def _post(anth_router, payload, *, headers=None):
    all_headers = {"Authorization": f"Bearer {CALLER}", "Content-Type": "application/json"}
    all_headers.update(headers or {})
    req = Request(
        anth_router["base"] + "/anthropic/v1/messages",
        data=json.dumps(payload).encode(),
        headers=all_headers,
    )
    try:
        with urlopen(req, timeout=10) as response:
            return response.status, response.read()
    except HTTPError as error:
        return error.code, error.read()


def _send(anth_router, payload, *, scenario=None, headers=None):
    FakeUpstream.reset_state()
    if scenario is not None:
        FakeUpstream.scenario = scenario
    status, raw = _post(anth_router, payload, headers=headers)
    return status, raw, list(FakeUpstream.calls)


def _assert_no_eligible(status, raw, calls):
    """Router-owned rejection: 502 no-eligible-target and zero upstream calls."""
    assert status == 502, raw[:300]
    assert json.loads(raw)["error"]["type"] == "no-eligible-target"
    assert calls == []


def _message(content, *, stop_reason="end_turn", stop_sequence=None, msg_id="msg_anth_cap"):
    return {
        "id": msg_id,
        "type": "message",
        "role": "assistant",
        "model": "synthetic-upstream",
        "content": content,
        "stop_reason": stop_reason,
        "stop_sequence": stop_sequence,
        "usage": {"input_tokens": 5, "output_tokens": 3},
    }


def _frame(event, payload):
    return f"event: {event}\ndata: {json_dumps(payload)}\n\n"


def _user(content=PROMPT_CANARY):
    return [{"role": "user", "content": content}]


def test_anth_06_thinking_controls_follow_capability_profile(anth_router):
    """ANTH-06: thinking modes, budgets and tool_choice follow target reasoning metadata."""
    # Omitted thinking: nothing is synthesized for a target without default thinking.
    status, raw, calls = _send(anth_router, {"model": "anth-plain", "max_tokens": 2000, "messages": _user()})
    assert status == 200, raw[:300]
    assert len(calls) == 1 and "thinking" not in calls[0]["body"]

    # Explicit disabled thinking is not a reasoning request and is forwarded verbatim.
    status, raw, calls = _send(
        anth_router,
        {"model": "anth-plain", "max_tokens": 2000, "thinking": {"type": "disabled"}, "messages": _user()},
    )
    assert status == 200, raw[:300]
    assert calls[0]["body"]["thinking"] == {"type": "disabled"}

    # Manual thinking on a target without reasoning metadata: rejected, never downgraded.
    _assert_no_eligible(
        *_send(
            anth_router,
            {
                "model": "anth-plain",
                "max_tokens": 2000,
                "thinking": {"type": "enabled", "budget_tokens": 1500},
                "messages": _user(),
            },
        )
    )
    # Same for a translated (non-native) target: Anthropic thinking stays on native routes.
    _assert_no_eligible(
        *_send(
            anth_router,
            {
                "model": "anth-bridge",
                "max_tokens": 2000,
                "thinking": {"type": "enabled", "budget_tokens": 1500},
                "messages": _user(),
            },
        )
    )

    # Token-budget target: caller budget inside [min, max] is preserved exactly.
    status, raw, calls = _send(
        anth_router,
        {
            "model": "anth-think",
            "max_tokens": 2000,
            "thinking": {"type": "enabled", "budget_tokens": 1500},
            "messages": _user(),
        },
    )
    assert status == 200, raw[:300]
    body = calls[0]["body"]
    assert body["model"] == "synthetic-think"
    assert body["thinking"] == {"type": "enabled", "budget_tokens": 1500}
    assert body["max_tokens"] == 2000

    # Budget boundaries are clamped to the configured target profile (documented normalization).
    for budget, max_tokens, expected in ((100, 2000, 1024), (9000, 9500, 4096)):
        status, raw, calls = _send(
            anth_router,
            {
                "model": "anth-think",
                "max_tokens": max_tokens,
                "thinking": {"type": "enabled", "budget_tokens": budget},
                "messages": _user(),
            },
        )
        assert status == 200, raw[:300]
        assert calls[0]["body"]["thinking"] == {"type": "enabled", "budget_tokens": expected}
        assert calls[0]["body"]["max_tokens"] == max_tokens

    # budget_must_be_less_than_max_tokens: rejected before any upstream call.
    status, raw, calls = _send(
        anth_router,
        {
            "model": "anth-think",
            "max_tokens": 2000,
            "thinking": {"type": "enabled", "budget_tokens": 2000},
            "messages": _user(),
        },
    )
    assert status == 502, raw[:300]
    assert json.loads(raw)["error"]["details"]["error_class"] == "encode_error"
    assert calls == []

    # Thinking + client tools + caller tool_choice: both forwarded, tool_choice untouched.
    status, raw, calls = _send(
        anth_router,
        {
            "model": "anth-think",
            "max_tokens": 4000,
            "thinking": {"type": "enabled", "budget_tokens": 2048},
            "tools": [LOOKUP_TOOL],
            "tool_choice": {"type": "auto"},
            "messages": _user(),
        },
    )
    assert status == 200, raw[:300]
    body = calls[0]["body"]
    assert body["thinking"] == {"type": "enabled", "budget_tokens": 2048}
    assert body["tool_choice"] == {"type": "auto"}
    assert body["tools"] == [LOOKUP_TOOL]

    # Adaptive thinking + effort are provider-owned native fields: forwarded verbatim to the
    # native target (the router neither rewrites them into a manual budget nor drops them).
    status, raw, calls = _send(
        anth_router,
        {
            "model": "anth-think",
            "max_tokens": 2000,
            "thinking": {"type": "adaptive"},
            "output_config": {"effort": "low"},
            "messages": _user(),
        },
    )
    assert status == 200, raw[:300]
    assert calls[0]["body"]["thinking"] == {"type": "adaptive"}
    assert calls[0]["body"]["output_config"] == {"effort": "low"}


def test_anth_07_eager_input_streaming_fragments_passthrough(anth_router):
    """ANTH-07: per-tool eager_input_streaming and raw input_json fragments pass through natively."""
    tools = [
        dict(LOOKUP_TOOL, name="eager_on", eager_input_streaming=True),
        dict(LOOKUP_TOOL, name="eager_off", eager_input_streaming=False),
        dict(LOOKUP_TOOL, name="eager_omitted"),
    ]
    fragments = ['{"q": "line\\n', "\\u00e9t\\u00e9", '\\"quoted\\" and trunc']
    frames = [
        _frame(
            "message_start",
            {"type": "message_start", "message": _message([], stop_reason=None, msg_id="msg_anth_07")},
        ),
        _frame(
            "content_block_start",
            {
                "type": "content_block_start",
                "index": 0,
                "content_block": {"type": "tool_use", "id": "toolu_anth_07", "name": "eager_on", "input": {}},
            },
        ),
        *[
            _frame(
                "content_block_delta",
                {"type": "content_block_delta", "index": 0, "delta": {"type": "input_json_delta", "partial_json": frag}},
            )
            for frag in fragments
        ],
        _frame("content_block_stop", {"type": "content_block_stop", "index": 0}),
        _frame(
            "message_delta",
            {"type": "message_delta", "delta": {"stop_reason": "max_tokens", "stop_sequence": None}, "usage": {"output_tokens": 9}},
        ),
        _frame("message_stop", {"type": "message_stop"}),
    ]

    def validate(call):
        body = call["body"]
        assert body["stream"] is True
        assert [t["name"] for t in body["tools"]] == ["eager_on", "eager_off", "eager_omitted"]
        assert body["tools"][0]["eager_input_streaming"] is True
        assert body["tools"][1]["eager_input_streaming"] is False
        assert "eager_input_streaming" not in body["tools"][2]
        # Caller beta headers are not forwarded; upstream headers are router-controlled.
        assert not any(k.lower() == "anthropic-beta" for k in call["headers"])

    scenario = ScriptedScenario().expect(path_suffix="/messages", validate=validate, response=frames)
    status, raw, calls = _send(
        anth_router,
        {"model": "anth-plain", "max_tokens": 32, "stream": True, "tools": tools, "messages": _user()},
        scenario=scenario,
        headers={"anthropic-beta": "fine-grained-tool-streaming-2025-05-14"},
    )
    assert status == 200, raw[:300]
    scenario.assert_complete()
    events = sse_events(raw)
    deltas = [
        p["delta"]["partial_json"]
        for _, p in events
        if isinstance(p, dict) and p.get("type") == "content_block_delta"
    ]
    # Fragments arrive byte-exact and in order; nothing is repaired or re-chunked.
    assert deltas == fragments
    accumulated = "".join(deltas)
    with pytest.raises(json.JSONDecodeError):
        json.loads(accumulated)
    stops = [p["delta"]["stop_reason"] for _, p in events if isinstance(p, dict) and p.get("type") == "message_delta"]
    # Truncated input stays classified as max_tokens, so an executor can refuse to run it.
    assert stops == ["max_tokens"]


@pytest.mark.parametrize(
    "stop_reason,stop_sequence",
    [
        ("end_turn", None),
        ("tool_use", None),
        ("stop_sequence", "<<END>>"),
        ("max_tokens", None),
        ("refusal", None),
        ("pause_turn", None),
    ],
)
def test_anth_08_stop_reasons_remain_distinguishable(anth_router, stop_reason, stop_sequence):
    """ANTH-08: every Messages stop class survives unary (with and without tools) and SSE."""
    content = [{"type": "text", "text": f"partial-{stop_reason}"}]
    if stop_reason == "tool_use":
        content.append({"type": "tool_use", "id": "toolu_anth_08", "name": "lookup", "input": {"q": "x"}})
    upstream = _message(content, stop_reason=stop_reason, stop_sequence=stop_sequence, msg_id="msg_anth_08")
    for tools in (None, [LOOKUP_TOOL]):
        payload = {"model": "anth-plain", "max_tokens": 64, "messages": _user()}
        if stop_sequence:
            payload["stop_sequences"] = [stop_sequence]
        if tools:
            payload["tools"] = tools
        scenario = ScriptedScenario().expect(path_suffix="/messages", response=upstream)
        status, raw, calls = _send(anth_router, payload, scenario=scenario)
        assert status == 200, raw[:300]
        scenario.assert_complete()
        if stop_sequence:
            assert calls[0]["body"]["stop_sequences"] == [stop_sequence]
        body = json.loads(raw)
        assert body["stop_reason"] == stop_reason
        assert body.get("stop_sequence") == stop_sequence
        assert body["content"] == content
        assert body["id"] == "msg_anth_08"

    frames = [
        _frame("message_start", {"type": "message_start", "message": _message([], stop_reason=None, msg_id="msg_anth_08s")}),
        _frame("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}),
        _frame(
            "content_block_delta",
            {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": f"partial-{stop_reason}"}},
        ),
        _frame("content_block_stop", {"type": "content_block_stop", "index": 0}),
        _frame(
            "message_delta",
            {
                "type": "message_delta",
                "delta": {"stop_reason": stop_reason, "stop_sequence": stop_sequence},
                "usage": {"output_tokens": 3},
            },
        ),
        _frame("message_stop", {"type": "message_stop"}),
    ]
    scenario = ScriptedScenario().expect(path_suffix="/messages", response=frames)
    status, raw, _ = _send(
        anth_router,
        {"model": "anth-plain", "max_tokens": 64, "stream": True, "messages": _user()},
        scenario=scenario,
    )
    assert status == 200, raw[:300]
    scenario.assert_complete()
    deltas = [p["delta"] for _, p in sse_events(raw) if isinstance(p, dict) and p.get("type") == "message_delta"]
    assert [(d["stop_reason"], d.get("stop_sequence")) for d in deltas] == [(stop_reason, stop_sequence)]


def test_anth_09_native_output_config_and_strict_tools_forwarded(anth_router):
    """ANTH-09 (partial evidence): native output_config.format and strict tools pass verbatim."""
    output_config = {
        "format": {
            "type": "json_schema",
            "schema": {
                "type": "object",
                "properties": {"ticket": {"type": "string"}, "priority": {"enum": ["low", "high"]}},
                "required": ["ticket", "priority"],
                "additionalProperties": False,
            },
        }
    }
    strict_tool = dict(LOOKUP_TOOL, strict=True)
    for tools in (None, [strict_tool]):
        payload = {"model": "anth-plain", "max_tokens": 64, "output_config": output_config, "messages": _user()}
        if tools:
            payload["tools"] = tools
        status, raw, calls = _send(anth_router, payload)
        assert status == 200, raw[:300]
        body = calls[0]["body"]
        assert body["output_config"] == output_config
        # OpenAI structured-output fields are never synthesized for an Anthropic target.
        assert "response_format" not in body and "text" not in body
        if tools:
            assert body["tools"] == [strict_tool]


def test_anth_11_image_sources_and_nested_tool_result_images(anth_router):
    """ANTH-11: URL/file/base64 and tool_result-nested images trigger eligibility; order kept."""
    url_image = {"type": "image", "source": {"type": "url", "url": _PUBLIC_IMAGE_URL}}
    file_image = {"type": "image", "source": {"type": "file", "file_id": "file_anth_11_synthetic"}}
    b64_image = {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": _PNG_B64}}

    def nested_history(result_content):
        return [
            {"role": "user", "content": PROMPT_CANARY},
            {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_anth_11", "name": "lookup", "input": {"q": "x"}}]},
            {
                "role": "user",
                "content": [
                    {"type": "tool_result", "tool_use_id": "toolu_anth_11", "content": result_content},
                    {"type": "text", "text": "continue"},
                ],
            },
        ]

    # Text-only group: every image source form rejects before upstream.
    for image in (url_image, file_image, b64_image):
        _assert_no_eligible(
            *_send(anth_router, {"model": "anth-text-only", "max_tokens": 64, "messages": _user([image])})
        )
    # An image that only appears inside a tool_result still requires an image-capable target.
    _assert_no_eligible(
        *_send(
            anth_router,
            {
                "model": "anth-text-only",
                "max_tokens": 64,
                "tools": [LOOKUP_TOOL],
                "messages": nested_history([b64_image]),
            },
        )
    )

    # Mixed group: nested text + image result routes to the vision target, order and bytes intact.
    result_content = [{"type": "text", "text": "receipt total"}, b64_image]
    status, raw, calls = _send(
        anth_router,
        {"model": "anth-vision", "max_tokens": 64, "tools": [LOOKUP_TOOL], "messages": nested_history(result_content)},
    )
    assert status == 200, raw[:300]
    body = calls[0]["body"]
    assert body["model"] == "synthetic-vision"
    tool_result = body["messages"][2]["content"][0]
    assert tool_result["type"] == "tool_result" and tool_result["tool_use_id"] == "toolu_anth_11"
    assert tool_result["content"] == result_content
    assert body["messages"][2]["content"][1] == {"type": "text", "text": "continue"}
    forwarded = base64.b64decode(tool_result["content"][1]["source"]["data"])
    assert hashlib.sha256(forwarded).hexdigest() == _PNG_SHA

    # Top-level interleaving of URL and base64 sources keeps semantic order.
    content = [{"type": "text", "text": "first"}, url_image, {"type": "text", "text": "second"}, b64_image]
    status, raw, calls = _send(anth_router, {"model": "anth-vision", "max_tokens": 64, "messages": _user(content)})
    assert status == 200, raw[:300]
    assert calls[0]["body"]["model"] == "synthetic-vision"
    assert calls[0]["body"]["messages"][0]["content"] == content


def test_anth_12_native_server_tools_documents_and_unknown_blocks(anth_router):
    """ANTH-12 (partial evidence): native passthrough preserves server tools and opaque blocks."""
    server_tool = {"type": "web_search_20250305", "name": "web_search", "max_uses": 1}
    document = {
        "type": "document",
        "source": {"type": "text", "media_type": "text/plain", "data": "Synthetic doc body."},
        "title": "synthetic",
        "citations": {"enabled": True},
    }
    search_result = {
        "type": "search_result",
        "source": "https://example.invalid/synthetic",
        "title": "synthetic",
        "content": [{"type": "text", "text": "Synthetic result."}],
        "citations": {"enabled": True},
    }
    future_block = {"type": "future_block_20991231", "opaque": {"k": [1, None, False]}}
    user_content = [document, search_result, future_block, {"type": "text", "text": PROMPT_CANARY}]
    response_content = [
        {"type": "server_tool_use", "id": "srvtoolu_anth_12", "name": "web_search", "input": {"query": "q"}},
        {
            "type": "web_search_tool_result",
            "tool_use_id": "srvtoolu_anth_12",
            "content": [{"type": "web_search_result", "url": "https://example.invalid/r", "title": "r", "encrypted_content": "opaque=="}],
        },
        {
            "type": "text",
            "text": "cited",
            "citations": [{"type": "char_location", "cited_text": "Synthetic", "document_index": 0, "start_char_index": 0, "end_char_index": 9}],
        },
    ]
    for tools in ([server_tool], [server_tool, LOOKUP_TOOL]):
        scenario = ScriptedScenario().expect(path_suffix="/messages", response=_message(response_content, msg_id="msg_anth_12"))
        status, raw, calls = _send(
            anth_router,
            {"model": "anth-plain", "max_tokens": 64, "tools": tools, "messages": _user(user_content)},
            scenario=scenario,
        )
        assert status == 200, raw[:300]
        scenario.assert_complete()
        assert calls[0]["body"]["tools"] == tools
        assert calls[0]["body"]["messages"][0]["content"] == user_content
        assert json.loads(raw)["content"] == response_content

    # Translated (non-native) target: a server tool is never flattened into a client tool.
    _assert_no_eligible(
        *_send(
            anth_router,
            {"model": "anth-bridge", "max_tokens": 64, "tools": [server_tool], "messages": _user()},
        )
    )
