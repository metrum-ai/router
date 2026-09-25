#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Deterministic fake OpenJev System One shim for synthetic wiring tests.

Implements POST /v1/systemone with the same answer field names the policy
expects. Classification is keyword-based so tests are reproducible without a GPU.
"""
from __future__ import annotations

import argparse
import json
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ADV = re.compile(
    r"\b(prove|theorem|multi-?constraint|research|ambiguous|architect|"
    r"trade-?offs?|high[\s-]?stakes|legal analysis|formal verification)\b",
    re.I,
)
MED = re.compile(
    r"\b(implement|refactor|debug|python|sql|function|api|unit test|"
    r"analyze|benchmark|write code|coding)\b",
    re.I,
)
SIMPLE = re.compile(
    r"\b(summarize|summary|extract|classify|rewrite|translate|"
    r"one sentence|tl;dr|bullet)\b",
    re.I,
)


def classify(state: str) -> tuple[str, float, float, bool]:
    text = state or ""
    if ADV.search(text):
        return "advanced", 0.88, 3.6, "high-stakes" in text.lower() or "legal" in text.lower()
    if MED.search(text):
        return "medium", 0.82, 2.4, False
    if SIMPLE.search(text):
        return "simple", 0.90, 0.8, False
    # Default medium-low confidence so escalation tests can force a bump.
    return "medium", 0.40, 2.0, False


class Handler(BaseHTTPRequestHandler):
    server_version = "fake-openjev/1.0"
    shuffle = False

    def log_message(self, *_args: object) -> None:
        return

    def do_POST(self) -> None:  # noqa: N802
        if self.path != "/v1/systemone":
            self.send_error(404)
            return
        n = int(self.headers.get("content-length", "0"))
        body = json.loads(self.rfile.read(n) or b"{}")
        state = str(body.get("state") or "")
        task, conf, score, high_risk = classify(state)
        criteria = ["simple", "medium", "advanced"]
        if self.shuffle:
            criteria = list(reversed(criteria))
        probs = {c: (conf if c == task else (1.0 - conf) / 2.0) for c in criteria}
        total = sum(probs.values()) or 1.0
        probs = {k: v / total for k, v in probs.items()}
        answers = {
            "task": {
                "choice": task,
                "confidence": conf,
                "probabilities": probs,
            },
            "complexity": {"score": score, "expected": score},
            "high_risk": {
                "answer": "yes" if high_risk else "no",
                "probability": 0.8 if high_risk else 0.1,
                "probabilities": {"yes": 0.8 if high_risk else 0.1, "no": 0.2 if high_risk else 0.9},
            },
        }
        raw = json.dumps({"model": body.get("model") or "openjev", "answers": answers}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=3000)
    ap.add_argument("--shuffle-options", action="store_true")
    args = ap.parse_args()
    Handler.shuffle = args.shuffle_options
    print(f"fake openjev on http://{args.host}:{args.port}/v1/systemone", flush=True)
    ThreadingHTTPServer((args.host, args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
