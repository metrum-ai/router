# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""ANTH-13/14: independently validated Messages tool translations (#295)."""

import json
import subprocess
import time
from pathlib import Path

import pytest

from conftest import request as http_request, unused_port
from harness import CALLER, CALLER_DIGEST
from harness.scripted_upstream import FakeUpstream, ScriptedScenario


@pytest.fixture(params=["openai-chat", "openai-responses"])
def translated_tools_router(request, router, tmp_path):
    dialect = request.param
    port = unused_port()
    target = {
        "provider": "translated", "model": "synthetic-tools",
        "tool_support": {
            "anthropic_messages": ["client_tools", "tool_choice"],
            "openai_chat": ["tools", "tool_choice"],
            "openai_responses": ["function", "tool_choice"],
        },
        "request_shape_support": {
            "supported_inbound_dialects": ["anthropic"],
            "validation_status": "passed",
        },
    }
    config = {
        "server": {
            "listen": f"127.0.0.1:{port}", "identifiers": {"mode": "passthrough"},
            "cache": {"enabled": False}, "usage_db": {"enabled": False},
            "logging": {"path": str(tmp_path / "router.jsonl")},
        },
        "state_path": str(tmp_path / "state.json"),
        "providers": {"translated": {
            "base_url": f"http://127.0.0.1:{router['server'].server_port}/v1",
            "dialect": dialect,
        }},
        "models": {"translated": {"strategy": "static", "targets": [target]}},
        "callers": [{"id": "synthetic", "token_sha256": CALLER_DIGEST, "allow": ["translated"]}],
    }
    path = tmp_path / "config.json"
    path.write_text(json.dumps(config))
    process = subprocess.Popen(
        [str(Path(router["work"]) / "router"), "--config", str(path)],
        cwd=tmp_path, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    base = f"http://127.0.0.1:{port}"
    try:
        for _ in range(100):
            try:
                if http_request(base, "/readyz", token=CALLER)[0] == 200:
                    break
            except OSError:
                pass
            time.sleep(0.05)
        else:
            pytest.fail("translated tool router did not become ready")
        yield base, dialect
    finally:
        process.terminate()
        process.wait(timeout=5)


def test_anthropic_translated_tools_roundtrip(translated_tools_router):
    base, dialect = translated_tools_router
    responses = dialect == "openai-responses"
    scenario = ScriptedScenario()

    def validate_first(call):
        body = call["body"]
        assert body["tools"][0]["name" if responses else "type"] == ("echo" if responses else "function")
        assert body["tool_choice"] == "auto"

    def validate_second(call):
        body = call["body"]
        if responses:
            assert {"type": "function_call_output", "call_id": "call_echo", "output": "ok"} in body["input"]
        else:
            assert body["messages"][-1] == {"role": "tool", "tool_call_id": "call_echo", "content": "ok"}
            assert body["messages"][-2]["tool_calls"][0]["id"] == "call_echo"

    if responses:
        first = {"id": "resp_first", "status": "completed", "output": [{"type": "function_call", "id": "fc_echo", "call_id": "call_echo", "name": "echo", "arguments": '{"text":"ok"}'}]}
        final = {"id": "resp_final", "status": "completed", "output": [{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "done"}]}]}
    else:
        first = {"id": "chat_first", "choices": [{"finish_reason": "tool_calls", "message": {"role": "assistant", "tool_calls": [{"id": "call_echo", "type": "function", "function": {"name": "echo", "arguments": '{"text":"ok"}'}}]}}]}
        final = {"id": "chat_final", "choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": "done"}}]}
    suffix = "/responses" if responses else "/chat/completions"
    scenario.expect(path_suffix=suffix, validate=validate_first, response=first)
    scenario.expect(path_suffix=suffix, validate=validate_second, response=final)
    FakeUpstream.scenario = scenario
    payload = {
        "model": "translated", "max_tokens": 128,
        "messages": [{"role": "user", "content": "echo"}],
        "tools": [{"name": "echo", "input_schema": {"type": "object", "properties": {"text": {"type": "string"}}}}],
        "tool_choice": {"type": "auto"},
    }
    status, _, raw = http_request(base, "/anthropic/v1/messages", payload)
    first_reply = json.loads(raw)
    assert status == 200
    assert first_reply["stop_reason"] == "tool_use"
    assert first_reply["content"] == [{"type": "tool_use", "id": "call_echo", "name": "echo", "input": {"text": "ok"}}]
    payload["messages"] += [
        {"role": "assistant", "content": first_reply["content"]},
        {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_echo", "content": "ok"}]},
    ]
    status, _, raw = http_request(base, "/anthropic/v1/messages", payload)
    assert status == 200
    assert json.loads(raw)["content"] == [{"type": "text", "text": "done"}]
    assert json.loads(raw)["stop_reason"] == "end_turn"
    scenario.assert_complete()
