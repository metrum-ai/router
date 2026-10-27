#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Runner for the TypeScript SDK API-compat suite (#271, #94 DoD 2).

Builds the router, starts the shared scripted fake upstream (reusing the
tests/api_compat harness config), runs `node --test` with the official
OpenAI and Anthropic TypeScript clients, and tears everything down. Offline
and deterministic. Usage: python3 tests/api_compat_ts/run_suite.py
"""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
API_COMPAT = ROOT / "tests" / "api_compat"
sys.path.insert(0, str(API_COMPAT))

from harness import CALLER, CALLER_DIGEST  # noqa: E402
from harness.scripted_upstream import FakeUpstream, start_fake_upstream  # noqa: E402

CONFIG = """server:
  listen: "127.0.0.1:{port}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{log_path}"}}
state_path: "{state_path}"
providers:
  chat: {{base_url: "{upstream}/v1", dialect: openai-chat}}
  responses: {{base_url: "{upstream}/v1", dialect: openai-responses}}
  messages: {{base_url: "{upstream}/anthropic", dialect: anthropic}}
models:
  chat: {{strategy: static, targets: [{{provider: chat, model: synthetic-chat, tool_support: {{openai_chat: [tools, tool_choice]}}}}]}}
  responses: {{strategy: static, targets: [{{provider: responses, model: synthetic-responses, tool_support: {{openai_responses: [function, tool_choice]}}}}]}}
  messages: {{strategy: static, targets: [{{provider: messages, model: synthetic-messages, tool_support: {{anthropic_messages: [client_tools]}}, request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}}}]}}
callers:
  - id: ts-sdk-suite
    token_sha256: "{digest}"
    allow: [chat, responses, messages]
    rate: {{rpm: 2400, tpm: 100000000, concurrent: 160}}
    quota: {{day: {{requests: 100000, tokens: 400000000}}, month: {{tokens: 8000000000}}}}
    key: {{lifetime_tokens: 40000000000}}
"""


def unused_port() -> int:
    import socket

    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_ready(base: str, process: subprocess.Popen, stderr_path: Path) -> None:
    import time
    from urllib.request import urlopen

    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"router exited: {stderr_path.read_text()[-2000:]}")
        try:
            if urlopen(base + "/readyz", timeout=2).status == 200:
                return
        except OSError:
            time.sleep(0.05)
    raise RuntimeError("router did not become ready")


def main() -> int:
    FakeUpstream.reset_state()
    upstream, thread, upstream_url = start_fake_upstream(FakeUpstream)
    port = unused_port()
    with tempfile.TemporaryDirectory() as tmp:
        work = Path(tmp)
        build_env = os.environ | {"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
        router_binary = work / "router"
        subprocess.run(
            ["go", "build", "-tags", "dev_no_license", "-o", str(router_binary), "./cmd/metrum-ai-router"],
            cwd=ROOT, env=build_env, check=True,
        )
        config = CONFIG.format(
            port=port, upstream=upstream_url, digest=CALLER_DIGEST,
            log_path=work / "router.jsonl", state_path=work / "state.json",
        )
        (work / "config.yaml").write_text(config, encoding="utf-8")
        stderr_path = work / "router.stderr"
        with stderr_path.open("w", encoding="utf-8") as stderr_file:
            process = subprocess.Popen(
                [str(router_binary), "-config", "config.yaml"],
                cwd=work, env=build_env, stderr=stderr_file,
            )
            try:
                wait_ready(f"http://127.0.0.1:{port}", process, stderr_path)
                env = os.environ | {
                    "ROUTER_BASE_URL": f"http://127.0.0.1:{port}",
                    "ROUTER_CALLER_TOKEN": CALLER,
                }
                run = subprocess.run(
                    ["npm", "test", "--prefix", str(ROOT / "tests" / "api_compat_ts")],
                    env=env, cwd=ROOT,
                )
                return run.returncode
            finally:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
    # upstream cleanup
    upstream.shutdown()
    thread.join(timeout=5)


if __name__ == "__main__":
    raise SystemExit(main())
