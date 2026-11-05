#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""CPU-only OpenAI-compatible mock model server for the edge bundle.

This server answers like a vLLM `/v1` endpoint without weights, a GPU or a model.
It exists so the bundle's install, router configuration, model groups, escalation
group, quotas and spend report can be exercised end to end on a machine that has
no accelerator (CI included). It does not produce model output and must never be
described as a model or as quality evidence.

The server is a test double, not a load target: it answers from a fixed script and
its connection handling is stdlib-simple, so a run that opens dozens of concurrent
keep-alive connections can see resets from it before any router in front does. Use
it to exercise procedure (groups, quotas, reports, auth), never to measure a box.

Environment:
  MOCK_PORT         listen port (default 18081)
  MOCK_SERVED_NAME  served model name (default local-tiny)
  MOCK_API_KEY      when set, require "Authorization: Bearer <key>"
  MOCK_LABEL        name returned in the reply text (default mock)
  MOCK_RECORD_BODY  path to append one JSON line per /v1 request: {"label", "body"}
  MOCK_REJECT_MAX_TOKENS_LABELS
                    comma-separated labels that answer a body carrying "max_tokens"
                    with the 400 current OpenAI models return (see the test bundle)

Usage: mock-upstream.py [--port N] [--served-name NAME] [--api-key-env VAR]
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class State:
    def __init__(self, served_name: str, api_key: str | None, label: str) -> None:
        self.served_name = served_name
        self.api_key = api_key
        self.label = label
        self.record_path = os.environ.get("MOCK_RECORD_BODY", "")
        rejecting = {
            item.strip()
            for item in os.environ.get("MOCK_REJECT_MAX_TOKENS_LABELS", "").split(",")
            if item.strip()
        }
        self.rejects_max_tokens = label in rejecting
        self.requests = 0
        self.lock = threading.Lock()

    def count(self) -> int:
        with self.lock:
            self.requests += 1
            return self.requests

    def record(self, body: object) -> None:
        if not self.record_path:
            return
        with self.lock:
            with open(self.record_path, "a", encoding="utf-8") as handle:
                handle.write(json.dumps({"label": self.label, "body": body}) + "\n")


def handler_factory(state: State):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt: str, *args: object) -> None:  # quieter CI logs
            sys.stderr.write("mock-upstream: " + fmt % args + "\n")

        def authorized(self) -> bool:
            if not state.api_key:
                return True
            header = self.headers.get("Authorization", "")
            return header == f"Bearer {state.api_key}"

        def send_json(self, status: int, payload: dict) -> None:
            body = json.dumps(payload).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def error(self, status: int, kind: str, message: str) -> None:
            self.send_json(status, {"error": {"type": kind, "message": message}})

        def do_GET(self) -> None:  # noqa: N802 - stdlib naming
            if self.path in ("/health", "/healthz"):
                self.send_json(200, {"status": "ok", "mock": True})
                return
            if self.path == "/v1/models":
                if not self.authorized():
                    self.error(401, "invalid_api_key", "mock api key required")
                    return
                self.send_json(200, {"object": "list", "data": [{"id": state.served_name, "object": "model"}]})
                return
            self.error(404, "not_found", f"unknown path {self.path}")

        def do_POST(self) -> None:  # noqa: N802 - stdlib naming
            if self.path not in ("/v1/chat/completions", "/v1/completions"):
                self.error(404, "not_found", f"unknown path {self.path}")
                return
            if not self.authorized():
                self.error(401, "invalid_api_key", "mock api key required")
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
                body = json.loads(self.rfile.read(length) or b"{}")
            except (ValueError, TypeError):
                self.error(400, "invalid_request", "unparseable body")
                return
            if state.rejects_max_tokens and "max_tokens" in body:
                # What current OpenAI models answer. The edge bundle's own escalate
                # provider must never reach this branch; the test asserts that a
                # rendered deployment sends max_completion_tokens instead.
                state.record(body)
                self.error(
                    400,
                    "invalid_request_error",
                    "Unsupported parameter: 'max_tokens' is not supported with this model. "
                    "Use 'max_completion_tokens' instead.",
                )
                return
            state.record(body)
            index = state.count()
            prompt_tokens = max(1, len(str(body.get("messages") or body.get("prompt") or "")) // 4)
            max_tokens = int(body.get("max_tokens") or body.get("max_completion_tokens") or 16)
            completion_tokens = max(1, min(max_tokens, 32))
            text = f"{state.label} reply #{index}"
            completion_id = f"chatcmpl-mock-{index}"
            created = int(time.time())
            usage = {
                "prompt_tokens": prompt_tokens,
                "completion_tokens": completion_tokens,
                "total_tokens": prompt_tokens + completion_tokens,
            }
            if body.get("stream"):
                self.stream(completion_id, created, text, usage)
                return
            self.send_json(
                200,
                {
                    "id": completion_id,
                    "object": "chat.completion",
                    "created": created,
                    "model": state.served_name,
                    "choices": [
                        {
                            "index": 0,
                            "message": {"role": "assistant", "content": text},
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": usage,
                },
            )

        def stream(self, completion_id: str, created: int, text: str, usage: dict) -> None:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.end_headers()

            def chunk(delta: dict, finish: str | None, extra: dict | None) -> None:
                payload = {
                    "id": completion_id,
                    "object": "chat.completion.chunk",
                    "created": created,
                    "model": state.served_name,
                    "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
                }
                if extra:
                    payload.update(extra)
                self.wfile.write(f"data: {json.dumps(payload)}\n\n".encode("utf-8"))
                self.wfile.flush()

            chunk({"role": "assistant", "content": ""}, None, None)
            for word in text.split(" "):
                chunk({"content": word + " "}, None, None)
            chunk({}, "stop", {"usage": usage})
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
            self.close_connection = True

    return Handler


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--port", type=int, default=int(os.environ.get("MOCK_PORT", "18081")))
    parser.add_argument("--served-name", default=os.environ.get("MOCK_SERVED_NAME", "local-tiny"))
    parser.add_argument("--api-key-env", default="LOCAL_VLLM_API_KEY")
    parser.add_argument("--label", default=os.environ.get("MOCK_LABEL", "mock"))
    args = parser.parse_args(argv)

    api_key = os.environ.get(args.api_key_env) if args.api_key_env else None
    state = State(args.served_name, api_key or None, args.label)
    server = ThreadingHTTPServer(("127.0.0.1", args.port), handler_factory(state))
    server.daemon_threads = True
    # Small bursts arrive faster than the stdlib default backlog of 5 can absorb.
    server.request_queue_size = 128
    sys.stderr.write(
        f"mock-upstream: {args.served_name} on 127.0.0.1:{args.port} "
        f"(auth {'on' if state.api_key else 'off'}, not a model)\n"
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
