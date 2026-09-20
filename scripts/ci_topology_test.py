#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Fail if CI drifts back to heavyweight or provider-backed pull-request gates."""

from __future__ import annotations

import re
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = ROOT / ".github" / "workflows"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def jobs(text: str) -> dict[str, str]:
    lines = text.splitlines()
    start = next((index for index, line in enumerate(lines) if line == "jobs:"), None)
    require(start is not None, "workflow is missing jobs:")
    found: dict[str, list[str]] = {}
    current: str | None = None
    for line in lines[start + 1 :]:
        if re.fullmatch(r"  [A-Za-z0-9_-]+:", line):
            current = line.strip()[:-1]
            found[current] = []
            continue
        if current is not None:
            found[current].append(line)
    return {name: "\n".join(body) for name, body in found.items()}


def steps(job_text: str) -> list[str]:
    return re.split(r"\n      - ", "\n" + job_text)


def make_rule(text: str, name: str) -> str:
    lines = text.splitlines()
    start = next((index for index, line in enumerate(lines) if line.startswith(f"{name}:")), None)
    require(start is not None, f"Makefile is missing {name}")
    body = [lines[start]]
    for line in lines[start + 1 :]:
        if line.startswith("\t") or line.startswith("#") or line == "":
            body.append(line)
            continue
        break
    return "\n".join(body)


def assert_pull_request_cancellation() -> None:
    for path in sorted(WORKFLOWS.glob("*.yml")):
        text = read(path)
        if not re.search(r"(?m)^  pull_request:\s*$", text):
            continue
        require(
            re.search(
                r"cancel-in-progress:\s*(true|\$\{\{\s*github\.event_name\s*!=\s*'merge_group'\s*\}\})",
                text,
            ),
            f"{path.name} runs on pull_request without cancel-in-progress",
        )
        if re.search(r"(?m)^  merge_group:\s*$", text):
            require(
                "github.event_name != 'merge_group'" in text,
                f"{path.name} cancels merge_group runs and can thrash the queue",
            )


def assert_make_tiers() -> None:
    makefile = read(ROOT / "Makefile")
    fast = make_rule(makefile, "test-fast")
    full = make_rule(makefile, "test")
    require("go test ./..." in fast, "test-fast dropped Go coverage")
    require("harbor-adapter-test" in fast, "test-fast dropped the stdlib Harbor contract")
    require("api-compat-mock" not in fast, "test-fast regained api-compat-mock")
    require("harbor-local" not in fast, "test-fast regained harbor-local")
    require("capability-smoke-unit" not in fast.splitlines()[0], "test-fast repeats focused Go tests")
    require("test-fast" in full.splitlines()[0] and "secret-check" in full.splitlines()[0], "test dropped a tier")
    require("api-compat-mock" in full and "harbor-local" in full, "full suite dropped an offline target")
    require(make_rule(makefile, "test-full").splitlines()[0] == "test-full: test", "test-full must alias test")


def assert_fast_go_workflow() -> None:
    text = read(WORKFLOWS / "go.yml")
    parsed = jobs(text)
    require("merge_group:" in text, "go.yml must report on merge groups")
    step = next(item for item in steps(parsed["test-and-vet"]) if "run: make test-fast" in item)
    require("github.event_name != 'merge_group'" in step, "merge queue would rerun test-fast beside test-full")
    require("\n        run: make test\n" not in text, "go.yml must not run the full suite")


def assert_deterministic_gate() -> None:
    text = read(WORKFLOWS / "deterministic-gate.yml")
    require("merge_group:" in text and "workflow_dispatch:" in text, "deterministic gate triggers are incomplete")
    require("schedule:" not in text, "deterministic gate must not be scheduled")
    parsed = jobs(text)
    body = parsed["test-full"]
    for marker in ("run: make test", "make docs-qa", "scripts/ci_lrp_synthetic.sh", "anchore/sbom-action"):
        matched = [step for step in steps(body) if marker in step]
        require(matched, f"deterministic gate missing {marker}")
        for step in matched:
            require(
                "github.event_name != 'pull_request'" in step,
                f"{marker} can run on pull_request",
            )
    require("Defer full gate until merge queue" in body, "pull requests have no explicit full-gate deferral")


def assert_provider_workflows() -> None:
    security = read(WORKFLOWS / "security-evidence.yml")
    require("anchore/sbom-action" not in security, "SBOM returned to every pull request")

    live = jobs(read(WORKFLOWS / "api-compat-live.yml"))
    require("secrets." not in live["live-unit"], "pull-request API compat job gained secrets")
    require("github.event_name == 'workflow_dispatch'" in live["live"], "live API compat is not manual")
    require("environment: evaluation" in live["live"], "live API compat left the protected environment")
    require("make api-compat-live" in live["live"], "manual lane does not run the fail-closed live target")
    require("--ci-report" not in live["live"], "manual lane can exit 0 without running")
    require("schedule:" not in read(WORKFLOWS / "api-compat-live.yml"), "live API compat is scheduled")

    inspect = jobs(read(WORKFLOWS / "inspect-coding-evaluation.yml"))
    require("secrets." not in inspect["contract"], "Inspect pull-request job gained secrets")
    require("eval-ci-smoke" not in inspect["contract"] and "eval-ci-full" not in inspect["contract"], "Inspect pull request runs a live suite")
    require("environment: evaluation" in inspect["smoke"], "Inspect smoke left the protected environment")
    require("environment: evaluation" in inspect["full-or-promotion"], "Inspect full lane left the protected environment")
    require("schedule:" not in read(WORKFLOWS / "inspect-coding-evaluation.yml"), "Inspect provider lane is scheduled")

    harbor_text = read(WORKFLOWS / "harbor-promotion.yml")
    require("pull_request:" not in harbor_text and "schedule:" not in harbor_text, "Harbor promotion is not manual-only")
    harbor = jobs(harbor_text)
    require("environment: evaluation" in harbor["harbor"], "Harbor promotion left the protected environment")
    require("timeout-minutes: 90" in harbor["harbor"], "Harbor promotion has no workflow timeout")
    require("cancel-in-progress: false" in harbor_text, "Harbor promotion can cancel an in-flight paid run")

    lrp = read(WORKFLOWS / "lrp.yml")
    paths = lrp.split("pull_request:", 1)[1].split("permissions:", 1)[0]
    require("internal/router" not in paths and "Makefile" not in paths, "LRP rootfs still triggers on unrelated PRs")
    require("scripts/ci_lrp_synthetic.sh" in lrp, "LRP workflow drifted from the shared script")


def main() -> int:
    assert_pull_request_cancellation()
    assert_make_tiers()
    assert_fast_go_workflow()
    assert_deterministic_gate()
    assert_provider_workflows()
    script = ROOT / "scripts" / "ci_lrp_synthetic.sh"
    syntax = subprocess.run(["bash", "-n", str(script)], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    require(syntax.returncode == 0, f"LRP CI script failed bash -n:\n{syntax.stderr}")
    print("CI topology contract passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
