# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""MEDIA-01..03 P0 image eligibility; MEDIA-05/06/09 cheap negatives (issue #94)."""

from __future__ import annotations

import base64
import hashlib
import json
import os
import struct
import subprocess
import threading
import time
import zlib
from pathlib import Path

import pytest

from conftest import (
    CALLER,
    CALLER_DIGEST,
    PROMPT_CANARY,
    TOOL_CANARY,
    assert_generated_artifacts_redacted,
    request,
    unused_port,
)
from harness.scripted_upstream import FakeUpstream, ScriptedScenario, start_fake_upstream


def _make_png(rgb: tuple[int, int, int]) -> bytes:
    """Generate a tiny deterministic 1x1 truecolor PNG (no external deps)."""
    raw = bytes([0, rgb[0], rgb[1], rgb[2]])
    compressed = zlib.compress(raw, 9)

    def chunk(tag: bytes, data: bytes) -> bytes:
        crc = zlib.crc32(tag)
        crc = zlib.crc32(data, crc) & 0xFFFFFFFF
        return struct.pack(">I", len(data)) + tag + data + struct.pack(">I", crc)

    return (
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", 1, 1, 8, 2, 0, 0, 0))
        + chunk(b"IDAT", compressed)
        + chunk(b"IEND", b"")
    )


# Fixtures generated at import/test time with known hashes (no public URL bytes deps).
_PNG_A = _make_png((255, 0, 0))
_PNG_B = _make_png((0, 255, 0))
_PNG_A_B64 = base64.b64encode(_PNG_A).decode("ascii")
_PNG_B_B64 = base64.b64encode(_PNG_B).decode("ascii")
_PNG_A_HASH = hashlib.sha256(_PNG_A).hexdigest()
_PNG_B_HASH = hashlib.sha256(_PNG_B).hexdigest()
_PNG_A_DATA_URL = f"data:image/png;base64,{_PNG_A_B64}"
_PNG_B_DATA_URL = f"data:image/png;base64,{_PNG_B_B64}"

# Literal public IP — image URL admission without DNS and without downloading.
_PUBLIC_IMAGE_URL = "https://8.8.8.8/media-p0-pixel.png"


class VisionUpstream(FakeUpstream):
    """Isolated upstream so session FakeUpstream class state is not shared."""

    calls: list = []
    scenario = None
    chat_include_usage_empty_choices = False
    _attempt_counter = 0
    _counter_lock = threading.Lock()

    @classmethod
    def reset_state(cls) -> None:
        cls.calls = []
        cls.scenario = None
        cls.chat_include_usage_empty_choices = False
        cls._attempt_counter = 0


def _chat_ok(model: str = "synthetic-vision") -> dict:
    return {
        "id": "chatcmpl_media",
        "object": "chat.completion",
        "model": model,
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": "media-ok"},
                "finish_reason": "stop",
            }
        ],
        "usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
    }


def _responses_ok(model: str = "synthetic-vision-responses") -> dict:
    return {
        "id": "resp_media",
        "object": "response",
        "model": model,
        "status": "completed",
        "output": [
            {
                "type": "message",
                "role": "assistant",
                "content": [{"type": "output_text", "text": "media-ok"}],
            }
        ],
        "output_text": "media-ok",
        "usage": {"input_tokens": 3, "output_tokens": 2, "total_tokens": 5},
    }


def _messages_ok(model: str = "synthetic-vision-messages") -> dict:
    return {
        "id": "msg_media",
        "type": "message",
        "role": "assistant",
        "model": model,
        "stop_reason": "end_turn",
        "content": [{"type": "text", "text": "media-ok"}],
        "usage": {"input_tokens": 3, "output_tokens": 2},
    }


def _assert_png_hash_in_upstream(body: dict, png_b64: str, digest: str) -> None:
    raw = json.dumps(body, separators=(",", ":"))
    assert png_b64 in raw, f"missing base64 fixture in upstream body ({digest})"
    assert hashlib.sha256(base64.b64decode(png_b64)).hexdigest() == digest


