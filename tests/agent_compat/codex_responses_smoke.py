#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Run pinned Codex CLI through a local Responses stream and a workspace sandbox."""
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

CODEX = ROOT / "tests/agent_compat/node_modules/.bin/codex"


class Mock(BaseHTTPRequestHandler):
    calls: list[dict] = []
    errors: list[str] = []
    tool_name = "exec_command"
    tool_arguments = {"cmd": "printf 'tool-result-42\\n' > result.txt"}

    def log_message(self, *_args):
        pass

    def do_POST(self):
        if self.path != "/v1/responses":
            self.send_error(404)
            return
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        Mock.calls.append(body)
        index = len(Mock.calls)
        if not body.get("stream") or index > 2:
            Mock.errors.append("unexpected stream flag or extra request")
            self.send_error(400)
            return
        response = {"id": f"resp_{index}", "object": "response", "created_at": 1, "status": "in_progress", "model": "synthetic-responses", "output": []}
        events = [("response.created", {"type": "response.created", "response": response})]
        if index == 1:
            names = [t.get("name") for t in body.get("tools", [])]
            if Mock.tool_name not in names:
                Mock.errors.append(f"client did not declare {Mock.tool_name}; names={names}")
                self.send_error(400)
                return
            args = json.dumps(Mock.tool_arguments)
            item = {"id": "fc_write", "type": "function_call", "status": "in_progress", "call_id": "call_write", "name": Mock.tool_name, "arguments": ""}
            events.extend([
                ("response.output_item.added", {"type": "response.output_item.added", "output_index": 0, "item": item}),
                ("response.function_call_arguments.delta", {"type": "response.function_call_arguments.delta", "item_id": "fc_write", "output_index": 0, "delta": args}),
                ("response.function_call_arguments.done", {"type": "response.function_call_arguments.done", "item_id": "fc_write", "output_index": 0, "arguments": args}),
            ])
            item = item | {"status": "completed", "arguments": args}
            events.append(("response.output_item.done", {"type": "response.output_item.done", "output_index": 0, "item": item}))
        else:
            inputs = body.get("input", [])
            if not isinstance(inputs, list) or not any(i.get("type") == "function_call_output" and i.get("call_id") == "call_write" for i in inputs if isinstance(i, dict)):
                Mock.errors.append("Codex tool output continuation not forwarded")
                self.send_error(400)
                return
            item = {"id": "msg_2", "type": "message", "status": "completed", "role": "assistant", "content": [{"type": "output_text", "text": "Completed.", "annotations": []}]}
            events.extend([
                ("response.output_item.added", {"type": "response.output_item.added", "output_index": 0, "item": {"id": "msg_2", "type": "message", "status": "in_progress", "role": "assistant", "content": []}}),
                ("response.content_part.added", {"type": "response.content_part.added", "item_id": "msg_2", "output_index": 0, "content_index": 0, "part": {"type": "output_text", "text": "", "annotations": []}}),
                ("response.output_text.delta", {"type": "response.output_text.delta", "item_id": "msg_2", "output_index": 0, "content_index": 0, "delta": "Completed."}),
                ("response.output_item.done", {"type": "response.output_item.done", "output_index": 0, "item": item}),
            ])
        complete = response | {"status": "completed", "output": [item], "usage": {"input_tokens": 10, "output_tokens": 8, "total_tokens": 18}}
        events.append(("response.completed", {"type": "response.completed", "response": complete}))
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for seq, (name, event) in enumerate(events):
            event["sequence_number"] = seq
            self.wfile.write(("event: " + name + "\ndata: " + json.dumps(event) + "\n\n").encode())
            self.wfile.flush()


def main():
    # CI cancellation must unwind the router and the separately grouped CLI.
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupted)
    if not CODEX.is_file():
        raise SystemExit("install pinned clients with npm ci --ignore-scripts --prefix tests/agent_compat")
    Mock.calls.clear()
    Mock.errors.clear()
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), Mock)
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="router-codex-compat-") as tmp:
            work = Path(tmp)
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
  mock: {{base_url: "http://127.0.0.1:{upstream.server_port}/v1", dialect: openai-responses}}
models:
  chat: {{strategy: static, targets: [{{provider: mock, model: synthetic-responses, tool_support: {{openai_responses: [function, tool_choice]}}, reasoning: {{supported: true, mode: opt_in, control: effort_enum}}}}]}}
callers:
  - id: agent-compat
    token_sha256: "{DIGEST}"
    allow: [chat]
    rate: {{rpm: 2400, tpm: 100000000, concurrent: 160}}
    quota: {{day: {{requests: 100000, tokens: 400000000}}, month: {{tokens: 8000000000}}}}
    key: {{lifetime_tokens: 40000000000}}
'''
            (work / "config.yaml").write_text(config, encoding="utf-8")
            codex_home = work / "codex-home"
            codex_home.mkdir()
            (codex_home / "config.toml").write_text(f'''model_provider = "router-compat"
model = "chat"
web_search = "disabled"
[model_providers.router-compat]
name = "Local router"
base_url = "http://127.0.0.1:{port}/v1"
env_key = "ROUTER_CALLER_TOKEN"
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
''', encoding="utf-8")
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
                    child_env = {"PATH": env["PATH"], "HOME": str(work), "CODEX_HOME": str(codex_home), "ROUTER_CALLER_TOKEN": TOKEN}
                    command = [str(CODEX), "exec", "--skip-git-repo-check", "--ephemeral", "--sandbox", "workspace-write", "--json", "Write the requested file and finish."]
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
                        raise RuntimeError(f"Codex timed out, mock errors={Mock.errors}: {stderr[-1500:]}\n{stdout[-1500:]}")
                    finally:
                        if process.poll() is None:
                            os.killpg(process.pid, signal.SIGKILL)
                            process.wait(timeout=5)
                    if process.returncode:
                        raise RuntimeError(f"Codex failed ({process.returncode}), mock errors={Mock.errors}: {stderr[-1500:]}\n{stdout[-1500:]}")
                    result_path = work / "result.txt"
                    if not result_path.exists() or result_path.read_text(encoding="utf-8") != "tool-result-42\n":
                        results = [item for call in Mock.calls for item in call.get("input", []) if isinstance(item, dict) and item.get("type") == "function_call_output"]
                        raise RuntimeError(f"generated file missing or incorrect; tool output={results}; stderr={stderr[-1500:]}; events={stdout[-3000:]}")
                    if Mock.errors or len(Mock.calls) != 2:
                        raise RuntimeError(f"mock errors={Mock.errors}, calls={len(Mock.calls)}")
                    print("PASS: codex / OpenAI Responses / same dialect / depth 1: streamed tool call and tool-result continuation")
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
