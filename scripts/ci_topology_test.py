#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Fail if CI drifts back to heavyweight or provider-backed pull-request gates."""

from __future__ import annotations

import json
import os
import re
import subprocess
import tempfile
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
    require("$(MAKE) validate-build-metadata" in make_rule(makefile, "ci-python"),
            "consolidated CI must validate build metadata before the merge group")
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


def assert_fast_go_workflow() -> None:
    text = read(WORKFLOWS / "go.yml")
    parsed = jobs(text)
    require("merge_group:" in text, "go.yml must report on merge groups")
    require("if: github.event_name != 'merge_group'" in parsed["test-and-vet"], "PR go job must skip merge groups")
    require(any("run: make test-fast" in item for item in steps(parsed["test-and-vet"])), "PR go job dropped test-fast")
    require("astral-sh/setup-uv@" in parsed["test-and-vet"], "PR go job must install uv for harbor-adapter and api-compat bootstrap")
    merge = parsed["test-and-vet-merge-group"]
    require("if: github.event_name == 'merge_group'" in merge, "merge-group go job missing")
    require("name: test-and-vet" in merge, "merge-group go job must keep the required check name")
    require(
        "actions/setup-go" not in merge
        and "actions/setup-python" not in merge
        and "setup-uv" not in merge,
        "merge-group go job still downloads actions",
    )
    require("\n        run: make test\n" not in text, "go.yml must not run the full suite")


def assert_deterministic_gate() -> None:
    text = read(WORKFLOWS / "deterministic-gate.yml")
    require("merge_group:" in text and "workflow_dispatch:" in text, "deterministic gate triggers are incomplete")
    require("schedule:" not in text, "deterministic gate must not be scheduled")
    parsed = jobs(text)
    coverage = {
        "go": ("gofmt -l .", "go vet ./...", "go test ./internal/router"),
        "contracts": ("make secret-check capability-smoke-contracts d3-policy-test harbor-adapter-test",
                      "scripts/outcome_calibrated_policy_test.py", "scripts/api_compat_bootstrap_test.py",
                      "scripts/ci_topology_test.py", "scripts/harbor_promotion_test.py",
                      "scripts/release_provenance_test.py",
                      "make docs-qa", "tsc --noEmit"),
        "api-compat": ("make api-compat-mock", "make api-compat-ts"),
        "harbor": ("make harbor-local",),
        "d3": ("make d3-policy-e2e",),
        "lrp": ("scripts/ci_lrp_synthetic.sh",),
        "sbom": ("anchore/sbom-action", "anchore/scan-action", "actions/upload-artifact"),
    }
    require(set(parsed) == set(coverage) | {"changes", "test-full"}, "gate lane inventory drifted")
    assert_go_timeout_headroom(parsed["go"])
    for lane, markers in coverage.items():
        for marker in markers:
            matched = [step for step in steps(parsed[lane]) if marker in step]
            require(len(matched) == 1, f"{lane} must run {marker} exactly once")
            require("github.event_name != 'pull_request'" in matched[0], f"{marker} can run on pull_request")
    for lane, body in parsed.items():
        require("Defer full gate until merge queue" in body, f"{lane} has no PR deferral")
    aggregate = parsed["test-full"]
    require("name: test-full" in aggregate and "always()" in aggregate, "required aggregate context drifted")
    for lane in coverage.keys() | {"changes"}:
        require(f"- {lane}" in aggregate, f"aggregate does not need {lane}")
    require("name == 'lrp' and job['result'] == 'skipped'" in aggregate,
            "only a filtered LRP skip may pass the aggregate")
    shell = aggregate.split("        run: |-\n", 1)[1]
    shell = "\n".join(line[10:] for line in shell.splitlines())
    for lane in coverage.keys() | {"changes"}:
        for result in ("success", "skipped", "failure", "cancelled"):
            results = {name: {"result": "success"} for name in coverage.keys() | {"changes"}}
            results[lane]["result"] = result
            check = subprocess.run(["bash", "-c", shell], capture_output=True, text=True,
                                   env={**os.environ, "RESULTS": json.dumps(results)}, check=False)
            expected = 0 if result == "success" or (lane == "lrp" and result == "skipped") else 1
            require(check.returncode == expected, f"aggregate accepted {lane}={result}: {check.stderr}")
    versions = {"actions/checkout": "v7.0.1", "actions/setup-go": "v7.0.0",
                "actions/setup-python": "v7.0.0", "actions/setup-node": "v7.1.0",
                "astral-sh/setup-uv": "v10.3.0", "dorny/paths-filter": "v4.0.3"}
    for line in text.splitlines():
        for action, version in versions.items():
            if f"uses: {action}@" in line:
                require(re.search(rf"@\w{{40}} # {re.escape(version)}$", line) is not None,
                        f"missing pinned version comment: {line}")
    require(text.count("runs-on: router-arc") == len(parsed),
            "gate lanes must use the repository-restricted ARC scale set")
    require("needs.changes.outputs.lrp" in parsed["lrp"], "LRP is not path-filtered")
    changes = parsed["changes"]
    for marker in ("dorny/paths-filter@", "github.event.merge_group.base_sha",
                   "github.event.merge_group.head_sha", "workflow_dispatch",
                   "services/learned-routing-policy/**", "scripts/ci_lrp_synthetic.sh"):
        require(marker in changes, f"LRP filter missing {marker}")


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
        if "cache: false" in text:
            require(path.name == "ci.yml" and "actions/cache/restore@" in text
                    and "actions/cache/save@" in text,
                    f"{path.name} must use explicit Actions caches, not warm host caches")
        require("/home/" not in text, f"{path.name} must not hardcode a host HOME")


