# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""OPS usage_db-enabled profile and canary artifact checks (issue #94)."""

from __future__ import annotations

import json
import os
import sqlite3
import subprocess
import time
from pathlib import Path

import pytest

from conftest import (
    CALLER,
    CALLER_DIGEST,
    DENIED_CALLER,
    DENIED_CALLER_DIGEST,
    PROMPT_CANARY,
    TOOL_CANARY,
    assert_generated_artifacts_redacted,
    request,
    unused_port,
)
from harness.constants import FORBIDDEN_ARTIFACT_VALUES

# OPS-10 extensions beyond the shared auth/prompt/tool canaries.
IMAGE_CANARY = "api-compat-image-canary"
REASONING_CANARY = "api-compat-reasoning-canary"
TOOL_RESULT_CANARY = "api-compat-tool-result-canary"

EXPECTED_CHAT_USAGE = {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}


@pytest.fixture(scope="module")
def usage_router(router, tmp_path_factory):
    """Opt-in router with usage_db sqlite enabled; default session fixture stays disabled."""
    work = tmp_path_factory.mktemp("api-compat-usage")
    usage_db = work / "usage.sqlite"
    port = unused_port()
    upstream_port = router["server"].server_address[1]
    upstream_url = f"http://127.0.0.1:{upstream_port}"
    config = f'''server:
  listen: "127.0.0.1:{port}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {{enabled: false}}
  usage_db:
    enabled: true
    driver: sqlite
    path: "{usage_db}"
    migration_policy: auto-safe
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  chat: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
  responses: {{base_url: "{upstream_url}/v1", dialect: openai-responses}}
  messages: {{base_url: "{upstream_url}/anthropic", dialect: anthropic}}
models:
  chat: {{strategy: static, targets: [{{provider: chat, model: synthetic-chat, tool_support: {{openai_chat: [tools, tool_choice]}}}}]}}
  responses: {{strategy: static, targets: [{{provider: responses, model: synthetic-responses, tool_support: {{openai_responses: [function, tool_choice]}}}}]}}
  messages: {{strategy: static, targets: [{{provider: messages, model: synthetic-messages, tool_support: {{anthropic_messages: [client_tools]}}, request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}}}]}}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [chat, responses, messages]
  - id: synthetic-denied
    token_sha256: {DENIED_CALLER_DIGEST}
    allow: [chat]
'''
    config_path = work / "config.yaml"
    config_path.write_text(config)
    build_env = os.environ | {"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
    router_binary = Path(router["work"]) / "router"
    # DEVNULL: auto-safe migration logs fill PIPE and stall the child.
    stderr_path = work / "router.stderr"
    stderr_file = stderr_path.open("w", encoding="utf-8")
    process = subprocess.Popen(
        [str(router_binary), "-config", "config.yaml"],
        cwd=work,
        env=build_env,
        stdout=subprocess.DEVNULL,
        stderr=stderr_file,
        text=True,
    )
    base = f"http://127.0.0.1:{port}"
    for _ in range(400):
        if process.poll() is not None:
            stderr_file.close()
            raise RuntimeError(stderr_path.read_text()[-4000:])
        try:
            if request(base, "/readyz", token=CALLER)[0] == 200:
                break
        except OSError:
            time.sleep(0.05)
    else:
        process.terminate()
        stderr_file.close()
        raise RuntimeError(stderr_path.read_text()[-4000:] or "usage_router failed to become ready")
    config_path.unlink(missing_ok=True)
    yield {"base": base, "work": work, "usage_db": usage_db, "process": process}
    process.terminate()
    process.wait(timeout=5)
    stderr_file.close()
    assert_generated_artifacts_redacted({"work": work})


@pytest.fixture
def usage_api(usage_router):
    return lambda path, payload=None, token=CALLER: request(
        usage_router["base"], path, payload, token
    )


def _wait_usage_row(usage_db: Path, *, timeout_s: float = 2.0) -> tuple:
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        if usage_db.is_file():
            conn = sqlite3.connect(usage_db)
            try:
                row = conn.execute(
                    "SELECT input_tokens, output_tokens, total_tokens, stream, status "
                    "FROM request_usage ORDER BY ts DESC LIMIT 1"
                ).fetchone()
            except sqlite3.OperationalError:
                row = None
            finally:
                conn.close()
            if row is not None:
                return row
        time.sleep(0.05)
    raise AssertionError("usage_db did not record a request_usage row")


def test_ops01_unary_chat_usage_fields(usage_api, usage_router):
    """OPS-01: canonical chat usage fields on unary with usage_db enabled."""
    status, _, raw = usage_api(
        "/v1/chat/completions",
        {
            "model": "chat",
            "messages": [{"role": "user", "content": PROMPT_CANARY}],
        },
    )
    assert status == 200
    payload = json.loads(raw)
    usage = payload["usage"]
    assert usage == EXPECTED_CHAT_USAGE
    assert usage["total_tokens"] == usage["prompt_tokens"] + usage["completion_tokens"]

    input_tokens, output_tokens, total_tokens, stream, row_status = _wait_usage_row(
        usage_router["usage_db"]
    )
    assert (input_tokens, output_tokens, total_tokens) == (3, 2, 5)
    assert stream in (0, False)
    assert int(row_status) == 200


def test_ops10_extended_canaries_absent_from_artifacts(api, router):
    """OPS-10: extend canary coverage beyond auth/prompt/tool into failure + result paths."""
    status, _, _ = api(
        "/v1/chat/completions",
        {
            "model": "chat",
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": PROMPT_CANARY},
                        {"type": "text", "text": IMAGE_CANARY},
                    ],
                },
                {
                    "role": "assistant",
                    "content": None,
                    "tool_calls": [
                        {
                            "id": "call_canary",
                            "type": "function",
                            "function": {"name": "lookup", "arguments": "{}"},
                        }
                    ],
                },
                {
                    "role": "tool",
                    "tool_call_id": "call_canary",
                    "content": TOOL_RESULT_CANARY,
                },
                {"role": "user", "content": REASONING_CANARY},
            ],
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
        },
    )
    assert status == 200
    # Auth failure path must not persist canaries either.
    status, _, _ = api(
        "/v1/chat/completions",
        {"model": "chat", "messages": [{"role": "user", "content": PROMPT_CANARY}]},
        token=None,
    )
    assert status == 401
    status, _, _ = api(
        "/v1/responses",
        {"model": "responses", "input": REASONING_CANARY},
        token=DENIED_CALLER,
    )
    assert status == 403

    assert_generated_artifacts_redacted(router)
    extra = {IMAGE_CANARY, REASONING_CANARY, TOOL_RESULT_CANARY}
    work = Path(router["work"])
    router_binary = work / "router"
    for path in work.rglob("*"):
        if not path.is_file() or path == router_binary:
            continue
        contents = path.read_bytes()
        assert not any(value.encode() in contents for value in FORBIDDEN_ARTIFACT_VALUES)
        assert not any(value.encode() in contents for value in extra)
