#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Scripted fake of the d3 System One API (`POST /v1/systemone`) for offline tests.

This is not a model. It answers the questions it is sent with a small,
deterministic keyword heuristic so Metrum AI Router tests can prove wiring
without a GPU: requests that mention proofs, distributed systems, concurrency,
optimisation or similar are "hard"; everything else is "trivial"/"moderate".
The response shapes match `d3_server.py` from the vllm-sr/d3 model packages
(choice: choice/probabilities/confidence; noul: noul; score: expected index
score with probabilities keyed "0".."n-1"), including the `server-timing`
header.

Failure modes for tests (`POST /__mode {"mode": ...}` or `--mode`):
  ok, http500, http422, http400, malformed, question_error, slow, hang
`DECISION_API_KEY` in the environment enables bearer auth like d3_server.py.

The fake keeps only counters and state lengths, never request text.

Copyright 2026 Metrum AI.
"""
from __future__ import annotations

import argparse
import hmac
import json
import os
import re
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

MODES = ("ok", "http500", "http422", "http400", "malformed", "question_error", "slow", "hang")
HARD_RE = re.compile(
    r"\b(prove|proof|theorem|distributed|consensus|concurren\w*|lock-free|optimi[sz]\w*|"
    r"complexity|derive|formal|invariant|race condition|compiler|scheduler|asymptotic|"
    r"np-hard|cryptograph\w*|architecture)\b",
    re.I,
)
CODE_RE = re.compile(r"\b(code|function|python|go|rust|bug|class|compile|refactor|test|api|sql|regex)\b", re.I)
# The arithmetic branch anchors on the digit either side of the operator, with
# bounded whitespace, so `search` stays linear on long digit/space runs (CodeQL
# py/polynomial-redos). For `search` this matches exactly what
# `\d+\s*[-+*/^]\s*\d+` did, apart from more than 8 spaces around the operator.
MATH_RE = re.compile(
    r"\b(integral|equation|prove|theorem|sum|probability|matrix|calculate|derivative|prime)\b|"
    r"\d\s{0,8}[-+*/^]\s{0,8}\d",
    re.I,
)
AGENT_RE = re.compile(r"\b(tool|write the file|read the file|create the file|run the command|proof\.txt)\b", re.I)


# Keyword scans look at the tail only, matching the adapter's default
# left-truncation (`text.max_chars`), so oversized states stay cheap.
MAX_SCAN_CHARS = 32768


def classify(state: str) -> dict[str, float]:
    """Return heuristic signals in [0, 1]."""
    tail = state[-MAX_SCAN_CHARS:]
    hard = 1.0 if HARD_RE.search(tail) else 0.0
    if len(state) > 6000:
        hard = max(hard, 0.6)
    return {
        "hard": hard,
        "code": 1.0 if CODE_RE.search(tail) else 0.0,
        "math": 1.0 if MATH_RE.search(tail) else 0.0,
        "agentic": 1.0 if AGENT_RE.search(tail) else 0.0,
    }


def _norm(values: list[float]) -> list[float]:
    total = sum(values)
    return [v / total for v in values]


def _confidence(values: list[float]) -> float:
    ordered = sorted(values, reverse=True)
    return round(ordered[0] - (ordered[1] if len(ordered) > 1 else 0.0), 4)


def answer(question: dict[str, Any], signals: dict[str, float]) -> dict[str, Any]:
    kind = question.get("type")
    if kind == "noul":
        p = 0.82 if signals["hard"] >= 1.0 else (0.55 if signals["math"] else 0.18)
        return {"type": "noul", "noul": p}
    if kind == "score":
        levels = list(question.get("criteria") or [])
        n = len(levels)
        weights = [1.0] * n
        # Hard requests lean to the top levels, easy ones to the bottom.
        focus = n - 2 if signals["hard"] >= 1.0 else (n // 2 if signals["hard"] > 0 else 0)
        weights[max(0, min(focus, n - 1))] += 6.0
        if signals["hard"] >= 1.0 and n > 1:
            weights[n - 1] += 2.0
        probs = _norm(weights)
        return {
            "type": "score",
            "score": sum(i * p for i, p in enumerate(probs)),
            "probabilities": {str(i): p for i, p in enumerate(probs)},
            "confidence": _confidence(probs),
            "legend": {str(i): str(level) for i, level in enumerate(levels)},
        }
    if kind == "choice":
        keys = list((question.get("criteria") or {}).keys())
        weights = []
        for key in keys:
            w = 1.0
            if key in ("agentic", "tool", "tools") and signals["agentic"]:
                w += 8.0
            elif key == "code" and signals["code"]:
                w += 5.0
            elif key == "math" and signals["math"]:
                w += 4.0
            elif key in ("chat", "other") and not (signals["code"] or signals["math"] or signals["agentic"]):
                w += 4.0
            weights.append(w)
        probs = _norm(weights)
        best = max(range(len(keys)), key=probs.__getitem__)
        return {"type": "choice", "choice": keys[best], "probabilities": dict(zip(keys, probs)),
                "confidence": _confidence(probs)}
    return {"type": str(kind), "error": "invalid_question", "message": "unsupported question type"}


class FakeD3(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, host: str = "127.0.0.1", port: int = 0, mode: str = "ok",
                 model: str = "vllm-sr/d3-lite", slow_ms: int = 2000, api_key: str | None = None) -> None:
        super().__init__((host, port), FakeD3Handler)
        self.mode = mode
        self.model_name = model
        self.slow_ms = slow_ms
        self.api_key = api_key if api_key is not None else os.environ.get("DECISION_API_KEY", "")
        self.lock = threading.Lock()
        self.serial = threading.Lock()  # d3_server.py serves one request at a time
        self.calls = 0
        self.state_chars: list[int] = []

    def set_mode(self, mode: str) -> None:
        if mode not in MODES:
            raise ValueError(mode)
        with self.lock:
            self.mode = mode


class FakeD3Handler(BaseHTTPRequestHandler):
    server: FakeD3
    server_version = "fake-d3/1.0"

    def log_message(self, *_args: object) -> None:
        return

    def _json(self, code: int, body: Any, started: float | None = None) -> None:
        raw = (body if isinstance(body, bytes) else json.dumps(body).encode("utf-8"))
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        if started is not None:
            self.send_header("server-timing", f"total;dur={(time.perf_counter() - started) * 1000:.1f}")
        self.end_headers()
        try:
            self.wfile.write(raw)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def _authorized(self) -> bool:
        key = self.server.api_key
        if not key:
            return True
        got = self.headers.get("Authorization", "")
        return hmac.compare_digest(got.encode(), f"Bearer {key}".encode())

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/health":
            return self._json(200, {"status": "ready", "model": self.server.model_name, "modalities": ["text"],
                                    "authentication": bool(self.server.api_key), "fake": True})
        if self.path == "/__stats":
            with self.server.lock:
                return self._json(200, {"calls": self.server.calls, "mode": self.server.mode})
        return self._json(404, {"detail": "Not Found"})

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length) if length else b""
        if self.path == "/__mode":
            try:
                self.server.set_mode(json.loads(raw)["mode"])
            except (ValueError, KeyError, TypeError):
                return self._json(400, {"detail": "bad mode"})
            return self._json(200, {"mode": self.server.mode})
        if self.path not in ("/v1/systemone", "/v1/decisions"):
            return self._json(404, {"detail": "Not Found"})
        if not self._authorized():
            return self._json(401, {"detail": "Missing or invalid API key."})
        started = time.perf_counter()
        with self.server.serial:
            with self.server.lock:
                self.server.calls += 1
                mode = self.server.mode
            if mode == "slow":
                time.sleep(self.server.slow_ms / 1000.0)
            elif mode == "hang":
                time.sleep(30)
            if mode == "http500":
                return self._json(500, {"detail": "synthetic failure"}, started)
            if mode == "http400":
                return self._json(400, {"detail": "synthetic bad request"}, started)
            try:
                body = json.loads(raw)
            except ValueError:
                return self._json(422, {"detail": "The request body must be JSON."}, started)
            if mode == "http422" or not isinstance(body, dict) or not isinstance(body.get("questions"), dict):
                return self._json(422, {"detail": "synthetic unprocessable input"}, started)
            if mode == "malformed":
                return self._json(200, b'{"answers": {"difficulty": ', started)
            state = body.get("state")
            state_text = state if isinstance(state, str) else json.dumps(state)
            with self.server.lock:
                self.server.state_chars.append(len(state_text))
            signals = classify(state_text)
            answers = {}
            for name, question in body["questions"].items():
                answers[name] = answer(question if isinstance(question, dict) else {}, signals)
                if mode == "question_error":
                    answers[name] = {"type": answers[name].get("type"), "error": "max_length_exceeded",
                                     "message": "synthetic"}
            return self._json(200, {"model": self.server.model_name, "answers": answers,
                                    "usage": {"input_tokens": max(1, len(state_text) // 4), "output_tokens": 0}},
                              started)


def start_in_thread(**kwargs: Any) -> FakeD3:
    server = FakeD3(**kwargs)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=18095)
    ap.add_argument("--mode", default="ok", choices=MODES)
    ap.add_argument("--model", default="vllm-sr/d3-lite")
    ap.add_argument("--slow-ms", type=int, default=2000)
    args = ap.parse_args()
    server = FakeD3(args.host, args.port, args.mode, args.model, args.slow_ms)
    print(json.dumps({"event": "listening", "url": f"http://{args.host}:{server.server_port}", "fake": True}),
          flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
