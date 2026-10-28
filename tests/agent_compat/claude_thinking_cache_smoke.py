#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Pinned Claude Code thinking + tool continuation and cache-control probe."""

import json
import os
import pathlib
import signal
import subprocess
import sys
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[1]


def main():
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    # Forward no ambient provider credentials to the local mock or client.
    env = {key: os.environ[key] for key in ("PATH", "HOME", "TMPDIR", "LD_LIBRARY_PATH") if key in os.environ}
    env["AGENT_COMPAT_THINKING"] = "1"
    with tempfile.TemporaryDirectory(prefix="claude-thinking-cache-") as tmp:
        metadata = []
        for depth in (0, 1, 2, 3):
            report = pathlib.Path(tmp) / f"depth-{depth}.json"
            run_env = dict(env, AGENT_COMPAT_METADATA_REPORT=str(report))
            proc = subprocess.Popen([sys.executable, str(HERE / "claude_anthropic_smoke.py"), str(depth)],
                                    cwd=ROOT, env=run_env)
            try:
                try:
                    status = proc.wait(timeout=150)
                except subprocess.TimeoutExpired:
                    raise SystemExit(f"Claude synthetic thinking/tool test timed out at depth {depth}")
            finally:
                if proc.poll() is None:
                    proc.terminate()  # child unwinds its router and CLI processes on SIGTERM
                    try:
                        proc.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait(timeout=5)
            if status:
                raise SystemExit(f"Claude synthetic thinking/tool test failed at depth {depth}")
            rows = json.loads(report.read_text(encoding="utf-8"))
            if len(rows) != 2 or any(not row["thinking"]["type"] or len(row["cache_controls"]) < 3 or
                                     any(c["type"] != "ephemeral" for c in row["cache_controls"]) for row in rows):
                raise SystemExit(f"Claude thinking/cache controls absent at depth {depth}")
            metadata.append(rows)
        if any(rows != metadata[0] for rows in metadata[1:]):
            raise SystemExit("thinking/cache control fields or locations differ from direct case")
    print("PASS: Claude Code thinking/signature + streamed tool continuation; cache controls and thinking fields match direct at depths 0/1/2/3")


if __name__ == "__main__":
    main()
