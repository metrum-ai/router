#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline bounds for the manual Harbor promotion runner."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

import harbor_promotion  # noqa: E402


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def expect_error(message: str, **kwargs: str) -> None:
    try:
        harbor_promotion.validate_request(**kwargs)
    except harbor_promotion.PromotionError as exc:
        require(message in str(exc), f"expected {message!r} in {exc}")
        return
    raise AssertionError(f"accepted invalid request: {kwargs}")


def main() -> int:
    case_study = (ROOT / "examples" / "harbor-algotune-pca" / "run_case_study.sh").read_text(encoding="utf-8")
    require("--n-concurrent 1" in case_study, "Harbor case study must keep concurrency at one")

    ok = harbor_promotion.validate_request(
        task="aider/polyglot_python_two-bucket",
        agents="codex,claude-code",
        groups="small",
        confirm="aider/polyglot_python_two-bucket",
    )
    require(ok["cells"] == 2, f"unexpected cell count: {ok}")

    expect_error("confirm must exactly match", task="task-a", agents="codex", groups="small", confirm="task-b")
    expect_error("at most", task="task-a", agents="codex", groups="a,b,c", confirm="task-a")
    expect_error("at most", task="task-a", agents="a,b,c", groups="small", confirm="task-a")
    expect_error("bounded Harbor task", task="", agents="codex", groups="small", confirm="")

    try:
        harbor_promotion.require_credentials({})
    except harbor_promotion.PromotionError as exc:
        require("blocked:" in str(exc), f"missing credentials were accepted: {exc}")
    else:
        raise AssertionError("missing credentials were accepted")

    token = "synthetic-harbor-token-not-real"
    with tempfile.TemporaryDirectory() as temporary:
        out = Path(temporary) / "summary.tsv"
        env = os.environ.copy()
        env.update(
            {
                "HARBOR_ROUTER_TOKEN": token,
                "ROUTER_BASE_URL": "http://127.0.0.1:9",
                "HARBOR_PROMOTION_TASK": "aider/polyglot_python_two-bucket",
                "HARBOR_PROMOTION_AGENTS": "codex",
                "HARBOR_PROMOTION_GROUPS": "small",
                "HARBOR_PROMOTION_CONFIRM": "aider/polyglot_python_two-bucket",
                "HARBOR_PROMOTION_OUT": str(out),
            }
        )
        completed = subprocess.run(
            [sys.executable, str(ROOT / "scripts" / "harbor_promotion.py"), "--dry-run"],
            cwd=ROOT,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        combined = completed.stdout + completed.stderr
        require(completed.returncode == 0, f"dry-run failed:\n{combined}")
        require(token not in combined, "dry-run printed the Harbor token")
        require("not statistical certification" in combined, f"missing dry-run summary:\n{combined}")
        summary = out.read_text(encoding="utf-8")
        require("dry-run" in summary, f"sanitized summary missing dry-run row:\n{summary}")
        require("log" not in summary.splitlines()[0], "summary kept the log path column")
        require(token not in summary, "summary contained the Harbor token")

        blocked = subprocess.run(
            [sys.executable, str(ROOT / "scripts" / "harbor_promotion.py"), "--dry-run"],
            cwd=ROOT,
            env={key: value for key, value in env.items() if key != "HARBOR_ROUTER_TOKEN"},
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        require(blocked.returncode != 0, "missing token was treated as a pass")
        require("blocked:" in blocked.stderr, f"missing token was not fail-closed:\n{blocked.stderr}")

    bounds = harbor_promotion.main(
        [
            "--check-only",
            "--task",
            "aider/polyglot_python_two-bucket",
            "--agents",
            "codex",
            "--groups",
            "small",
            "--confirm",
            "aider/polyglot_python_two-bucket",
        ]
    )
    require(bounds == 0, "check-only bounds failed")
    print(json.dumps({"result": "passed"}))
    print("Harbor promotion bounds passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
