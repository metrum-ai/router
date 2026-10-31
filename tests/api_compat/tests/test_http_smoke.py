# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import json

from conftest import DENIED_CALLER, PROMPT_CANARY


def body(reply):
    return json.loads(reply[2])


def test_models_are_caller_filtered(api):
    status, _, raw = api("/v1/models")
    assert status == 200
    ids = {item["id"] for item in json.loads(raw)["data"]}
    assert {"chat", "responses", "messages"} <= ids
    status, _, raw = api("/v1/models", token=DENIED_CALLER)
    assert status == 200
    assert {item["id"] for item in json.loads(raw)["data"]} == {"chat"}


def test_auth_model_access_and_invalid_shape_reject_before_upstream(api, router):
    status, _, raw = api(
        "/v1/chat/completions",
        {"model": "chat", "messages": [{"role": "user", "content": PROMPT_CANARY}]},
        token=None,
    )
    assert status == 401
    assert body((status, None, raw)) == {
        "error": {"type": "unauthorized", "message": "unauthorized", "code": "unauthorized", "param": None}
    }
    assert router["upstream"].calls == []
    status, _, raw = api("/v1/chat/completions", {"model": "chat", "messages": [{"role": "user", "content": PROMPT_CANARY}]}, token="not-a-caller")
    assert status == 401 and body((status, None, raw))["error"]["type"] == "unauthorized"
    assert router["upstream"].calls == []
    status, _, raw = api("/v1/responses", {"model": "responses", "input": PROMPT_CANARY}, token=DENIED_CALLER)
    assert status == 404 and body((status, None, raw))["error"]["code"] == "model_not_found"
    assert router["upstream"].calls == []
    status, _, raw = api("/v1/chat/completions", {"model": "chat", "input": PROMPT_CANARY})
    assert status == 400 and body((status, None, raw))["error"]["code"] == "responses-body-on-chat-endpoint-disabled"
    assert router["upstream"].calls == []
    status, _, raw = api("/v1/responses", {"model": "responses", "input": PROMPT_CANARY, "tools": [{"type": "sse", "url": "http://127.0.0.1/never-call"}]})
    assert status == 400 and body((status, None, raw))["error"]["code"] == "provider-hosted-tools-forbidden"
    assert router["upstream"].calls == []
