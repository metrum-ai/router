# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""ROUTE-01..05: dialect matrix, zero-upstream rejections, bridges, capability fallback."""

from __future__ import annotations

import json
import os
import subprocess
import time
from pathlib import Path

import pytest
import yaml

from conftest import PROMPT_CANARY, TOOL_CANARY, request, unused_port
from harness import CALLER, CALLER_DIGEST
from harness.manifest_loader import cases_by_id, load_manifests
from harness.scripted_upstream import FakeUpstream, ScriptedScenario

MATRIX_PATH = Path(__file__).resolve().parents[1] / "testdata" / "route_dialect_matrix.yaml"

LOOKUP_TOOL_RESPONSES = {
    "type": "function",
    "name": "lookup",
    "description": TOOL_CANARY,
    "parameters": {"type": "object", "properties": {"q": {"type": "string"}}},
}
LOOKUP_TOOL_CHAT = {
    "type": "function",
    "function": {
        "name": "lookup",
        "description": TOOL_CANARY,
        "parameters": {"type": "object", "properties": {"q": {"type": "string"}}},
    },
}


def _body(raw: bytes) -> dict:
    return json.loads(raw)


def test_route_manifest_rows_cover_p0_and_blocked_redis():
    cases = cases_by_id()
    for case_id in ("ROUTE-01", "ROUTE-02", "ROUTE-03", "ROUTE-04", "ROUTE-05"):
        assert cases[case_id]["priority"] == "P0"
        assert cases[case_id]["disposition"] != "blocked"
    for case_id in ("ROUTE-06", "ROUTE-07", "ROUTE-08", "ROUTE-09"):
        assert cases[case_id]["disposition"] == "blocked"
        assert "Redis" in cases[case_id]["rationale"]
    assert cases["ROUTE-10"]["disposition"] == "blocked"
    assert any(c["id"].startswith("ROUTE-") for c in load_manifests())


def test_route_dialect_matrix_is_machine_readable():
    """ROUTE-01: allowed/rejected matrix is explicit; skins alone never imply support."""
    raw = yaml.safe_load(MATRIX_PATH.read_text())
    pairs = raw["pairs"]
    assert len(pairs) >= 8
    dispositions = {row["disposition"] for row in pairs}
    assert "native_supported" in dispositions
    assert "supported_translation" in dispositions
    assert "policy_rejection" in dispositions

    by_key = {(row["inbound"], row["outbound"], tuple(row["features"])): row for row in pairs}
    assert by_key[("openai-responses", "openai-chat", ("stream",))]["disposition"] == "policy_rejection"
    assert by_key[("openai-chat", "openai-responses", ("stream",))]["disposition"] == "policy_rejection"
    assert by_key[("openai-responses", "openai-chat", ("previous_response_id",))][
        "disposition"
    ] == "policy_rejection"
    assert by_key[("openai-responses", "openai-chat", ("structured_output",))][
        "disposition"
    ] == "policy_rejection"
    # Provider skin without bridge opt-in remains a rejection row.
    disabled = by_key[("openai-responses", "openai-chat", ("text",))]
    assert disabled["disposition"] == "policy_rejection"
    assert "responses_to_chat.enabled=false" in disabled["requires"]


def test_route_rejections_make_zero_upstream_calls(api, router):
    """ROUTE-02: disabled bridge / unsupported feature → zero upstream calls."""
    before = len(router["upstream"].calls)

    # Bridge opt-in disabled: chat group has no responses_to_chat.
    status, _, raw = api("/v1/responses", {"model": "chat", "input": PROMPT_CANARY})
    assert status == 502
    assert _body(raw)["error"]["type"] == "no-eligible-target"
    assert len(router["upstream"].calls) == before

    # Unsupported structured output is not silently stripped to admit the text bridge.
    status, _, raw = api(
        "/v1/responses",
        {
            "model": "responses-to-chat",
            "input": PROMPT_CANARY,
            "text": {
                "format": {
                    "type": "json_schema",
                    "name": "ticket",
                    "schema": {"type": "object", "properties": {"id": {"type": "string"}}},
                }
            },
        },
    )
    assert status == 502
    assert _body(raw)["error"]["type"] == "no-eligible-target"
    assert len(router["upstream"].calls) == before