def assert_consolidated_ci() -> None:
    text = read(WORKFLOWS / "ci.yml")
    lanes = jobs(text)
    require(set(lanes) == {"router", "other-go", "other", "warm", "ci"}, "fanout lane inventory drifted")
    require("push:\n    branches: [main]" in text and "if: github.event_name == 'push'" in lanes["warm"],
            "main-only cache warmer missing")
    require("fail-fast: false" in lanes["router"] and "--shard-count 6" in lanes["router"]
            and "shard: [0, 1, 2, 3, 4, 5]" in lanes["router"],
            "router shard inventory drifted")
    for name in ("router", "other-go", "other"):
        require("if: github.event_name != 'push'" in lanes[name]
                and "scripts/ci_changes.py" in lanes[name] and "actions/setup-go@" in lanes[name]
                and "cache: false" in lanes[name] and "actions/cache/restore@" in lanes[name]
                and "actions/cache/save@" not in lanes[name]
                and "hashFiles('go.mod', 'go.sum')" in lanes[name] and "restore-keys:" in lanes[name]
                and "steps.go-paths.outputs.build" in lanes[name]
                and "steps.go-paths.outputs.modules" in lanes[name]
                and "GOCACHE=$RUNNER_TEMP/router-go-build" in lanes[name]
                and "GOMODCACHE=$RUNNER_TEMP/router-go-mod" in lanes[name],
                f"{name} lost selection or isolated Go cache")
    require("ci-go ci-windows" in lanes["other-go"] and "ci-python compat-mock" in lanes["other"]
            and "ci-examples" in lanes["other"], "fanout dropped a consolidated lane")
    for name in ("other", "warm"):
        require("${{ env.COMPAT_CACHE }}" in lanes[name] and "COMPAT_CACHE=$RUNNER_TEMP/router-compat" in lanes[name]
                and "scripts/ci_tester_pins.py" in lanes[name]
                and "steps.pins.outputs.openai" in lanes[name] and "steps.pins.outputs.anthropic" in lanes[name],
                f"{name} missing commit-keyed tester cache")
    require("actions/setup-node@" not in text, "Node setup returned to lanes that do not need it")
    for name in ("router", "other-go", "other", "warm"):
        setup = next((step for step in steps(lanes[name]) if step.startswith("name: Isolate job caches")), None)
        require(setup is not None, f"{name} missing job-local cache setup")
        command = setup.split("        run: |\n", 1)[1]
        command = "\n".join(line[10:] for line in command.splitlines())
        with tempfile.TemporaryDirectory() as temp:
            env_file = Path(temp) / "env"
            check = subprocess.run(["bash", "-c", command], capture_output=True, text=True,
                                   env={**os.environ, "RUNNER_TEMP": temp, "GITHUB_ENV": str(env_file)}, check=False)
            require(check.returncode == 0 and set(env_file.read_text().splitlines()) == {
                f"GOCACHE={temp}/router-go-build", f"GOMODCACHE={temp}/router-go-mod",
                f"COMPAT_CACHE={temp}/router-compat"}, f"{name} cache isolation failed")
    require("actions/cache/save@" in lanes["warm"] and "actions/cache/restore@" in lanes["warm"]
            and "--shard-count 6 --shard-index" in lanes["warm"] and "ci-go ci-windows" in lanes["warm"],
            "trusted main cache warming is incomplete")
    pins = subprocess.run(["python3", str(ROOT / "scripts/ci_tester_pins.py")],
                          capture_output=True, text=True, check=True).stdout.splitlines()
    require(len(pins) == 2 and all(re.fullmatch(r"(?:openai|anthropic)=[0-9a-f]{40}", p) for p in pins),
            "tester cache keys must use full commit pins")
    aggregate = lanes["ci"]
    require("name: ci" in aggregate and "if: always()" in aggregate
            and "needs: [router, other-go, other, warm]" in aggregate, "required ci aggregator drifted")
    shell = aggregate.split("        run: |\n", 1)[1]
    shell = "\n".join(line[10:] for line in shell.splitlines())
    names = ("ROUTER_RESULT", "OTHER_GO_RESULT", "OTHER_RESULT", "WARM_RESULT")
    for event in ("pull_request", "merge_group", "workflow_dispatch", "push"):
        expected = {n: ("success" if (n == "WARM_RESULT") == (event == "push") else "skipped") for n in names}
        for name in names:
            for result in ("success", "failure", "cancelled", "skipped"):
                env = {**os.environ, "CI_EVENT": event, **expected, name: result}
                check = subprocess.run(["bash", "-c", shell], capture_output=True, text=True, env=env, check=False)
                require((check.returncode == 0) == (result == expected[name]),
                        f"ci accepted {event} {name}={result}")


def main() -> int:
    assert_consolidated_ci()
    assert_arc_runners()
    assert_pull_request_cancellation()
    assert_make_tiers()
    assert_fast_go_workflow()
    assert_deterministic_gate()
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
