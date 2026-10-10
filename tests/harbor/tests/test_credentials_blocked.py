# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

from pathlib import Path

import yaml

from harness.credentials import (
    blocked_evidence,
    has_real_agent_credentials,
    real_agent_run_disposition,
)
from harness.paths import MANIFEST_PATH, TASK_IDS


def test_missing_credentials_are_blocked_not_pass():
    empty = {}
    assert has_real_agent_credentials(empty) is False
    assert real_agent_run_disposition(empty) == "blocked"
    evidence = blocked_evidence()
    assert evidence["disposition"] == "blocked"
    assert evidence["passed"] is False
    assert evidence["reward"] is None
    assert evidence["skip_is_pass"] is False


def test_present_credentials_are_not_blocked():
    env = {"HARBOR_ROUTER_TOKEN": "synthetic-not-a-real-secret"}
    assert has_real_agent_credentials(env) is True
    assert real_agent_run_disposition(env) == "native_supported"


def test_manifest_lists_harbor_p0_cases():
    raw = yaml.safe_load(MANIFEST_PATH.read_text(encoding="utf-8"))
    by_id = {case["id"]: case for case in raw["cases"]}
    for task_id in TASK_IDS:
        assert task_id in by_id
        assert by_id[task_id]["priority"] == "P0"
        assert by_id[task_id]["disposition"] == "native_supported"
    assert by_id["HARBOR-REAL-AGENT"].get("rationale")


def test_real_agent_row_is_blocked_or_backed_by_dated_evidence():
    """A real-agent cell is green only with dated live evidence on disk; otherwise blocked."""
    root = Path(__file__).resolve().parents[3]
    for manifest in (MANIFEST_PATH, root / "tests" / "api_compat" / "manifest" / "harbor.yaml"):
        raw = yaml.safe_load(manifest.read_text(encoding="utf-8"))
        row = {case["id"]: case for case in raw["cases"]}["HARBOR-REAL-AGENT"]
        if row["disposition"] == "blocked":
            continue
        assert row["disposition"] == "native_supported"
        dated = [p for p in row["evidence_paths"] if p.startswith(f"docs/evidence/api-compat/{row['reviewed_date']}/")]
        assert dated, f"{manifest.name}: green real-agent row needs evidence dated {row['reviewed_date']}"
        for rel in dated:
            assert (root / rel).is_file(), rel
        evidence = (root / f"docs/evidence/api-compat/{row['reviewed_date']}/harbor-real-agent.json").read_text()
        import json

        cells = [c for c in json.loads(evidence)["cells"] if c["classification"] == "certified"]
        assert {c["client"] for c in cells} >= {"pi", "claude-code", "codex"}
        for cell in cells:
            assert cell["trials"] and all(t["reward"] == 1.0 for t in cell["trials"])
            assert cell["router_usage"]["router_requests"] > 0


def test_api_compat_mirror_manifest_present():
    mirror = (
        Path(__file__).resolve().parents[2]
        / "api_compat"
        / "manifest"
        / "harbor.yaml"
    )
    assert mirror.is_file()
    raw = yaml.safe_load(mirror.read_text(encoding="utf-8"))
    ids = {case["id"] for case in raw["cases"]}
    assert set(TASK_IDS).issubset(ids)
    assert "HARBOR-REAL-AGENT" in ids
