#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Synthetic OpenAI Chat upstream for the Claude Code cross-dialect smoke."""
from __future__ import annotations

import json
from http.server import BaseHTTPRequestHandler
from urllib.parse import urlsplit


class ChatMock(BaseHTTPRequestHandler):
    calls: list[dict] = []
    errors: list[str] = []
    result_path = ""

    def log_message(self, *_args):
        pass

    def do_POST(self):
        if urlsplit(self.path).path != "/v1/chat/completions":
            self.send_error(404)
            return
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        ChatMock.calls.append(body)
        index = len(ChatMock.calls)
        if not body.get("stream") or index > 2:
            ChatMock.errors.append("unexpected stream flag or extra request")
            self.send_error(400)
            return
        if index == 1:
            names = [t.get("function", {}).get("name") for t in body.get("tools", [])]
            if "Read" not in names:
                ChatMock.errors.append("Read tool was not translated")
                self.send_error(400)
                return
            arguments = json.dumps({"file_path": ChatMock.result_path}, separators=(",", ":"))
            cut = len(arguments) // 2
            deltas = [
                {"role": "assistant", "tool_calls": [{"index": 0, "id": "call_read", "type": "function", "function": {"name": "Read", "arguments": arguments[:cut]}}]},
                {"tool_calls": [{"index": 0, "function": {"arguments": arguments[cut:]}}]},
            ]
            finish = "tool_calls"
        else:
            results = [m for m in body.get("messages", []) if m.get("role") == "tool" and m.get("tool_call_id") == "call_read"]
            if len(results) != 1 or "tool-result-42" not in json.dumps(results[0].get("content")):
                ChatMock.errors.append("translated tool-result continuation missing")
                self.send_error(400)
                return
            deltas = [{"role": "assistant", "content": "Completed."}]
            finish = "stop"
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for delta in deltas + [{}]:
            chunk = {"id": "chatcmpl_mock", "object": "chat.completion.chunk", "created": 1, "model": "synthetic-chat", "choices": [{"index": 0, "delta": delta, "finish_reason": finish if not delta else None}]}
            self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()
