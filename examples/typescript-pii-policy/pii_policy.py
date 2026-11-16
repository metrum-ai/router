#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Trusted external PII-aware routing policy. Never logs request content."""

from __future__ import annotations

import argparse
import json
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

PII_PATTERNS = tuple(re.compile(pattern) for pattern in (
    r"(?i:\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b)",
    r"\b(?:\+?1[-.\s]?)?(?:\(?\d{3}\)?[-.\s]?)\d{3}[-.\s]?\d{4}\b",
    r"\b\d{3}-\d{2}-\d{4}\b",
    r"\b(?:\d[ -]*?){13,19}\b",
    r"\b\d{1,5}\s+[A-Z0-9][A-Z0-9.'-]*(?:\s+[A-Z0-9][A-Z0-9.'-]*)*\s+(?i:Street|St|Avenue|Ave|Road|Rd|Boulevard|Blvd|Drive|Dr|Lane|Ln|Court|Ct|Way|Place|Pl)\b",
    r"\b[A-Z][a-z]+(?:\s+[A-Z][a-z]+){1,3}\b",
    r"(?i:\b(?:DOB|date of birth|birthdate)\s*[:=-]?\s*)(?:\d{1,2}[/-]\d{1,2}[/-]\d{2,4}|[A-Z][a-z]+\s+\d{1,2},\s+\d{4})\b",
    r"(?i:\b(?:MRN|medical record|patient id)\s*[:#-]?\s*)[A-Z0-9-]{4,}\b",
    r"(?i:\b(?:account|acct|routing)\s*(?:number|no\.?|#)?\s*[:#-]?\s*)\d{6,17}\b",
    r"(?i:\b(?:passport|driver'?s license|license)\s*(?:number|no\.?|#)?\s*[:#-]?\s*)[A-Z0-9-]{5,}\b",
))


class NoSensitiveTarget(Exception):
    pass


def _is_sensitive(target: dict[str, Any]) -> bool:
    return any(re.search(r"\b(sensitive|private)\b", str(target.get(field) or ""), re.IGNORECASE)
               for field in ("tier", "displayName", "model"))


def route(payload: dict[str, Any]) -> dict[str, Any]:
    # This service requires external_policy.include_request: true. Deploy it only
    # where the policy is trusted to receive request text.
    text = str(payload.get("text") or "")
    targets = payload.get("targets") or []
    eligible = [
        (index, target)
        for index, target in enumerate(targets)
        if target.get("keyConfigured") and int(target.get("weight") or 0) > 0
    ]
    fallback = eligible[0][0] if eligible else 0
    has_pii = any(pattern.search(text) for pattern in PII_PATTERNS)
    if has_pii:
        sensitive = [(index, target) for index, target in eligible if _is_sensitive(target)]
        if not sensitive:
            raise NoSensitiveTarget
        selected = sensitive[0][0]
        return {
            "targetIndex": selected,
            "fallbackIndexes": [index for index, _ in sensitive if index != selected],
            "classLabel": "pii-detected:sensitive-route",
        }
    normal = next(((index, target) for index, target in eligible if not _is_sensitive(target)), None)
    selected = normal[0] if normal else fallback
    return {
        "targetIndex": selected,
        "fallbackIndexes": [index for index, _ in eligible if index != selected],
        "classLabel": "pii-detected:none",
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
        except NoSensitiveTarget:
            self._write_json(422, {"error": "pii-detected:no-sensitive-target"})
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
    parser.add_argument("--port", type=int, default=18094)
    args = parser.parse_args()
    ThreadingHTTPServer((args.host, args.port), PolicyHandler).serve_forever()


if __name__ == "__main__":
    main()