@pytest.fixture(scope="module")
def vision_router(router, tmp_path_factory):
    """Router with vision-capable groups; reuses the already-built binary."""
    work = tmp_path_factory.mktemp("api-compat-vision")
    VisionUpstream.reset_state()
    upstream, thread, upstream_url = start_fake_upstream(VisionUpstream)
    port = unused_port()
    config = f'''server:
  listen: "127.0.0.1:{port}"
  identifiers: {{mode: passthrough}}
  default_model_group: vision-chat
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  chat-vision: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
  chat-text: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
  responses-vision: {{base_url: "{upstream_url}/v1", dialect: openai-responses}}
  messages-vision: {{base_url: "{upstream_url}/anthropic", dialect: anthropic}}
models:
  vision-chat:
    strategy: static
    targets:
      - provider: chat-vision
        model: synthetic-vision
        input_modalities: [text, image]
        tool_support: {{openai_chat: [tools, tool_choice]}}
  vision-responses:
    strategy: static
    targets:
      - provider: responses-vision
        model: synthetic-vision-responses
        input_modalities: [text, image]
        tool_support: {{openai_responses: [function, tool_choice]}}
  vision-messages:
    strategy: static
    targets:
      - provider: messages-vision
        model: synthetic-vision-messages
        input_modalities: [text, image]
        tool_support: {{anthropic_messages: [client_tools]}}
        request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}
  vision-mix:
    strategy: static
    targets:
      - provider: chat-text
        model: synthetic-text-only
        input_modalities: [text]
        tool_support: {{openai_chat: [tools, tool_choice]}}
      - provider: chat-vision
        model: synthetic-vision-fallback
        input_modalities: [text, image]
        tool_support: {{openai_chat: [tools, tool_choice]}}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [vision-chat, vision-responses, vision-messages, vision-mix]
'''
    config_path = work / "config.yaml"
    config_path.write_text(config)
    router_binary = Path(router["work"]) / "router"
    build_env = os.environ | {"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
    process = subprocess.Popen(
        [str(router_binary), "-config", "config.yaml"],
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
    yield {"base": base, "work": work, "upstream": VisionUpstream, "server": upstream}
    process.terminate()
    process.wait(timeout=5)
    upstream.shutdown()
    thread.join(timeout=5)


@pytest.fixture(autouse=True)
def _clear_vision_calls(vision_router):
    VisionUpstream.reset_state()
    yield
    assert_generated_artifacts_redacted(vision_router)


def _api(vision_router, path, payload=None, token=CALLER):
    return request(vision_router["base"], path, payload, token)


def test_media_01_native_dialect_images_preserve_hash(vision_router):
    """MEDIA-01: native chat/responses/anthropic images + URL vs data; hash upstream."""
    assert _PNG_A != _PNG_B
    assert hashlib.sha256(_PNG_A).hexdigest() == _PNG_A_HASH

    scenario = ScriptedScenario()

    def validate_chat_data(call: dict) -> None:
        assert call["body"]["model"] == "synthetic-vision"
        content = call["body"]["messages"][0]["content"]
        assert content[0] == {"type": "text", "text": PROMPT_CANARY}
        assert content[1]["image_url"]["url"] == _PNG_A_DATA_URL
        _assert_png_hash_in_upstream(call["body"], _PNG_A_B64, _PNG_A_HASH)

    def validate_chat_url(call: dict) -> None:
        content = call["body"]["messages"][0]["content"]
        assert content[1]["image_url"]["url"] == _PUBLIC_IMAGE_URL

    def validate_responses(call: dict) -> None:
        assert call["body"]["model"] == "synthetic-vision-responses"
        parts = call["body"]["input"][0]["content"]
        assert parts[0]["type"] == "input_text"
        assert parts[1]["type"] == "input_image"
        assert parts[1]["image_url"] == _PNG_A_DATA_URL
        _assert_png_hash_in_upstream(call["body"], _PNG_A_B64, _PNG_A_HASH)

    def validate_anthropic(call: dict) -> None:
        assert call["body"]["model"] == "synthetic-vision-messages"
        parts = call["body"]["messages"][0]["content"]
        assert parts[0] == {"type": "text", "text": PROMPT_CANARY}
        source = parts[1]["source"]
        assert source == {
            "type": "base64",
            "media_type": "image/png",
            "data": _PNG_A_B64,
        }
        assert hashlib.sha256(base64.b64decode(source["data"])).hexdigest() == _PNG_A_HASH

    scenario.expect(path_suffix="/chat/completions", validate=validate_chat_data, response=_chat_ok())
    scenario.expect(path_suffix="/chat/completions", validate=validate_chat_url, response=_chat_ok())
    scenario.expect(path_suffix="/responses", validate=validate_responses, response=_responses_ok())
    scenario.expect(path_suffix="/messages", validate=validate_anthropic, response=_messages_ok())
    VisionUpstream.scenario = scenario

    status, _, raw = _api(
        vision_router,
        "/v1/chat/completions",
        {
            "model": "vision-chat",
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": PROMPT_CANARY},
                        {"type": "image_url", "image_url": {"url": _PNG_A_DATA_URL}},
                    ],
                }
            ],
        },
    )
    assert status == 200, raw

    status, _, raw = _api(
        vision_router,
        "/v1/chat/completions",
        {
            "model": "vision-chat",
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": "url-" + PROMPT_CANARY},
                        {"type": "image_url", "image_url": {"url": _PUBLIC_IMAGE_URL}},
                    ],
                }
            ],
        },
    )
    assert status == 200, raw

    status, _, raw = _api(
        vision_router,
        "/v1/responses",
        {
            "model": "vision-responses",
            "input": [
                {
                    "role": "user",
                    "content": [
                        {"type": "input_text", "text": PROMPT_CANARY},
                        {"type": "input_image", "image_url": _PNG_A_DATA_URL},
                    ],
                }
            ],
        },
    )
    assert status == 200, raw

    status, _, raw = _api(
        vision_router,
        "/anthropic/v1/messages",
        {
            "model": "vision-messages",
            "max_tokens": 32,
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": PROMPT_CANARY},
                        {
                            "type": "image",
                            "source": {
                                "type": "base64",
                                "media_type": "image/png",
                                "data": _PNG_A_B64,
                            },
                        },
                    ],
                }
            ],
        },
    )
    assert status == 200, raw
    scenario.assert_complete()


