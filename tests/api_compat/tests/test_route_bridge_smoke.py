# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import json

from conftest import PROMPT_CANARY, TOOL_CANARY


def body(reply):
    return json.loads(reply[2])


def test_stateless_responses_to_chat_and_chat_to_responses_bridges(api, router):
    status, _, raw = api("/v1/responses", {"model": "responses-to-chat", "input": PROMPT_CANARY})
    bridge_text = body((status, None, raw))
    assert status == 200 and bridge_text["object"] == "response" and bridge_text["output_text"] == "synthetic chat"
    assert router["upstream"].calls[0]["path"] == "/v1/chat/completions"
    status, _, raw = api(
        "/v1/responses",
        {
            "model": "responses-to-chat",
            "input": PROMPT_CANARY,
            "tools": [
                {
                    "type": "function",
                    "name": "lookup",
                    "description": TOOL_CANARY,
                    "parameters": {"type": "object"},
                }
            ],
            "tool_choice": "auto",
        },
    )
    bridge_tool = body((status, None, raw))
    assert status == 200 and any(
        item["type"] == "function_call" and item["name"] == "lookup" for item in bridge_tool["output"]
    )
    status, _, raw = api(
        "/v1/chat/completions",
        {
            "model": "chat-to-responses",
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
        },
    )
    chat_text = body((status, None, raw))
    assert status == 200
    assert chat_text["object"] == "chat.completion"
    assert chat_text["choices"][0]["message"]["content"] == "synthetic responses"
    assert router["upstream"].calls[-1]["path"] == "/v1/responses"
    assert router["upstream"].calls[-1]["body"]["input"] == [
        {
            "role": "user",
            "content": [{"type": "input_text", "text": PROMPT_CANARY}],
        }
    ]
    status, _, raw = api(
        "/v1/chat/completions",
        {
            "model": "chat-to-responses",
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
            "tools": [
                {
                    "type": "function",
                    "function": {
                        "name": "lookup",
                        "description": TOOL_CANARY,
                        "parameters": {"type": "object"},
                    },
                }
            ],
            "tool_choice": "auto",
        },
    )
    chat_tool = body((status, None, raw))
    assert (
        status == 200
        and chat_tool["object"] == "chat.completion"
        and chat_tool["choices"][0]["message"]["tool_calls"][0]["function"]["name"] == "lookup"
    )
    assert router["upstream"].calls[-1]["path"] == "/v1/responses"
    before = len(router["upstream"].calls)
    status, _, raw = api(
        "/v1/responses",
        {"model": "responses-to-chat", "input": PROMPT_CANARY, "previous_response_id": "resp_synthetic"},
    )
    assert status == 502 and body((status, None, raw))["error"]["type"] == "no-eligible-target"
    assert len(router["upstream"].calls) == before
    status, headers, raw = api(
        "/v1/chat/completions",
        {
            "model": "chat-to-responses",
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
            "stream": True,
        },
    )
    assert status == 200
    assert "text/event-stream" in headers["Content-Type"]
    assert b"chat.completion.chunk" in raw
    assert b"synthetic responses" in raw
    assert router["upstream"].calls[-1]["path"] == "/v1/responses"
    assert router["upstream"].calls[-1]["body"].get("stream") is True
