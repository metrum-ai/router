# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""HTTP-04/05: router-owned negative admission and HTTP surface contracts."""

from __future__ import annotations

import json
import os
import subprocess
import threading
import time
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import pytest

from conftest import (
    CALLER,
    CALLER_DIGEST,
    DENIED_CALLER,
    PROMPT_CANARY,
    assert_generated_artifacts_redacted,
    request,
    unused_port,
)
from harness.scripted_upstream import FakeUpstream, ScriptedScenario, start_fake_upstream


class _NoDefaultUpstream(FakeUpstream):
    """Isolated class state so missing-default probes do not share session counters."""

    calls: list = []
    scenario: ScriptedScenario | None = None
    chat_include_usage_empty_choices: bool = False
    _attempt_counter: int = 0
    _counter_lock = threading.Lock()


def _error_body(raw: bytes) -> dict:
    parsed = json.loads(raw)
    assert "error" in parsed and isinstance(parsed["error"], dict)
    assert "type" in parsed["error"] and "message" in parsed["error"]
    return parsed


def _raw_request(
    base: str,
    path: str,
    *,
    method: str = "POST",
    body: bytes | None = None,
    token: str | None = CALLER,
    content_type: str | None = None,
    follow_redirects: bool = False,
):
    headers: dict[str, str] = {}
    if token is not None:
        headers["Authorization"] = f"Bearer {token}"
    if content_type is not None:
        headers["Content-Type"] = content_type
    elif body is not None:
        headers["Content-Type"] = "application/json"
    req = Request(base + path, data=body, headers=headers, method=method)
    try:
        with urlopen(req, timeout=5) as response:
            return response.status, dict(response.headers), response.read()
    except HTTPError as error:
        # urllib follows redirects by default for some codes; surface Location anyway.
        headers_out = dict(error.headers)
        if not follow_redirects and error.code in {301, 302, 303, 307, 308}:
            return error.code, headers_out, error.read()
        return error.code, headers_out, error.read()


def _chat_payload(**extra):
    payload = {"messages": [{"role": "user", "content": PROMPT_CANARY}]}
    payload.update(extra)
    return payload


def _assert_zero_upstream(router):
    assert router["upstream"].calls == []
    scenario = ScriptedScenario()
    router["upstream"].scenario = scenario
    scenario.assert_complete()


def _assert_router_owned_reject(status: int, headers: dict, upstream_calls: list, *, raw: bytes = b""):
    """Deterministic reject: 4xx, no redirect, no upstream, no accidental success body."""
    assert 400 <= status < 500, f"status={status} body={raw[:200]!r}"
    assert status not in {301, 302, 303, 307, 308}
    assert not headers.get("Location")
    assert upstream_calls == []


def test_http_04_path_method_and_body_negatives(api, router):
    """HTTP-04: trailing slash, wrong method, unknown path, bad JSON — no upstream."""
    base = router["base"]
    Upstream = router["upstream"]
    Upstream.reset_state()

    # Trailing slash must not redirect onto the real POST route.
    status, headers, raw = _raw_request(
        base,
        "/v1/chat/completions/",
        body=json.dumps(_chat_payload(model="chat")).encode(),
    )
    _assert_router_owned_reject(status, headers, Upstream.calls, raw=raw)
    assert status in {404, 405}

    # Duplicated /v1 prefix is an unknown endpoint, not a rewrite.
    status, headers, raw = _raw_request(
        base,
        "/v1/v1/chat/completions",
        body=json.dumps(_chat_payload(model="chat")).encode(),
    )
    _assert_router_owned_reject(status, headers, Upstream.calls, raw=raw)
    assert status in {404, 405}

    # Truly unknown endpoint.
    status, headers, raw = _raw_request(
        base,
        "/v1/not-a-real-endpoint",
        body=json.dumps(_chat_payload(model="chat")).encode(),
    )
    _assert_router_owned_reject(status, headers, Upstream.calls, raw=raw)
    assert status in {404, 405}

    # Wrong method on a registered path: reserved /v1 path, not docs HTML success.
    status, headers, raw = _raw_request(base, "/v1/chat/completions", method="GET")
    _assert_router_owned_reject(status, headers, Upstream.calls, raw=raw)
    assert status in {404, 405}
    assert b"<!doctype html>" not in raw.lower() and b"<html" not in raw.lower()

    # Query string on a valid path is fine (no accidental 404/redirect).
    status, _, raw = api("/v1/chat/completions?n=1", _chat_payload(model="chat"))
    assert status == 200
    assert Upstream.calls, "valid path+query must reach upstream"
    Upstream.reset_state()

    # Malformed JSON and non-object top-level shapes reject before upstream.
    for body in (
        b'{"model":"chat","messages":',
        b"[]",
        b'"scalar"',
        b"null",
        b"1",
        b"true",
    ):
        status, headers, raw = _raw_request(base, "/v1/chat/completions", body=body)
        assert status == 400, body
        err = _error_body(raw)
        assert err["error"]["type"] == "invalid-request"
        assert Upstream.calls == []

    # Wrong content-type with a non-JSON body is a router-owned rejection.
    status, _, raw = _raw_request(
        base,
        "/v1/chat/completions",
        body=b"model=chat&messages=hi",
        content_type="application/x-www-form-urlencoded",
    )
    assert status == 400
    assert _error_body(raw)["error"]["type"] == "invalid-request"
    assert Upstream.calls == []

    status, _, raw = _raw_request(
        base,
        "/v1/chat/completions",
        body=b"<chat/>",
        content_type="application/xml",
    )
    assert status == 400
    assert _error_body(raw)["error"]["type"] == "invalid-request"
    _assert_zero_upstream(router)


