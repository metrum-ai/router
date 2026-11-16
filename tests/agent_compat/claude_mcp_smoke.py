#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Run pinned Claude Code through the local MCP gateway and a scripted model."""
from __future__ import annotations

import hashlib
import json
import os
import signal
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit
from urllib.request import urlopen

from run_chat_smoke import free_port

ROOT = Path(__file__).resolve().parents[2]
CLAUDE = ROOT / "tests/agent_compat/node_modules/@anthropic-ai/claude-code/cli-wrapper.cjs"
TOKEN = "mcp-gateway-local-test-token"
UPSTREAM_CREDENTIAL = "mcp-upstream-local-test-credential"
DIGEST = hashlib.sha256(TOKEN.encode()).hexdigest()


class Fixtures(BaseHTTPRequestHandler):
    model_calls: list[dict] = []
    mcp_methods: list[str] = []
    errors: list[str] = []
    tool_name = ""

    def log_message(self, *_args):
        pass

    def do_POST(self):
        path = urlsplit(self.path).path
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if path == "/v1/messages":
            self.model_request(json.loads(body))
        elif path == "/mcp-upstream":
            self.mcp_request(json.loads(body))
        else:
            self.send_error(404)

    def model_request(self, body: dict):
        Fixtures.model_calls.append(body)
        index = len(Fixtures.model_calls)
        names = [tool.get("name", "") for tool in body.get("tools", [])]
        if index == 1:
            matches = [name for name in names if name.startswith("mcp__gateway__")]
            if len(matches) != 1:
                Fixtures.errors.append(f"expected one gateway tool, got {names}")
                self.send_error(400)
                return
            Fixtures.tool_name = matches[0]
            argument = json.dumps({"text": "continue through MCP"}, separators=(",", ":"))
            events = [
                ("message_start", {"type": "message_start", "message": {"id": "msg_mcp", "type": "message", "role": "assistant", "model": "synthetic", "content": [], "stop_reason": None, "stop_sequence": None, "usage": {"input_tokens": 8, "output_tokens": 0}}}),
                ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "tool_use", "id": "toolu_mcp", "name": Fixtures.tool_name, "input": {}}}),
                ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "input_json_delta", "partial_json": argument}}),
                ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "tool_use", "stop_sequence": None}, "usage": {"output_tokens": 5}}),
                ("message_stop", {"type": "message_stop"}),
            ]
        elif index == 2:
            if not any(block.get("type") == "tool_result" and block.get("tool_use_id") == "toolu_mcp" and "mcp-result-42" in json.dumps(block.get("content")) for message in body.get("messages", []) for block in (message.get("content") if isinstance(message.get("content"), list) else [])):
                Fixtures.errors.append("MCP tool result missing from model continuation")
                self.send_error(400)
                return
            if Fixtures.tool_name not in names:
                Fixtures.errors.append("MCP tool missing from continuation tool list")
                self.send_error(400)
                return
            events = [
                ("message_start", {"type": "message_start", "message": {"id": "msg_done", "type": "message", "role": "assistant", "model": "synthetic", "content": [], "stop_reason": None, "stop_sequence": None, "usage": {"input_tokens": 12, "output_tokens": 0}}}),
                ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}),
                ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Completed with mcp-result-42."}}),
                ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 8}}),
                ("message_stop", {"type": "message_stop"}),
            ]
        else:
            Fixtures.errors.append(f"unexpected model request {index}")
            self.send_error(400)
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for event_name, event in events:
            self.wfile.write((f"event: {event_name}\ndata: {json.dumps(event)}\n\n").encode())
            self.wfile.flush()

    def mcp_request(self, body: dict):
        Fixtures.mcp_methods.append(body.get("method", ""))
        if self.headers.get("Authorization") != "Bearer " + UPSTREAM_CREDENTIAL:
            Fixtures.errors.append("router did not use the server-side MCP credential")
            self.send_error(401)
            return
        method = body.get("method")
        if method == "initialize":
            self.send_response(200)
            self.send_header("Mcp-Session-Id", "mcp-gateway-smoke-session")
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"jsonrpc": "2.0", "id": body.get("id"), "result": {"protocolVersion": "2025-11-25", "capabilities": {"tools": {}}, "serverInfo": {"name": "fixture", "version": "1"}}}).encode())
        elif method == "notifications/initialized":
            self.send_response(202)
            self.end_headers()
        elif method == "tools/list":
            if self.headers.get("Mcp-Session-Id") != "mcp-gateway-smoke-session":
                Fixtures.errors.append("router did not preserve upstream MCP session")
                self.send_error(400)
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"jsonrpc": "2.0", "id": body.get("id"), "result": {"tools": [{"name": "echo", "description": "Return the provided text", "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}}]}}).encode())
        elif method == "tools/call":
            if self.headers.get("Mcp-Session-Id") != "mcp-gateway-smoke-session":
                Fixtures.errors.append("router did not preserve upstream MCP session")
                self.send_error(400)
                return
            text = body.get("params", {}).get("arguments", {}).get("text", "")
            if text != "continue through MCP":
                Fixtures.errors.append(f"tool arguments mismatch: {text}")
                self.send_error(400)
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"jsonrpc": "2.0", "id": body.get("id"), "result": {"content": [{"type": "text", "text": "mcp-result-42"}], "isError": False}}).encode())
        else:
            Fixtures.errors.append(f"unexpected MCP method {method}")
            self.send_error(400)


