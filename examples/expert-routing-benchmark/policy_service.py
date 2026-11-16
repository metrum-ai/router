# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Standalone, bounded-content classifier for the expert-routing benchmark."""

from __future__ import annotations

import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

VOCAB = {
    "biomedical": ("gene", "genes", "protein", "proteins", "biomedical", "genome", "mutation", "patient", "clinical", "diagnosis", "treatment", "disease", "blood", "nerve", "syndrome"),
    "finance": ("finance", "financial", "revenue", "earnings", "investment", "stock", "stocks", "securities", "재무", "증권"),
    "math": ("math", "calculate", "sum", "numbers", "number", "total", "multiply", "divide", "arithmetic"),
    "coding": ("def", "function", "python", "code", "programming", "algorithm", "implement"),
}
MAX_TEXT_CHARS = 8192
MAX_BODY_BYTES = 1 << 20


def classify(text: str) -> str:
    lower = text[:MAX_TEXT_CHARS].lower()
    scores: list[tuple[int, str]] = []
    for domain, words in VOCAB.items():
        score = sum(
            1 for word in words
            if (word in lower if not word.isascii() else _has_ascii_word(lower, word))
        )
        scores.append((score, domain))
    scores.sort(key=lambda item: -item[0])
    if not scores[0][0] or scores[0][0] == scores[1][0]:
        return "unknown"
    return scores[0][1]


def _has_ascii_word(text: str, word: str) -> bool:
    import re

    return re.search(r"\b" + re.escape(word) + r"\b", text) is not None


def route(payload: dict[str, Any]) -> dict[str, Any]:
    if not isinstance(payload, dict):
        raise ValueError("request must be a JSON object")
    text = payload.get("text")
    targets = payload.get("targets")
    if not isinstance(text, str) or not isinstance(targets, list):
        raise ValueError("request text and targets are required")
    domain = classify(text)
    provider = "coding" if domain == "unknown" else domain
    for index, target in enumerate(targets):
        if isinstance(target, dict) and target.get("provider") == provider and int(target.get("weight") or 0) > 0:
            return {"targetIndex": index, "fallbackIndexes": [], "classLabel": f"domain:{domain}"}
    raise ValueError("required expert unavailable")


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
            self._write_json(422, {"error": "required expert unavailable"})

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
    parser.add_argument("--port", type=int, default=18427)
    args = parser.parse_args()
    ThreadingHTTPServer((args.host, args.port), PolicyHandler).serve_forever()


if __name__ == "__main__":
    main()
