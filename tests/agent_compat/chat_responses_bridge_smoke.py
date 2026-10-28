#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Real Chat client tool continuation through the Chat-to-Responses bridge."""
from __future__ import annotations

import json
import os
import signal
import subprocess
import sys
import tempfile
import threading
import time
from http.server import ThreadingHTTPServer
from pathlib import Path
from urllib.request import urlopen

from codex_responses_smoke import Mock
from run_chat_smoke import DIGEST, PI, ROOT, TOKEN, free_port, runtime_environment


def main():
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupted)
    client = sys.argv[1] if len(sys.argv) > 1 else "pi"
    if client not in {"pi", "langgraph", "openai-agents"}:
        raise SystemExit("client must be pi, langgraph, or openai-agents")
    if client == "pi" and not PI.is_file():
        raise SystemExit("install pinned clients with npm ci --ignore-scripts --prefix tests/agent_compat")
    Mock.calls.clear()
    Mock.errors.clear()
    Mock.tool_name = "write"
    Mock.expect_strict = client == "openai-agents"
    Mock.tool_arguments = {"path": "result.txt", "content": "tool-result-42\n"}
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Mock)
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="router-agent-bridge-") as tmp:
            work = Path(tmp)
            env = runtime_environment()
            binary = work / "router"
            subprocess.run(["go", "build", "-tags", "dev_no_license", "-o", str(binary), "./cmd/metrum-ai-router"], cwd=ROOT, env=env, check=True)
            port = free_port()
            config = f'''server:
  listen: "127.0.0.1:{port}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  mock: {{base_url: "http://127.0.0.1:{upstream.server_port}/v1", dialect: openai-responses}}
models:
  chat: {{strategy: static, targets: [{{provider: mock, model: synthetic-responses, tool_support: {{openai_responses: [function, tool_choice]}}, bridges: {{chat_to_responses: {{enabled: true, text: true, tools: true, tool_choice: true}}}}}}]}}
callers:
  - id: agent-compat
    token_sha256: "{DIGEST}"
    allow: [chat]
    rate: {{rpm: 2400, tpm: 100000000, concurrent: 160}}
    quota: {{day: {{requests: 100000, tokens: 400000000}}, month: {{tokens: 8000000000}}}}
    key: {{lifetime_tokens: 40000000000}}
'''
            (work / "config.yaml").write_text(config, encoding="utf-8")
            agent_dir = work / "agent"
            agent_dir.mkdir()
            models = {"providers": {"router-compat": {"baseUrl": f"http://127.0.0.1:{port}/v1", "api": "openai-completions", "apiKey": TOKEN, "models": [{"id": "chat", "name": "Synthetic", "reasoning": False, "input": ["text"], "contextWindow": 16384, "maxTokens": 512, "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}]}}}
            (agent_dir / "models.json").write_text(json.dumps(models), encoding="utf-8")
            with (work / "router.stderr").open("w", encoding="utf-8") as stderr:
                router = subprocess.Popen([str(binary), "-config", "config.yaml"], cwd=work, env=env, stdout=stderr, stderr=stderr)
                try:
                    deadline = time.monotonic() + 60
                    while time.monotonic() < deadline:
                        if router.poll() is not None:
                            raise RuntimeError("router exited before readiness")
                        try:
                            if urlopen(f"http://127.0.0.1:{port}/readyz", timeout=1).status == 200:
                                break
                        except OSError:
                            time.sleep(0.05)
                    else:
                        raise RuntimeError("router did not become ready")
                    child_env = env | {"HOME": str(work), "PI_CODING_AGENT_DIR": str(agent_dir), "ROUTER_BASE_URL": f"http://127.0.0.1:{port}/v1", "ROUTER_CALLER_TOKEN": TOKEN}
                    if client == "pi":
                        command = [str(PI), "--provider", "router-compat", "--model", "chat", "--mode", "json", "--tools", "write", "--no-extensions", "--no-mcp", "--no-context-files", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-session", "--no-approve", "--offline", "Write the requested file and finish."]
                    else:
                        command = [sys.executable, str(ROOT / "tests/agent_compat/sdk_client.py"), client]
                    run = subprocess.run(command, cwd=work, env=child_env, capture_output=True, text=True, timeout=90)
                    if run.returncode:
                        raise RuntimeError(f"{client} failed ({run.returncode}), mock errors={Mock.errors}: {run.stderr[-1500:]}\n{run.stdout[-1500:]}")
                    if client == "pi":
                        events = [json.loads(line) for line in run.stdout.splitlines() if line.strip()]
                        if not any(e.get("type") == "tool_execution_end" and e.get("toolName") == "write" and not e.get("isError") for e in events):
                            raise RuntimeError(f"pi did not execute write: {run.stdout[-2500:]}; mock errors={Mock.errors}")
                    elif "agent tool loop complete" not in run.stdout:
                        raise RuntimeError(f"{client} did not report successful tool execution")
                    if (work / "result.txt").read_text(encoding="utf-8") != "tool-result-42\n":
                        raise RuntimeError("generated file content did not match")
                    if Mock.errors or len(Mock.calls) != 2:
                        raise RuntimeError(f"mock errors={Mock.errors}, calls={len(Mock.calls)}")
                    print(f"PASS: {client} / Chat to Responses bridge / depth 1: streamed tool call and continuation")
                finally:
                    router.terminate()
                    try:
                        router.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        router.kill()
                        router.wait(timeout=5)
    finally:
        upstream.shutdown()
        upstream.server_close()
        thread.join(timeout=5)


if __name__ == "__main__":
    main()
