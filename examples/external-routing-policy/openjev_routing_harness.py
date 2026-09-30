#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Synthetic wiring harness for the OpenJev external routing policy.

Proves task-class mapping, confidence escalation, and fail-closed errors against
fake OpenJev. Not provider-backed quality evidence.
"""
from __future__ import annotations

import json
import sys
import time
import urllib.error
import urllib.request

URL = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:18093"

TARGETS = [
    {
        "provider": "openai",
        "model": "gpt-5.6-luna",
        "modelRef": "luna",
        "dialect": "openai-chat",
        "tier": "cheap",
        "weight": 50,
        "inputPricePerMillionUsd": 0.20,
        "outputPricePerMillionUsd": 1.20,
        "keyConfigured": True,
    },
    {
        "provider": "openai",
        "model": "gpt-5.6-sol",
        "modelRef": "sol",
        "dialect": "openai-chat",
        "tier": "medium",
        "weight": 30,
        "inputPricePerMillionUsd": 4.00,
        "outputPricePerMillionUsd": 20.00,
        "keyConfigured": True,
    },
    {
        "provider": "openai",
        "model": "gpt-6-astra",
        "modelRef": "astra",
        "dialect": "openai-chat",
        "tier": "advanced",
        "weight": 20,
        "inputPricePerMillionUsd": 10.00,
        "outputPricePerMillionUsd": 50.00,
        "keyConfigured": True,
    },
]


def post(path: str, body: dict) -> dict:
    req = urllib.request.Request(
        URL + path,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=5) as resp:
        return json.loads(resp.read())


def route(text: str) -> dict:
    return post(
        "/route",
        {
            "group": "openjev-routing-demo",
            "context": {
                "model": "openjev-routing-demo",
                "dialect": "openai-chat",
                "textChars": len(text),
                "messageCount": 1,
                "stream": False,
            },
            "requirements": ["text"],
            "inputModalities": ["text"],
            "caller": {
                "id": "demo",
                "user": "demo",
                "project": "openjev",
                "environment": "dev",
                "tokenId": "rtr_demo_k1",
                "allow": ["openjev-routing-demo"],
            },
            "targets": TARGETS,
            "now": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "request": {
                "model": "openjev-routing-demo",
                "messages": [{"role": "user", "content": text}],
            },
            "text": text,
        },
    )


def expect(label: str, text: str, want_idx: int, want_task: str) -> None:
    d = route(text)
    assert d["targetIndex"] == want_idx, f"{label}: target {d}"
    assert d["classLabel"] == f"openjev:{want_task}", f"{label}: label {d}"
    assert d["metadata"]["task"] == want_task, f"{label}: meta {d}"
    print(f"ok {label}: -> {TARGETS[want_idx]['model']} ({want_task})")


def main() -> int:
    health = json.loads(urllib.request.urlopen(URL + "/health", timeout=2).read())
    assert health.get("ok"), health

    expect("simple", "Please summarize this note in one sentence.", 0, "simple")
    expect("medium", "Implement a Python function to parse CSV and unit test it.", 1, "medium")
    expect(
        "advanced",
        "Prove the multi-constraint trade-offs for this formal verification research design.",
        2,
        "advanced",
    )
    # Low-confidence default path escalates medium -> advanced.
    d = route("Hello there.")
    assert d["metadata"]["escalated"] is True, d
    assert d["targetIndex"] == 2, d
    print(f"ok escalate-low-confidence: -> {TARGETS[d['targetIndex']]['model']}")

    # High-risk medium escalates to advanced.
    d = route("Implement a Python API for high-stakes legal analysis of contracts.")
    assert d["metadata"]["task"] == "advanced", d
    assert d["targetIndex"] == 2, d
    print("ok escalate-high-risk")

    print("openjev synthetic harness passed")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (AssertionError, urllib.error.URLError) as err:
        print(f"openjev harness failed: {err}", file=sys.stderr)
        raise SystemExit(1)
