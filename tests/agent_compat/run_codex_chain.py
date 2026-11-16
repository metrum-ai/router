#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Report real sandboxed Codex Responses tool loops across local chains.

A denied workspace sandbox is a failed cell; never disable the sandbox. This
runner makes no hosted calls and publishes results even when a cell fails.
"""
import argparse
import contextlib
import errno
import json
import os
import signal
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

from run_chat_smoke import ROOT, runtime_environment

DEPTHS = (0, 1, 2, 3, 5)
DRIVER = ROOT / "tests/agent_compat/codex_responses_smoke.py"
TEMP_CLEANUP_TIMEOUT_SECONDS = 10


def remove_temp_tree(path: Path) -> None:
    """Retry transient non-empty errors from Codex's asynchronous home writes."""
    deadline = time.monotonic() + TEMP_CLEANUP_TIMEOUT_SECONDS
    while True:
        try:
            shutil.rmtree(path)
            return
        except FileNotFoundError:
            return
        except OSError as exc:
            if exc.errno not in {errno.EBUSY, errno.EACCES, errno.ENOTEMPTY, errno.ENOENT}:
                raise
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.05)


@contextlib.contextmanager
def retrying_temporary_directory(prefix: str):
    directory = tempfile.TemporaryDirectory(prefix=prefix, ignore_cleanup_errors=True)
    try:
        yield Path(directory.name)
    finally:
        try:
            remove_temp_tree(Path(directory.name))
        finally:
            directory.cleanup()


def run_cell(depth):
    process = subprocess.Popen(
        [sys.executable, str(DRIVER), str(depth)], cwd=ROOT,
        env=runtime_environment(), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, start_new_session=True,
    )
    try:
        try:
            stdout, stderr = process.communicate(timeout=180)
        except subprocess.TimeoutExpired:
            return {"client": "codex", "surface": "openai-responses", "path": "same-dialect", "depth": depth, "status": "fail", "reason": "timeout"}
    finally:
        if process.poll() is None:
            # SIGTERM lets the driver unwind and kill its separately grouped Codex.
            try:
                os.kill(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.communicate(timeout=20)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.communicate(timeout=5)
    marker = f"PASS: codex / OpenAI Responses / same dialect / depth {depth}:"
    ok = process.returncode == 0 and marker in stdout
    cell = {"client": "codex", "surface": "openai-responses", "path": "same-dialect", "depth": depth, "status": "pass" if ok else "fail"}
    if not ok:
        cell["reason"] = f"exit={process.returncode}; " + (stderr or stdout)[-1500:]
    return cell


def main():
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    report = {"schema": "router-agent-chain-smoke-v1", "fixture": "local-scripted-sse", "hosted_model_calls": 0, "results": []}
    for depth in DEPTHS:
        cell = run_cell(depth)
        report["results"].append(cell)
        args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(f"{cell['status'].upper()}: codex / responses / depth {depth}", flush=True)
    return 0 if all(cell["status"] == "pass" for cell in report["results"]) else 1


if __name__ == "__main__":
    raise SystemExit(main())
