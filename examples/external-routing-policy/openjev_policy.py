#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""OpenJev task-class external routing policy for Metrum AI Router.

OpenJev answers typed decisions (choice / score / noul). This service asks it
which *task class* a request is (simple / medium / advanced), then maps that
class onto the cheapest, mid, and flagship eligible OpenAI targets.

Trusted demo only: requires `external_policy.include_request: true`.

OpenJev weights are CC BY-NC 4.0. Use this example for research and other
non-commercial evaluation unless you have commercial permission from the
OpenJev authors. The helper/shim code is Apache 2.0. OpenJev is independent of
TypeSafe's hosted Jev product.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import urlparse

TASK_ORDER = ("simple", "medium", "advanced")
TIER_BY_TASK = {
    "simple": "cheap",
    "medium": "medium",
    "advanced": "advanced",
}
# Prefer matching by known model IDs when tiers are missing from config.
MODEL_HINTS = {
    "simple": (
        "gpt-6-luna",
        "gpt-5.6-luna",
        "gpt-5.4-nano",
        "gpt-5-nano",
    ),
    "medium": (
        "gpt-6-sol",
        "gpt-5.6-sol",
        "gpt-5.6-terra",
        "gpt-5.4-mini",
        "gpt-5.4",
    ),
    "advanced": (
        "gpt-6-astra",
        "gpt-5.6-sol",
        "gpt-5.5-pro",
        "gpt-5.4-pro",
        "gpt-5.5",
    ),
}

DEFAULT_CONFIDENCE_FLOOR = 0.45
DEFAULT_TIMEOUT_S = 2.0
SAFE_CLASS_RE = re.compile(r"^[A-Za-z0-9_.:-]{1,64}$")


def _env_float(name: str, default: float) -> float:
    raw = os.environ.get(name)
    if raw is None or raw == "":
        return default
    return float(raw)


def extract_text(payload: dict[str, Any]) -> str:
    """Build the OpenJev state string from trusted request content."""
    chunks: list[str] = []
    text = payload.get("text")
    if isinstance(text, str) and text.strip():
        chunks.append(text.strip())
    req = payload.get("request") or {}
    system = req.get("system")
    if isinstance(system, str) and system.strip():
        chunks.append("System: " + system.strip())
    for msg in req.get("messages") or []:
        if not isinstance(msg, dict):
            continue
        role = str(msg.get("role") or "user")
        content = msg.get("content")
        if isinstance(content, list):
            parts = []
            for part in content:
                if isinstance(part, dict) and part.get("text"):
                    parts.append(str(part["text"]))
            content = " ".join(parts)
        if content:
            chunks.append(f"{role}: {content}")
    joined = "\n".join(chunks).strip()
    if joined:
        return joined[:12000]
    ctx = payload.get("context") or {}
    return (
        f"(no request text; textChars={ctx.get('textChars', 0)}; "
        f"estimatedTokens={ctx.get('estimatedTokens', 0)}; "
        f"toolCount={ctx.get('toolCount', 0)})"
    )


def openjev_questions() -> dict[str, Any]:
    return {
        "task": {
            "type": "choice",
            "instructions": (
                "Classify the user request into exactly one task class for model routing. "
                "simple = short summarization, extraction, classification, rewrite, or "
                "other high-volume low-stakes text work. "
                "medium = bounded coding, analysis, multi-step but well-scoped work. "
                "advanced = hard reasoning, ambiguous research, multi-constraint design, "
                "or high-stakes judgment."
            ),
            "criteria": {
                "simple": None,
                "medium": None,
                "advanced": None,
            },
        },
        "complexity": {
            "type": "score",
            "instructions": "How complex is this request?",
            "criteria": ["trivial", "light", "moderate", "hard", "expert"],
        },
        "high_risk": {
            "type": "noul",
            "instructions": (
                "Is this high-risk if answered poorly (safety, legal, irreversible "
                "actions, or material money loss)?"
            ),
        },
    }


