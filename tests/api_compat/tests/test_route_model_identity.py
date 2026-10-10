# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""ROUTE-MI: caller-facing model identity (Metrum AI Router issue #254).

With ``server.responses.model_identity: requested_group`` every caller-facing
``model`` field is the requested router group; the default ``upstream`` mode
keeps reporting the provider model. Upstream requests always name the
provider model.
"""

from __future__ import annotations

import json

import pytest

from conftest import assert_generated_artifacts_redacted
from conftest import request as http_request
from harness.scripted_upstream import ScriptedScenario
from harness.sse import sse_events

# Native Chat frames that carry the provider model, including assistant text
# that mentions it; only the protocol field may change.
CHAT_MODEL_FRAMES = [
    'data: {"id":"chatcmpl_mi","object":"chat.completion.chunk","model":"synthetic-chat","choices":[{"index":0,"delta":{"role":"assistant","content":"I am synthetic-chat"},"finish_reason":null}]}\n\n',
    'data: {"id":"chatcmpl_mi","object":"chat.completion.chunk","model":"synthetic-chat","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}\n\n',
    "data: [DONE]\n\n",
]

# (group, provider model, caller path, unary payload)
CASES = [
    (
        "chat",
        "synthetic-chat",
        "/v1/chat/completions",
        {"model": "chat", "messages": [{"role": "user", "content": "hi"}]},
    ),
    (
        "responses",
        "synthetic-responses",
        "/v1/responses",
        {"model": "responses", "input": "hi"},
    ),
    (
        "messages",
        "synthetic-messages",
        "/anthropic/v1/messages",
        {"model": "messages", "max_tokens": 16, "messages": [{"role": "user", "content": "hi"}]},
    ),
]

BRIDGES = [
    (
        "responses-to-chat",
        "synthetic-bridge-chat",
        "/v1/responses",
        {"model": "responses-to-chat", "input": "hi"},
    ),
    (
        "chat-to-responses",
        "synthetic-bridge-responses",
        "/v1/chat/completions",
        {"model": "chat-to-responses", "messages": [{"role": "user", "content": "hi"}]},
    ),
]


def _models(value):
    """Every JSON "model" string in value, at any depth."""
    if isinstance(value, dict):
        for key, child in value.items():
            if key == "model" and isinstance(child, str):
                yield child
            else:
                yield from _models(child)
    elif isinstance(value, list):
        for child in value:
            yield from _models(child)


def _router(request_fixture, mode):
    return request_fixture.getfixturevalue("router_group" if mode == "requested_group" else "router")


def _expected(mode, group, provider_model):
    return group if mode == "requested_group" else provider_model


def _upstream_models(router):
    return [call["body"].get("model") for call in router["upstream"].calls]


@pytest.mark.parametrize("mode", ["upstream", "requested_group"])
@pytest.mark.parametrize("group,provider_model,path,payload", CASES)
def test_route_model_identity_unary(request, mode, group, provider_model, path, payload):
    router = _router(request, mode)
    router["upstream"].reset_state()
    status, _, body = request_json(router, path, payload)
    assert status == 200, body
    assert body["model"] == _expected(mode, group, provider_model)
    assert _upstream_models(router) == [provider_model]
    assert_generated_artifacts_redacted(router)


@pytest.mark.parametrize("mode", ["upstream", "requested_group"])
@pytest.mark.parametrize("group,provider_model,path,payload", CASES)
def test_route_model_identity_sse(request, mode, group, provider_model, path, payload):
    router = _router(request, mode)
    router["upstream"].reset_state()
    if group == "chat":
        router["upstream"].scenario = ScriptedScenario().expect(
            path_suffix="/chat/completions", response=CHAT_MODEL_FRAMES
        )
    status, _, raw = http_request(router["base"], path, payload | {"stream": True})
    assert status == 200, raw
    models = [m for _, data in sse_events(raw) for m in _models(data)]
    assert models, raw
    assert set(models) == {_expected(mode, group, provider_model)}, models
    if group == "chat":
        assert b"I am synthetic-chat" in raw
    assert _upstream_models(router) == [provider_model]


@pytest.mark.parametrize("mode", ["upstream", "requested_group"])
@pytest.mark.parametrize("group,provider_model,path,payload", BRIDGES)
def test_route_model_identity_bridges(request, mode, group, provider_model, path, payload):
    router = _router(request, mode)
    router["upstream"].reset_state()
    status, _, body = request_json(router, path, payload)
    assert status == 200, body
    assert body["model"] == _expected(mode, group, provider_model)
    assert _upstream_models(router) == [provider_model]


def test_route_model_identity_models_list_is_group_scoped(router_group):
    status, _, raw = http_request(router_group["base"], "/v1/models")
    assert status == 200, raw
    assert b"synthetic-" not in raw
    ids = {item["id"] for item in json.loads(raw)["data"]}
    assert "chat" in ids


def request_json(router, path, payload):
    status, headers, raw = http_request(router["base"], path, payload)
    return status, headers, json.loads(raw)
