# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import json
import os
import socket
import subprocess
import time
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import pytest

from harness import (
    CALLER,
    CALLER_DIGEST,
    DENIED_CALLER,
    DENIED_CALLER_DIGEST,
    FORBIDDEN_ARTIFACT_VALUES,
    PROMPT_CANARY,
    TOOL_CANARY,
)
from harness.scripted_upstream import FakeUpstream, start_fake_upstream

__all__ = [
    "CALLER",
    "DENIED_CALLER",
    "CALLER_DIGEST",
    "DENIED_CALLER_DIGEST",
    "PROMPT_CANARY",
    "TOOL_CANARY",
    "FORBIDDEN_ARTIFACT_VALUES",
    "assert_generated_artifacts_redacted",
    "request",
    "unused_port",
]


def unused_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(base, path, payload=None, token=CALLER):
    headers = {}
    if token is not None:
        headers["Authorization"] = f"Bearer {token}"
    data = None if payload is None else json.dumps(payload).encode()
    if data:
        headers["Content-Type"] = "application/json"
    try:
        with urlopen(Request(base + path, data=data, headers=headers), timeout=5) as response:
            return response.status, response.headers, response.read()
    except HTTPError as error:
        return error.code, error.headers, error.read()


@pytest.fixture(scope="session")
def router(tmp_path_factory):
    work = tmp_path_factory.mktemp("api-compat")
    FakeUpstream.reset_state()
    upstream, thread, upstream_url = start_fake_upstream(FakeUpstream)
    port = unused_port()
    config = f'''server:
  listen: "127.0.0.1:{port}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
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
  responses-to-chat: {{strategy: static, targets: [{{provider: chat, model: synthetic-bridge-chat, tool_support: {{openai_chat: [tools, tool_choice]}}, responses_to_chat: {{enabled: true, text: true, function_tools: true, tool_choice: true, validation_status: passed}}}}]}}
  chat-to-responses: {{strategy: static, targets: [{{provider: responses, model: synthetic-bridge-responses, tool_support: {{openai_responses: [function, tool_choice]}}, bridges: {{chat_to_responses: {{enabled: true, text: true, tools: true, tool_choice: true}}}}}}]}}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [chat, responses, messages, responses-to-chat, chat-to-responses]
  - id: synthetic-denied
    token_sha256: {DENIED_CALLER_DIGEST}
    allow: [chat]
'''
    config_path = work / "config.yaml"
    config_path.write_text(config)
    # GOTOOLCHAIN=local avoids re-verifying a downloaded toolchain when GOSUMDB=off.
    build_env = os.environ | {"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
    router_binary = work / "router"
    subprocess.run(
        ["go", "build", "-tags", "dev_no_license", "-o", str(router_binary), "./cmd/metrum-ai-router"],
        cwd=Path(__file__).parents[3],
        env=build_env,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
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
    yield {"base": base, "work": work, "upstream": FakeUpstream, "server": upstream}
    process.terminate()
    process.wait(timeout=5)
    upstream.shutdown()
    thread.join(timeout=5)


@pytest.fixture(autouse=True)
def clear_calls(router):
    FakeUpstream.reset_state()
    yield
    assert_generated_artifacts_redacted(router)


def assert_generated_artifacts_redacted(router):
    work = Path(router["work"])
    router_binary = work / "router"
    for path in work.rglob("*"):
        if not path.is_file() or path == router_binary:
            continue
        contents = path.read_bytes()
        assert not any(value.encode() in contents for value in FORBIDDEN_ARTIFACT_VALUES)


@pytest.fixture
def api(router):
    return lambda path, payload=None, token=CALLER: request(router["base"], path, payload, token)