def main():
    if not CLAUDE.is_file():
        raise SystemExit("install pinned clients with npm ci --ignore-scripts --prefix tests/agent_compat")
    Fixtures.model_calls.clear()
    Fixtures.mcp_methods.clear()
    Fixtures.errors.clear()
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Fixtures)
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    router = None
    try:
        with tempfile.TemporaryDirectory(prefix="router-claude-mcp-") as temp:
            work = Path(temp)
            env = {"PATH": os.environ["PATH"], "HOME": os.environ["HOME"], "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "ROUTER_MCP_UPSTREAM_CREDENTIAL": UPSTREAM_CREDENTIAL}
            # setup-python on CI may need this to load its shared libpython.
            if "LD_LIBRARY_PATH" in os.environ:
                env["LD_LIBRARY_PATH"] = os.environ["LD_LIBRARY_PATH"]
            binary = work / "router"
            subprocess.run(["go", "build", "-tags", "dev_no_license", "-o", str(binary), "./cmd/metrum-ai-router"], cwd=ROOT, env=env, check=True)
            listen = "127.0.0.1:" + str(free_port())
            router_url = "http://" + listen
            config = f'''server:
  listen: "{listen}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {{enabled: false}}
  usage_db: {{enabled: true, driver: sqlite, path: "{work / 'usage.sqlite'}", migration_policy: auto-safe}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  mock: {{base_url: "http://127.0.0.1:{upstream.server_port}", dialect: anthropic, api_key: "{TOKEN}"}}
models:
  chat: {{strategy: static, targets: [{{provider: mock, model: synthetic, tool_support: {{anthropic_messages: [client_tools, tool_choice]}}, reasoning: {{supported: true, mode: opt_in, control: token_budget}}}}]}}
callers:
  - id: agent
    token_sha256: "{DIGEST}"
    allow: [chat]
mcp:
  enabled: true
  servers: [{{name: local, url: "http://127.0.0.1:{upstream.server_port}/mcp-upstream", allow_hosts: [127.0.0.1], credential_env: ROUTER_MCP_UPSTREAM_CREDENTIAL}}]
  policies: [{{caller: agent, tools: [local/echo], deny_by_default: true}}]
'''
            config_path = work / "config.yaml"
            config_path.write_text(config, encoding="utf-8")
            with (work / "router.stderr").open("w", encoding="utf-8") as stderr:
                router = subprocess.Popen([str(binary), "-config", str(config_path)], cwd=work, env=env, stdout=stderr, stderr=stderr)
                deadline = time.monotonic() + 60
                while time.monotonic() < deadline:
                    if router.poll() is not None:
                        raise RuntimeError("router exited before readiness")
                    try:
                        if urlopen(router_url + "/readyz", timeout=1).status == 200:
                            break
                    except OSError:
                        time.sleep(0.05)
                else:
                    raise RuntimeError("router did not become ready")

                mcp_config = work / "mcp.json"
                mcp_config.write_text(json.dumps({"mcpServers": {"gateway": {"type": "http", "url": router_url + "/mcp", "headers": {"Authorization": "Bearer " + TOKEN}}}}), encoding="utf-8")
                child_env = {"PATH": env["PATH"], "HOME": str(work), "ANTHROPIC_BASE_URL": router_url, "ANTHROPIC_API_KEY": TOKEN, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT": "1", "MAX_THINKING_TOKENS": "0", "DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1"}
                command = ["node", str(CLAUDE), "--bare", "--print", "--model", "chat", "--mcp-config", str(mcp_config), "--strict-mcp-config", "--permission-mode", "bypassPermissions", "--tools", "", "--output-format", "text", "Call the MCP echo tool with text 'continue through MCP', then use its result to finish."]
                process = subprocess.Popen(command, cwd=work, env=child_env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
                try:
                    stdout, stderr = process.communicate(timeout=60)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGTERM)
                    try:
                        stdout, stderr = process.communicate(timeout=5)
                    except subprocess.TimeoutExpired:
                        os.killpg(process.pid, signal.SIGKILL)
                        stdout, stderr = process.communicate(timeout=5)
                    raise RuntimeError(f"Claude Code timed out, model calls={len(Fixtures.model_calls)}, MCP methods={Fixtures.mcp_methods}: {stderr[-1200:]}\n{stdout[-1200:]}")
                if process.returncode != 0 or "mcp-result-42" not in stdout or Fixtures.errors or len(Fixtures.model_calls) != 2 or "tools/call" not in Fixtures.mcp_methods:
                    raise RuntimeError(f"Claude Code MCP smoke failed ({process.returncode}), model calls={len(Fixtures.model_calls)}, MCP methods={Fixtures.mcp_methods}, errors={Fixtures.errors}: {stderr[-1200:]}\n{stdout[-1200:]}")
                sdk_python = ROOT / "tests/agent_compat/.venv/bin/python"
                if not sdk_python.is_file():
                    raise RuntimeError("install pinned clients with uv sync --locked --directory tests/agent_compat")
                sdk_env = {"PATH": env["PATH"], "HOME": env["HOME"]}
                if "LD_LIBRARY_PATH" in env:
                    sdk_env["LD_LIBRARY_PATH"] = env["LD_LIBRARY_PATH"]
                subprocess.run([
                    str(sdk_python), "tests/agent_compat/mcp_sdk_smoke.py", router_url + "/mcp", TOKEN,
                ], cwd=ROOT, env=sdk_env, check=True, timeout=60)
                print("PASS: Claude Code continued through MCP; Python MCP SDK listed and called the tool")
    finally:
        if router is not None and router.poll() is None:
            router.terminate()
            try:
                router.wait(timeout=5)
            except subprocess.TimeoutExpired:
                router.kill()
                router.wait(timeout=5)
        upstream.shutdown()
        upstream.server_close()
        thread.join(timeout=1)


if __name__ == "__main__":
    main()
