#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Fail if CI drifts back to heavyweight or provider-backed pull-request gates."""

from __future__ import annotations

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
        if path.name == "ci.yml":
            require("cancel-in-progress: ${{ github.event_name != 'merge_group' && github.event_name != 'push' }}" in text,
                    "ci must cancel superseded PRs but retain in-flight merge groups and main warmers")
        else:
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
    require("release_image_layer_test.py" in fast and "release_image_binary_test.py" in fast,
            "test-fast dropped release image reproducibility and byte-identity tests")
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
    require("pull_request:" in ci and "merge_group:" in ci and "name: ci" in jobs(ci)["ci"],
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
        "generated-release-files": ("make admin-openapi-check admin-build", "git diff --exit-code"),
        "contracts": ("make secret-check", "scripts/api_compat_bootstrap_test.py", "make docs-qa", "tsc --noEmit"),
        "api-compat": ("make api-compat-mock api-compat-ts",),
        "harbor": ("make harbor-local",),
        "d3": ("make d3-policy-e2e",),
        "lrp": ("scripts/ci_lrp_synthetic.sh", "needs.changes.outputs.lrp"),
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
    expected = {
        ("gomod", "/"): "10",
        ("npm", "/docs-site"): "10",
        ("npm", "/internal/router/admindist/web"): "5",
        ("pip", "/services/learned-routing-policy"): "5",
        ("pip", "/evaluators/livecodebench"): "5",
        ("pip", "/tests/api_compat"): "5",
        ("pip", "/tests/harbor"): "5",
        ("github-actions", "/"): "5",
    }
    entries = re.split(r"(?m)^  - package-ecosystem: ", dependabot)[1:]
    actual = {}
    for entry in entries:
        ecosystem = entry.splitlines()[0].strip()
        directory = re.search(r"(?m)^    directory: (\S+)\s*$", entry)
        limit = re.search(r"(?m)^    open-pull-requests-limit: (\d+)\s*$", entry)
        require(directory is not None and limit is not None, "invalid Dependabot update entry")
        key = (ecosystem, directory.group(1))
        require(key not in actual, f"duplicate Dependabot entry: {key}")
        actual[key] = limit.group(1)
        require(re.search(r"(?m)^      interval: weekly\s*$", entry) is not None,
                f"Dependabot schedule must be weekly: {key}")
        require(re.search(r"(?m)^      default-days: 7\s*$", entry) is not None,
                f"Dependabot cooldown must be seven days: {key}")
        if ecosystem != "github-actions":
            require(re.search(r"(?m)^      semver-major-days: 14\s*$", entry) is not None,
                    f"Dependabot major cooldown must be fourteen days: {key}")
    require(actual == expected, "Dependabot update directories or limits differ from the post-release policy")


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


def assert_eval_infra_nightly_workflow() -> None:
    text = read(WORKFLOWS / "eval-infra-nightly.yml")
    triggers = text.split("permissions:", 1)[0]
    require("schedule:" in triggers and "workflow_dispatch:" in triggers,
            "evaluation nightly needs scheduled and manual triggers")
    require(all(f"{event}:" not in triggers for event in ("pull_request", "push", "merge_group")),
            "paid evaluation nightly must not run on PR, push or merge-group events")
    require("$1.50 total per workflow run and per UTC night" in text,
            "evaluation nightly must document its workflow and daily spend ceiling")
    workflow_jobs = jobs(text)
    require(set(workflow_jobs) == {"nightly", "harbor"}, "evaluation nightly must split Docker Harbor from non-Docker cells")
    job = workflow_jobs["nightly"]
    require("if: github.ref == 'refs/heads/main'" in job,
            "evaluation nightly must run only from main")
    require("environment: evaluation" in job,
            "evaluation nightly must use the protected evaluation environment")
    require("timeout-minutes: 45" in job and "cancel-in-progress: false" in text,
            "evaluation nightly must be bounded and cannot cancel a live run")
    require("contents: read" in text and "actions: read" in text,
            "evaluation nightly permissions must stay read-only")
    secret_refs = set(re.findall(r"\$\{\{\s*secrets\.([A-Za-z0-9_]+)\s*\}\}", text))
    require(secret_refs == {"COMPAT_MATRIX_OPENAI_API_KEY", "COMPAT_MATRIX_CALLER_TOKEN"},
            "evaluation nightly must expose only the two required environment secrets")
    require("artifacts/run-report.json" in text and "artifacts/rolling-aggregate.json" in text
            and "artifacts/history" not in text.split("      - name: Publish sanitized evaluation reports", 1)[1],
            "evaluation nightly artifacts must contain only the current report and aggregate")
    require("gh run download" in text and "refusing to bypass the nightly spend ceiling" in text,
            "same-day cost history must fail closed when reports cannot be retrieved")
    harbor_job = workflow_jobs["harbor"]
    require("runs-on: ubuntu-latest" in harbor_job and "docker info" in harbor_job,
            "Harbor live job must use the Docker-capable hosted runner")
    require("needs: nightly" in harbor_job and "environment: evaluation" in harbor_job,
            "Docker Harbor job must run after non-Docker work in the protected environment")
    require("HARBOR_MAX_RESERVE_USD" in harbor_job and "eval-harbor-${{ github.run_id }}" in harbor_job,
            "Docker Harbor job must enforce and publish its bounded daily reservation")
    require("harbor-venv/bin/python" in harbor_job,
            "Harbor runner must use the interpreter that installs the pinned Harbor package")
    require("prepare_catalog_tasks.py" in harbor_job and "HARBOR_CATALOG_TASKS" in harbor_job,
            "Harbor nightly must stage its bespoke live catalog tasks")
    harbor_runner = read(ROOT / "scripts" / "harbor_e2e" / "run_matrix.py")
    require(all(task_id in harbor_runner for task_id in
                ("HARBOR-02", "HARBOR-03", "HARBOR-04", "HARBOR-05", "HARBOR-06")),
            "Harbor nightly must include all bespoke v5.0.0 P0 task IDs")
    require("charged_usd" in harbor_runner and "request_usage" in harbor_runner and
            "missing_usage_rows" in harbor_runner,
            "Harbor per-cell costs must come from measured usage rows and fail closed when absent")
    require("HARBOR-02…HARBOR-06" in read(ROOT / "docs" / "EVALUATION_INFRA.md") and
            "HARBOR-07…HARBOR-18" in read(ROOT / "docs" / "EVALUATION_INFRA.md") and "#999" in
            read(ROOT / "docs" / "EVALUATION_INFRA.md"),
            "Harbor documentation must distinguish v5.0 live cells from offline stubs")


def assert_tool_compat_live_workflow() -> None:
    text = read(WORKFLOWS / "agent-tool-compat-live.yml")
    triggers = text.split("permissions:", 1)[0]
    require("schedule:" in triggers and "workflow_dispatch:" in triggers
            and all(f"{event}:" not in triggers for event in ("pull_request", "push", "merge_group")),
            "paid tool compatibility runs must be scheduled or manually dispatched only")
    job = jobs(text)["live-matrix"]
    require("if: github.ref == 'refs/heads/main'" in job and "environment: evaluation" in job
            and "contents: read" in text,
            "paid tool compatibility job must be main-only, protected, and least-privilege")
    require("if: always() && steps.gate.outcome != 'skipped'" in text
            and "if: always()" in text
            and "FRESH_AFTER" in text
            and "tmp/compat-matrix/gate.json" in text.split("      - name: Upload results", 1)[1],
            "failed live cells must still convert, ingest, and upload only this run's sanitized gate report")
    require("parallel_tool_live_router_test.py" in read(ROOT / "Makefile"),
            "real SDK -> router -> scripted upstream smoke must run in CI")


def assert_hierarchy_release_gate() -> None:
    matrix = read(WORKFLOWS / "hierarchy-matrix.yml")
    require("workflow_call:" in matrix and "workflow_dispatch:" in matrix,
            "hierarchy matrix must be callable from the release workflow and runnable alone")
    require("secrets." not in matrix, "hierarchy matrix must stay credential-free")
    require("make agent-chain-mock" in matrix and "run_codex_chain.py" in matrix,
            "hierarchy matrix lost a shipped suite")
    matrix_jobs = jobs(matrix)
    for name in ("agent-chain", "codex-chain"):
        require("scripts/hierarchy_release_gate.py" in matrix_jobs.get(name, ""),
                f"hierarchy matrix job {name} must decide with the release gate script")
    require("release-exceptions.json" in matrix and "if: always()" in matrix,
            "hierarchy matrix must review exceptions and retain per-cell results")
    release = jobs(read(WORKFLOWS / "release.yml"))
    require("uses: ./.github/workflows/hierarchy-matrix.yml" in release.get("hierarchy-matrix", ""),
            "release workflow no longer calls the hierarchy matrix")
    require("needs: hierarchy-matrix" in release.get("resolve", "")
            and "needs: resolve" in release.get("package-binaries", ""),
            "release packaging must depend on the hierarchy matrix")
    compat = read(WORKFLOWS / "release-compat.yml")
    require("agent-chain-mock" not in compat,
            "the agent chain matrix must not run twice on a tag build")


def assert_arc_runners() -> None:
    small_jobs = {
        "admin-typecheck.yml": {"admin-typecheck"},
        "agent-tool-compat.yml": {"local-agent-mock"},
        "api-compat-live.yml": {"live-unit"},
        "ci.yml": {"ci", "k8s-security", "parallel-tools", "tls-acme"},
        "dco.yml": {"signed-off-commits"},
        "decision-models-smoke.yml": {"decision-models"},
        "docs-qa.yml": {"docs-qa"},
        "examples-catalog-smoke.yml": {"tokenomics"},
        "expert-routing-smoke.yml": {"mock"},
        "ft-example.yml": {"offline"},
        "governance-example-smoke.yml": {"governance"},
        "inspect-coding-evaluation.yml": {"contract"},
        "run3-gate-smoke.yml": {"gate"},
        "run3-gpu-safety-smoke.yml": {"gpu-safety"},
        "security-evidence.yml": {"secret-scan"},
        "source-headers.yml": {"validate"},
        "supplemental-checks.yml": {"generated-release-files", "contracts", "api-compat", "harbor", "d3", "changes"},
    }
    for path in sorted(WORKFLOWS.glob("*.yml")):
        text = read(path)
        if path.name == "runner-cache-trim.yml":
            require("runs-on: [self-hosted, Linux, X64, router," in text,
                    "pikachu cache maintenance must stay on its original host")
            continue
        for name, body in jobs(text).items():
            if path.name == "eval-infra-nightly.yml" and name == "harbor":
                # Docker is part of the paid Harbor job's declared runtime;
                # keep that one job on the hosted runner with main-only,
                # protected-environment boundaries asserted separately.
                require(re.search(r"(?m)^    runs-on: ubuntu-latest$", body) is not None,
                        "the paid Harbor Docker job must use the hosted runner")
                require("environment: evaluation" in body and "if: always() && github.ref == 'refs/heads/main'" in body,
                        "the hosted Harbor exception must remain main-only and protected")
                continue
            if path.name == "ci.yml" and name == "harbor-task-images":
                # Docker builds require a daemon unavailable on the ARC runner.
                # This credential-free job only builds and smokes fixed local images.
                require(re.search(r"(?m)^    runs-on: ubuntu-latest$", body) is not None,
                        "Harbor task image smoke tests require the Docker VM runner")
                require("scripts/harbor_e2e/test_nonroot_images.sh" in body and "secrets." not in body,
                        "the Docker VM exception must remain limited to secret-free image checks")
                continue
            if ((path.name == "agent-tool-compat.yml" and name == "codex-workspace-sandbox") or
                    (path.name in ("agent-chain-nightly.yml", "hierarchy-matrix.yml") and
                     name in ("codex-chain-sandbox", "codex-chain")) or
                    (path.name == "release-dry-run.yml" and name == "hierarchy-codex-chain")):
                # Codex needs unprivileged namespaces absent from ARC. These VM
                # jobs are credential-free and execute only local mock fixtures.
                require(re.search(r"(?m)^    runs-on: ubuntu-22\.04$", body) is not None,
                        "Codex sandbox must use the VM runner reserved for this test")
                require("run_codex_chain.py" in body and "secrets." not in body,
                        "Codex VM exception is only for the offline sandbox matrix")
                if path.name == "agent-tool-compat.yml":
                    require("make agent-tool-mock-codex" in body, "Codex PR smoke was removed")
                continue
            if "uses: ./.github/workflows/" in body:
                # A reusable-workflow call job has no runner of its own; the
                # called workflow's jobs are checked when that file is read.
                require(name != "package", f"{path.name}/{name} must not be a reusable-workflow call")
                continue
            runner = "router-arc-small" if name in small_jobs.get(path.name, set()) else "router-arc"
            # A job may pin the label or route it through the deployment
            # variable that defaults to that same label; nothing else is
            # accepted, so a job cannot silently move to a different runner.
            variable = "ROUTER_LIGHT_RUNNER" if runner == "router-arc-small" else "ROUTER_HEAVY_RUNNER"
            pinned = re.search(rf"(?m)^    runs-on: {re.escape(runner)}$", body) is not None
            configurable = re.search(
                rf"(?m)^    runs-on: \$\{{\{{ vars\.{variable} \|\| '{re.escape(runner)}' \}}\}}$", body) is not None
            require(pinned or configurable,
                    f"{path.name}/{name} must use {runner} or the {variable} variable")
        if "cache: false" in text:
            if path.name == "release.yml":
                rebuild = jobs(text).get("reproducible-build", "")
                require("cache: false" in rebuild and "make package" in rebuild
                        and "compare_release_checksums.py" in rebuild,
                        "the clean release rebuild must disable Go cache reuse and compare checksums")
            elif path.name == "release-dry-run.yml":
                cache_actions = re.findall(r"(?m)^\s+uses: (actions/cache/(?:restore|save)@[^\s]+)(?:\s+#.*)?$", text)
                require("workflow_dispatch:" in text and "ref: main" in text
                        and cache_actions == [
                            "actions/cache/restore@55cc8345863c7cc4c66a329aec7e433d2d1c52a9",
                            "actions/cache/save@55cc8345863c7cc4c66a329aec7e433d2d1c52a9",
                        ],
                        "release dry run must check out main and use cache only for the review artifact")
            else:
                require(path.name == "ci.yml" and "actions/cache/restore@" in text
                        and "actions/cache/save@" in text,
                        f"{path.name} must use explicit Actions caches, not warm host caches")
        require("/home/" not in text, f"{path.name} must not hardcode a host HOME")


def assert_small_runner_filters() -> None:
    workflows = {
        "agent-tool-compat.yml": ("local-agent-mock", "codex-workspace-sandbox"),
        "decision-models-smoke.yml": ("decision-models",),
        "examples-catalog-smoke.yml": ("tokenomics",),
        "ft-example.yml": ("offline",),
        "governance-example-smoke.yml": ("governance",),
        "run3-gate-smoke.yml": ("gate",),
        "run3-gpu-safety-smoke.yml": ("gpu-safety",),
    }
    for filename, names in workflows.items():
        text = read(WORKFLOWS / filename)
        pr = text.split("  pull_request:", 1)[1].split("  merge_group:", 1)[0]
        require("    paths:" in pr and f".github/workflows/{filename}" in pr,
                f"{filename} must have PR path filters including itself")
        for name in names:
            body = jobs(text)[name]
            require("fetch-depth: 0" in body and "github.event.merge_group.base_sha" in body
                    and "python3 scripts/ci_changes.py" in body, f"{filename}/{name} has no merge-group classifier")
            condition = "github.event_name != 'merge_group' || env.CI_RUN_GO == '1' || env.CI_RUN_EXAMPLES == '1'"
            steps = re.split(r"(?m)^      - ", body)
            selection = next((index for index, step in enumerate(steps) if "python3 scripts/ci_changes.py" in step), -1)
            require(selection >= 0 and selection < len(steps) - 1, f"{filename}/{name} has no selected work")
            for step in steps[selection + 1:]:
                require(condition in step, f"{filename}/{name} has an ungated step after the classifier")
    supplemental = jobs(read(WORKFLOWS / "supplemental-checks.yml"))
    for name in ("api-compat", "harbor", "d3"):
        require("needs: changes" in supplemental[name] and "needs.changes.outputs.go == '1'" in supplemental[name]
                and "needs.changes.outputs.examples == '1'" in supplemental[name],
                f"supplemental {name} must skip unrelated merge groups")
    require("needs: changes" not in supplemental["contracts"], "docs and secret contracts must stay unconditional")
    dco = read(WORKFLOWS / "dco.yml")
    require(re.search(r"(?m)^  pull_request:\s*$", dco) is not None
            and "paths:" not in dco.split("  pull_request:", 1)[1].split("  merge_group:", 1)[0],
            "required DCO must not have a PR path filter")


def assert_consolidated_ci() -> None:
    text = read(WORKFLOWS / "ci.yml")
    lanes = jobs(text)
    require(set(lanes) == {"router", "other-go", "other", "sbom", "warm", "harbor-task-images", "k8s-security", "parallel-tools", "tls-acme", "admin-authz-advisories", "engine-advisory-gate", "ci"}, "fanout lane inventory drifted")
    advisory = lanes["admin-authz-advisories"]
    require("runs-on: ${{ vars.ROUTER_HEAVY_RUNNER || 'router-arc' }}" in advisory
            and "GOMAXPROCS: '2'" in advisory and "GOMEMLIMIT: 8GiB" in advisory,
            "advisory source scan must use bounded Go runtime on the heavy class")
    engine_gate = lanes["engine-advisory-gate"]
    for marker in ("name: inference-engine-advisory-gate", "scripts/engine_advisory_gate_test.py",
                   "scripts/engine_advisory_gate.py --check", "GITHUB_TOKEN: ${{ github.token }}",
                   "--require-hashes", "--hash=sha256:"):
        require(marker in engine_gate, f"required inference-engine advisory gate lost {marker}")
    require("push:\n    branches: [main]" in text and "if: github.event_name == 'push'" in lanes["warm"],
            "main-only cache warmer missing")
    require("fail-fast: false" in lanes["router"] and "--shard-count 6" in lanes["router"]
            and "shard: [0, 1, 2, 3, 4, 5]" in lanes["router"], "router shard inventory drifted")
    for name in ("router", "other-go", "other"):
        require("if: github.event_name != 'push'" in lanes[name]
                and "scripts/ci_changes.py" in lanes[name] and "actions/setup-go@" in lanes[name]
                and "cache: false" in lanes[name] and "actions/cache/restore@" in lanes[name]
                and "actions/cache/save@" not in lanes[name]
                and "hashFiles('go.mod', 'go.sum')" in lanes[name] and "restore-keys:" in lanes[name]
                and "steps.go-paths.outputs.build" in lanes[name] and "steps.go-paths.outputs.modules" in lanes[name]
                and "GOCACHE=$RUNNER_TEMP/router-go-build" in lanes[name]
                and "GOMODCACHE=$RUNNER_TEMP/router-go-mod" in lanes[name], f"{name} lost selection or isolated Go cache")
    require("ci-go ci-windows" in lanes["other-go"] and "ci-python compat-mock" in lanes["other"]
            and "ci-examples" in lanes["other"], "fanout dropped a consolidated lane")
    acme = lanes["tls-acme"]
    for marker in ("if: github.event_name != 'push'", "scripts/ci_changes.py", "actions/setup-go@",
                   "cache: false", "actions/cache/restore@", "hashFiles('go.mod', 'go.sum')",
                   "GOCACHE=$RUNNER_TEMP/router-go-build", "GOMODCACHE=$RUNNER_TEMP/router-go-mod",
                   "cmd/pebble@", "cmd/pebble-challtestsrv@", "go test ./internal/tlsacme/..."):
        require(marker in acme, f"tls-acme lane lost {marker}")
    for name in ("other", "warm"):
        require("${{ env.COMPAT_CACHE }}" in lanes[name] and "COMPAT_CACHE=$RUNNER_TEMP/router-compat" in lanes[name]
                and "scripts/ci_tester_pins.py" in lanes[name] and "steps.pins.outputs.openai" in lanes[name]
                and "steps.pins.outputs.anthropic" in lanes[name], f"{name} missing commit-keyed tester cache")
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
    restore = next((step for step in steps(lanes["warm"]) if "id: go-cache" in step), None)
    require(restore is not None and "if:" not in restore, "main must discover cache misses even on docs-only pushes")
    policy = next((step for step in steps(lanes["warm"]) if step.startswith("name: Decide Go cache warming")), None)
    require(policy is not None and "steps.go-cache.outputs.cache-matched-key" in policy,
            "main cache warming must distinguish a usable fallback from a miss")
    command = policy.split("        run: |\n", 1)[1]
    command = "\n".join(line[10:] for line in command.splitlines())
    for selected in ("0", "1"):
        for matched in ("", "ci-go-previous-main"):
            with tempfile.TemporaryDirectory() as temp:
                output = Path(temp) / "output"
                check = subprocess.run(["bash", "-c", command], capture_output=True, text=True,
                                       env={**os.environ, "CI_RUN_GO": selected, "MATCHED_KEY": matched,
                                            "GITHUB_OUTPUT": str(output)}, check=False)
                expected = "needed=true" if selected == "1" or not matched else "needed=false"
                require(check.returncode == 0 and output.read_text().strip() == expected,
                        f"cache warm policy failed: go={selected} matched={bool(matched)}")
    require("steps.warm-policy.outputs.needed == 'true'" in lanes["warm"],
            "cache warm and save must follow the bootstrap policy")
    pins = subprocess.run(["python3", str(ROOT / "scripts/ci_tester_pins.py")],
                          capture_output=True, text=True, check=True).stdout.splitlines()
    require(len(pins) == 2 and all(re.fullmatch(r"(?:openai|anthropic)=[0-9a-f]{40}", p) for p in pins),
            "tester cache keys must use full commit pins")
    sbom = lanes["sbom"]
    for marker in ("github.event_name == 'merge_group' || github.event_name == 'workflow_dispatch'",
                   "anchore/sbom-action@", "anchore/scan-action@", "severity-cutoff: high", "fail-build: true",
                   "actions/upload-artifact@"):
        require(marker in sbom, f"required SBOM lane lost {marker}")
    live_eval = read(WORKFLOWS / "eval-infra-nightly.yml")
    require('sudo chown "$(id -u):10001" "$HARBOR_E2E_CREDS/router-token"' in live_eval
            and 'chmod 0640 "$HARBOR_E2E_CREDS/router-token"' in live_eval,
            "live Harbor router token must be readable only by host owner and UID 10001 group")
    harbor_images = lanes["harbor-task-images"]
    require("runs-on: ubuntu-latest" in harbor_images
            and "scripts/harbor_e2e/test_nonroot_images.sh" in harbor_images,
            "Harbor task images must be built and smoke-tested in CI")
    security = lanes["k8s-security"]
    require("if: github.event_name != 'push'" in security
            and "scripts/render_k8s_security.sh" in security
            and "scripts/validate_trivy_k8s_ignores.py" in security
            and "scan-type: config" in security
            and "scan-ref: ${{ runner.temp }}/security-rendered" in security
            and "trivyignores: .github/trivy-k8s-ignore.yaml" in security
            and "severity: MEDIUM,HIGH,CRITICAL" in security and "exit-code: '1'" in security,
            "rendered-manifest security scan must gate every PR and merge group")
    aggregate = lanes["ci"]
    require("name: ci" in aggregate and "if: always()" in aggregate
            and "needs: [router, other-go, other, sbom, warm, harbor-task-images, k8s-security, parallel-tools, tls-acme, admin-authz-advisories, engine-advisory-gate]" in aggregate, "required ci aggregator drifted")
    parallel = lanes["parallel-tools"]
    require("if: github.event_name != 'push'" in parallel
            and "go test ./internal/stream -run '^TestParallelThreeStreamTranslations$'" in parallel
            and "go test ./internal/router -run '^TestParallelNative(UnaryContinuation|ToolControls|Streams)$'" in parallel
            and "actions/cache/restore@" in parallel and "actions/cache/save@" not in parallel,
            "parallel tool-call fixtures must run on every PR and merge group")
    shell = aggregate.split("        run: |\n", 1)[1]
    shell = "\n".join(line[10:] for line in shell.splitlines())
    names = ("ROUTER_RESULT", "OTHER_GO_RESULT", "OTHER_RESULT", "SBOM_RESULT", "WARM_RESULT", "HARBOR_IMAGES_RESULT", "K8S_SECURITY_RESULT", "PARALLEL_TOOLS_RESULT", "TLS_ACME_RESULT", "ADVISORY_RESULT", "ENGINE_ADVISORY_RESULT")
    for event in ("pull_request", "merge_group", "workflow_dispatch", "push"):
        expected = {n: ("success" if n in ("ROUTER_RESULT", "OTHER_GO_RESULT", "OTHER_RESULT", "HARBOR_IMAGES_RESULT", "K8S_SECURITY_RESULT", "PARALLEL_TOOLS_RESULT", "TLS_ACME_RESULT")
                         and event != "push" or n == "WARM_RESULT" and event == "push"
                         or n == "SBOM_RESULT" and event in ("merge_group", "workflow_dispatch")
                         or n in ("ADVISORY_RESULT", "ENGINE_ADVISORY_RESULT")
                         else "skipped") for n in names}
        for name in names:
            for result in ("success", "failure", "cancelled", "skipped"):
                env = {**os.environ, "CI_EVENT": event, **expected, name: result}
                check = subprocess.run(["bash", "-c", shell], capture_output=True, text=True, env=env, check=False)
                require((check.returncode == 0) == (result == expected[name]),
                        f"ci accepted {event} {name}={result}")


def main() -> int:
    assert_consolidated_ci()
    assert_small_runner_filters()
    assert_arc_runners()
    assert_pull_request_cancellation()
    assert_make_tiers()
    assert_retirement_topology()
    assert_provider_workflows()
    assert_agent_chain_workflow()
    assert_eval_infra_nightly_workflow()
    assert_tool_compat_live_workflow()
    assert_hierarchy_release_gate()
    release = read(WORKFLOWS / "release.yml")
    require("environment: release" in release, "release image publication must be protected by the release environment")
    require("ROUTER_DOCKER_RUNNER" not in release and "docker-runner-preflight" not in release,
            "release image publication must not require a Docker runner")
    require("index append" in release and "mutate \"$base\" --append \"$layer\"" in release and "release_image_layer.py" in release,
            "release images must be assembled daemonlessly from binary packages")
    require("packages: write" in release and "cosign sign --yes" in release,
            "protected release job must push and sign OCI images")
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