def test_http_05_model_admission_and_discovery_agree(api, router):
    """HTTP-05: omitted/default, nonexistent, unauthorized — distinct safe errors."""
    Upstream = router["upstream"]
    Upstream.reset_state()

    # Discovery catalog for the allowed caller.
    status, _, raw = api("/v1/models")
    assert status == 200
    allowed_ids = {item["id"] for item in json.loads(raw)["data"]}
    assert "chat" in allowed_ids
    assert "does-not-exist" not in allowed_ids

    # Omitted model with configured default uses default_model_group and reaches upstream.
    Upstream.scenario = ScriptedScenario().expect(path_suffix="/chat/completions")
    status, _, raw = api("/v1/chat/completions", _chat_payload())
    assert status == 200
    assert json.loads(raw)["choices"][0]["message"]["content"] == "synthetic chat"
    assert Upstream.calls[0]["body"]["model"] == "synthetic-chat"
    Upstream.scenario.assert_complete()
    Upstream.reset_state()

    # Nonexistent group: not discoverable and rejected with model-not-allowed.
    status, _, raw = api("/v1/chat/completions", _chat_payload(model="does-not-exist"))
    assert status == 403
    assert _error_body(raw)["error"]["type"] == "model-not-allowed"
    assert Upstream.calls == []

    # Unauthorized group for DENIED_CALLER: discovery and admission agree.
    status, _, raw = api("/v1/models", token=DENIED_CALLER)
    assert status == 200
    denied_ids = {item["id"] for item in json.loads(raw)["data"]}
    assert denied_ids == {"chat"}
    assert "responses" not in denied_ids

    status, _, raw = api(
        "/v1/responses",
        {"model": "responses", "input": PROMPT_CANARY},
        token=DENIED_CALLER,
    )
    assert status == 403
    assert _error_body(raw)["error"]["type"] == "model-not-allowed"
    assert Upstream.calls == []

    # Same caller requesting a group that discovery lists for them still works.
    Upstream.scenario = ScriptedScenario().expect(path_suffix="/chat/completions")
    status, _, raw = api(
        "/v1/chat/completions",
        _chat_payload(model="chat"),
        token=DENIED_CALLER,
    )
    assert status == 200
    Upstream.scenario.assert_complete()


@pytest.fixture
def no_default_router(router, tmp_path_factory):
    """Secondary router with no server.default_model_group for missing-model."""
    work = tmp_path_factory.mktemp("http-no-default")
    _NoDefaultUpstream.reset_state()
    upstream, thread, upstream_url = start_fake_upstream(_NoDefaultUpstream)
    port = unused_port()
    config = f'''server:
  listen: "127.0.0.1:{port}"
  identifiers: {{mode: passthrough}}
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  chat: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
models:
  chat: {{strategy: static, targets: [{{provider: chat, model: synthetic-chat, tool_support: {{openai_chat: [tools, tool_choice]}}}}]}}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [chat]
'''
    config_path = work / "config.yaml"
    config_path.write_text(config)
    binary = Path(router["work"]) / "router"
    assert binary.is_file(), "session fixture must have built the router binary"
    build_env = os.environ | {"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
    process = subprocess.Popen(
        [str(binary), "-config", "config.yaml"],
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
    try:
        yield {"base": base, "work": work, "upstream": _NoDefaultUpstream, "server": upstream}
    finally:
        process.terminate()
        process.wait(timeout=5)
        upstream.shutdown()
        thread.join(timeout=5)
        assert_generated_artifacts_redacted({"work": work})


def test_http_05_omitted_model_without_default_rejects(no_default_router):
    """HTTP-05: omitted model with no default_model_group → missing-model, zero upstream."""
    base = no_default_router["base"]
    Upstream = no_default_router["upstream"]
    Upstream.reset_state()
    Upstream.scenario = ScriptedScenario()

    status, _, raw = request(base, "/v1/chat/completions", _chat_payload())
    assert status == 400
    assert _error_body(raw)["error"]["type"] == "missing-model"
    assert Upstream.calls == []
    Upstream.scenario.assert_complete()

    # Explicit model still works on the same router (default omission is the only gap).
    Upstream.scenario = ScriptedScenario().expect(path_suffix="/chat/completions")
    status, _, raw = request(base, "/v1/chat/completions", _chat_payload(model="chat"))
    assert status == 200
    Upstream.scenario.assert_complete()
