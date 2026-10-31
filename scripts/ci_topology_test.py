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


def assert_go_timeout_headroom(text: str, *, make_recipe: bool = False) -> None:
    # Only internal/router gets temporary shared-CPU headroom (Refs #367), and
    # it must run under the time guard so a slow package cannot reappear
    # silently (Refs #367).
    lines = [line.strip() for line in text.splitlines()]
    other_packages = "go test $(go list ./... | grep -v '/internal/router$') -timeout=15m"
    if make_recipe:
        other_packages = other_packages.replace("$", "$$")
        require([line for line in lines if line.startswith("go test ")] == [other_packages],
                "Go coverage/timeouts drifted: all non-router packages retain 15m")
        guard = [line for line in lines if "router_test_time_guard.py" in line]
        require(len(guard) == 1 and "pipefail" in guard[0]
                and "go test ./internal/router -timeout=30m -json" in guard[0],
                "internal/router must keep 30m under the time guard in the Makefile")
    else:
        require([line for line in lines if line.startswith("go test ")] == [
            other_packages,
            "go test ./internal/router -timeout=30m -json | python3 scripts/router_test_time_guard.py",
        ], "Go coverage/timeouts drifted: only internal/router may use 30m, under the time guard; all other packages retain 15m")
    require("shared-CPU" in text and "dedicated c-8" in text and "Refs #367" in text,
            "temporary router timeout must retain its capacity/revert explanation")


def assert_make_tiers() -> None:
    makefile = read(ROOT / "Makefile")
    fast = make_rule(makefile, "test-fast")
    full = make_rule(makefile, "test")
    python_lane = make_rule(makefile, "ci-python")
    require("$(MAKE) validate-build-metadata" in python_lane,
            "consolidated CI must validate build metadata before the merge group")
    require("python3 scripts/check_source_headers.py" in python_lane,
            "consolidated CI must validate source headers")
    assert_go_timeout_headroom(fast, make_recipe=True)
    require("harbor-adapter-test" in fast, "test-fast dropped the stdlib Harbor contract")
    require("scripts/router_test_time_guard_test.py" in fast, "test-fast dropped the time-guard regression")
    require("scripts/release_provenance_test.py" in fast, "test-fast dropped the release provenance contract")
    require("api-compat-mock" not in fast, "test-fast regained api-compat-mock")
    require("harbor-local" not in fast, "test-fast regained harbor-local")
    require("capability-smoke-unit" not in fast.splitlines()[0], "test-fast repeats focused Go tests")
    require("test-fast" in full.splitlines()[0] and "secret-check" in full.splitlines()[0], "test dropped a tier")
    require("api-compat-mock" in full and "harbor-local" in full, "full suite dropped an offline target")
    require(make_rule(makefile, "test-full").splitlines()[0] == "test-full: test", "test-full must alias test")
    ts = make_rule(makefile, "api-compat-ts")
    require("GOTOOLCHAIN=local go mod download" in ts and "tests/api_compat_ts/run_suite.py" in ts
            and ts.index("go mod download") < ts.index("tests/api_compat_ts/run_suite.py"),
            "TypeScript suite must provision Go modules before its offline build")
    runner = read(ROOT / "tests/api_compat_ts/run_suite.py")
    require('"GOPROXY": "off"' in runner and '"GOSUMDB": "off"' in runner,
            "TypeScript runner must retain its fail-closed offline build")


