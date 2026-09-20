#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Fail if tracked env examples contain live-looking secrets."""

from __future__ import annotations

import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Iterable


ROOT = Path(__file__).resolve().parents[1]

LIVE_SECRET_PATTERNS = [
    re.compile(r"\bsk-ant-[A-Za-z0-9_-]{20,}\b"),
    re.compile(r"\bsk-or-v1-[A-Za-z0-9_-]{20,}\b"),
    re.compile(r"\bsk_live_[A-Za-z0-9]{20,}\b"),
    re.compile(r"\bsk_test_[A-Za-z0-9]{20,}\b"),
    re.compile(r"\bpk_live_[A-Za-z0-9]{20,}\b"),
    re.compile(r"\bpk_test_[A-Za-z0-9]{20,}\b"),
    re.compile(r"\bwhsec_[A-Za-z0-9_-]{20,}\b"),
    re.compile(r"\bsk-[A-Za-z0-9_-]{20,}\b"),
    re.compile(r"\bxai-[A-Za-z0-9_-]{20,}\b"),
    re.compile(r"\brtr_metrum_[A-Za-z0-9_-]{20,}\b"),
    re.compile(r"\bgh[pousr]_[A-Za-z0-9_]{20,}\b"),
    re.compile(r"\b[A-Fa-f0-9]{64}\b"),
]

# Local operator credential files that must remain untracked and out of Docker context.
LOCAL_CREDENTIAL_IGNORE_PATHS = (
    "commerce.env.json",
    "nested/commerce.env.json",
    "env.json",
)

SECRET_KEY_RE = re.compile(r"(API_KEY|TOKEN|SECRET|PASSWORD|PRIVATE_KEY)$|^ROUTER_ID_TRANSFORM_KEY")
PLACEHOLDER_RE = re.compile(
    r"^(?:REPLACE(?:_WITH)?(?:[_-][A-Z0-9_-]+)?|YOUR[_-][A-Z0-9_-]+|EXAMPLE[_-][A-Z0-9_-]+|DUMMY[_-][A-Z0-9_-]+|PLACEHOLDER|CHANGEME|<[^>]+>)$",
    re.IGNORECASE,
)

RETIRED_AGENT_CREDENTIAL_PATHS = (
    ".metrum-agents/local/profile.json",
    ".metrum-agents/runtime/state.json",
    ".metrum-agents/workspace/env.json",
    ".metrum-agents/workspace/provider-secret.txt",
    ".metrum-agents/workspace/router-token.txt",
    ".metrum-agents/workspace/credential.json",
)


def tracked_env_examples() -> list[Path]:
    try:
        result = subprocess.run(
            ["git", "ls-files", "*env.example.json"],
            cwd=ROOT,
            check=True,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
    except (OSError, subprocess.CalledProcessError) as exc:
        raise SystemExit(f"unable to list tracked env examples: {exc}") from exc
    return [ROOT / line for line in result.stdout.splitlines() if line]


def secret_key_errors(path: Path, data: object) -> Iterable[str]:
    if not isinstance(data, dict):
        yield f"{path.relative_to(ROOT)} must contain a JSON object"
        return
    for key, value in data.items():
        if not SECRET_KEY_RE.search(str(key)):
            continue
        if not isinstance(value, str):
            yield f"{path.relative_to(ROOT)}:{key} must be a string placeholder"
            continue
        if value == "":
            continue
        if PLACEHOLDER_RE.search(value):
            continue
        yield f"{path.relative_to(ROOT)}:{key} must be empty or a placeholder, not a concrete value"


def live_pattern_errors(path: Path, text: str) -> Iterable[str]:
    for pattern in LIVE_SECRET_PATTERNS:
        if pattern.search(text):
            yield f"{path.relative_to(ROOT)} contains a live-looking secret pattern: {pattern.pattern}"


def retired_agent_ignore_errors() -> Iterable[str]:
    for path in RETIRED_AGENT_CREDENTIAL_PATHS:
        try:
            result = subprocess.run(
                ["git", "check-ignore", "--no-index", "--quiet", "--", path],
                cwd=ROOT,
                check=False,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                text=True,
            )
        except OSError as exc:
            yield f"unable to verify retired credential path {path}: {exc}"
            continue
        if result.returncode == 1:
            yield f"retired credential path is not ignored: {path}"
        elif result.returncode != 0:
            detail = result.stderr.strip() or f"git check-ignore exited {result.returncode}"
            yield f"unable to verify retired credential path {path}: {detail}"


def local_credential_ignore_errors() -> Iterable[str]:
    for path in LOCAL_CREDENTIAL_IGNORE_PATHS:
        try:
            result = subprocess.run(
                ["git", "check-ignore", "--no-index", "--quiet", "--", path],
                cwd=ROOT,
                check=False,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                text=True,
            )
        except OSError as exc:
            yield f"unable to verify local credential path {path}: {exc}"
            continue
        if result.returncode == 1:
            yield f"local credential path is not ignored: {path}"
        elif result.returncode != 0:
            detail = result.stderr.strip() or f"git check-ignore exited {result.returncode}"
            yield f"unable to verify local credential path {path}: {detail}"


def main() -> int:
    paths = tracked_env_examples()
    if not paths:
        print("no tracked env.example.json files found", file=sys.stderr)
        return 1

    errors: list[str] = []
    for path in paths:
        text = path.read_text(encoding="utf-8")
        errors.extend(live_pattern_errors(path, text))
        try:
            data = json.loads(text)
        except json.JSONDecodeError as exc:
            errors.append(f"{path.relative_to(ROOT)} is not valid JSON: {exc}")
            continue
        errors.extend(secret_key_errors(path, data))
    errors.extend(retired_agent_ignore_errors())
    errors.extend(local_credential_ignore_errors())
    # Service fixtures are public too. Content-bearing runtime artifacts must
    # remain outside the product tree; scan tracked service sources and fixtures.
    service_files = subprocess.run(
        ["git", "ls-files", "services/"], cwd=ROOT, check=True, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    for relative in service_files.stdout.splitlines():
        path = ROOT / relative
        if path.suffix in {".onnx", ".parquet", ".sqlite", ".db"}:
            errors.append(f"{relative} is a protected runtime artifact")
            continue
        # Lockfiles legitimately contain integrity hashes. Other public service
        # files still receive provider/token pattern checks, without printing hits.
        if path.suffix == ".lock":
            continue
        try:
            content = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            errors.append(f"{relative} is an unexpected binary service artifact")
            continue
        for pattern in LIVE_SECRET_PATTERNS[:-1]:
            if pattern.search(content):
                errors.append(f"{relative} contains a live-looking credential pattern")
        if path.suffix == ".ndjson":
            for line in content.splitlines():
                try:
                    row = json.loads(line)
                except json.JSONDecodeError:
                    errors.append(f"{relative} contains invalid NDJSON")
                    break
                if row.get("source") != "synthetic":
                    errors.append(f"{relative} contains a non-synthetic fixture")
                    break

    if errors:
        print("env example secret check failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print(f"checked {len(paths)} tracked env example file(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
