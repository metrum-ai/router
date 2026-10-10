# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""RESP-07..10, RESP-12: Responses state, reasoning, controls, tools, and Chat-path opt-in.

Expected behavior comes from the OpenAI Responses reference and the router's
documented contract in docs-site/docs/reference/api-compatibility.md (Metrum AI
Router), not from goldens produced by the router's own encoders. Upstream
fixtures below are hand-written official-shape bodies.
"""

from __future__ import annotations

import copy
import json
from pathlib import Path

import pytest

from conftest import (
    CALLER,
    CALLER_DIGEST,
    DENIED_CALLER_DIGEST,
    PROMPT_CANARY,
    TOOL_CANARY,
    assert_generated_artifacts_redacted,
    request,
    spawn_router,
    stop_router,
)
from harness.sse import sse_events
from harness.scripted_upstream import FakeUpstream, ScriptedScenario

# Opaque reasoning material: must reach the upstream and the caller byte-for-byte
# but never land in router logs, state, or other generated artifacts.
ENCRYPTED_CANARY = "api-compat-encrypted-reasoning-canary-gAAAAB3NzaC1yc2E"
SUMMARY_CANARY = "api-compat-reasoning-summary-canary"

LOOKUP_TOOL = {
    "type": "function",
    "name": "lookup",
    "description": TOOL_CANARY,
    "parameters": {"type": "object", "properties": {"q": {"type": "string"}}, "required": ["q"]},
}

TICKET_SCHEMA = {
    "type": "object",
    "properties": {
        "ticket_id": {"type": "string"},
        "priority": {"type": "string", "enum": ["low", "medium", "high"]},
        "tags": {"type": "array", "items": {"type": "string"}},
        "owner": {"type": ["string", "null"]},
        "note": {"type": "string", "description": "Ünïcødé ✓ description"},
    },
    "required": ["ticket_id", "priority", "tags", "owner", "note"],
    "additionalProperties": False,
}


def _usage(inp=3, out=2):
    return {"input_tokens": inp, "output_tokens": out, "total_tokens": inp + out}


def _message_response(resp_id: str, text: str, **extra) -> dict:
    body = {
        "id": resp_id,
        "object": "response",
        "status": "completed",
        "model": "synthetic-responses",
        "output": [
            {
                "type": "message",
                "id": f"msg_{resp_id}",
                "status": "completed",
                "role": "assistant",
                "content": [{"type": "output_text", "text": text, "annotations": []}],
            }
        ],
        "usage": _usage(),
    }
    body.update(extra)
    return body


@pytest.fixture(scope="module")
def contracts_router(router, tmp_path_factory):
    """Router with structured/reasoning Responses metadata and the Chat-path opt-in enabled."""
    work = tmp_path_factory.mktemp("api-compat-resp-contracts")
    upstream_url = f"http://127.0.0.1:{router['server'].server_address[1]}"
    config = f'''server:
  listen: "127.0.0.1:{{port}}"
  identifiers: {{mode: passthrough}}
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
  openai_compatibility: {{tolerate_responses_body_on_chat_endpoint: true}}
state_path: "{work / 'state.json'}"
providers:
  chat: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
  responses: {{base_url: "{upstream_url}/v1", dialect: openai-responses}}
models:
  responses-native:
    strategy: static
    targets:
      - provider: responses
        model: synthetic-responses
        force_store_false: true
        tool_support: {{openai_responses: [function, tool_choice, structured_outputs]}}
        reasoning: {{supported: true, mode: opt_in, control: effort_enum, supports_summaries: true}}
  responses-basic:
    strategy: static
    targets: [{{provider: responses, model: synthetic-responses-basic, tool_support: {{openai_responses: [function]}}}}]
  responses-to-chat:
    strategy: static
    targets: [{{provider: chat, model: synthetic-bridge-chat, tool_support: {{openai_chat: [tools, tool_choice]}}, responses_to_chat: {{enabled: true, text: true, function_tools: true, tool_choice: true, validation_status: passed}}}}]
  chat:
    strategy: static
    targets: [{{provider: chat, model: synthetic-chat, tool_support: {{openai_chat: [tools, tool_choice]}}}}]
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [responses-native, responses-basic, responses-to-chat, chat]
  - id: synthetic-denied
    token_sha256: {DENIED_CALLER_DIGEST}
    allow: [chat]
'''
    handle = spawn_router(router, work, config)
    yield handle
    stop_router(handle)
    _assert_opaque_canaries_absent(work)
    assert_generated_artifacts_redacted({"work": work})


def _assert_opaque_canaries_absent(work: Path) -> None:
    for path in Path(work).rglob("*"):
        if not path.is_file():
            continue
        contents = path.read_bytes()
        assert ENCRYPTED_CANARY.encode() not in contents, path.name
        assert SUMMARY_CANARY.encode() not in contents, path.name


@pytest.fixture
def capi(contracts_router):
    return lambda path, payload=None, token=CALLER: request(
        contracts_router["base"], path, payload, token
    )


def _json(raw: bytes) -> dict:
    return json.loads(raw)


def _error_type(raw: bytes) -> str:
    return _json(raw)["error"]["type"]


# ---------------------------------------------------------------------------
# RESP-07: previous_response_id continuation and stale/mismatched state.
# ---------------------------------------------------------------------------


def test_resp_07_previous_response_id_continuation(capi, contracts_router):
    """RESP-07: upstream response IDs round-trip; new instructions are not lost or replayed."""
    scenario = ScriptedScenario()

    def turn_one(call):
        body = call["body"]
        assert "previous_response_id" not in body
        assert body["input"] == PROMPT_CANARY
        assert body["instructions"] == "turn one rules"

    def turn_two(call):
        body = call["body"]
        # Continuation uses the exact upstream ID, sends only the new turn, and
        # carries the new instructions (Responses does not inherit instructions).
        assert body["previous_response_id"] == "resp_upstream_turn1"
        assert body["instructions"] == "turn two rules"
        assert body["input"] == [
            {"role": "user", "content": [{"type": "input_text", "text": "second turn"}]}
        ]
        assert PROMPT_CANARY not in json.dumps(body)

    def turn_three(call):
        body = call["body"]
        assert body["previous_response_id"] == "resp_upstream_turn2"
        assert body["input"] == [
            {"type": "function_call_output", "call_id": "call_turn2", "output": '{"ok":true}'}
        ]
        assert body["tools"][0]["name"] == "lookup"

    scenario.expect(path_suffix="/responses", validate=turn_one,
                    response=_message_response("resp_upstream_turn1", "one"))
    scenario.expect(
        path_suffix="/responses",
        validate=turn_two,
        response={
            "id": "resp_upstream_turn2",
            "object": "response",
            "status": "completed",
            "model": "synthetic-responses",
            "output": [
                {
                    "type": "function_call",
                    "id": "fc_turn2",
                    "call_id": "call_turn2",
                    "name": "lookup",
                    "arguments": '{"q":"x"}',
                    "status": "completed",
                }
            ],
            "usage": _usage(),
        },
    )
    scenario.expect(path_suffix="/responses", validate=turn_three,
                    response=_message_response("resp_upstream_turn3", "three"))
    FakeUpstream.scenario = scenario

    status, _, raw = capi(
        "/v1/responses",
        {"model": "responses-native", "input": PROMPT_CANARY, "instructions": "turn one rules"},
    )
    assert status == 200, raw
    first = _json(raw)
    assert first["id"] == "resp_upstream_turn1"

    status, _, raw = capi(
        "/v1/responses",
        {
            "model": "responses-native",
            "previous_response_id": first["id"],
            "instructions": "turn two rules",
            "tools": [LOOKUP_TOOL],
            "input": [{"role": "user", "content": [{"type": "input_text", "text": "second turn"}]}],
        },
    )
    assert status == 200, raw
    second = _json(raw)
    assert second["id"] == "resp_upstream_turn2"
    call = second["output"][0]
    # Output item id and call_id stay distinct; the call_id answers the call.
    assert (call["id"], call["call_id"]) == ("fc_turn2", "call_turn2")

    status, _, raw = capi(
        "/v1/responses",
        {
            "model": "responses-native",
            "previous_response_id": second["id"],
            "tools": [LOOKUP_TOOL],
            "input": [
                {"type": "function_call_output", "call_id": call["call_id"], "output": '{"ok":true}'}
            ],
        },
    )
    assert status == 200, raw
    assert _json(raw)["id"] == "resp_upstream_turn3"
    scenario.assert_complete()


def test_resp_07_stale_previous_response_id_is_not_replayed(capi, contracts_router):
    """RESP-07: provider-owned stale-ID error propagates once; no stateless replay or fallback."""
    scenario = ScriptedScenario()
    scenario.expect(
        path_suffix="/responses",
        validate=lambda call: call["body"]["previous_response_id"] == "resp_expired",
        status=400,
        response={
            "error": {
                "type": "invalid_request_error",
                "code": "previous_response_not_found",
                "message": "Previous response with id 'resp_expired' not found.",
                "param": "previous_response_id",
            }
        },
    )
    FakeUpstream.scenario = scenario
    status, _, raw = capi(
        "/v1/responses",
        {"model": "responses-native", "previous_response_id": "resp_expired", "input": "again"},
    )
    assert 400 <= status < 600 and status != 200, raw
    assert "error" in _json(raw)
    # Exactly one attempt: the router must not retry without the ID (silent history loss).
    assert len(FakeUpstream.calls) == 1
    scenario.assert_complete()


def test_resp_07_native_only_state_rejects_on_stateless_bridge(capi, contracts_router):
    """RESP-07: previous_response_id on a Chat-bridged group rejects before upstream."""
    status, _, raw = capi(
        "/v1/responses",
        {"model": "responses-to-chat", "previous_response_id": "resp_upstream_turn1", "input": "x"},
    )
    assert status == 502, raw
    assert _error_type(raw) == "no-eligible-target"
    assert FakeUpstream.calls == []


# ---------------------------------------------------------------------------
# RESP-08: stateless reasoning item replay with opaque encrypted content.
# ---------------------------------------------------------------------------


def _reasoning_item(item_id="rs_turn1"):
    return {
        "type": "reasoning",
        "id": item_id,
        "summary": [{"type": "summary_text", "text": SUMMARY_CANARY}],
        "encrypted_content": ENCRYPTED_CANARY,
    }


def _replay_history(include_tools: bool):
    history = [
        {"role": "user", "content": [{"type": "input_text", "text": "first"}]},
        _reasoning_item(),
    ]
    if include_tools:
        history += [
            {
                "type": "function_call",
                "id": "fc_turn1",
                "call_id": "call_turn1",
                "name": "lookup",
                "arguments": '{"q":"x"}',
            },
            {"type": "function_call_output", "call_id": "call_turn1", "output": "42"},
        ]
    else:
        history.append(
            {
                "type": "message",
                "role": "assistant",
                "content": [{"type": "output_text", "text": "visible"}],
            }
        )
    history.append({"role": "user", "content": [{"type": "input_text", "text": "next"}]})
    return history


@pytest.mark.parametrize("include_tools", [True, False], ids=["tools", "no-tools"])
def test_resp_08_encrypted_reasoning_replay_unary(capi, contracts_router, include_tools):
    """RESP-08: opaque reasoning items survive request and response; nothing fabricated."""
    history = _replay_history(include_tools)
    caller_body = {
        "model": "responses-native",
        "input": copy.deepcopy(history),
        "include": ["reasoning.encrypted_content"],
        "store": False,
        "reasoning": {"effort": "low", "summary": "auto"},
    }
    if include_tools:
        caller_body["tools"] = [LOOKUP_TOOL]

    def validate(call):
        body = call["body"]
        # Item array, order, IDs and opaque fields reach the upstream unchanged.
        assert body["input"] == history
        assert body["include"] == ["reasoning.encrypted_content"]
        assert body["store"] is False
        assert body["reasoning"] == {"effort": "low", "summary": "auto"}

    # Reasoning plus a tool call and no visible text: no message may be invented.
    upstream_output = [
        _reasoning_item("rs_turn2"),
        {
            "type": "function_call",
            "id": "fc_turn2",
            "call_id": "call_turn2",
            "name": "lookup",
            "arguments": '{"q":"y"}',
            "status": "completed",
        },
    ]
    scenario = ScriptedScenario()
    scenario.expect(
        path_suffix="/responses",
        validate=validate,
        response={
            "id": "resp_reasoning_turn2",
            "object": "response",
            "status": "completed",
            "model": "synthetic-responses",
            "output": copy.deepcopy(upstream_output),
            "usage": {
                "input_tokens": 9,
                "output_tokens": 4,
                "total_tokens": 13,
                "output_tokens_details": {"reasoning_tokens": 3},
            },
        },
    )
    FakeUpstream.scenario = scenario
    status, _, raw = capi("/v1/responses", caller_body)
    assert status == 200, raw
    payload = _json(raw)
    assert payload["id"] == "resp_reasoning_turn2"
    assert payload["output"] == upstream_output
    assert not any(item.get("type") == "message" for item in payload["output"])
    scenario.assert_complete()


def test_resp_08_encrypted_reasoning_native_stream(capi, contracts_router):
    """RESP-08: native SSE carries reasoning output items with opaque content verbatim."""
    reasoning_done = _reasoning_item("rs_stream")
    frames = [
        'event: response.created\ndata: {"type":"response.created","sequence_number":0,"response":{"id":"resp_stream_rs","object":"response","status":"in_progress","output":[]}}\n\n',
        'event: response.output_item.added\ndata: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"rs_stream","summary":[]}}\n\n',
        "event: response.output_item.done\ndata: "
        + json.dumps(
            {"type": "response.output_item.done", "sequence_number": 2, "output_index": 0, "item": reasoning_done}
        )
        + "\n\n",
        "event: response.completed\ndata: "
        + json.dumps(
            {
                "type": "response.completed",
                "sequence_number": 3,
                "response": {
                    "id": "resp_stream_rs",
                    "object": "response",
                    "status": "completed",
                    "output": [reasoning_done],
                    "usage": _usage(4, 3),
                },
            }
        )
        + "\n\n",
    ]

    def validate(call):
        body = call["body"]
        assert body["stream"] is True
        assert body["input"] == _replay_history(True)

    scenario = ScriptedScenario()
    scenario.expect(path_suffix="/responses", validate=validate, response=frames)
    FakeUpstream.scenario = scenario
    status, headers, raw = capi(
        "/v1/responses",
        {
            "model": "responses-native",
            "stream": True,
            "tools": [LOOKUP_TOOL],
            "include": ["reasoning.encrypted_content"],
            "input": _replay_history(True),
        },
    )
    assert status == 200, raw
    assert headers.get("Content-Type", "").startswith("text/event-stream")
    events = [data for _, data in sse_events(raw) if isinstance(data, dict)]
    done = [e for e in events if e.get("type") == "response.output_item.done"]
    assert done and done[0]["item"] == reasoning_done
    completed = [e for e in events if e.get("type") == "response.completed"]
    assert completed[-1]["response"]["output"] == [reasoning_done]
    scenario.assert_complete()


# ---------------------------------------------------------------------------
# RESP-09: text.format, output cap, reasoning, and store/metadata overrides.
# ---------------------------------------------------------------------------


def _validate_ticket(value: dict) -> None:
    """Independent check of TICKET_SCHEMA (no router code involved)."""
    assert set(value) == set(TICKET_SCHEMA["required"])
    assert isinstance(value["ticket_id"], str)
    assert value["priority"] in {"low", "medium", "high"}
    assert isinstance(value["tags"], list) and all(isinstance(t, str) for t in value["tags"])
    assert value["owner"] is None or isinstance(value["owner"], str)
    assert isinstance(value["note"], str)


def test_resp_09_text_format_reasoning_and_store_overrides(capi, contracts_router):
    """RESP-09: schema unchanged, Responses control names kept, store/metadata policy applied."""
    text_format = {
        "format": {
            "type": "json_schema",
            "name": "ticket_extract",
            "strict": True,
            "schema": TICKET_SCHEMA,
        }
    }
    structured_text = json.dumps(
        {"ticket_id": "INC-1234", "priority": "high", "tags": ["db"], "owner": None, "note": "Ünïcødé ✓"},
        ensure_ascii=False,
    )

    def validate(call):
        body = call["body"]
        assert body["text"] == text_format
        assert body["max_output_tokens"] == 321
        assert "max_tokens" not in body and "max_completion_tokens" not in body
        assert body["reasoning"] == {"effort": "high", "summary": "detailed"}
        # force_store_false target: caller store:true is overridden; metadata stripped.
        assert body["store"] is False
        assert "metadata" not in body

    scenario = ScriptedScenario()
    scenario.expect(
        path_suffix="/responses",
        validate=validate,
        response=_message_response("resp_structured", structured_text),
    )
    FakeUpstream.scenario = scenario
    status, _, raw = capi(
        "/v1/responses",
        {
            "model": "responses-native",
            "input": "Extract INC-1234 high",
            "text": copy.deepcopy(text_format),
            "max_output_tokens": 321,
            "reasoning": {"effort": "high", "summary": "detailed"},
            "store": True,
            "metadata": {"trace": "synthetic"},
        },
    )
    assert status == 200, raw
    payload = _json(raw)
    message = next(item for item in payload["output"] if item["type"] == "message")
    _validate_ticket(json.loads(message["content"][0]["text"]))
    scenario.assert_complete()


@pytest.mark.parametrize(
    "extra",
    [
        {"text": {"format": {"type": "json_schema", "name": "t", "schema": TICKET_SCHEMA}}},
        {"reasoning": {"effort": "high"}},
    ],
    ids=["text-format", "reasoning"],
)
def test_resp_09_unsupported_controls_reject_before_upstream(capi, contracts_router, extra):
    """RESP-09: targets without structured/reasoning metadata are never sent stripped requests."""
    payload = {"model": "responses-basic", "input": "x"} | extra
    status, _, raw = capi("/v1/responses", payload)
    assert status == 502, raw
    assert _error_type(raw) == "no-eligible-target"
    assert FakeUpstream.calls == []


# ---------------------------------------------------------------------------
# RESP-10: function/custom/namespace vs hosted vs MCP tool matrix.
# ---------------------------------------------------------------------------


CUSTOM_TOOL = {"type": "custom", "name": "apply_patch", "description": "freeform patch"}
NAMESPACE_TOOL = {"type": "namespace", "name": "multi_tool_use", "tools": []}


def test_resp_10_client_tools_forwarded_and_generic_hosted_stripped(capi, contracts_router):
    """RESP-10: client tools pass through verbatim; generic hosted descriptors are stripped."""

    def validate(call):
        body = call["body"]
        assert body["tools"] == [LOOKUP_TOOL, CUSTOM_TOOL, NAMESPACE_TOOL]

    scenario = ScriptedScenario()
    scenario.expect(
        path_suffix="/responses",
        validate=validate,
        response={
            "id": "resp_custom",
            "object": "response",
            "status": "completed",
            "output": [
                {
                    "type": "custom_tool_call",
                    "id": "ctc_1",
                    "call_id": "call_custom",
                    "name": "apply_patch",
                    "input": "*** Begin Patch\n*** End Patch\n",
                }
            ],
            "usage": _usage(),
        },
    )
    FakeUpstream.scenario = scenario
    status, _, raw = capi(
        "/v1/responses",
        {
            "model": "responses-native",
            "input": "patch it",
            "tools": [
                LOOKUP_TOOL,
                {"type": "web_search", "external_web_access": False},
                CUSTOM_TOOL,
                {"type": "image_generation", "output_format": "png"},
                NAMESPACE_TOOL,
            ],
        },
    )
    assert status == 200, raw
    item = _json(raw)["output"][0]
    # Custom tool calls are returned as-is, not flattened into text.
    assert item["type"] == "custom_tool_call"
    assert item["input"] == "*** Begin Patch\n*** End Patch\n"
    scenario.assert_complete()


@pytest.mark.parametrize(
    "tool",
    [
        {"type": "mcp", "server_label": "x", "server_url": "https://mcp.invalid/sse"},
        {"type": "sse", "url": "https://mcp.invalid/sse"},
        {"type": "file_search", "vector_store_ids": ["vs_synthetic"]},
        {"type": "code_interpreter", "container": {"type": "auto"}},
        {"type": "computer_use_preview", "display_width": 1, "display_height": 1, "environment": "browser"},
    ],
    ids=lambda tool: tool["type"],
)
def test_resp_10_provider_hosted_tools_reject_before_upstream(capi, contracts_router, tool):
    """RESP-10: remote provider-hosted/MCP descriptors are policy rejections, never text-only 200s."""
    status, _, raw = capi(
        "/v1/responses",
        {"model": "responses-native", "input": "x", "tools": [LOOKUP_TOOL, tool]},
    )
    assert status == 400, raw
    assert _error_type(raw) == "provider-hosted-tools-forbidden"
    assert FakeUpstream.calls == []


@pytest.mark.parametrize("tool", [CUSTOM_TOOL, NAMESPACE_TOOL, {"type": "web_search"}], ids=lambda t: t["type"])
def test_resp_10_non_function_tools_skip_stateless_bridge(capi, contracts_router, tool):
    """RESP-10: the Responses-to-Chat bridge only carries function tools; others reject pre-upstream."""
    status, _, raw = capi(
        "/v1/responses",
        {"model": "responses-to-chat", "input": "x", "tools": [LOOKUP_TOOL, tool]},
    )
    assert status == 502, raw
    assert _error_type(raw) == "no-eligible-target"
    assert FakeUpstream.calls == []


# ---------------------------------------------------------------------------
# RESP-12: Responses-shaped body on /v1/chat/completions (opt-in switch).
# ---------------------------------------------------------------------------


def test_resp_12_switch_disabled_rejects_on_default_router(api, router):
    """RESP-12: default router rejects Responses bodies on the Chat path; Chat bodies still work."""
    status, _, raw = api("/v1/chat/completions", {"model": "chat", "input": PROMPT_CANARY})
    assert status == 400, raw
    assert _error_type(raw) == "responses-body-on-chat-endpoint-disabled"
    assert router["upstream"].calls == []
    status, _, raw = api(
        "/v1/chat/completions",
        {"model": "chat", "messages": [{"role": "user", "content": PROMPT_CANARY}]},
    )
    assert status == 200, raw
    assert router["upstream"].calls[0]["path"].endswith("/chat/completions")


def test_resp_12_enabled_unary_tools_and_state_route_as_responses(capi, contracts_router):
    """RESP-12: opt-in Chat path is Responses ingress: native target, cap normalization, state."""

    def validate(call):
        body = call["body"]
        assert call["path"].endswith("/responses")
        assert body["input"] == "use the tool"
        assert body["instructions"] == "compat rules"
        assert body["max_output_tokens"] == 64
        assert "max_tokens" not in body
        assert body["previous_response_id"] == "resp_prior"
        assert body["tools"] == [LOOKUP_TOOL]
        assert body["tool_choice"] == "auto"
        assert "messages" not in body

    scenario = ScriptedScenario()
    scenario.expect(
        path_suffix="/responses",
        validate=validate,
        response={
            "id": "resp_chat_path",
            "object": "response",
            "status": "completed",
            "output": [
                {
                    "type": "function_call",
                    "id": "fc_chat_path",
                    "call_id": "call_chat_path",
                    "name": "lookup",
                    "arguments": '{"q":"z"}',
                }
            ],
            "usage": _usage(),
        },
    )
    FakeUpstream.scenario = scenario
    status, _, raw = capi(
        "/v1/chat/completions",
        {
            "model": "responses-native",
            "input": "use the tool",
            "instructions": "compat rules",
            "max_tokens": 64,
            "previous_response_id": "resp_prior",
            "tools": [LOOKUP_TOOL],
            "tool_choice": "auto",
        },
    )
    assert status == 200, raw
    payload = _json(raw)
    # Responses ingress => Responses-shaped caller object.
    assert payload["object"] == "response"
    assert payload["output"][0]["call_id"] == "call_chat_path"
    scenario.assert_complete()


def test_resp_12_enabled_stream_returns_responses_lifecycle(capi, contracts_router):
    """RESP-12: stream=true on the Chat path yields Responses SSE, not Chat chunks."""
    scenario = ScriptedScenario()
    scenario.expect(
        path_suffix="/responses",
        validate=lambda call: call["body"]["stream"] is True,
        response=None,
    )
    FakeUpstream.scenario = scenario
    status, headers, raw = capi(
        "/v1/chat/completions",
        {"model": "responses-native", "input": "stream please", "stream": True},
    )
    assert status == 200, raw
    assert headers.get("Content-Type", "").startswith("text/event-stream")
    types = [data.get("type") for _, data in sse_events(raw) if isinstance(data, dict)]
    assert types[0] == "response.created"
    assert types[-1] == "response.completed"
    assert b"chat.completion.chunk" not in raw
    scenario.assert_complete()


def test_resp_12_enabled_bridges_to_chat_only_group(capi, contracts_router):
    """RESP-12: a Chat-only group is reached through the validated Responses-to-Chat bridge."""

    def validate(call):
        body = call["body"]
        assert call["path"].endswith("/chat/completions")
        assert body["messages"][-1] == {"role": "user", "content": "bridge me"}

    scenario = ScriptedScenario()
    scenario.expect(path_suffix="/chat/completions", validate=validate, response=None)
    FakeUpstream.scenario = scenario
    status, _, raw = capi(
        "/v1/chat/completions", {"model": "responses-to-chat", "input": "bridge me"}
    )
    assert status == 200, raw
    assert _json(raw)["object"] == "response"
    scenario.assert_complete()


@pytest.mark.parametrize(
    "payload,error_type",
    [
        (
            {"model": "chat", "input": "x", "previous_response_id": "resp_prior"},
            "responses-body-on-chat-endpoint-previous-response-id-unsupported",
        ),
        (
            {"model": "responses-native", "input": "x", "metadata": {"a": "b"}},
            "responses-body-on-chat-endpoint-unsupported-field",
        ),
        (
            {"model": "responses-native", "input": "x", "messages": [{"role": "user", "content": "x"}]},
            "responses-body-on-chat-endpoint-mixed-body",
        ),
        (
            {"model": "responses-native", "input": "x", "tools": [{"type": "web_search"}]},
            "responses-body-on-chat-endpoint-hosted-tools-unsupported",
        ),
        (
            {"model": "responses-native", "input": "x", "max_tokens": 5, "max_output_tokens": 5},
            "responses-body-on-chat-endpoint-output-cap-conflict",
        ),
    ],
    ids=["state-without-native", "unsupported-field", "mixed-body", "hosted-tool", "cap-conflict"],
)
def test_resp_12_enabled_rejections_before_upstream(capi, contracts_router, payload, error_type):
    """RESP-12: documented compatibility rejections are 400s with zero upstream calls."""
    status, _, raw = capi("/v1/chat/completions", payload)
    assert status == 400, raw
    assert _error_type(raw) == error_type
    assert FakeUpstream.calls == []
