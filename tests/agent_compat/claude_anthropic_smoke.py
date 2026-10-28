#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Run pinned Claude Code's Read tool through local Anthropic Messages SSE."""
from __future__ import annotations

import json
import os
import signal
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.request import urlopen

from run_chat_smoke import DIGEST, ROOT, TOKEN, free_port

# Official wrapper supports npm --ignore-scripts without a postinstall.
CLAUDE = ROOT / "tests/agent_compat/node_modules/@anthropic-ai/claude-code/cli-wrapper.cjs"


class Mock(BaseHTTPRequestHandler):
    calls: list[dict] = []
    errors: list[str] = []
    result_path = ""

    def log_message(self, *_args):
        pass

    def do_POST(self):
        if self.path != "/v1/messages":
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
            names = [t.get("name") for t in body.get("tools", [])]
            if "Read" not in names:
                Mock.errors.append(f"Claude Code did not declare Read; names={names}")
                self.send_error(400)
                return
            argument = json.dumps({"file_path": Mock.result_path})
            events = [
                ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "tool_use", "id": "toolu_read", "name": "Read", "input": {}}}),
                ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "input_json_delta", "partial_json": argument[:len(argument)//2]}}),
                ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "input_json_delta", "partial_json": argument[len(argument)//2:]}}),
                ("content_block_stop", {"type": "content_block_stop", "index": 0}),
            ]
            stop = "tool_use"
        else:
            blocks = [b for m in body.get("messages", []) if isinstance(m.get("content"), list) for b in m["content"] if isinstance(b, dict)]
            if not any(b.get("type") == "tool_result" and b.get("tool_use_id") == "toolu_read" and "tool-result-42" in json.dumps(b.get("content")) for b in blocks):
                Mock.errors.append("tool_result continuation not forwarded")
                self.send_error(400)
                return
            events = [
                ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}),
                ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Completed."}}),
                ("content_block_stop", {"type": "content_block_stop", "index": 0}),
            ]
            stop = "end_turn"
        events.insert(0, ("message_start", {"type": "message_start", "message": {"id": "msg_mock", "type": "message", "role": "assistant", "model": "synthetic-messages", "content": [], "stop_reason": None, "stop_sequence": None, "usage": {"input_tokens": 10, "output_tokens": 0}}}))
        events.extend([("message_delta", {"type": "message_delta", "delta": {"stop_reason": stop, "stop_sequence": None}, "usage": {"output_tokens": 8}}), ("message_stop", {"type": "message_stop"})])
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for name, event in events:
            self.wfile.write(("event: " + name + "\ndata: " + json.dumps(event) + "\n\n").encode())
            self.wfile.flush()


def main():
    # CI cancellation must unwind the router and the separately grouped CLI.
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupted)
    if not CLAUDE.is_file():
        raise SystemExit("install pinned clients with npm ci --ignore-scripts --prefix tests/agent_compat")
    Mock.calls.clear()
    Mock.errors.clear()
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Mock)
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="router-claude-compat-") as tmp:
            work = Path(tmp)
            Mock.result_path = str(work / "result.txt")
            (work / "result.txt").write_text("tool-result-42\n", encoding="utf-8")
            env = {"PATH": os.environ["PATH"], "HOME": os.environ["HOME"], "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
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
  mock: {{base_url: "http://127.0.0.1:{upstream.server_port}", dialect: anthropic}}
models:
  chat: {{strategy: static, targets: [{{provider: mock, model: synthetic-messages, tool_support: {{anthropic_messages: [client_tools]}}, reasoning: {{supported: true, mode: opt_in, control: token_budget}}, allowed_betas: [context-management-2025-06-27], request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}}}]}}
callers:
  - id: agent-compat
    token_sha256: "{DIGEST}"
    allow: [chat]
    rate: {{rpm: 2400, tpm: 100000000, concurrent: 160}}
    quota: {{day: {{requests: 100000, tokens: 400000000}}, month: {{tokens: 8000000000}}}}
    key: {{lifetime_tokens: 40000000000}}
'''
            (work / "config.yaml").write_text(config, encoding="utf-8")
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
                    child_env = {"PATH": env["PATH"], "HOME": str(work), "ANTHROPIC_BASE_URL": f"http://127.0.0.1:{port}", "ANTHROPIC_API_KEY": TOKEN, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1"}
                    command = ["node", str(CLAUDE), "--bare", "--print", "--model", "chat", "--betas", "context-management-2025-06-27", "--tools", "Read", "--allowedTools", "Read", "--permission-mode", "acceptEdits", "--setting-sources", "", "--no-session-persistence", "--disable-slash-commands", "--output-format", "stream-json", "--verbose", "Read the requested file and finish."]
                    def router_errors():
                        path = work / "router.jsonl"
                        if not path.exists():
                            return []
                        records = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
                        return [{k: row.get(k) for k in ("status", "error_class", "error_message")} for row in records[-3:]]

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
                        raise RuntimeError(f"Claude Code timed out, mock calls={len(Mock.calls)}, errors={Mock.errors}, router={router_errors()}: {stderr[-1500:]}\n{stdout[-1500:]}")
                    finally:
                        if process.poll() is None:
                            os.killpg(process.pid, signal.SIGKILL)
                            process.wait(timeout=5)
                    if process.returncode:
                        raise RuntimeError(f"Claude Code failed ({process.returncode}), mock calls={len(Mock.calls)}, errors={Mock.errors}: {stderr[-1500:]}\n{stdout[-1500:]}")
                    if (work / "result.txt").read_text(encoding="utf-8") != "tool-result-42\n":
                        raise RuntimeError("input file content changed")
                    if Mock.errors or len(Mock.calls) != 2:
                        raise RuntimeError(f"mock errors={Mock.errors}, calls={len(Mock.calls)}")
                    print("PASS: claude-code / Anthropic Messages / same dialect / depth 1: streamed tool use and tool-result continuation")
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
