# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline tests for the caller-token migration inventory."""

import json
import subprocess
import sys
import tempfile
from pathlib import Path


SCRIPT = Path(__file__).with_name("legacy_caller_inventory.py")


def run(contents):
    with tempfile.TemporaryDirectory() as directory:
        config = Path(directory) / "config.yaml"
        config.write_text(contents, encoding="utf-8")
        return subprocess.run([sys.executable, str(SCRIPT), str(config)], capture_output=True, text=True)


def main():
    secret = "fixture-secret-verifier-never-print"
    result = run(f"callers:\n  - id: zed\n    token_sha256: {secret}\n  - id: alice\n    token_sha256: ''\n  - id: bob\n    token_hash: hash\n")
    assert result.returncode == 0, result.stderr
    assert json.loads(result.stdout) == {"legacy_caller_ids": ["alice", "zed"], "count": 2, "scope": "yaml-file-only"}
    assert secret not in result.stdout + result.stderr
    result = run(f"callers: [\n  {secret}\n")
    assert result.returncode == 2 and secret not in result.stdout + result.stderr
    assert run("callers: not-a-list\n").returncode == 2
    assert run("callers:\n - token_sha256: value\n").returncode == 2
    assert json.loads(run("{}\n").stdout)["count"] == 0
    print("legacy caller inventory tests passed")


if __name__ == "__main__":
    main()
