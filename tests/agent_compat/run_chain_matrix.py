#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Run bounded same-dialect agent chain cases and report every tested cell."""
from __future__ import annotations

import argparse
import json
import os
import signal
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
DEPTHS = (0, 1, 2, 3, 5)
CLIENTS = ("pi", "langgraph", "openai-agents", "claude-code")


def run_case(client: str, depth: int) -> dict:
    if client == "claude-code":
        command = [sys.executable, str(HERE / "claude_anthropic_smoke.py"), str(depth)]
        surface = "anthropic-messages"
    else:
        command = [sys.executable, str(HERE / "run_chat_smoke.py"), client, str(depth)]
        surface = "openai-chat"
    try:
        process = subprocess.Popen(command, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
    except OSError as exc:
        return {"client": client, "surface": surface, "path": "same-dialect", "depth": depth, "status": "fail", "diagnostic": f"client launch failed: {exc}"}
    timed_out = False
    try:
        try:
            stdout, stderr = process.communicate(timeout=180)
        except subprocess.TimeoutExpired:
            timed_out = True
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                stdout, stderr = process.communicate(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                stdout, stderr = process.communicate()
    finally:
        if process.poll() is None:
            # Give the child a chance to terminate its router and its own
            # separately grouped CLI before escalating.
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
    passed = process.returncode == 0 and not timed_out
    result = {"client": client, "surface": surface, "path": "same-dialect", "depth": depth, "status": "pass" if passed else "fail"}
    if not passed:
        # Only synthetic fixture data is sent to the processes; cap diagnostics.
        result["diagnostic"] = ("timeout\n" if timed_out else "") + (stderr + stdout)[-2000:]
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True, help="JSON results path")
    args = parser.parse_args()

    def interrupted(_signum, _frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupted)
    results = []
    for client in CLIENTS:
        for depth in DEPTHS:
            result = run_case(client, depth)
            results.append(result)
            print(f"{result['status'].upper()}: {client}, {result['surface']}, depth {depth}", flush=True)
    report = {"schema": "router-agent-chain-smoke-v1", "fixture": "local-scripted-sse", "hosted_model_calls": 0, "results": results}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return int(any(result["status"] != "pass" for result in results))


if __name__ == "__main__":
    raise SystemExit(main())