def parse_openjev_answers(body: dict[str, Any]) -> dict[str, Any]:
    """Normalize OpenJev / System One response shapes into routing fields."""
    answers = body.get("answers") or body.get("results") or body
    task_block = answers.get("task") if isinstance(answers, dict) else None
    if not isinstance(task_block, dict):
        raise ValueError("missing task answer")

    task = (
        task_block.get("choice")
        or task_block.get("answer")
        or task_block.get("value")
        or task_block.get("selected")
    )
    if isinstance(task, dict):
        task = task.get("id") or task.get("label") or task.get("name")
    task = str(task or "").strip().lower()
    if task not in TASK_ORDER:
        # Some helpers return letter indices mapped via criteria keys.
        probs = task_block.get("probabilities") or task_block.get("scores") or {}
        if isinstance(probs, dict) and probs:
            task = max(probs.items(), key=lambda kv: float(kv[1]))[0].lower()
    if task not in TASK_ORDER:
        raise ValueError(f"invalid task class: {task!r}")

    confidence = task_block.get("confidence")
    if confidence is None:
        probs = task_block.get("probabilities") or {}
        if isinstance(probs, dict) and task in probs:
            confidence = float(probs[task])
        else:
            confidence = 0.0
    confidence = float(confidence)

    complexity = None
    cblock = answers.get("complexity") if isinstance(answers, dict) else None
    if isinstance(cblock, dict):
        complexity = cblock.get("score")
        if complexity is None:
            complexity = cblock.get("expected") or cblock.get("value")

    high_risk = False
    hblock = answers.get("high_risk") if isinstance(answers, dict) else None
    if isinstance(hblock, dict):
        if "probability" in hblock:
            high_risk = float(hblock["probability"]) >= 0.5
        elif "yes" in (hblock.get("probabilities") or {}):
            high_risk = float(hblock["probabilities"]["yes"]) >= 0.5
        elif "answer" in hblock:
            high_risk = str(hblock["answer"]).lower() in {"yes", "true", "1"}

    return {
        "task": task,
        "confidence": confidence,
        "complexity": complexity,
        "high_risk": high_risk,
        "raw_task": task_block,
    }


def escalate(task: str, *, confidence: float, high_risk: bool, floor: float) -> str:
    idx = TASK_ORDER.index(task)
    if high_risk or confidence < floor:
        idx = min(idx + 1, len(TASK_ORDER) - 1)
    return TASK_ORDER[idx]


def _model_id(target: dict[str, Any]) -> str:
    return str(target.get("model") or target.get("modelRef") or "").lower()


def find_target_index(
    targets: list[dict[str, Any]],
    task: str,
) -> int | None:
    """Resolve task class to an eligible target by tier, then model hints."""
    want_tier = TIER_BY_TASK[task]
    eligible = [
        (i, t)
        for i, t in enumerate(targets)
        if t.get("keyConfigured", True) and int(t.get("weight") or 0) >= 0
    ]
    if not eligible:
        return None

    by_tier = [i for i, t in eligible if str(t.get("tier") or "").lower() == want_tier]
    if by_tier:
        return by_tier[0]

    hints = MODEL_HINTS[task]
    for hint in hints:
        for i, t in eligible:
            mid = _model_id(t)
            if mid == hint or mid.endswith("/" + hint) or hint in mid:
                return i

    # Fall back along the ladder: prefer cheaper for simple, flagship for advanced.
    if task == "simple":
        return eligible[0][0]
    if task == "advanced":
        return eligible[-1][0]
    mid = len(eligible) // 2
    return eligible[mid][0]


def decide_from_parsed(
    payload: dict[str, Any],
    parsed: dict[str, Any],
    *,
    confidence_floor: float,
) -> dict[str, Any]:
    targets = list(payload.get("targets") or [])
    if not targets:
        raise ValueError("no targets")

    final_task = escalate(
        parsed["task"],
        confidence=float(parsed["confidence"]),
        high_risk=bool(parsed["high_risk"]),
        floor=confidence_floor,
    )
    idx = find_target_index(targets, final_task)
    if idx is None:
        raise ValueError("no eligible target")

    fallbacks = [
        i
        for i, t in enumerate(targets)
        if i != idx and t.get("keyConfigured", True)
    ]
    # Prefer escalating fallbacks for reliability.
    higher = []
    lower = []
    final_idx = TASK_ORDER.index(final_task)
    for i in fallbacks:
        t = targets[i]
        tier = str(t.get("tier") or "").lower()
        try:
            ti = ["cheap", "medium", "advanced"].index(tier)
        except ValueError:
            ti = final_idx
        if ti > final_idx:
            higher.append(i)
        else:
            lower.append(i)
    ordered_fallback = higher + lower

    label = f"openjev:{final_task}"
    if not SAFE_CLASS_RE.match(label):
        label = "openjev:unsafe"

    return {
        "targetIndex": idx,
        "fallbackIndexes": ordered_fallback,
        "classLabel": label,
        "metadata": {
            "task": final_task,
            "requestedTask": parsed["task"],
            "confidenceBand": (
                "high"
                if parsed["confidence"] >= 0.75
                else "mid"
                if parsed["confidence"] >= confidence_floor
                else "low"
            ),
            "escalated": final_task != parsed["task"],
            "highRisk": bool(parsed["high_risk"]),
            "complexity": parsed.get("complexity"),
        },
    }


