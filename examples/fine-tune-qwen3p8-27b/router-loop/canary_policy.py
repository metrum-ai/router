# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Standalone key-and-project-bound canary policy for the local demo."""

from __future__ import annotations

import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

MAX_BODY_BYTES = 1 << 20


def route(payload: dict[str, Any]) -> dict[str, Any]:
    if not isinstance(payload, dict):
        raise ValueError("request must be a JSON object")
    caller = payload.get("caller") or {}
    canary = isinstance(caller, dict) and caller.get("project") == "acme-support" and caller.get("tokenId") == "rtr_acme-support"
    provider = "ft-canary" if canary else "teacher"
    for index, target in enumerate(payload.get("targets") or []):
        if isinstance(target, dict) and target.get("provider") == provider and int(target.get("weight") or 0) > 0:
            return {"targetIndex": index, "fallbackIndexes": [], "classLabel": "support-key-canary" if canary else "teacher"}
    raise ValueError("required bound target unavailable")


class PolicyHandler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        self._write_json(200, {"ok": True}) if self.path == "/healthz" else self._write_json(404, {"error": "not found"})

    def do_POST(self) -> None:
        if self.path != "/route":
            self._write_json(404, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("content-length", "0"))
            if length < 0 or length > MAX_BODY_BYTES:
                self._write_json(413, {"error": "request too large"})
                return
            payload = json.loads(self.rfile.read(length) or b"{}")
            if not isinstance(payload, dict):
                self._write_json(400, {"error": "invalid request"})
                return
            self._write_json(200, route(payload))
        except (ValueError, TypeError, json.JSONDecodeError):
            self._write_json(422, {"error": "required bound target unavailable"})

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def _write_json(self, status: int, body: dict[str, Any]) -> None:
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=18412)
    args = parser.parse_args()
    ThreadingHTTPServer((args.host, args.port), PolicyHandler).serve_forever()


if __name__ == "__main__":
    main()