def test_media_02_multi_image_order_and_history(vision_router):
    """MEDIA-02: multiplicity/order and image in earlier turn."""
    scenario = ScriptedScenario()

    def validate(call: dict) -> None:
        messages = call["body"]["messages"]
        assert len(messages) == 2
        older = messages[0]["content"]
        assert older[1]["image_url"]["url"] == _PNG_A_DATA_URL
        newer = messages[1]["content"]
        assert [p["type"] for p in newer] == ["text", "image_url", "text", "image_url"]
        assert newer[1]["image_url"]["url"] == _PNG_B_DATA_URL
        assert newer[3]["image_url"]["url"] == _PNG_A_DATA_URL
        raw = json.dumps(call["body"])
        pos_a_first = raw.find(_PNG_A_B64)
        pos_b = raw.find(_PNG_B_B64)
        pos_a_second = raw.find(_PNG_A_B64, pos_a_first + 1)
        assert 0 <= pos_a_first < pos_b < pos_a_second
        _assert_png_hash_in_upstream(call["body"], _PNG_A_B64, _PNG_A_HASH)
        _assert_png_hash_in_upstream(call["body"], _PNG_B_B64, _PNG_B_HASH)

    scenario.expect(path_suffix="/chat/completions", validate=validate, response=_chat_ok())
    VisionUpstream.scenario = scenario

    status, _, raw = _api(
        vision_router,
        "/v1/chat/completions",
        {
            "model": "vision-chat",
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": "older-" + PROMPT_CANARY},
                        {"type": "image_url", "image_url": {"url": _PNG_A_DATA_URL}},
                    ],
                },
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": "between"},
                        {"type": "image_url", "image_url": {"url": _PNG_B_DATA_URL}},
                        {"type": "text", "text": "after-b"},
                        {"type": "image_url", "image_url": {"url": _PNG_A_DATA_URL}},
                    ],
                },
            ],
        },
    )
    assert status == 200, raw
    scenario.assert_complete()


