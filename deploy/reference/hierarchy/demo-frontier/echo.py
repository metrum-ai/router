#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Loopback OpenAI-Chat frontier fixture for the #434 demo stack.

It returns fixed synthetic completions, streams when asked, and records the
last request's headers and a call count so `verify.sh` can prove what actually
crossed the hop boundary. It is a demo backend, not a model, and it is not part
of the reference deployment's data path in production.
"""

from __future__ import annotations

import argparse
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOCK = threading.Lock()
STATE = {"count": 0, "last_headers": {}, "last_body": "", "streams": 0, "unary": 0}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):  # keep the demo quiet
        return

    def _write_json(self, status: int, payload: dict) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):  # noqa: N802 - BaseHTTPRequestHandler API
        if self.path == "/__echo":
            with LOCK:
                self._write_json(200, dict(STATE))
            return
        self._write_json(404, {"error": "not-found"})

    def do_POST(self):  # noqa: N802 - BaseHTTPRequestHandler API
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        if self.path == "/__reset":
            with LOCK:
                STATE.update(count=0, last_headers={}, last_body="", streams=0, unary=0)
            self._write_json(200, {"ok": True})
            return
        if self.path.rstrip("/") not in ("/v1/chat/completions", "/chat/completions"):
            self._write_json(404, {"error": "not-found"})
            return
        try:
            body = json.loads(raw or b"{}")
        except json.JSONDecodeError:
            self._write_json(400, {"error": "invalid-json"})
            return
        entries = {}
        for key in self.headers.keys():
            entries[key.lower()] = ", ".join(self.headers.get_all(key) or [])
        stream = bool(body.get("stream"))
        with LOCK:
            STATE["count"] += 1
            STATE["last_headers"] = entries
            STATE["last_body"] = raw.decode("utf-8", "replace")[:2000]
            STATE["streams" if stream else "unary"] += 1
        if stream:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.end_headers()
            for index in range(3):
                event = {
                    "id": "chatcmpl_frontier_demo",
                    "object": "chat.completion.chunk",
                    "model": body.get("model", "frontier-demo"),
                    "choices": [
                        {
                            "index": 0,
                            "delta": {"content": f"tok{index} "} if index else {"role": "assistant", "content": "tok0 "},
                            "finish_reason": None,
                        }
                    ],
                }
                self.wfile.write(f"data: {json.dumps(event)}\n\n".encode())
                self.wfile.flush()
                time.sleep(0.01)
            final = {
                "id": "chatcmpl_frontier_demo",
                "object": "chat.completion.chunk",
                "model": body.get("model", "frontier-demo"),
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 16, "completion_tokens": 4, "total_tokens": 20},
            }
            self.wfile.write(f"data: {json.dumps(final)}\n\ndata: [DONE]\n\n".encode())
            self.close_connection = True
            return
        self._write_json(
            200,
            {
                "id": "chatcmpl_frontier_demo",
                "object": "chat.completion",
                "model": body.get("model", "frontier-demo"),
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": "frontier demo reply"},
                        "finish_reason": "stop",
                    }
                ],
                "usage": {"prompt_tokens": 16, "completion_tokens": 4, "total_tokens": 20},
            },
        )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=9100)
    args = parser.parse_args()
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    server.daemon_threads = True
    print(f"frontier demo fixture on {args.host}:{args.port}", flush=True)
    server.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
