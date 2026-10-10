# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""MEDIA-04 format/detail matrix and MEDIA-08 image canary paths (issue #94).

Fixtures are generated in-test with known bytes and sha256 hashes; nothing is
downloaded. The router validates image references without decoding them, so the
contract under test is byte-exact forwarding plus preserved controls, not image
parsing. Metrum AI router compatibility suite.
"""

from __future__ import annotations

import base64
import hashlib
import json
import sqlite3
import struct
import threading
import time
import zlib
from pathlib import Path

import pytest

from conftest import (
    CALLER,
    CALLER_DIGEST,
    assert_generated_artifacts_redacted,
    request,
    spawn_router,
    stop_router,
)
from harness.scripted_upstream import FakeUpstream, ScriptedScenario, start_fake_upstream

# MEDIA-08 canaries. None of these may appear in router-generated artifacts.
MEDIA_TEXT_CANARY = "api-compat-media-text-canary"
MEDIA_URL_CANARY = "api-compat-media-url-canary"
MEDIA_PIXEL_CANARY = b"api-compat-media-pixel-canary"
PII_EMAIL = "media-owner@example.com"


def _png(width: int, height: int, *, rgba: bool = False, text: bytes | None = None) -> bytes:
    """Deterministic PNG with optional alpha channel and tEXt chunk (stdlib only)."""
    channels = 4 if rgba else 3
    row = bytes([0]) + bytes([200, 30, 60, 128][:channels]) * width
    raw = row * height

    def chunk(tag: bytes, data: bytes) -> bytes:
        crc = zlib.crc32(data, zlib.crc32(tag)) & 0xFFFFFFFF
        return struct.pack(">I", len(data)) + tag + data + struct.pack(">I", crc)

    color_type = 6 if rgba else 2
    body = chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, color_type, 0, 0, 0))
    if text is not None:
        body += chunk(b"tEXt", b"Comment\x00" + text)
    body += chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b"")
    return b"\x89PNG\r\n\x1a\n" + body


def _gif_1x1_transparent() -> bytes:
    """GIF89a 1x1 with a graphic-control extension marking index 0 transparent."""
    return (
        b"GIF89a"
        + struct.pack("<HHBBB", 1, 1, 0x80, 0, 0)
        + b"\x00\x00\x00\xff\xff\xff"
        + b"!\xf9\x04\x01\x00\x00\x00\x00"
        + b",\x00\x00\x00\x00\x01\x00\x01\x00\x00"
        + b"\x02\x02D\x01\x00;"
    )


def _webp_1x1_lossless() -> bytes:
    """RIFF/WEBP VP8L 1x1 container assembled from its documented header fields."""
    vp8l = b"\x2f\x00\x00\x00\x10\x07\x10\x11\x11\x88\x88\xfe\x07"
    # RIFF chunks are padded to even length; the size field excludes the pad byte.
    payload = b"WEBP" + b"VP8L" + struct.pack("<I", len(vp8l)) + vp8l + b"\x00"
    return b"RIFF" + struct.pack("<I", len(payload)) + payload


def _jpeg_marker_stub() -> bytes:
    """SOI/APP0(JFIF)/EOI marker sequence: byte identity is what the router must keep."""
    app0 = b"JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"
    return b"\xff\xd8" + b"\xff\xe0" + struct.pack(">H", len(app0) + 2) + app0 + b"\xff\xd9"


FORMAT_FIXTURES = {
    "png-landscape": ("image/png", _png(3, 1)),
    "png-portrait-alpha": ("image/png", _png(1, 3, rgba=True)),
    "jpeg": ("image/jpeg", _jpeg_marker_stub()),
    "webp": ("image/webp", _webp_1x1_lossless()),
    "gif-transparent": ("image/gif", _gif_1x1_transparent()),
}
DETAILS = ("high", "low", "auto")

# Base64 canary fixture: PNG whose tEXt chunk carries a pixel canary.
_CANARY_PNG = _png(1, 1, text=MEDIA_PIXEL_CANARY)
_CANARY_PNG_B64 = base64.b64encode(_CANARY_PNG).decode("ascii")
_CANARY_DATA_URL = f"data:image/png;base64,{_CANARY_PNG_B64}"
_CANARY_PUBLIC_URL = f"https://8.8.8.8/{MEDIA_URL_CANARY}.png"
_CANARY_PRIVATE_URL = f"http://127.0.0.1/{MEDIA_URL_CANARY}-private.png"
MEDIA_CANARIES = (
    MEDIA_TEXT_CANARY.encode(),
    MEDIA_URL_CANARY.encode(),
    MEDIA_PIXEL_CANARY,
    _CANARY_PNG_B64.encode(),
    # Base64 chunks are offset-dependent; a 40-char interior slice still flags partial copies.
    _CANARY_PNG_B64[16:56].encode(),
    PII_EMAIL.encode(),
)


class MediaUpstream(FakeUpstream):
    """Isolated class state so the session FakeUpstream counters are not shared."""

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


_VISION_GROUPS = """
  vision-chat:
    strategy: static
    targets:
      - {{provider: chat-vision, model: synthetic-vision, input_modalities: [text, image]}}
  vision-responses:
    strategy: static
    targets:
      - {{provider: responses-vision, model: synthetic-vision-responses, input_modalities: [text, image]}}
  vision-messages:
    strategy: static
    targets:
      - provider: messages-vision
        model: synthetic-vision-messages
        input_modalities: [text, image]
        request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}
  vision-pii:
    strategy: static
    pii_filter:
      enabled: true
      mode: redact_only
      rules:
        - {{name: email, expression: '[a-z-]+@example\\.com', placeholder_prefix: EMAIL}}
    targets:
      - {{provider: chat-vision, model: synthetic-vision-pii, input_modalities: [text, image]}}
  text-chat:
    strategy: static
    targets:
      - {{provider: chat-vision, model: synthetic-text-cache, input_modalities: [text]}}
