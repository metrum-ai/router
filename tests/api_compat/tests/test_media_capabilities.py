# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""MEDIA-07: localhost media preservation through both translated endpoints."""
import json

import pytest
from conftest import request as http_request
from harness.scripted_upstream import (
    FakeUpstream, ScriptedScenario, default_chat_stream_frames,
    default_responses_stream_frames,
)
from test_anthropic_translated_tools import translated_tools_router  # noqa: F401


@pytest.mark.parametrize("stream", [False, True])
@pytest.mark.parametrize("tools", [False, True])
def test_media_07_translated_document_and_image(translated_tools_router, stream, tools):
    base, dialect = translated_tools_router
    responses = dialect == "openai-responses"
    document = {"type": "document", "source": {"type": "file", "file_id": "file-media-07"}}
    image = {"type": "image", "source": {"type": "url", "url": "https://8.8.8.8/media-07.png"}}

    def validate(call):
        body = call["body"]
        content = body["input" if responses else "messages"][0]["content"]
        assert content[0]["type"] == ("input_file" if responses else "file")
        assert (content[0] if responses else content[0]["file"])["file_id"] == "file-media-07"
        assert content[1]["type"] == ("input_image" if responses else "image_url")
        assert "https://8.8.8.8/media-07.png" in json.dumps(content[1])
        assert body["stream"] is stream

    response = {"id": "chat_media", "choices": [{"message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}]}
    if responses:
        response = {"id": "resp_media", "status": "completed", "output": [{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "ok"}]}]}
    if stream:
        response = default_responses_stream_frames() if responses else default_chat_stream_frames()
    scenario = ScriptedScenario().expect(path_suffix="/responses" if responses else "/chat/completions", validate=validate, response=response)
    FakeUpstream.scenario = scenario
    payload = {"model": "translated", "max_tokens": 128, "stream": stream, "messages": [{"role": "user", "content": [document, image]}]}
    if tools:
        payload["tools"] = [{"name": "echo", "input_schema": {"type": "object", "properties": {}}}]
    status, _, raw = http_request(base, "/anthropic/v1/messages", payload)
    assert status == 200, raw[:400]
    scenario.assert_complete()


def test_anth_09_translated_format(translated_tools_router):
    base, dialect = translated_tools_router
    responses = dialect == "openai-responses"
    schema = {"type": "object", "properties": {"ok": {"type": "boolean"}}, "required": ["ok"], "additionalProperties": False}
    def validate(call):
        body = call["body"]
        f = body["text"]["format"] if responses else body["response_format"]["json_schema"]
        assert f["schema"] == schema
        assert f["strict"] is True
    response = {"id": "chat_format", "choices": [{"message": {"role": "assistant", "content": '{"ok":true}'}, "finish_reason": "stop"}]}
    if responses:
        response = {"id": "resp_format", "status": "completed", "output": [{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": '{"ok":true}'}]}]}
    scenario = ScriptedScenario().expect(path_suffix="/responses" if responses else "/chat/completions", validate=validate, response=response)
    FakeUpstream.scenario = scenario
    status, _, raw = http_request(base, "/anthropic/v1/messages", {"model": "translated", "max_tokens": 128, "messages": [{"role": "user", "content": "ok"}], "output_config": {"format": {"type": "json_schema", "schema": schema}}})
    assert status == 200, raw[:400]
    scenario.assert_complete()