def test_media_03_capability_intersection_never_strips_image(vision_router):
    """MEDIA-03: text-only filtered; vision failure does not discard image to succeed."""
    scenario = ScriptedScenario()

    def validate_vision_only(call: dict) -> None:
        assert call["body"]["model"] == "synthetic-vision-fallback"
        content = call["body"]["messages"][0]["content"]
        assert any(p.get("type") == "image_url" for p in content)
        _assert_png_hash_in_upstream(call["body"], _PNG_A_B64, _PNG_A_HASH)

    scenario.expect(
        path_suffix="/chat/completions",
        validate=validate_vision_only,
        response=_chat_ok("synthetic-vision-fallback"),
    )
    VisionUpstream.scenario = scenario

    image_payload = {
        "model": "vision-mix",
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": PROMPT_CANARY},
                    {"type": "image_url", "image_url": {"url": _PNG_A_DATA_URL}},
                ],
            }
        ],
        "tools": [
            {
                "type": "function",
                "function": {
                    "name": "lookup",
                    "description": TOOL_CANARY,
                    "parameters": {"type": "object", "properties": {}},
                },
            }
        ],
    }
    status, _, raw = _api(vision_router, "/v1/chat/completions", image_payload)
    assert status == 200, raw
    scenario.assert_complete()
    assert len(VisionUpstream.calls) == 1

    VisionUpstream.reset_state()
    fail_scenario = ScriptedScenario()
    fail_scenario.expect(
        path_suffix="/chat/completions",
        validate=validate_vision_only,
        status=500,
        response={"error": {"message": "vision upstream failed", "type": "server_error"}},
    )
    VisionUpstream.scenario = fail_scenario

    status, _, raw = _api(vision_router, "/v1/chat/completions", image_payload)
    assert status != 200, raw
    body = json.loads(raw)
    assert "error" in body
    fail_scenario.assert_complete()
    assert len(VisionUpstream.calls) == 1
    assert all(c["body"]["model"] != "synthetic-text-only" for c in VisionUpstream.calls)


def test_media_05_empty_image_data_rejects(vision_router):
    """MEDIA-05 (cheap): unsupported image URL scheme rejects with zero upstream calls."""
    VisionUpstream.scenario = None
    status, _, raw = _api(
        vision_router,
        "/v1/chat/completions",
        {
            "model": "vision-chat",
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": PROMPT_CANARY},
                        {
                            "type": "image_url",
                            "image_url": {"url": "ftp://example.com/empty.png"},
                        },
                    ],
                }
            ],
        },
    )
    assert status >= 400, raw
    err = json.loads(raw).get("error", {})
    details = err.get("details") or {}
    assert details.get("error_class") == "image_url_forbidden" or err.get("type") == "image_url_forbidden"
    assert VisionUpstream.calls == []


def test_media_06_private_image_url_rejected(vision_router):
    """MEDIA-06 (cheap): loopback image URL rejected; no hidden fetch / no upstream."""
    VisionUpstream.scenario = None
    status, _, raw = _api(
        vision_router,
        "/v1/chat/completions",
        {
            "model": "vision-chat",
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": PROMPT_CANARY},
                        {
                            "type": "image_url",
                            "image_url": {"url": "http://127.0.0.1/secret.png"},
                        },
                    ],
                }
            ],
        },
    )
    assert status >= 400, raw
    err = json.loads(raw).get("error", {})
    details = err.get("details") or {}
    assert details.get("error_class") == "image_url_forbidden" or err.get("type") == "image_url_forbidden"
    assert VisionUpstream.calls == []


def test_media_09_image_generation_unsupported(vision_router):
    """MEDIA-09 (cheap): image generation is out of scope (not found / not allowed)."""
    VisionUpstream.scenario = None
    status, _, raw = _api(
        vision_router,
        "/v1/images/generations",
        {"model": "vision-chat", "prompt": "draw a cat"},
    )
    assert status in {404, 405}, raw
    assert VisionUpstream.calls == []
