# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""HTTP-06..09: Anthropic header policy, upstream error surface, request limits, SDK reuse.

These contracts run against a dedicated router profile (provider keys, configured
Anthropic beta header, a byte-limited target) and an isolated scripted upstream so
attempt counts and forwarded headers are exact. Metrum AI router issue #94.
"""

from __future__ import annotations

import asyncio
import http.client
import json
import socket
import threading
import time

import anthropic
import openai
import pytest

from conftest import CALLER, CALLER_DIGEST, PROMPT_CANARY, request, spawn_router, stop_router
from harness.scripted_upstream import FakeUpstream, ScriptedScenario, start_fake_upstream

# Synthetic upstream credential; must never reach the caller or artifacts.
UPSTREAM_KEY = "sk-api-compat-upstream-key-canary"
# Planted in every upstream error body/header; must never reach the caller.
LEAK_CANARY = "api-compat-upstream-leak-canary"
CONFIGURED_BETA = "api-compat-configured-beta-2026-10-10"
LIMIT_BYTES = 4096


class _HttpUpstream(FakeUpstream):
    """Isolated class state plus optional extra response headers for error fixtures."""

    calls: list = []
    scenario: ScriptedScenario | None = None
    chat_include_usage_empty_choices: bool = False
    _attempt_counter: int = 0
    _counter_lock = threading.Lock()
    extra_headers: dict[str, str] = {}

    @classmethod
    def reset_state(cls) -> None:
        super().reset_state()
        cls.extra_headers = {}

    def end_headers(self) -> None:
        for name, value in self.__class__.extra_headers.items():
            self.send_header(name, value)
        super().end_headers()


@pytest.fixture(scope="module")
def http_router(router, tmp_path_factory):
    work = tmp_path_factory.mktemp("http-p1")
    _HttpUpstream.reset_state()
    upstream, thread, upstream_url = start_fake_upstream(_HttpUpstream)
    config = f'''server:
  listen: "127.0.0.1:{{port}}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  chat: {{base_url: "{upstream_url}/v1", dialect: openai-chat, api_key: "${{API_COMPAT_UPSTREAM_KEY}}"}}
  messages: {{base_url: "{upstream_url}/anthropic", dialect: anthropic, api_key: "${{API_COMPAT_UPSTREAM_KEY}}", headers: {{anthropic-beta: "{CONFIGURED_BETA}"}}}}
  messages_bearer: {{base_url: "{upstream_url}/anthropic", dialect: anthropic, auth_scheme: bearer, api_key: "${{API_COMPAT_UPSTREAM_KEY}}"}}
models:
  chat: {{strategy: static, targets: [{{provider: chat, model: synthetic-chat}}]}}
  limited: {{strategy: static, targets: [{{provider: chat, model: synthetic-limited, request_shape_support: {{max_request_bytes: {LIMIT_BYTES}}}}}]}}
  messages: {{strategy: static, targets: [{{provider: messages, model: synthetic-messages, request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}}}]}}
  messages-bearer: {{strategy: static, targets: [{{provider: messages_bearer, model: synthetic-messages-bearer, request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}}}]}}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [chat, limited, messages, messages-bearer]
'''
    handle = spawn_router(router, work, config, env={"API_COMPAT_UPSTREAM_KEY": UPSTREAM_KEY})
    handle["upstream"] = _HttpUpstream
    handle["upstream_url"] = upstream_url
    try:
        yield handle
    finally:
        stop_router(handle)
        upstream.shutdown()
        thread.join(timeout=5)
        for path in work.rglob("*"):
            if path.is_file():
                contents = path.read_bytes()
                assert UPSTREAM_KEY.encode() not in contents
                assert LEAK_CANARY.encode() not in contents


@pytest.fixture
def upstream(http_router):
    _HttpUpstream.reset_state()
    yield _HttpUpstream
    if _HttpUpstream.scenario is not None:
        _HttpUpstream.scenario.assert_complete()
    _HttpUpstream.reset_state()


def _chat(model="chat", content=PROMPT_CANARY):
    return {"model": model, "messages": [{"role": "user", "content": content}]}


def _anthropic_post(base, headers, payload):
    conn = http.client.HTTPConnection(base.removeprefix("http://"), timeout=10)
    try:
        conn.putrequest("POST", "/anthropic/v1/messages")
        body = json.dumps(payload).encode()
        conn.putheader("Content-Type", "application/json")
        conn.putheader("Content-Length", str(len(body)))
        for name, value in headers:
            conn.putheader(name, value)
        conn.endheaders(body)
        response = conn.getresponse()
        return response.status, response.read()
    finally:
        conn.close()


# --- HTTP-06 -----------------------------------------------------------------


def test_http_06_anthropic_version_and_beta_header_policy(http_router, upstream):
    """HTTP-06: caller anthropic-version/beta never leak upstream; router pins its own policy."""
    base = http_router["base"]
    payload = {
        "model": "messages",
        "max_tokens": 16,
        "messages": [{"role": "user", "content": PROMPT_CANARY}],
    }
    variants = [
        # Router caller token via x-api-key, newer caller version, two beta header lines.
        [
            ("x-api-key", CALLER),
            ("anthropic-version", "2099-01-01"),
            ("anthropic-beta", "caller-beta-a-2026-01-01,caller-beta-b-2026-01-01"),
            ("anthropic-beta", "caller-beta-c-2026-01-01"),
        ],
        # Version omitted entirely (SDK-less caller) with Bearer auth.
        [("Authorization", f"Bearer {CALLER}")],
    ]
    for headers in variants:
        upstream.reset_state()
        upstream.scenario = ScriptedScenario().expect(path="/anthropic/v1/messages")
        status, raw = _anthropic_post(base, headers, payload)
        assert status == 200, raw[:300]
        assert json.loads(raw)["content"][0]["text"] == "synthetic messages"
        upstream.scenario.assert_complete()
        sent = {k.lower(): v for k, v in upstream.calls[0]["headers"].items()}
        # Router-owned version pin and configured provider beta, never caller values.
        assert sent["anthropic-version"] == "2023-06-01"
        assert sent["anthropic-beta"] == CONFIGURED_BETA
        assert "caller-beta" not in json.dumps(sent)
        # Provider skin is x-api-key; the caller's router token is never forwarded.
        assert sent["x-api-key"] == UPSTREAM_KEY
        assert "authorization" not in sent
        assert CALLER not in json.dumps(sent)


def test_http_06_provider_skin_bearer_vs_api_key(http_router, upstream):
    """HTTP-06: a Bearer-skin Anthropic provider receives Authorization, not x-api-key."""
    client = anthropic.Anthropic(
        base_url=http_router["base"] + "/anthropic",
        api_key=CALLER,
        auth_token=CALLER,
        max_retries=0,
        default_headers={"anthropic-beta": "caller-beta-sdk-2026-01-01"},
    )
    upstream.scenario = ScriptedScenario().expect(path="/anthropic/v1/messages")
    message = client.messages.create(
        model="messages-bearer",
        max_tokens=16,
        messages=[{"role": "user", "content": PROMPT_CANARY}],
    )
    assert message.content[0].text == "synthetic messages"
    sent = {k.lower(): v for k, v in upstream.calls[0]["headers"].items()}
    assert sent["authorization"] == f"Bearer {UPSTREAM_KEY}"
    assert "x-api-key" not in sent
    assert sent["anthropic-version"] == "2023-06-01"
    # No configured beta on this skin, and the SDK caller's beta is not forwarded.
    assert "anthropic-beta" not in sent
    assert upstream.calls[0]["body"]["model"] == "synthetic-messages-bearer"


# --- HTTP-07 -----------------------------------------------------------------

_ERROR_FIXTURES = [
    # (upstream status, body, content type, extra headers, caller status, caller error type, error_class)
    (
        500,
        json.dumps({"error": {"message": f"boom {LEAK_CANARY}", "type": "server_error"}}).encode(),
        "application/json",
        {"x-request-id": f"upstream-{LEAK_CANARY}"},
        502,
        "upstream-failed",
        "upstream_status_5xx",
    ),
    (
        400,
        json.dumps({"error": {"message": f"bad {LEAK_CANARY}", "type": "invalid_request_error"}}).encode(),
        "application/json",
        {},
        502,
        "upstream-failed",
        "upstream_bad_request",
    ),
    (
        429,
        json.dumps({"error": {"message": f"slow down {LEAK_CANARY}"}}).encode(),
        "application/json",
        {"Retry-After": "7", "x-ratelimit-remaining-requests": "0"},
        503,
        "upstream-rate-limited",
        "upstream_rate_limited",
    ),
    (
        502,
        f"<html><body><h1>502 Bad Gateway</h1>{LEAK_CANARY}</body></html>".encode(),
        "text/html",
        {},
        502,
        "upstream-failed",
        "upstream_status_5xx",
    ),
    (
        400,
        f"not json at all {LEAK_CANARY}".encode(),
        "application/json",
        {},
        502,
        "upstream-failed",
        "upstream_bad_request",
    ),
    (
        200,
        f"<html>{LEAK_CANARY}</html>".encode(),
        "text/html",
        {},
        502,
        "upstream-failed",
        "decode_error",
    ),
]


@pytest.mark.parametrize(
    "up_status,body,content_type,extra,caller_status,error_type,error_class",
    _ERROR_FIXTURES,
    ids=["500-json", "400-json", "429-retry-after", "502-html-proxy", "400-malformed", "200-wrong-type"],
)
def test_http_07_upstream_errors_are_sanitized_and_sdk_usable(
    http_router, upstream, up_status, body, content_type, extra, caller_status, error_type, error_class
):
    """HTTP-07: provider-owned failures propagate as safe, parseable router errors."""
    upstream.extra_headers = dict(extra)
    upstream.scenario = ScriptedScenario().expect(
        path="/v1/chat/completions", status=up_status, response=body, content_type=content_type
    )
    status, headers, raw = request(http_router["base"], "/v1/chat/completions", _chat())
    upstream.scenario.assert_complete()
    # Exactly one attempt on the single-target group: no hidden replay.
    assert len(upstream.calls) == 1
    assert status == caller_status
    assert headers["Content-Type"].startswith("application/json")
    parsed = json.loads(raw)
    error = parsed["error"]
    assert error["type"] == error_type
    details = error["details"]
    assert details["error_class"] == error_class
    assert details["attempts"] == 1
    assert headers["X-Router-Error-Class"] == error_class
    router_request_id = headers["X-Request-Id"]
    assert router_request_id.startswith("req_")
    assert details["request_id"] == router_request_id
    if up_status != 200:
        assert details["upstream_status"] == up_status
        assert headers["X-Upstream-Status"] == str(up_status)
    # Safe-only surface: no upstream body, key, URL, or upstream request id.
    surface = raw + json.dumps(dict(headers.items())).encode()
    assert LEAK_CANARY.encode() not in surface
    assert UPSTREAM_KEY.encode() not in surface
    assert http_router["upstream_url"].removeprefix("http://").encode() not in surface
    assert b"<html" not in surface

    # The official SDK maps the same response to a usable typed exception.
    upstream.reset_state()
    upstream.extra_headers = dict(extra)
    upstream.scenario = ScriptedScenario().expect(
        path="/v1/chat/completions", status=up_status, response=body, content_type=content_type
    )
    client = openai.OpenAI(base_url=http_router["base"] + "/v1", api_key=CALLER, max_retries=0)
    with pytest.raises(openai.APIStatusError) as exc_info:
        client.chat.completions.create(**_chat())
    exc = exc_info.value
    assert exc.status_code == caller_status
    assert exc.request_id and exc.request_id.startswith("req_")
    assert isinstance(exc.body, dict) and exc.body.get("type") == error_type
    assert LEAK_CANARY not in str(exc)
    assert len(upstream.calls) == 1


def test_http_07_anthropic_sdk_error_is_typed_and_sanitized(http_router, upstream):
    """HTTP-07: Anthropic SDK receives a typed exception for a provider 500, no leakage."""
    upstream.scenario = ScriptedScenario().expect(
        path="/anthropic/v1/messages",
        status=500,
        response=json.dumps({"type": "error", "error": {"type": "api_error", "message": LEAK_CANARY}}).encode(),
    )
    client = anthropic.Anthropic(
        base_url=http_router["base"] + "/anthropic", api_key=CALLER, auth_token=CALLER, max_retries=0
    )
    with pytest.raises(anthropic.APIStatusError) as exc_info:
        client.messages.create(
            model="messages", max_tokens=16, messages=[{"role": "user", "content": PROMPT_CANARY}]
        )
    exc = exc_info.value
    assert exc.status_code == 502
    assert LEAK_CANARY not in str(exc) and LEAK_CANARY not in json.dumps(exc.body)
    assert exc.response.headers["x-request-id"].startswith("req_")
    assert len(upstream.calls) == 1


def test_http_07_upstream_socket_close_hides_provider_url(http_router, upstream):
    """HTTP-07: a transport failure reports a safe class without the provider URL or address."""
    upstream.scenario = ScriptedScenario().expect(path="/v1/chat/completions", close_socket=True)
    status, headers, raw = request(http_router["base"], "/v1/chat/completions", _chat())
    upstream.scenario.assert_complete()
    assert len(upstream.calls) == 1
    assert status == 502
    error = json.loads(raw)["error"]
    assert error["type"] == "upstream-failed"
    assert error["details"]["error_class"] == "upstream_network_error"
    assert headers["X-Request-Id"].startswith("req_")
    upstream_host = http_router["upstream_url"].removeprefix("http://")
    assert upstream_host.encode() not in raw
    assert b"127.0.0.1" not in raw
    assert b"/v1/chat/completions" not in raw
    assert UPSTREAM_KEY.encode() not in raw


# --- HTTP-08 -----------------------------------------------------------------


def _chat_body_of_size(model: str, size: int) -> bytes:
    base = json.dumps(_chat(model=model, content="")).encode()
    padding = size - len(base)
    assert padding >= 0
    body = json.dumps(_chat(model=model, content="x" * padding)).encode()
    assert len(body) == size
    return body


def _raw_post(base: str, path: str, body: bytes):
    conn = http.client.HTTPConnection(base.removeprefix("http://"), timeout=30)
    try:
        conn.request(
            "POST",
            path,
            body=body,
            headers={"Authorization": f"Bearer {CALLER}", "Content-Type": "application/json"},
        )
        response = conn.getresponse()
        return response.status, response.read()
    finally:
        conn.close()


def test_http_08_target_request_byte_limit_boundary(http_router, upstream):
    """HTTP-08: configured max_request_bytes admits at the limit and rejects above it pre-upstream."""
    base = http_router["base"]
    for size in (LIMIT_BYTES - 1, LIMIT_BYTES):
        upstream.reset_state()
        upstream.scenario = ScriptedScenario().expect(path="/v1/chat/completions")
        status, raw = _raw_post(base, "/v1/chat/completions", _chat_body_of_size("limited", size))
        assert status == 200, (size, raw[:300])
        upstream.scenario.assert_complete()
        assert upstream.calls[0]["body"]["model"] == "synthetic-limited"

    upstream.reset_state()
    upstream.scenario = ScriptedScenario()
    status, raw = _raw_post(base, "/v1/chat/completions", _chat_body_of_size("limited", LIMIT_BYTES + 1))
    assert status == 502
    assert json.loads(raw)["error"]["type"] == "no-eligible-target"
    assert upstream.calls == []

    # Healthy follow-up after the rejection.
    upstream.reset_state()
    upstream.scenario = ScriptedScenario().expect(path="/v1/chat/completions")
    status, _, _ = request(base, "/v1/chat/completions", _chat())
    assert status == 200


def test_http_08_ingress_body_cap_rejects_before_upstream(http_router, upstream):
    """HTTP-08: the 64 MiB model-API ingress cap rejects oversize bodies before upstream."""
    base = http_router["base"]
    cap = 64 << 20
    prefix = b'{"model":"chat","messages":[{"role":"user","content":"'
    suffix = b'"}]}'
    oversize = prefix + b"x" * (cap + 1 - len(prefix) - len(suffix)) + suffix
    assert len(oversize) == cap + 1
    upstream.scenario = ScriptedScenario()
    try:
        status, raw = _raw_post(base, "/v1/chat/completions", oversize)
    except (BrokenPipeError, ConnectionResetError):
        status, raw = None, b""
    # Either a parseable 400 invalid-body, or the server closed the oversize upload early.
    if status is not None:
        assert status == 400, raw[:300]
        assert json.loads(raw)["error"]["type"] == "invalid-body"
    assert upstream.calls == []

    # Healthy follow-up on a fresh connection.
    upstream.reset_state()
    upstream.scenario = ScriptedScenario().expect(path="/v1/chat/completions")
    status, _ = _raw_post(base, "/v1/chat/completions", json.dumps(_chat()).encode())
    assert status == 200


def test_http_08_slow_and_abandoned_bodies_then_keepalive_reuse(http_router, upstream):
    """HTTP-08: slow body completes, abandoned body never reaches upstream, keep-alive reuse works."""
    host = http_router["base"].removeprefix("http://")
    body = json.dumps(_chat()).encode()

    # Slow body: trickled in small pieces over ~1s, still admitted exactly once.
    upstream.scenario = ScriptedScenario().expect(path="/v1/chat/completions")
    conn = http.client.HTTPConnection(host, timeout=10)
    conn.putrequest("POST", "/v1/chat/completions")
    conn.putheader("Authorization", f"Bearer {CALLER}")
    conn.putheader("Content-Type", "application/json")
    conn.putheader("Content-Length", str(len(body)))
    conn.endheaders()
    step = max(1, len(body) // 10)
    for start in range(0, len(body), step):
        conn.send(body[start : start + step])
        time.sleep(0.1)
    response = conn.getresponse()
    assert response.status == 200
    assert json.loads(response.read())["choices"][0]["message"]["content"] == "synthetic chat"
    conn.close()
    upstream.scenario.assert_complete()

    # Abandoned body: declared length never arrives; the router must not call upstream.
    upstream.reset_state()
    upstream.scenario = ScriptedScenario()
    host_name, port = host.split(":")
    with socket.create_connection((host_name, int(port)), timeout=5) as sock:
        sock.sendall(
            b"POST /v1/chat/completions HTTP/1.1\r\nHost: " + host.encode()
            + b"\r\nAuthorization: Bearer " + CALLER.encode()
            + b"\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n"
            + body[: len(body) // 2]
        )
        time.sleep(0.2)
    time.sleep(0.3)
    assert upstream.calls == []

    # Keep-alive: two healthy requests share one TCP connection.
    upstream.reset_state()
    upstream.scenario = (
        ScriptedScenario().expect(path="/v1/chat/completions").expect(path="/v1/chat/completions")
    )
    conn = http.client.HTTPConnection(host, timeout=10)
    headers = {"Authorization": f"Bearer {CALLER}", "Content-Type": "application/json"}
    conn.request("POST", "/v1/chat/completions", body=body, headers=headers)
    first = conn.getresponse()
    assert first.status == 200
    first.read()
    sock_before = conn.sock
    conn.request("POST", "/v1/chat/completions", body=body, headers=headers)
    second = conn.getresponse()
    assert second.status == 200
    second.read()
    assert conn.sock is sock_before and sock_before is not None
    conn.close()
    upstream.scenario.assert_complete()


# --- HTTP-09 (partial: pinned SDK only) ----------------------------------------


def test_http_09_pinned_sdk_pool_reuse_and_async_cancel(http_router, upstream):
    """HTTP-09 (partial): pinned SDK pool reuse and async cancel do not poison later requests."""
    base = http_router["base"] + "/v1"
    gate = threading.Event()
    upstream.scenario = (
        ScriptedScenario()
        .expect(path="/v1/chat/completions", gate=gate)
        .expect(path="/v1/chat/completions")
        .expect(path="/v1/chat/completions")
    )

    async def run():
        client = openai.AsyncOpenAI(base_url=base, api_key=CALLER, max_retries=0, timeout=10)
        try:
            task = asyncio.create_task(client.chat.completions.create(**_chat()))
            deadline = time.monotonic() + 5
            while not upstream.calls and time.monotonic() < deadline:
                await asyncio.sleep(0.02)
            assert len(upstream.calls) == 1
            task.cancel()
            with pytest.raises(asyncio.CancelledError):
                await task
            gate.set()
            # The same pooled client keeps working after a caller-side cancellation.
            results = [await client.chat.completions.create(**_chat()) for _ in range(2)]
            return [r.choices[0].message.content for r in results]
        finally:
            gate.set()
            await client.close()

    assert asyncio.run(run()) == ["synthetic chat", "synthetic chat"]
    # Cancel produced no SDK-level retry: exactly three upstream attempts in total.
    assert len(upstream.calls) == 3