"""


@pytest.fixture(scope="module")
def media_router(router, tmp_path_factory):
    """Vision groups plus cache, usage DB, logging and a PII-filter group enabled."""
    work = tmp_path_factory.mktemp("api-compat-media-matrix")
    MediaUpstream.reset_state()
    upstream, thread, upstream_url = start_fake_upstream(MediaUpstream)
    config = f'''server:
  listen: "127.0.0.1:{{port}}"
  identifiers: {{{{mode: passthrough}}}}
  default_model_group: vision-chat
  cache: {{{{enabled: true, max_bytes: 1048576, default_ttl: 5m}}}}
  usage_db:
    enabled: true
    driver: sqlite
    path: "{work / 'usage.sqlite'}"
    migration_policy: auto-safe
  logging: {{{{path: "{work / 'router.jsonl'}"}}}}
state_path: "{work / 'state.json'}"
providers:
  chat-vision: {{{{base_url: "{upstream_url}/v1", dialect: openai-chat}}}}
  responses-vision: {{{{base_url: "{upstream_url}/v1", dialect: openai-responses}}}}
  messages-vision: {{{{base_url: "{upstream_url}/anthropic", dialect: anthropic}}}}
models:{_VISION_GROUPS}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [vision-chat, vision-responses, vision-messages, vision-pii, text-chat]
'''
    # The f-string above doubles braces once for Python; collapse the YAML layer.
    handle = spawn_router(router, work, config.replace("{{", "{").replace("}}", "}"))
    yield handle | {"upstream": MediaUpstream}
    stop_router(handle)
    upstream.shutdown()
    thread.join(timeout=5)
    assert_generated_artifacts_redacted({"work": work})


@pytest.fixture(autouse=True)
def _clear_media_calls(media_router):
    MediaUpstream.reset_state()
    yield


def _api(media_router, path, payload=None):
    return request(media_router["base"], path, payload, CALLER)


def _chat_ok(model: str) -> dict:
    return {
        "id": "chatcmpl_media_matrix",
        "object": "chat.completion",
        "model": model,
        "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
        "usage": {"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4},
    }


def _responses_ok(model: str) -> dict:
    return {
        "id": "resp_media_matrix",
        "object": "response",
        "model": model,
        "status": "completed",
        "output": [{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "ok"}]}],
        "usage": {"input_tokens": 3, "output_tokens": 1, "total_tokens": 4},
    }


def _messages_ok(model: str) -> dict:
    return {
        "id": "msg_media_matrix",
        "type": "message",
        "role": "assistant",
        "model": model,
        "stop_reason": "end_turn",
        "content": [{"type": "text", "text": "ok"}],
        "usage": {"input_tokens": 3, "output_tokens": 1},
    }


def _decode_data_url(url: str) -> tuple[str, bytes]:
    head, _, data = url.partition(",")
    assert head.startswith("data:") and head.endswith(";base64"), head
    return head[len("data:") : -len(";base64")], base64.b64decode(data, validate=True)


def test_media_04_format_and_detail_matrix(media_router):
    """MEDIA-04: PNG/JPEG/WebP/GIF, portrait/landscape/alpha, detail per native dialect."""
    # Independent fixture sanity: magic numbers come from the format specs, not the router.
    assert FORMAT_FIXTURES["jpeg"][1][:2] == b"\xff\xd8" and FORMAT_FIXTURES["jpeg"][1][-2:] == b"\xff\xd9"
    assert FORMAT_FIXTURES["webp"][1][:4] == b"RIFF" and FORMAT_FIXTURES["webp"][1][8:12] == b"WEBP"
    assert FORMAT_FIXTURES["gif-transparent"][1][:6] == b"GIF89a"
    assert struct.unpack(">II", FORMAT_FIXTURES["png-portrait-alpha"][1][16:24]) == (1, 3)
    assert FORMAT_FIXTURES["png-portrait-alpha"][1][25] == 6  # RGBA color type

    for name, (media_type, blob) in FORMAT_FIXTURES.items():
        digest = hashlib.sha256(blob).hexdigest()
        b64 = base64.b64encode(blob).decode("ascii")
        data_url = f"data:{media_type};base64,{b64}"
        for detail in DETAILS:
            scenario = ScriptedScenario()

            def validate_chat(call, data_url=data_url, detail=detail, digest=digest, media_type=media_type):
                parts = call["body"]["messages"][0]["content"]
                assert [p["type"] for p in parts] == ["text", "image_url"]
                image = parts[1]["image_url"]
                assert image["detail"] == detail
                got_type, got = _decode_data_url(image["url"])
                assert got_type == media_type
                assert hashlib.sha256(got).hexdigest() == digest

            def validate_responses(call, detail=detail, digest=digest, media_type=media_type):
                parts = call["body"]["input"][0]["content"]
                assert [p["type"] for p in parts] == ["input_text", "input_image"]
                assert parts[1]["detail"] == detail
                got_type, got = _decode_data_url(parts[1]["image_url"])
                assert got_type == media_type
                assert hashlib.sha256(got).hexdigest() == digest

            scenario.expect(path_suffix="/chat/completions", validate=validate_chat, response=_chat_ok("v"))
            scenario.expect(path_suffix="/responses", validate=validate_responses, response=_responses_ok("v"))
            MediaUpstream.scenario = scenario

            status, _, raw = _api(
                media_router,
                "/v1/chat/completions",
                {
                    "model": "vision-chat",
                    "messages": [
                        {
                            "role": "user",
                            "content": [
                                {"type": "text", "text": f"describe {name}"},
                                {"type": "image_url", "image_url": {"url": data_url, "detail": detail}},
                            ],
                        }
                    ],
                },
            )
            assert status == 200, (name, detail, raw[:300])
            status, _, raw = _api(
                media_router,
                "/v1/responses",
                {
                    "model": "vision-responses",
                    "input": [
                        {
                            "role": "user",
                            "content": [
                                {"type": "input_text", "text": f"describe {name}"},
                                {"type": "input_image", "image_url": data_url, "detail": detail},
                            ],
                        }
                    ],
                },
            )
            assert status == 200, (name, detail, raw[:300])
            scenario.assert_complete()
            MediaUpstream.reset_state()

        # Anthropic Messages has no detail control; base64 source bytes and media_type survive.
        scenario = ScriptedScenario()

        def validate_messages(call, b64=b64, digest=digest, media_type=media_type):
            parts = call["body"]["messages"][0]["content"]
            assert [p["type"] for p in parts] == ["text", "image"]
            source = parts[1]["source"]
            assert source["type"] == "base64" and source["media_type"] == media_type
            assert hashlib.sha256(base64.b64decode(source["data"])).hexdigest() == digest

        scenario.expect(path_suffix="/messages", validate=validate_messages, response=_messages_ok("v"))
        MediaUpstream.scenario = scenario
        status, _, raw = _api(
            media_router,
            "/anthropic/v1/messages",
            {
                "model": "vision-messages",
                "max_tokens": 16,
                "messages": [
                    {
                        "role": "user",
                        "content": [
                            {"type": "text", "text": f"describe {name}"},
                            {"type": "image", "source": {"type": "base64", "media_type": media_type, "data": b64}},
                        ],
                    }
                ],
            },
        )
        assert status == 200, (name, raw[:300])
        scenario.assert_complete()
        MediaUpstream.reset_state()


def _scan_artifacts(work: Path) -> list[str]:
    hits = []
    for path in Path(work).rglob("*"):
        if not path.is_file():
            continue
        contents = path.read_bytes()
        for canary in MEDIA_CANARIES:
            if canary in contents:
                hits.append(f"{path.name}:{canary[:24]!r}")
    return hits


def _usage_rows(db: Path, minimum: int, timeout_s: float = 5.0) -> int:
    deadline = time.monotonic() + timeout_s
    count = 0
    while time.monotonic() < deadline:
        try:
            conn = sqlite3.connect(db)
            try:
                count = conn.execute("SELECT COUNT(*) FROM request_usage").fetchone()[0]
            finally:
                conn.close()
        except sqlite3.OperationalError:
            count = 0
        if count >= minimum:
            return count
        time.sleep(0.05)
    return count


def test_media_08_artifact_scan_detects_planted_canaries(tmp_path):
    """ARCH-08 self-check: the MEDIA-08 scanner fails when a canary leaks."""
    for canary in MEDIA_CANARIES:
        leak = tmp_path / "leak.jsonl"
        leak.write_bytes(b'{"prefix":"' + canary + b'"}')
        assert _scan_artifacts(tmp_path), canary
    leak.write_bytes(b'{"image":"[IMAGE_REDACTED]"}')
    assert _scan_artifacts(tmp_path) == []


def test_media_08_image_canaries_through_cache_pii_error_and_usage(media_router):
    """MEDIA-08: image URL/base64 canaries never reach scalar artifacts on any path."""
    image_message = {
        "role": "user",
        "content": [
            {"type": "text", "text": f"{MEDIA_TEXT_CANARY} contact {PII_EMAIL}"},
            {"type": "image_url", "image_url": {"url": _CANARY_DATA_URL}},
            {"type": "image_url", "image_url": {"url": _CANARY_PUBLIC_URL}},
        ],
    }

    # 1) Cache: identical deterministic image requests bypass the response cache.
    def validate_images_intact(call):
        parts = call["body"]["messages"][0]["content"]
        assert parts[1]["image_url"]["url"] == _CANARY_DATA_URL
        assert parts[2]["image_url"]["url"] == _CANARY_PUBLIC_URL

    scenario = ScriptedScenario()
    for _ in range(2):
        scenario.expect(path_suffix="/chat/completions", validate=validate_images_intact, response=_chat_ok("v"))
    # Control: the same cache serves a deterministic text request once.
    scenario.expect(path_suffix="/chat/completions", response=_chat_ok("t"))
    MediaUpstream.scenario = scenario
    payload = {"model": "vision-chat", "temperature": 0, "messages": [image_message]}
    for _ in range(2):
        status, _, raw = _api(media_router, "/v1/chat/completions", payload)
        assert status == 200, raw[:300]
    text_payload = {
        "model": "text-chat",
        "temperature": 0,
        "messages": [{"role": "user", "content": "cache control text"}],
    }
    for _ in range(2):
        status, _, raw = _api(media_router, "/v1/chat/completions", text_payload)
        assert status == 200, raw[:300]
    scenario.assert_complete()
    assert len(MediaUpstream.calls) == 3
    MediaUpstream.reset_state()

    # 2) PII filter: text is redacted, image bytes/URLs survive (apply_to.image_urls defaults off).
    def validate_pii(call):
        parts = call["body"]["messages"][0]["content"]
        assert PII_EMAIL not in json.dumps(call["body"])
        assert "[EMAIL_1]" in parts[0]["text"]
        assert parts[1]["image_url"]["url"] == _CANARY_DATA_URL
        _, got = _decode_data_url(parts[1]["image_url"]["url"])
        assert hashlib.sha256(got).hexdigest() == hashlib.sha256(_CANARY_PNG).hexdigest()
        assert parts[2]["image_url"]["url"] == _CANARY_PUBLIC_URL

    scenario = ScriptedScenario()
    scenario.expect(path_suffix="/chat/completions", validate=validate_pii, response=_chat_ok("p"))
    MediaUpstream.scenario = scenario
    status, _, raw = _api(media_router, "/v1/chat/completions", {"model": "vision-pii", "messages": [image_message]})
    assert status == 200, raw[:300]
    scenario.assert_complete()
    MediaUpstream.reset_state()

    # 3) Upstream error that echoes the image reference back.
    scenario = ScriptedScenario()
    scenario.expect(
        path_suffix="/chat/completions",
        status=400,
        response={"error": {"type": "invalid_request_error", "message": f"bad image {_CANARY_PUBLIC_URL}"}},
    )
    MediaUpstream.scenario = scenario
    status, _, raw = _api(media_router, "/v1/chat/completions", {"model": "vision-chat", "messages": [image_message]})
    assert status >= 400
    assert "error" in json.loads(raw)
    scenario.assert_complete()
    MediaUpstream.reset_state()

    # 4) Router-owned rejection of a private image URL: zero upstream calls.
    status, _, raw = _api(
        media_router,
        "/v1/chat/completions",
        {
            "model": "vision-chat",
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": MEDIA_TEXT_CANARY},
                        {"type": "image_url", "image_url": {"url": _CANARY_PRIVATE_URL}},
                    ],
                }
            ],
        },
    )
    assert status >= 400, raw[:300]
    details = json.loads(raw)["error"].get("details") or {}
    assert details.get("error_class") == "image_url_forbidden"
    assert details.get("attempts") == 0
    assert MediaUpstream.calls == []
    assert MEDIA_URL_CANARY.encode() not in raw

    # 5) Streaming Responses + Messages image paths.
    scenario = ScriptedScenario()
    scenario.expect(path_suffix="/responses")
    scenario.expect(path_suffix="/messages")
    MediaUpstream.scenario = scenario
    status, _, _ = _api(
        media_router,
        "/v1/responses",
        {
            "model": "vision-responses",
            "stream": True,
            "input": [
                {
                    "role": "user",
                    "content": [
                        {"type": "input_text", "text": MEDIA_TEXT_CANARY},
                        {"type": "input_image", "image_url": _CANARY_DATA_URL},
                    ],
                }
            ],
        },
    )
    assert status == 200
    status, _, _ = _api(
        media_router,
        "/anthropic/v1/messages",
        {
            "model": "vision-messages",
            "max_tokens": 16,
            "stream": True,
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": MEDIA_TEXT_CANARY},
                        {"type": "image", "source": {"type": "url", "url": _CANARY_PUBLIC_URL}},
                    ],
                }
            ],
        },
    )
    assert status == 200
    scenario.assert_complete()

    # Usage rows were written (so the scan below covers a populated DB), then
    # neither the DB, router log, state nor stderr hold any canary.
    assert _usage_rows(media_router["work"] / "usage.sqlite", minimum=6) >= 6
    status, _, usage_raw = _api(media_router, "/v1/usage")
    assert status == 200
    assert not any(canary in usage_raw for canary in MEDIA_CANARIES)
    assert _scan_artifacts(media_router["work"]) == []
