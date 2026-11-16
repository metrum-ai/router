#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""External request-shape policy service. Never logs request content."""

from __future__ import annotations

import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


def route(payload: dict[str, Any]) -> dict[str, Any]:
    context = payload.get("context") or {}
    targets = payload.get("targets") or []
    eligible = [
        (index, target)
        for index, target in enumerate(targets)
        if target.get("keyConfigured") and int(target.get("weight") or 0) > 0
    ]
    if not eligible:
        return {"targetIndex": 0, "classLabel": "request-shape:no-eligible-targets"}

    if int(context.get("imageCount") or 0) > 0:
        shape, tiers = "image", ("vision", "multimodal", "heavy")
    elif (context.get("reasoning") or {}).get("requested"):
        shape, tiers = "reasoning", ("reasoning", "heavy")
    elif context.get("hasTools") or int(context.get("toolCount") or 0) > 0:
        shape, tiers = "tools", ("tool", "heavy", "coding")
    elif context.get("hasStructuredOutput"):
        shape, tiers = "structured", ("structured", "heavy", "cheap")
    elif int(context.get("textChars") or 0) > 8000:
        shape, tiers = "long-context", ("heavy", "long_context")
    else:
        shape, tiers = "chat", ("cheap", "compact", "normal")

    selected = eligible[0][0]
    for tier in tiers:
        match = next((index for index, target in eligible if str(target.get("tier") or "").lower() == tier), None)
        if match is not None:
            selected = match
            break
    return {
        "targetIndex": selected,
        "fallbackIndexes": [index for index, _ in eligible if index != selected],
        "classLabel": f"request-shape:{shape}",
    }


class PolicyHandler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        if self.path == "/healthz":
            self._write_json(200, {"ok": True})
        else:
            self._write_json(404, {"error": "not found"})

    def do_POST(self) -> None:
        if self.path != "/route":
            self._write_json(404, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("content-length", "0"))
            if length < 0 or length > 1 << 20:
                self._write_json(413, {"error": "request too large"})
                return
            payload = json.loads(self.rfile.read(length) or b"{}")
            self._write_json(200, route(payload))
        except (ValueError, TypeError, json.JSONDecodeError):
            self._write_json(400, {"error": "invalid request"})

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def _write_json(self, status: int, body: dict[str, Any]) -> None:
        raw = json.dumps(body).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=18093)
    args = parser.parse_args()
    ThreadingHTTPServer((args.host, args.port), PolicyHandler).serve_forever()


if __name__ == "__main__":
    main()
