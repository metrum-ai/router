#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Run pinned agent clients through a local router and scripted Chat upstream.

No provider calls or credentials: the only generated tool operation writes a
fixed file in a disposable working directory. Unimplemented matrix cells must
not be inferred from this smoke.
"""
from __future__ import annotations

import hashlib
import json
import os
import signal
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PI = ROOT / "tests/agent_compat/node_modules/.bin/pi"
TOKEN = "agent-compat-local-test-token"
DIGEST = hashlib.sha256(TOKEN.encode()).hexdigest()


class Mock(BaseHTTPRequestHandler):
    calls: list[dict] = []
    errors: list[str] = []

    def log_message(self, *_args):
        pass

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_error(404)
            return
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        Mock.calls.append(body)
        index = len(Mock.calls)
        if not body.get("stream") or index > 2:
            Mock.errors.append("unexpected stream flag or extra request")
            self.send_error(400)
            return
        if index == 1:
            tool_names = {tool.get("function", {}).get("name") for tool in body.get("tools", [])}
            if "write" not in tool_names:
                Mock.errors.append("pi did not declare write tool")
                self.send_error(400)
                return
            frames = [
                {"role": "assistant", "tool_calls": [{"index": 0, "id": "call_write", "type": "function", "function": {"name": "write", "arguments": '{"path":"result.txt",'}}]},
                {"tool_calls": [{"index": 0, "function": {"arguments": '"content":"tool-result-42\\n"}'}}]},
            ]
            finish = "tool_calls"
        else:
            results = [m for m in body.get("messages", []) if m.get("role") == "tool" and m.get("tool_call_id") == "call_write"]
            if len(results) != 1 or not results[0].get("content"):
                Mock.errors.append("tool-result continuation not forwarded")
                self.send_error(400)
                return
            frames = [{"role": "assistant", "content": "Completed."}]
            finish = "stop"
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for delta in frames:
            chunk = {"id": "chatcmpl_mock", "object": "chat.completion.chunk", "created": 1, "model": "synthetic-chat", "choices": [{"index": 0, "delta": delta, "finish_reason": None}]}
            self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
            self.wfile.flush()
        chunk = {"id": "chatcmpl_mock", "object": "chat.completion.chunk", "created": 1, "model": "synthetic-chat", "choices": [{"index": 0, "delta": {}, "finish_reason": finish}]}
        self.wfile.write(("data: " + json.dumps(chunk) + "\n\ndata: [DONE]\n\n").encode())
        self.wfile.flush()


def free_port():
    import socket
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def runtime_environment():
    env = {"PATH": os.environ["PATH"], "HOME": os.environ["HOME"], "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
    # setup-python on CI may need this to load its shared libpython.
    if "LD_LIBRARY_PATH" in os.environ:
        env["LD_LIBRARY_PATH"] = os.environ["LD_LIBRARY_PATH"]
    return env


def main():
    # Let cancellation unwind subprocess and router cleanup.
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupted)
    client = sys.argv[1] if len(sys.argv) > 1 else "pi"
    depth = int(sys.argv[2]) if len(sys.argv) > 2 else 1
    if depth < 0 or depth > 8:
        raise SystemExit("depth must be between 0 and 8")
    if client not in {"pi", "langgraph", "openai-agents"}:
        raise SystemExit("client must be pi, langgraph, or openai-agents")
    if client == "pi" and not PI.is_file():
        raise SystemExit("install pinned client with npm ci --ignore-scripts --prefix tests/agent_compat")
    Mock.calls.clear()
    Mock.errors.clear()
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Mock)
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="router-agent-compat-") as tmp:
            work = Path(tmp)
            env = runtime_environment()
            binary = work / "router"
            subprocess.run(["go", "build", "-tags", "dev_no_license", "-o", str(binary), "./cmd/metrum-ai-router"], cwd=ROOT, env=env, check=True)
            ports = []
            while len(ports) < depth:
                candidate = free_port()
                if candidate != upstream.server_port and candidate not in ports:
                    ports.append(candidate)
            port = ports[-1] if ports else upstream.server_port
            env["AGENT_COMPAT_NEXT_TOKEN"] = TOKEN
            for index, hop_port in enumerate(ports):
                upstream_port = upstream.server_port if index == 0 else ports[index - 1]
                target_model = "synthetic-chat" if index == 0 else "chat"
                config = f'''server:
  listen: "127.0.0.1:{hop_port}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / f'router-{index}.jsonl'}"}}
state_path: "{work / f'state-{index}.json'}"
providers:
  mock: {{base_url: "http://127.0.0.1:{upstream_port}/v1", dialect: openai-chat, api_key: "${{AGENT_COMPAT_NEXT_TOKEN}}", api_key_env: AGENT_COMPAT_NEXT_TOKEN}}
models:
  chat: {{strategy: static, targets: [{{provider: mock, model: {target_model}, tool_support: {{openai_chat: [tools, tool_choice]}}}}]}}
callers:
  - id: agent-compat
    token_sha256: "{DIGEST}"
    allow: [chat]
    rate: {{rpm: 2400, tpm: 100000000, concurrent: 160}}
    quota: {{day: {{requests: 100000, tokens: 400000000}}, month: {{tokens: 8000000000}}}}
    key: {{lifetime_tokens: 40000000000}}
'''
                (work / f"config-{index}.yaml").write_text(config, encoding="utf-8")
            agent_dir = work / "agent"
            agent_dir.mkdir()
            models = {"providers": {"router-compat": {"baseUrl": f"http://127.0.0.1:{port}/v1", "api": "openai-completions", "apiKey": TOKEN, "models": [{"id": "chat", "name": "Synthetic", "reasoning": False, "input": ["text"], "contextWindow": 16384, "maxTokens": 512, "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}]}}}
            (agent_dir / "models.json").write_text(json.dumps(models), encoding="utf-8")
            stderr_path = work / "router.stderr"
            with stderr_path.open("w", encoding="utf-8") as stderr:
                routers = []
                try:
                    from urllib.request import urlopen
                    import time
                    for index, hop_port in enumerate(ports):
                        router = subprocess.Popen([str(binary), "-config", f"config-{index}.yaml"], cwd=work, env=env, stdout=stderr, stderr=stderr)
                        routers.append(router)
                        deadline = time.monotonic() + 60
                        while time.monotonic() < deadline:
                            if router.poll() is not None:
                                raise RuntimeError(f"router {index} exited before readiness")
                            try:
                                if urlopen(f"http://127.0.0.1:{hop_port}/readyz", timeout=1).status == 200:
                                    break
                            except OSError:
                                time.sleep(0.05)
                        else:
                            raise RuntimeError(f"router {index} did not become ready")
                    child_env = env | {"HOME": str(work), "PI_CODING_AGENT_DIR": str(agent_dir), "ROUTER_BASE_URL": f"http://127.0.0.1:{port}/v1", "ROUTER_CALLER_TOKEN": TOKEN}
                    if client == "pi":
                        command = [str(PI), "--provider", "router-compat", "--model", "chat", "--mode", "json", "--tools", "write", "--no-extensions", "--no-mcp", "--no-context-files", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-session", "--no-approve", "--offline", "Write the requested file and finish."]
                    else:
                        command = [sys.executable, str(ROOT / "tests/agent_compat/sdk_client.py"), client]
                    run = subprocess.run(command, cwd=work, env=child_env, capture_output=True, text=True, timeout=90)
                    if run.returncode:
                        raise RuntimeError(f"{client} failed ({run.returncode}): {run.stderr[-1500:]}\n{run.stdout[-1500:]}")
                    if client == "pi":
                        events = [json.loads(line) for line in run.stdout.splitlines() if line.strip()]
                        if not any(e.get("type") == "tool_execution_end" and e.get("toolName") == "write" and not e.get("isError") for e in events):
                            raise RuntimeError(f"pi did not report a successful write tool execution; events={run.stdout[-4000:]}; mock errors={Mock.errors}; mock calls={len(Mock.calls)}")
                    elif "agent tool loop complete" not in run.stdout:
                        raise RuntimeError(f"{client} did not report successful tool execution")
                    if (work / "result.txt").read_text(encoding="utf-8") != "tool-result-42\n":
                        raise RuntimeError("generated file content did not match")
                    if Mock.errors or len(Mock.calls) != 2:
                        raise RuntimeError(f"mock errors={Mock.errors}, calls={len(Mock.calls)}")
                    print(f"PASS: {client} / OpenAI Chat / same dialect / depth {depth}: streamed tool call and tool-result continuation")
                finally:
                    for router in reversed(routers):
                        if router.poll() is None:
                            router.terminate()
                    for router in reversed(routers):
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