def test_route_streaming_and_stateful_bridges_remain_rejected(api, router):
    """ROUTE-04: keep unary-only bridge restrictions; do not accept 200 without evidence."""
    before = len(router["upstream"].calls)
    status, _, raw = api(
        "/v1/responses",
        {
            "model": "responses-to-chat",
            "input": PROMPT_CANARY,
            "previous_response_id": "resp_synthetic",
        },
    )
    assert status == 502
    assert _body(raw)["error"]["type"] == "no-eligible-target"
    assert len(router["upstream"].calls) == before

    status, _, raw = api(
        "/v1/chat/completions",
        {
            "model": "chat-to-responses",
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
            "stream": True,
        },
    )
    assert status == 502
    assert _body(raw)["error"]["type"] == "no-eligible-target"
    assert len(router["upstream"].calls) == before

    status, _, raw = api(
        "/v1/responses",
        {"model": "responses-to-chat", "input": PROMPT_CANARY, "stream": True},
    )
    assert status == 502
    assert _body(raw)["error"]["type"] == "no-eligible-target"
    assert len(router["upstream"].calls) == before


def test_route_unary_bridges_preserve_tool_semantics(api, router):
    """ROUTE-03: supported unary bridges preserve tool names/call IDs."""
    status, _, raw = api(
        "/v1/responses",
        {
            "model": "responses-to-chat",
            "input": PROMPT_CANARY,
            "tools": [LOOKUP_TOOL_RESPONSES],
            "tool_choice": "auto",
        },
    )
    payload = _body(raw)
    assert status == 200
    assert any(item.get("type") == "function_call" and item.get("name") == "lookup" for item in payload["output"])
    assert router["upstream"].calls[-1]["path"] == "/v1/chat/completions"
    assert router["upstream"].calls[-1]["body"]["tools"][0]["function"]["name"] == "lookup"

    status, _, raw = api(
        "/v1/chat/completions",
        {
            "model": "chat-to-responses",
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
            "tools": [LOOKUP_TOOL_CHAT],
            "tool_choice": "auto",
        },
    )
    chat = _body(raw)
    assert status == 200
    assert chat["choices"][0]["message"]["tool_calls"][0]["function"]["name"] == "lookup"
    assert chat["choices"][0]["message"]["tool_calls"][0]["id"] == "call_synthetic"
    assert router["upstream"].calls[-1]["path"] == "/v1/responses"
    assert router["upstream"].calls[-1]["body"]["tools"][0]["name"] == "lookup"


@pytest.fixture
def capability_fallback_router(router, tmp_path):
    """Isolated router with tool-capable primary + plain fallback (ROUTE-05)."""
    upstream_url = f"http://127.0.0.1:{router['server'].server_port}"
    port = unused_port()
    work = tmp_path / "route-fallback"
    work.mkdir()
    config = f'''server:
  listen: "127.0.0.1:{port}"
  identifiers: {{mode: passthrough}}
  default_model_group: tools-fallback
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  chat: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
models:
  tools-fallback:
    strategy: static
    targets:
      - provider: chat
        model: tool-model
        tool_support: {{openai_chat: [tools, tool_choice]}}
      - provider: chat
        model: plain-model
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [tools-fallback]
'''
    config_path = work / "config.yaml"
    config_path.write_text(config)
    binary = Path(router["work"]) / "router"
    assert binary.is_file()
    process = subprocess.Popen(
        [str(binary), "-config", "config.yaml"],
        cwd=work,
        env=os.environ | {"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"},
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
    yield {"base": base, "work": work, "process": process}
    process.terminate()
    process.wait(timeout=5)


def test_route_capability_fallback_skips_ineligible_target(capability_fallback_router, router):
    """ROUTE-05: ineligible plain fallback is never called after tool primary fails."""
    scenario = ScriptedScenario()
    seen_models: list[str] = []

    def validate_tool_primary(call: dict) -> None:
        seen_models.append(call["body"]["model"])
        assert call["body"]["model"] == "tool-model"
        assert call["body"]["tools"][0]["function"]["name"] == "lookup"

    scenario.expect(
        path_suffix="/chat/completions",
        validate=validate_tool_primary,
        status=500,
        response={"error": {"message": "temporary tool target failure"}},
    )
    FakeUpstream.scenario = scenario
    before = len(FakeUpstream.calls)

    status, _, raw = request(
        capability_fallback_router["base"],
        "/v1/chat/completions",
        {
            "model": "tools-fallback",
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
            "tools": [LOOKUP_TOOL_CHAT],
            "tool_choice": "auto",
        },
    )
    assert status == 502
    err = _body(raw)["error"]["type"]
    assert err in {"upstream-failed", "no-eligible-target"}
    new_calls = FakeUpstream.calls[before:]
    assert len(new_calls) == 1
    assert new_calls[0]["body"]["model"] == "tool-model"
    assert "plain-model" not in seen_models
    assert all(call["body"]["model"] != "plain-model" for call in new_calls)
