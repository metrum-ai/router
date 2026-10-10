# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import pytest

from harness.paths import TASK_DIR_BY_ID, TASK_IDS
from harness.runner import evaluate_mode


@pytest.mark.parametrize("task_id", TASK_IDS)
def test_task_layout_present(task_id: str):
    task_dir = TASK_DIR_BY_ID[task_id]
    assert (task_dir / "task.toml").is_file()
    assert (task_dir / "instruction.md").is_file()
    assert (task_dir / "environment" / "Dockerfile").is_file()
    assert (task_dir / "environment" / "workspace").is_dir()
    assert (task_dir / "tests" / "verify.py").is_file()
    assert (task_dir / "solution" / "apply.py").is_file()


@pytest.mark.parametrize("task_id", TASK_IDS)
def test_reference_passes_and_starter_fails(task_id: str):
    solution = evaluate_mode(task_id, "solution")
    assert solution.passed is True, f"{task_id} solution failed: {solution.detail}"
    assert solution.reward == 1.0

    starter = evaluate_mode(task_id, "starter")
    assert starter.passed is False, f"{task_id} starter unexpectedly passed"
    assert starter.reward == 0.0

    noop = evaluate_mode(task_id, "noop")
    assert noop.passed is False, f"{task_id} noop unexpectedly passed"


@pytest.mark.parametrize("task_id", TASK_IDS)
def test_wrong_result_fails(task_id: str):
    wrong = evaluate_mode(task_id, "wrong")
    assert wrong.passed is False, f"{task_id} wrong result unexpectedly passed: {wrong.detail}"
    assert wrong.reward == 0.0


def test_harbor01_verifier_runs_without_task_tree_and_ignores_workspace_cases(tmp_path):
    """HARBOR-01 must grade inside a container where only /tests exists (HARBOR-REAL-AGENT)."""
    import hashlib
    import json
    import shutil

    from harness.runner import run_verifier

    task_dir = TASK_DIR_BY_ID["HARBOR-01"]
    starter = task_dir / "environment" / "workspace" / "normalize.py"
    tests_copy = tmp_path / "tests"
    shutil.copytree(task_dir / "tests", tests_copy)
    verify_src = (tests_copy / "verify.py").read_text()
    assert hashlib.sha256(starter.read_bytes()).hexdigest() in verify_src
    hidden = json.loads((task_dir / "tests" / "cases.json").read_text())
    visible = json.loads((task_dir / "environment" / "workspace" / "cases.json").read_text())
    assert hidden == visible

    # Mutation: agent rewrites the visible cases to match a wrong implementation.
    workspace = tmp_path / "app"
    shutil.copytree(task_dir / "environment" / "workspace", workspace)
    (workspace / "normalize.py").write_text("def normalize(text):\n    return text\n")
    (workspace / "cases.json").write_text(
        json.dumps({"cases": [{"input": c["input"], "expected": c["input"]} for c in visible["cases"]]})
    )
    import importlib.util

    spec = importlib.util.spec_from_file_location("harbor01_verify_isolated", tests_copy / "verify.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    result = mod.verify(workspace)
    assert result["passed"] is False and result["reward"] == 0.0
    assert run_verifier("HARBOR-01", workspace).passed is False


@pytest.mark.parametrize("mode,expected", [("solution", "1.0"), ("starter", "0.0")])
def test_harbor01_test_sh_entrypoint_writes_reward_from_isolated_tests_dir(tmp_path, mode, expected):
    """Run the real test.sh from a /tests-like copy, as Harbor does (stdin python, no task tree)."""
    import os
    import shutil
    import subprocess

    from harness.runner import stage_workspace

    task_dir = TASK_DIR_BY_ID["HARBOR-01"]
    tests_copy = tmp_path / "tests"
    shutil.copytree(task_dir / "tests", tests_copy)
    workspace = stage_workspace("HARBOR-01", mode=mode)
    reward_dir = tmp_path / "logs" / "verifier"
    proc = subprocess.run(
        ["bash", str(tests_copy / "test.sh")],
        cwd=tmp_path,
        env=os.environ | {"HARBOR_WORKSPACE": str(workspace), "HARBOR_REWARD_DIR": str(reward_dir)},
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert (reward_dir / "reward.txt").read_text().strip() == expected, proc.stderr
    assert (proc.returncode == 0) is (expected == "1.0")