class OpenJevClient:
    def __init__(self, base_url: str, timeout_s: float, model: str = "openjev") -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout_s = timeout_s
        self.model = model

    def decide(self, state: str) -> dict[str, Any]:
        body = {
            "model": self.model,
            "state": state,
            "questions": openjev_questions(),
        }
        req = urllib.request.Request(
            self.base_url + "/v1/systemone",
            data=json.dumps(body).encode("utf-8"),
            headers={"Content-Type": "application/json", "Accept": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=self.timeout_s) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as err:
            detail = err.read().decode("utf-8", errors="replace")[:500]
            raise RuntimeError(f"openjev http {err.code}: {detail}") from err
        except urllib.error.URLError as err:
            raise RuntimeError(f"openjev unreachable: {err}") from err
        return json.loads(raw.decode("utf-8"))


class State:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.decisions: dict[str, Any] = {}
        self.errors = 0
        self.calls = 0


S = State()


def route(payload: dict[str, Any], client: OpenJevClient, floor: float) -> dict[str, Any]:
    state = extract_text(payload)
    started = time.perf_counter()
    body = client.decide(state)
    latency_ms = int((time.perf_counter() - started) * 1000)
    parsed = parse_openjev_answers(body)
    decision = decide_from_parsed(payload, parsed, confidence_floor=floor)
    decision["metadata"]["openjevLatencyMs"] = latency_ms
    with S.lock:
        S.calls += 1
        rid = ((payload.get("request") or {}).get("raw") or {}).get("metadata", {}).get(
            "request_id"
        )
        S.decisions[str(rid or S.calls)] = {
            "decision": decision,
            "parsed": {
                "task": parsed["task"],
                "confidence": parsed["confidence"],
                "high_risk": parsed["high_risk"],
            },
        }
    return decision


class Handler(BaseHTTPRequestHandler):
    server_version = "openjev-policy/1.0"
    client: OpenJevClient
    confidence_floor: float = DEFAULT_CONFIDENCE_FLOOR

    def log_message(self, *_args: object) -> None:
        return

    def _json(self, code: int, body: dict[str, Any]) -> None:
        raw = json.dumps(body).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def _body(self) -> dict[str, Any]:
        n = int(self.headers.get("content-length", "0"))
        return json.loads(self.rfile.read(n) or b"{}")

    def do_GET(self) -> None:  # noqa: N802
        path = urlparse(self.path).path
        if path == "/health":
            return self._json(200, {"ok": True, "calls": S.calls, "errors": S.errors})
        if path == "/state":
            with S.lock:
                return self._json(
                    200,
                    {"calls": S.calls, "errors": S.errors, "recent": list(S.decisions)[-20:]},
                )
        self._json(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        path = urlparse(self.path).path
        if path != "/route":
            return self._json(404, {"error": "not found"})
        payload = self._body()
        try:
            decision = route(payload, self.client, self.confidence_floor)
            return self._json(200, decision)
        except Exception as err:  # noqa: BLE001 - policy fail-closed path
            with S.lock:
                S.errors += 1
            # Fail closed: non-2xx so the router can apply on_error.
            return self._json(502, {"error": "openjev-policy-error", "class": type(err).__name__})


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=18093)
    ap.add_argument(
        "--openjev-url",
        default=os.environ.get("OPENJEV_URL", "http://127.0.0.1:3000"),
        help="OpenJev System One shim base URL",
    )
    ap.add_argument(
        "--timeout-s",
        type=float,
        default=_env_float("OPENJEV_TIMEOUT_S", DEFAULT_TIMEOUT_S),
    )
    ap.add_argument(
        "--confidence-floor",
        type=float,
        default=_env_float("OPENJEV_CONFIDENCE_FLOOR", DEFAULT_CONFIDENCE_FLOOR),
    )
    ap.add_argument("--model", default=os.environ.get("OPENJEV_MODEL", "openjev"))
    args = ap.parse_args()

    Handler.client = OpenJevClient(args.openjev_url, args.timeout_s, model=args.model)
    Handler.confidence_floor = args.confidence_floor
    print(
        f"openjev policy on http://{args.host}:{args.port}/route "
        f"(openjev={args.openjev_url} floor={args.confidence_floor})",
        flush=True,
    )
    ThreadingHTTPServer((args.host, args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
