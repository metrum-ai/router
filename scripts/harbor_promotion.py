#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Bounded manual Harbor promotion runner.

This is not a pull-request check. Missing credentials fail closed and are
never reported as a pass. Cell count, agent count, and group count are capped,
and the case-study runner keeps agent concurrency at one.
"""

from __future__ import annotations

import argparse
import csv
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
CASE_STUDY = ROOT / "examples" / "harbor-algotune-pca" / "run_case_study.sh"
MAX_AGENTS = 2
MAX_GROUPS = 2
MAX_CELLS = 4
NAME_RE = re.compile(r"^[A-Za-z0-9_.:/-]{1,160}$")
SUMMARY_FIELDS = (
    "agent",
    "model_group",
    "status",
    "exit_code",
    "elapsed_seconds",
    "reward",
    "errors",
)


class PromotionError(Exception):
    """Fail-closed promotion input or credential error."""

    def __init__(self, message: str, exit_code: int = 2) -> None:
        super().__init__(message)
        self.exit_code = exit_code


def split_csv(label: str, raw: str) -> list[str]:
    if not raw or not raw.strip():
        raise PromotionError(f"{label} is required")
    parts = [part.strip() for part in raw.split(",")]
    if any(not part for part in parts):
        raise PromotionError(f"{label} contains an empty entry")
    for part in parts:
        if NAME_RE.fullmatch(part) is None:
            raise PromotionError(f"{label} entry is not a bounded identifier")
    return parts


def validate_request(*, task: str, agents: str, groups: str, confirm: str) -> dict[str, object]:
    task_name = task.strip()
    if NAME_RE.fullmatch(task_name) is None:
        raise PromotionError("task must be one bounded Harbor task id")
    if confirm.strip() != task_name:
        raise PromotionError("confirm must exactly match the selected task")
    agent_list = split_csv("agents", agents)
    group_list = split_csv("groups", groups)
    if len(agent_list) > MAX_AGENTS or len(group_list) > MAX_GROUPS:
        raise PromotionError(f"at most {MAX_AGENTS} agents and {MAX_GROUPS} groups are allowed")
    cells = len(agent_list) * len(group_list)
    if cells < 1 or cells > MAX_CELLS:
        raise PromotionError(f"cell cap is {MAX_CELLS}")
    return {
        "task": task_name,
        "agents": agent_list,
        "groups": group_list,
        "cells": cells,
    }


def require_credentials(env: dict[str, str]) -> None:
    token = env.get("HARBOR_ROUTER_TOKEN", "").strip()
    base_url = env.get("ROUTER_BASE_URL", "").strip()
    if not token or not base_url:
        raise PromotionError(
            "blocked: Harbor router token or base URL missing; not a certification pass"
        )
    if not (
        base_url.startswith("https://")
        or base_url.startswith("http://127.")
        or base_url.startswith("http://localhost")
    ):
        raise PromotionError("ROUTER_BASE_URL must be https or loopback http")


def redact(text: str, token: str) -> tuple[str, bool]:
    if token and token in text:
        return text.replace(token, "[redacted]"), True
    return text, False


def sanitize_results(results_path: Path, out_path: Path) -> None:
    with results_path.open(encoding="utf-8", newline="") as handle:
        rows = list(csv.DictReader(handle, delimiter="\t"))
    out_path.parent.mkdir(parents=True, exist_ok=True)
    with out_path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=SUMMARY_FIELDS, delimiter="\t", lineterminator="\n")
        writer.writeheader()
        for row in rows:
            writer.writerow({field: row.get(field, "") for field in SUMMARY_FIELDS})


def write_blocked(out_path: str, message: str) -> None:
    if not out_path:
        return
    path = Path(out_path)
    path.parent.mkdir(parents=True, exist_ok=True)
    safe = message.replace("\t", " ").replace("\n", " ")
    path.write_text(f"status\tblocked\nmessage\t{safe}\n", encoding="utf-8")


def run_case_study(spec: dict[str, object], *, dry_run: bool, out_path: Path) -> int:
    token = os.environ.get("HARBOR_ROUTER_TOKEN", "")
    with tempfile.TemporaryDirectory(prefix="harbor-promotion-") as temporary:
        run_dir = Path(temporary)
        agents = spec["agents"]
        groups = spec["groups"]
        if not isinstance(agents, list) or not isinstance(groups, list):
            raise PromotionError("internal promotion bounds were not lists")
        env = os.environ.copy()
        env.update(
            {
                "CASE_ID": "harbor-promotion",
                "HARBOR_TASK": str(spec["task"]),
                "AGENTS": ",".join(str(item) for item in agents),
                "MODEL_GROUPS": ",".join(str(item) for item in groups),
                "RUN_DIR": str(run_dir),
                "TOKEN_ENV_FILE": str(run_dir / "missing-tokens.env"),
                "DRY_RUN": "1" if dry_run else "0",
            }
        )
        completed = subprocess.run(
            ["bash", str(CASE_STUDY)],
            cwd=ROOT,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        stdout, leaked = redact(completed.stdout, token)
        stderr, stderr_leaked = redact(completed.stderr, token)
        if stdout:
            print(stdout, end="" if stdout.endswith("\n") else "\n")
        if stderr:
            print(stderr, file=sys.stderr, end="" if stderr.endswith("\n") else "\n")
        if leaked or stderr_leaked:
            raise PromotionError("blocked: Harbor runner printed credential material")
        results_path = run_dir / "results.tsv"
        if results_path.is_file():
            sanitize_results(results_path, out_path)
        elif completed.returncode != 0:
            write_blocked(str(out_path), "harbor case study failed before writing results")
        return completed.returncode


def parse_args(argv: list[str] | None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--task", default=os.environ.get("HARBOR_PROMOTION_TASK", ""))
    parser.add_argument("--agents", default=os.environ.get("HARBOR_PROMOTION_AGENTS", ""))
    parser.add_argument("--groups", default=os.environ.get("HARBOR_PROMOTION_GROUPS", ""))
    parser.add_argument("--confirm", default=os.environ.get("HARBOR_PROMOTION_CONFIRM", ""))
    parser.add_argument("--out", default=os.environ.get("HARBOR_PROMOTION_OUT", ""))
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--check-only", action="store_true")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        spec = validate_request(
            task=args.task,
            agents=args.agents,
            groups=args.groups,
            confirm=args.confirm,
        )
        if args.check_only:
            print(json.dumps({"disposition": "bounds-ok", "certification": False, **spec}))
            return 0
        require_credentials(os.environ)
        if not args.out:
            raise PromotionError("HARBOR_PROMOTION_OUT is required")
        code = run_case_study(spec, dry_run=args.dry_run, out_path=Path(args.out))
    except PromotionError as exc:
        write_blocked(args.out, str(exc))
        print(str(exc), file=sys.stderr)
        return exc.exit_code
    if code != 0:
        print("blocked: Harbor promotion did not pass; not a certification", file=sys.stderr)
        return code
    print(
        f"harbor promotion finished cells={spec['cells']} task={spec['task']} "
        f"dry_run={str(args.dry_run).lower()}; not statistical certification"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