def assert_retirement_topology() -> None:
    for name in ("go.yml", "deterministic-gate.yml", "case-study-smoke.yml"):
        require(not (WORKFLOWS / name).exists(), f"retired workflow returned: {name}")
    ci = read(WORKFLOWS / "ci.yml")
    require("pull_request:" in ci and "merge_group:" in ci and "run: make ci" in ci,
            "consolidated ci must cover PR and merge group")
    makefile = read(ROOT / "Makefile")
    for lane in ("ci-go", "ci-router", "ci-windows", "ci-python", "ci-examples", "compat-mock"):
        require(f"{lane}:" in makefile and lane in makefile.split("CI_LANES :=", 1)[1].splitlines()[0],
                f"missing consolidated lane {lane}")
    examples = make_rule(makefile, "ci-examples")
    for directory in ("common", "support-agent", "document-qa", "coding-agent"):
        require(f"examples/case-studies/{directory}" in examples, f"missing case-study tests: {directory}")
    text = read(WORKFLOWS / "supplemental-checks.yml")
    require("merge_group:" in text and "workflow_dispatch:" in text and "pull_request:" not in text,
            "supplemental checks must run on merge groups and dispatch")
    parsed = jobs(text)
    coverage = {
        "contracts": ("make secret-check", "scripts/api_compat_bootstrap_test.py", "make docs-qa", "tsc --noEmit"),
        "api-compat": ("make api-compat-mock api-compat-ts",),
        "harbor": ("make harbor-local",),
        "d3": ("make d3-policy-e2e",),
        "lrp": ("scripts/ci_lrp_synthetic.sh", "needs.changes.outputs.lrp"),
        "sbom": ("anchore/sbom-action", "anchore/scan-action", "actions/upload-artifact"),
        "changes": ("dorny/paths-filter@", "github.event.merge_group.base_sha", "github.event.merge_group.head_sha"),
    }
    require(set(parsed) == set(coverage), "supplemental lane inventory drifted")
    for lane, markers in coverage.items():
        for marker in markers:
            require(marker in parsed[lane], f"{lane} lost {marker}")
    codeql = read(WORKFLOWS / "codeql.yml")
    require("merge_group:" in codeql and "schedule:" in codeql and "pull_request:" not in codeql,
            "advanced CodeQL must run on merge groups and schedule, not PRs")
    require("vars.CODEQL_ADVANCED_ENABLED == 'true'" in codeql,
            "CodeQL activation must remain coordinated with default setup")
    require("github/codeql-action/init@" in codeql and "github/codeql-action/analyze@" in codeql,
            "CodeQL analysis is incomplete")
    require((WORKFLOWS / "dco.yml").exists(), "DCO workflow removed")
    headers = read(WORKFLOWS / "source-headers.yml")
    require("push:" in headers and "pull_request:" not in headers and "merge_group:" not in headers,
            "source-header workflow must retain main push without duplicating ci")
    dependabot = read(ROOT / ".github" / "dependabot.yml")
    ecosystems = re.findall(r"(?m)^  - package-ecosystem:", dependabot)
    limits = re.findall(r"(?m)^    open-pull-requests-limit:\s*(\d+)\s*$", dependabot)
    require(bool(ecosystems) and len(ecosystems) == len(limits) and all(limit == "0" for limit in limits),
            "version-update PRs must remain paused in every Dependabot ecosystem")


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


def assert_agent_chain_workflow() -> None:
    text = read(WORKFLOWS / "agent-chain-nightly.yml")
    require("schedule:" in text and "workflow_dispatch:" in text,
            "agent chain mock needs nightly and manual triggers")
    require("secrets." not in text and "make agent-chain-mock" in text,
            "agent chain nightly must stay mock-only and credential-free")
    require("if: always()" in text and "chain-matrix.json" in text,
            "agent chain nightly must retain per-cell results on failure")


def assert_arc_runners() -> None:
    for path in sorted(WORKFLOWS.glob("*.yml")):
        text = read(path)
        if path.name == "runner-cache-trim.yml":
            require("runs-on: [self-hosted, Linux, X64, router," in text,
                    "pikachu cache maintenance must stay on its original host")
            continue
        for name, body in jobs(text).items():
            if ((path.name == "agent-tool-compat.yml" and name == "codex-workspace-sandbox") or
                    (path.name == "agent-chain-nightly.yml" and name == "codex-chain-sandbox")):
                # Codex needs unprivileged namespaces absent from ARC. These VM
                # jobs are credential-free and execute only local mock fixtures.
                require(re.search(r"(?m)^    runs-on: ubuntu-22\.04$", body) is not None,
                        "Codex sandbox must use the VM runner reserved for this test")
                require("run_codex_chain.py" in body and "secrets." not in body,
                        "Codex VM exception is only for the offline sandbox matrix")
                if path.name == "agent-tool-compat.yml":
                    require("make agent-tool-mock-codex" in body, "Codex PR smoke was removed")
                continue
            require(re.search(r"(?m)^    runs-on: router-arc$", body) is not None,
                    f"{path.name}/{name} must use router-arc")
        require("cache: false" not in text, f"{path.name} must not rely on warm host Go caches")
        require("/home/" not in text, f"{path.name} must not hardcode a host HOME")


def main() -> int:
    assert_arc_runners()
    assert_pull_request_cancellation()
    assert_make_tiers()
    assert_retirement_topology()
    assert_provider_workflows()
    assert_agent_chain_workflow()
    script = ROOT / "scripts" / "ci_lrp_synthetic.sh"
    syntax = subprocess.run(["bash", "-n", str(script)], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    require(syntax.returncode == 0, f"LRP CI script failed bash -n:\n{syntax.stderr}")
    body = script.read_text(encoding="utf-8")
    require("docker info" in body, "LRP CI script must probe host Docker as optional tooling")
    require("skipped_optional" in body or "docker unavailable" in body, "LRP CI script must tolerate missing Docker")
    print("CI topology contract passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
