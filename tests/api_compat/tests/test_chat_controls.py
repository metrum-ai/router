# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""CHAT-06..10: tool_choice, schema fidelity, structured output, caps, and sampling controls.

Expected behavior comes from docs-site/docs/reference/api-compatibility.md and the
OpenAI Chat Completions reference, not from router encoder output (ARCH-04).
Router-owned rejections assert zero upstream calls (ARCH-02). Metrum AI.
"""

from __future__ import annotations

import copy
import json

import pytest

from conftest import (
    CALLER_DIGEST,
    DENIED_CALLER_DIGEST,
    PROMPT_CANARY,
    TOOL_CANARY,
    request,
    spawn_router,
    stop_router,
)
from harness.scripted_upstream import ScriptedScenario

OMIT = object()

LOOKUP_TOOL = {
    "type": "function",
    "function": {
        "name": "lookup",
        "description": TOOL_CANARY,
        "parameters": {
            "type": "object",
            "properties": {"q": {"type": "string"}},
            "required": ["q"],
        },
    },
}


@pytest.fixture(scope="module")
def controls_router(router, tmp_path_factory):
    """Profile router with structured-output, output-cap encoding and tool_choice limits."""
    work = tmp_path_factory.mktemp("api-compat-chat-controls")
    upstream_url = f"http://127.0.0.1:{router['server'].server_address[1]}"
    chat_tools = "tool_support: {openai_chat: [tools, tool_choice]}"
    config = f'''server:
  listen: "127.0.0.1:{{port}}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat-structured
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  chat: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
models:
  chat-structured: {{strategy: static, targets: [{{provider: chat, model: synthetic-structured, tool_support: {{openai_chat: [tools, tool_choice, structured_outputs]}}}}]}}
  chat-mct: {{strategy: static, targets: [{{provider: chat, model: synthetic-mct, output_token_field: max_completion_tokens, force_store_false: true, {chat_tools}}}]}}
  chat-no-forced: {{strategy: static, targets: [{{provider: chat, model: synthetic-no-forced, {chat_tools}, request_shape_support: {{unsupported_request_features: [forced_tool_choice]}}}}]}}
  chat-no-choice: {{strategy: static, targets: [{{provider: chat, model: synthetic-no-choice, {chat_tools}, request_shape_support: {{unsupported_request_features: [tool_choice]}}}}]}}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [chat-structured, chat-mct, chat-no-forced, chat-no-choice]
  - id: synthetic-denied
    token_sha256: {DENIED_CALLER_DIGEST}
    allow: [chat-structured]
'''
    handle = spawn_router(router, work, config)
    yield handle
    stop_router(handle)


def _chat(model: str, **extra) -> dict:
    payload = {"model": model, "messages": [{"role": "user", "content": PROMPT_CANARY}]}
    for key, value in extra.items():
        if value is not OMIT:
            payload[key] = value
    return payload


def _post(base: str, payload: dict):
    return request(base, "/v1/chat/completions", payload)


def _only_call(router) -> dict:
    calls = router["upstream"].calls
    assert len(calls) == 1, [c["path"] for c in calls]
    assert calls[0]["path"].endswith("/chat/completions")
    return calls[0]["body"]


def _assert_no_eligible_zero_upstream(router, status: int, raw: bytes) -> None:
    assert status == 502, raw[:300]
    error = json.loads(raw)["error"]
    assert error["type"] == "no-eligible-target"
    assert router["upstream"].calls == []


def _scripted(router, response: dict, *, status: int = 200) -> ScriptedScenario:
    scenario = ScriptedScenario()
    scenario.expect(path_suffix="/chat/completions", response=response, status=status)
    router["upstream"].scenario = scenario
    return scenario


def _completion(choices: list[dict]) -> dict:
    return {
        "id": "chatcmpl_scripted",
        "object": "chat.completion",
        "created": 1,
        "model": "synthetic-chat",
        "choices": choices,
        "usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
    }


# --- CHAT-06 ---------------------------------------------------------------


@pytest.mark.parametrize(
    "tool_choice",
    [
        OMIT,
        None,
        "auto",
        "none",
        "required",
        {"type": "function", "function": {"name": "lookup"}},
    ],
    ids=["omitted", "null", "auto", "none", "required", "named"],
)
@pytest.mark.parametrize("parallel", [OMIT, True, False], ids=["ptc-omitted", "ptc-true", "ptc-false"])
def test_chat_06_tool_choice_and_parallel_preserved_natively(api, router, tool_choice, parallel):
    """CHAT-06: same-dialect tool_choice and parallel_tool_calls reach upstream verbatim."""
    status, _, raw = api(
        "/v1/chat/completions",
        _chat("chat", tools=[LOOKUP_TOOL], tool_choice=tool_choice, parallel_tool_calls=parallel),
    )
    assert status == 200, raw[:300]
    body = _only_call(router)
    if tool_choice is OMIT:
        # Omitted stays omitted: the router never invents a default choice.
        assert "tool_choice" not in body
    else:
        assert "tool_choice" in body and body["tool_choice"] == tool_choice
    if parallel is OMIT:
        assert "parallel_tool_calls" not in body
    else:
        assert body["parallel_tool_calls"] is parallel
    assert body["tools"] == [LOOKUP_TOOL]
    choice = json.loads(raw)["choices"][0]
    assert choice["finish_reason"] == "tool_calls"
    assert choice["message"]["tool_calls"][0]["id"] == "call_synthetic"


def test_chat_06_no_tools_does_not_invent_tool_fields(api, router):
    """CHAT-06: a request without tools reaches upstream without tools/tool_choice."""
    status, _, raw = api("/v1/chat/completions", _chat("chat"))
    assert status == 200, raw[:300]
    body = _only_call(router)
    for key in ("tools", "tool_choice", "parallel_tool_calls"):
        assert key not in body
    assert "tool_calls" not in json.loads(raw)["choices"][0]["message"]


def test_chat_06_nonexistent_named_tool_is_provider_validated(api, router):
    """CHAT-06: a named choice for an unknown tool is provider-owned validation.

    The router forwards it once (no rewrite to another tool), then surfaces the
    provider 400 as sanitized ``upstream_bad_request`` without retrying.
    """
    named_missing = {"type": "function", "function": {"name": "does_not_exist"}}
    scenario = _scripted(
        router,
        {"error": {"message": "tool does_not_exist not found", "type": "invalid_request_error"}},
        status=400,
    )
    status, headers, raw = api(
        "/v1/chat/completions", _chat("chat", tools=[LOOKUP_TOOL], tool_choice=named_missing)
    )
    scenario.assert_complete()
    assert status == 502, raw[:300]
    assert headers.get("X-Router-Error-Class") == "upstream_bad_request"
    assert headers.get("X-Upstream-Status") == "400"
    error = json.loads(raw)["error"]
    assert error["details"]["attempts"] == 1
    assert error["details"]["fallbackUsed"] is False
    assert b"does_not_exist not found" not in raw
    assert _only_call(router)["tool_choice"] == named_missing


@pytest.mark.parametrize(
    "tool_choice",
    ["required", {"type": "function", "function": {"name": "lookup"}}],
    ids=["required", "named"],
)
def test_chat_06_forced_choice_rejected_when_target_lacks_forced_support(controls_router, router, tool_choice):
    """CHAT-06: forced choices cannot select a target that declares them unsupported."""
    status, _, raw = _post(
        controls_router["base"], _chat("chat-no-forced", tools=[LOOKUP_TOOL], tool_choice=tool_choice)
    )
    _assert_no_eligible_zero_upstream(router, status, raw)


def test_chat_06_auto_choice_allowed_when_only_forced_is_unsupported(controls_router, router):
    status, _, raw = _post(controls_router["base"], _chat("chat-no-forced", tools=[LOOKUP_TOOL], tool_choice="auto"))
    assert status == 200, raw[:300]
    assert _only_call(router)["tool_choice"] == "auto"


@pytest.mark.parametrize("tool_choice", [None, "auto", "none"], ids=["null", "auto", "none"])
def test_chat_06_explicit_choice_rejected_when_tool_choice_unsupported(controls_router, router, tool_choice):
    """CHAT-06: an explicit tool_choice key (including null) skips a no-tool_choice target."""
    status, _, raw = _post(
        controls_router["base"], _chat("chat-no-choice", tools=[LOOKUP_TOOL], tool_choice=tool_choice)
    )
    _assert_no_eligible_zero_upstream(router, status, raw)


def test_chat_06_omitted_choice_allowed_when_tool_choice_unsupported(controls_router, router):
    status, _, raw = _post(controls_router["base"], _chat("chat-no-choice", tools=[LOOKUP_TOOL]))
    assert status == 200, raw[:300]
    assert "tool_choice" not in _only_call(router)


# --- CHAT-07 ---------------------------------------------------------------

_LARGE_DESCRIPTION = "Großer Beschreibungstext — 説明 ✓ " * 900  # ~30 KB of Unicode.

SCHEMA_TOOLS = [
    {
        "type": "function",
        "function": {
            "name": "create_ticket",
            "description": _LARGE_DESCRIPTION,
            "strict": True,
            "parameters": {
                "type": "object",
                "$defs": {
                    "address": {
                        "type": "object",
                        "properties": {"city": {"type": "string"}, "zip": {"type": ["string", "null"]}},
                        "required": ["city", "zip"],
                        "additionalProperties": False,
                    }
                },
                "properties": {
                    "title": {"type": "string", "description": "Titel – タイトル"},
                    "priority": {"type": "string", "enum": ["low", "medium", "high"]},
                    "assignee": {"type": ["string", "null"]},
                    "tags": {"type": "array", "items": {"type": "string", "enum": ["α", "β", "γ"]}},
                    "location": {"$ref": "#/$defs/address"},
                    "nested": {
                        "type": "object",
                        "properties": {
                            "depth": {"type": "integer", "minimum": 0},
                            "items": {
                                "type": "array",
                                "items": {
                                    "anyOf": [
                                        {"type": "number"},
                                        {"type": "object", "properties": {"k": {"type": "boolean"}}, "required": ["k"], "additionalProperties": False},
                                    ]
                                },
                            },
                        },
                        "required": ["depth", "items"],
                        "additionalProperties": False,
                    },
                },
                "required": ["title", "priority", "assignee", "tags", "location", "nested"],
                "additionalProperties": False,
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "loose",
            "strict": False,
            "parameters": {"type": "object", "properties": {"free": {}}, "additionalProperties": True},
        },
    },
    {
        # strict omitted, empty-object parameters.
        "type": "function",
        "function": {"name": "noop", "parameters": {"type": "object", "properties": {}}},
    },
]


def test_chat_07_function_schemas_reach_upstream_unmutated(api, router):
    """CHAT-07: strict/loose/omitted, $ref/$defs, nullable, enums, Unicode, large descriptions."""
    tools = copy.deepcopy(SCHEMA_TOOLS)
    status, _, raw = api("/v1/chat/completions", _chat("chat", tools=tools))
    assert status == 200, raw[:300]
    body = _only_call(router)
    assert body["tools"] == SCHEMA_TOOLS
    sent = body["tools"][0]["function"]
    assert sent["strict"] is True
    assert body["tools"][1]["function"]["strict"] is False
    assert "strict" not in body["tools"][2]["function"]
    # Nullable stays a type union; $ref is not inlined; property order of required kept.
    params = sent["parameters"]
    assert params["properties"]["assignee"]["type"] == ["string", "null"]
    assert params["properties"]["location"] == {"$ref": "#/$defs/address"}
    assert params["required"] == SCHEMA_TOOLS[0]["function"]["parameters"]["required"]
    assert sent["description"] == _LARGE_DESCRIPTION


def test_chat_07_strict_response_format_schema_reaches_upstream_unmutated(controls_router, router):
    """CHAT-07: a strict json_schema response_format is forwarded byte-for-byte semantically."""
    response_format = {
        "type": "json_schema",
        "json_schema": {
            "name": "ticket_extract",
            "strict": True,
            "schema": copy.deepcopy(SCHEMA_TOOLS[0]["function"]["parameters"]),
        },
    }
    status, _, raw = _post(controls_router["base"], _chat("chat-structured", response_format=copy.deepcopy(response_format)))
    assert status == 200, raw[:300]
    assert _only_call(router)["response_format"] == response_format


# --- CHAT-08 ---------------------------------------------------------------

JSON_SCHEMA_FORMAT = {
    "type": "json_schema",
    "json_schema": {
        "name": "ticket",
        "strict": True,
        "schema": {
            "type": "object",
            "properties": {"id": {"type": "string"}, "priority": {"type": "string", "enum": ["low", "high"]}},
            "required": ["id", "priority"],
            "additionalProperties": False,
        },
    },
}


@pytest.mark.parametrize(
    "response_format",
    [{"type": "json_object"}, JSON_SCHEMA_FORMAT],
    ids=["json_object", "json_schema"],
)
def test_chat_08_structured_output_requires_target_capability(api, router, response_format):
    """CHAT-08: structured output never routes to a target without structured_outputs."""
    status, _, raw = api("/v1/chat/completions", _chat("chat", response_format=response_format))
    _assert_no_eligible_zero_upstream(router, status, raw)


@pytest.mark.parametrize(
    "response_format",
    [{"type": "json_object"}, JSON_SCHEMA_FORMAT],
    ids=["json_object", "json_schema"],
)
def test_chat_08_structured_output_forwarded_and_valid_json_returned(controls_router, router, response_format):
    """CHAT-08: capable target receives the exact format; complete JSON parses."""
    content = '{"id":"INC-1234","priority":"high"}'
    _scripted(
        router,
        _completion([{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}]),
    )
    status, _, raw = _post(controls_router["base"], _chat("chat-structured", response_format=response_format))
    assert status == 200, raw[:300]
    assert _only_call(router)["response_format"] == response_format
    choice = json.loads(raw)["choices"][0]
    assert choice["finish_reason"] == "stop"
    assert json.loads(choice["message"]["content"]) == {"id": "INC-1234", "priority": "high"}


def test_chat_08_truncation_refusal_and_content_filter_stay_distinct(controls_router, router):
    """CHAT-08: length/content_filter/refusal are not collapsed into a successful stop."""
    truncated = '{"id":"INC-1234","prio'
    _scripted(
        router,
        _completion(
            [
                {"index": 0, "message": {"role": "assistant", "content": truncated}, "finish_reason": "length"},
                {"index": 1, "message": {"role": "assistant", "content": None, "refusal": "I can't help with that."}, "finish_reason": "stop"},
                {"index": 2, "message": {"role": "assistant", "content": ""}, "finish_reason": "content_filter"},
            ]
        ),
    )
    status, _, raw = _post(
        controls_router["base"], _chat("chat-structured", response_format=JSON_SCHEMA_FORMAT, n=3)
    )
    assert status == 200, raw[:300]
    choices = json.loads(raw)["choices"]
    assert [c["index"] for c in choices] == [0, 1, 2]
    assert [c["finish_reason"] for c in choices] == ["length", "stop", "content_filter"]
    assert choices[0]["message"]["content"] == truncated
    with pytest.raises(json.JSONDecodeError):
        json.loads(choices[0]["message"]["content"])
    assert choices[1]["message"]["content"] is None
    assert choices[1]["message"]["refusal"] == "I can't help with that."
    assert choices[2]["message"]["content"] == ""


def test_chat_08_text_path_length_finish_reason_preserved(api, router):
    """CHAT-08: plain text truncation keeps finish_reason=length on the caller response."""
    _scripted(
        router,
        _completion([{"index": 0, "message": {"role": "assistant", "content": "partial"}, "finish_reason": "length"}]),
    )
    status, _, raw = api("/v1/chat/completions", _chat("chat", max_tokens=4))
    assert status == 200, raw[:300]
    choice = json.loads(raw)["choices"][0]
    assert choice["finish_reason"] == "length"
    assert choice["message"]["content"] == "partial"


# --- CHAT-09 ---------------------------------------------------------------


@pytest.mark.parametrize("with_tools", [False, True], ids=["text", "tools"])
@pytest.mark.parametrize(
    ("caps", "expected"),
    [
        ({"max_tokens": 64}, {"max_tokens": 64}),
        ({"max_completion_tokens": 48}, {"max_tokens": 48}),
        ({"max_tokens": 64, "max_completion_tokens": 128}, {"max_tokens": 64}),
    ],
    ids=["max_tokens", "max_completion_tokens", "both-max_tokens-wins"],
)
def test_chat_09_default_target_encodes_single_max_tokens(api, router, with_tools, caps, expected):
    """CHAT-09: max_tokens wins when both are sent; default target encodes max_tokens only."""
    extra = dict(caps)
    if with_tools:
        extra["tools"] = [LOOKUP_TOOL]
    status, _, raw = api("/v1/chat/completions", _chat("chat", **extra))
    assert status == 200, raw[:300]
    body = _only_call(router)
    assert {k: body[k] for k in ("max_tokens", "max_completion_tokens", "max_output_tokens") if k in body} == expected


@pytest.mark.parametrize("with_tools", [False, True], ids=["text", "tools"])
@pytest.mark.parametrize(
    ("caps", "expected"),
    [
        ({"max_tokens": 64}, {"max_completion_tokens": 64}),
        ({"max_completion_tokens": 48}, {"max_completion_tokens": 48}),
        ({"max_tokens": 64, "max_completion_tokens": 128}, {"max_completion_tokens": 64}),
    ],
    ids=["max_tokens", "max_completion_tokens", "both-max_tokens-wins"],
)
def test_chat_09_output_token_field_target_encodes_max_completion_tokens(controls_router, router, with_tools, caps, expected):
    """CHAT-09: output_token_field: max_completion_tokens targets receive only that field."""
    extra = dict(caps)
    if with_tools:
        extra["tools"] = [LOOKUP_TOOL]
    status, _, raw = _post(controls_router["base"], _chat("chat-mct", **extra))
    assert status == 200, raw[:300]
    body = _only_call(router)
    assert {k: body[k] for k in ("max_tokens", "max_completion_tokens", "max_output_tokens") if k in body} == expected


def test_chat_09_stop_string_and_list_forwarded(api, router):
    """CHAT-09: stop sequences reach upstream (text path normalizes a string to a list)."""
    status, _, raw = api("/v1/chat/completions", _chat("chat", stop=["END", "\n\n"]))
    assert status == 200, raw[:300]
    assert _only_call(router)["stop"] == ["END", "\n\n"]
    router["upstream"].reset_state()
    status, _, raw = api("/v1/chat/completions", _chat("chat", stop="END"))
    assert status == 200, raw[:300]
    assert _only_call(router)["stop"] in ("END", ["END"])
    router["upstream"].reset_state()
    status, _, raw = api("/v1/chat/completions", _chat("chat", stop="END", tools=[LOOKUP_TOOL]))
    assert status == 200, raw[:300]
    assert _only_call(router)["stop"] == "END"


# --- CHAT-10 ---------------------------------------------------------------

SAMPLING_CONTROLS = {
    "temperature": 0.3,
    "top_p": 0.9,
    "seed": 7,
    "n": 2,
    "presence_penalty": 0.1,
    "frequency_penalty": 0.2,
    "logprobs": True,
    "top_logprobs": 2,
    "logit_bias": {"50256": -100},
    "stop": ["END"],
}


def test_chat_10_sampling_controls_forwarded_and_persistence_fields_stripped(api, router):
    """CHAT-10: native Chat passthrough keeps sampling controls; store/metadata are stripped."""
    status, _, raw = api(
        "/v1/chat/completions",
        _chat(
            "chat",
            tools=[LOOKUP_TOOL],
            store=True,
            metadata={"trace": PROMPT_CANARY},
            **SAMPLING_CONTROLS,
        ),
    )
    assert status == 200, raw[:300]
    body = _only_call(router)
    for key, value in SAMPLING_CONTROLS.items():
        assert body[key] == value, key
    assert "store" not in body
    assert "metadata" not in body


def test_chat_10_force_store_false_target_sends_store_false(controls_router, router):
    status, _, raw = _post(controls_router["base"], _chat("chat-mct", tools=[LOOKUP_TOOL], store=True, metadata={"a": "b"}))
    assert status == 200, raw[:300]
    body = _only_call(router)
    assert body["store"] is False
    assert "metadata" not in body


def test_chat_10_multiple_choices_keep_indexes_and_finish_reasons(api, router):
    """CHAT-10: n>1 responses keep independent choice indexes, content, and finish reasons."""
    _scripted(
        router,
        _completion(
            [
                {"index": 0, "message": {"role": "assistant", "content": None, "tool_calls": [{"id": "call_a", "type": "function", "function": {"name": "lookup", "arguments": '{"q":"a"}'}}]}, "finish_reason": "tool_calls"},
                {"index": 1, "message": {"role": "assistant", "content": "second"}, "finish_reason": "stop"},
            ]
        ),
    )
    status, _, raw = api("/v1/chat/completions", _chat("chat", tools=[LOOKUP_TOOL], n=2))
    assert status == 200, raw[:300]
    assert _only_call(router)["n"] == 2
    choices = json.loads(raw)["choices"]
    assert [(c["index"], c["finish_reason"]) for c in choices] == [(0, "tool_calls"), (1, "stop")]
    assert choices[0]["message"]["tool_calls"][0]["id"] == "call_a"
    assert choices[0]["message"]["tool_calls"][0]["function"]["arguments"] == '{"q":"a"}'
    assert choices[1]["message"]["content"] == "second"
