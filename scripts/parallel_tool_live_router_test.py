#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline real-SDK -> router -> scripted-provider smoke for live tool cells."""

from __future__ import annotations

import json
import os
import subprocess
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import yaml

from parallel_live_budget import Budget
from parallel_tool_live import Gate, TOKEN, router_config

ROOT = Path(__file__).resolve().parents[1]
MARKER = "LIVE-EVAL-MARKER-A"


class ScriptedProvider(BaseHTTPRequestHandler):
    mode = "success"
    calls: dict[str, int] = {}

    def log_message(self, _format, *_args):
        return

    def do_POST(self):
        path = self.path.split("?", 1)[0]
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        key = "messages" if path.endswith("/messages") else "responses" if path.endswith("/responses") else "chat"
        type(self).calls[key] = type(self).calls.get(key, 0) + 1
        call = type(self).calls[key]
        if type(self).mode == "timeout":
            time.sleep(0.5)
        if type(self).mode == "no-tool" and call == 1:
            if key == "messages":
                data = {"id": "msg_test", "type": "message", "role": "assistant", "model": "scripted", "content": [{"type": "text", "text": "No tool call"}], "stop_reason": "end_turn", "stop_sequence": None, "usage": {"input_tokens": 10, "output_tokens": 5}}
            elif key == "responses":
                data = {"id": "resp_test", "object": "response", "created_at": 1, "status": "completed", "model": "scripted", "output": [{"id": "msg_test", "type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "No tool call", "annotations": []}]}], "output_text": "No tool call", "usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}
            else:
                data = {"id": "chat_test", "object": "chat.completion", "created": 1, "model": "scripted", "choices": [{"index": 0, "message": {"role": "assistant", "content": "No tool call"}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}
        elif key == "messages":
            if call == 1:
                content, stop = [{"type": "tool_use", "id": "call_1", "name": "read_file", "input": {"path": "notes/a.txt"}}], "tool_use"
            else:
                content, stop = [{"type": "text", "text": MARKER}], "end_turn"
            data = {"id": f"msg_{call}", "type": "message", "role": "assistant", "model": "scripted", "content": content, "stop_reason": stop, "stop_sequence": None, "usage": {"input_tokens": 10, "output_tokens": 5}}
        elif key == "responses":
            if call == 1:
                output = [{"id": "fc_1", "type": "function_call", "status": "completed", "call_id": "call_1", "name": "read_file", "arguments": '{"path":"notes/a.txt"}'}]
                text = ""
            else:
                output = [{"id": "msg_2", "type": "message", "role": "assistant", "content": [{"type": "output_text", "text": MARKER, "annotations": []}]}]
                text = MARKER
            data = {"id": f"resp_{call}", "object": "response", "created_at": 1, "status": "completed", "model": "scripted", "output": output, "output_text": text, "usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}
        else:
            if call == 1:
                message = {"role": "assistant", "content": None, "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": '{"path":"notes/a.txt"}'}}]}
                finish = "tool_calls"
            else:
                message, finish = {"role": "assistant", "content": MARKER}, "stop"
            data = {"id": f"chat_{call}", "object": "chat.completion", "created": 1, "model": "scripted", "choices": [{"index": 0, "message": message, "finish_reason": finish}], "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}
        encoded = json.dumps(data).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        try:
            self.wfile.write(encoded)
        except (BrokenPipeError, ConnectionResetError):
            pass


class RealClientRouterSmokeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="sdk-router-smoke-")
        cls.root = Path(cls.temp.name)
        cls.binary = cls.root / "router-bin"
        subprocess.run(["go", "build", "-o", str(cls.binary), "./cmd/metrum-ai-router"], cwd=ROOT, check=True)
        cls.upstream = ThreadingHTTPServer(("127.0.0.1", 0), ScriptedProvider)
        cls.upstream_thread = threading.Thread(target=cls.upstream.serve_forever, daemon=True)
        cls.upstream_thread.start()
        ScriptedProvider.mode = "success"
        ScriptedProvider.calls = {}
        cls.router_dir = cls.root / "router"
        cls.router_dir.mkdir(exist_ok=True)
        config, cls.router_port = router_config(cls.router_dir, "dummy-openai-key", "dummy-anthropic-key")
        settings = yaml.safe_load(config.read_text(encoding="utf-8"))
        base = f"http://127.0.0.1:{cls.upstream.server_port}"
        settings["providers"]["openai"]["base_url"] = base + "/v1"
        settings["providers"]["openai-responses"]["base_url"] = base + "/v1"
        settings["providers"]["anthropic"]["base_url"] = base
        config.write_text(yaml.safe_dump(settings, sort_keys=False), encoding="utf-8")
        env = {"PATH": os.environ.get("PATH", ""), "OPENAI_API_KEY": "dummy-openai-key", "ANTHROPIC_API_KEY": "dummy-anthropic-key"}
        cls.router = subprocess.Popen([str(cls.binary), "-config", str(config)], cwd=cls.root, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            if cls.router.poll() is not None:
                raise RuntimeError("local router exited before readiness")
            try:
                import urllib.request
                with urllib.request.urlopen(f"http://127.0.0.1:{cls.router_port}/readyz", timeout=0.5) as response:
                    if response.status == 200:
                        break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError("local router did not become ready")

    @classmethod
    def tearDownClass(cls):
        cls.router.terminate()
        try:
            cls.router.wait(timeout=10)
        except subprocess.TimeoutExpired:
            cls.router.kill()
        cls.upstream.shutdown()
        cls.upstream.server_close()
        cls.temp.cleanup()

    def setUp(self):
        ScriptedProvider.mode = "success"
        ScriptedProvider.calls = {}

    def run_cell(self, name, surface, group, timeout=5):
        gate = Gate(Budget(), Path("unused"))
        gate.client_cell(name, surface, f"http://127.0.0.1:{self.router_port}", group=group, timeout=timeout)
        return gate.cells[0]

    def test_all_real_sdk_tool_loops_traverse_router(self):
        for name, surface, group in (
            ("openai-chat-sdk", "openai-chat", "openai-chat-sdk"),
            ("openai-responses-sdk", "openai-responses", "openai-responses-sdk"),
            ("anthropic-messages-sdk", "anthropic-messages", "anthropic-messages-sdk"),
        ):
            with self.subTest(cell=name):
                result = self.run_cell(name, surface, group)
                self.assertEqual(result["problems"], [], f"upstream calls={ScriptedProvider.calls}")
                self.assertEqual(ScriptedProvider.calls.get("messages" if surface == "anthropic-messages" else "responses" if surface == "openai-responses" else "chat"), 2)

    def test_missing_tool_call_is_a_cell_failure(self):
        ScriptedProvider.mode = "no-tool"
        result = self.run_cell("openai-chat-sdk", "openai-chat", "openai-chat-sdk")
        self.assertIn("tool-call-mismatch", result["problems"])

    def test_upstream_timeout_is_a_sanitized_cell_failure(self):
        ScriptedProvider.mode = "timeout"
        result = self.run_cell("openai-chat-sdk", "openai-chat", "openai-chat-sdk", timeout=0.15)
        self.assertIn("request-failed", result["problems"])
        self.assertNotIn("dummy", json.dumps(result))


if __name__ == "__main__":
    unittest.main()
