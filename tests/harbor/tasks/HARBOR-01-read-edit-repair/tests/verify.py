# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Independent verifier for HARBOR-01.

Pass requires:
  1. Hidden cases (tests/cases.json, outside the agent-editable /app) all match.
  2. Implementation differs from the starter (catches no-edit/read-loop).

Inside a Harbor container only ``/tests`` is present, so the starter fingerprint
is pinned here instead of being read from ``environment/workspace``; the offline
suite checks that the pin matches the checked-in starter. Metrum AI.
"""

from __future__ import annotations

import hashlib
import importlib.util
import json
import sys
from pathlib import Path

# sha256 of environment/workspace/normalize.py (pinned; see module docstring).
STARTER_SHA256 = "e84daa64bd7b71d15cea2c47183ae3b58874b4a75b7dec6d695fa9813176ac4b"
HIDDEN_CASES = Path(__file__).resolve().parent / "cases.json"


def _starter_fingerprint() -> str:
    return STARTER_SHA256


def _load_normalize(workspace: Path):
    path = workspace / "normalize.py"
    spec = importlib.util.spec_from_file_location("harbor01_normalize", path)
    if spec is None or spec.loader is None:
        raise ImportError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules["harbor01_normalize"] = module
    spec.loader.exec_module(module)
    return module


def verify(workspace: Path) -> dict:
    # Grade against the verifier's own copy so editing /app/cases.json cannot pass.
    cases_path = HIDDEN_CASES if HIDDEN_CASES.is_file() else workspace / "cases.json"
    cases = json.loads(cases_path.read_text(encoding="utf-8"))["cases"]
    artifact = workspace / "normalize.py"
    if not artifact.is_file():
        return {"passed": False, "reward": 0.0, "detail": "missing normalize.py"}

    current_hash = hashlib.sha256(artifact.read_bytes()).hexdigest()
    if current_hash == _starter_fingerprint():
        return {
            "passed": False,
            "reward": 0.0,
            "detail": "no-edit/read-loop: artifact identical to starter",
        }

    try:
        mod = _load_normalize(workspace)
    except Exception as exc:  # noqa: BLE001 — verifier must classify any load failure
        return {"passed": False, "reward": 0.0, "detail": f"import failed: {exc}"}

    failures = []
    for case in cases:
        got = mod.normalize(case["input"])
        if got != case["expected"]:
            failures.append({"input": case["input"], "expected": case["expected"], "got": got})
    if failures:
        return {"passed": False, "reward": 0.0, "detail": f"case failures: {failures}"}
    return {"passed": True, "reward": 1.0, "detail": "all cases passed; differs from starter"}
