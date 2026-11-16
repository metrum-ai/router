# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import copy
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import textwrap
import unittest

from check_admin_authz_advisories import GateError, evaluate, evaluate_inventory, validate_exceptions

CASBIN = "github.com/casbin/casbin/v2"
VERSION = "v2.135.0"
EMPTY_EXCEPTIONS = {"copyright": "Copyright 2026 Metrum AI", "license": "Apache-2.0", "schema": "router-admin-authz-advisory-exceptions-v1", "exceptions": []}


def advisory(version_range, ghsa="GHSA-test-test-test", package=CASBIN, ecosystem="Go"):
    return {"ghsa_id": ghsa, "vulnerabilities": [{"package": {"ecosystem": ecosystem, "name": package}, "vulnerable_version_range": version_range}]}


def scan(*extra, casbin_version=VERSION):
    events = [
        {"config": {"protocol_version": "v1.0.0", "scanner_name": "govulncheck", "scanner_version": "v1.9.0", "db": "https://vuln.go.dev", "scan_mode": "source", "scan_level": "symbol", "go_version": "go1.27.3"}},
        {"SBOM": {"go_version": "go1.27.3", "modules": [{"path": CASBIN, "version": casbin_version}]}},
    ]
    return "\n".join(json.dumps(event) for event in events + list(extra))


class AdvisoryGateTests(unittest.TestCase):
    def run_cli(self, affected):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            inputs = {
                "scan.json": scan(),
                "affected.json": json.dumps(affected),
                "upstream.json": "[]",
                "modules.txt": "github.com/metrum-ai/router\n" + CASBIN + " " + VERSION + "\n",
                "module-graph.txt": "github.com/metrum-ai/router " + CASBIN + "@" + VERSION + "\n",
                "exceptions.json": json.dumps(EMPTY_EXCEPTIONS),
            }
            for name, contents in inputs.items():
                (root / name).write_text(contents)
            script = Path(__file__).with_name("check_admin_authz_advisories.py")
            return subprocess.run([
                sys.executable, str(script), "--scan", str(root / "scan.json"),
                "--affected", str(root / "affected.json"), "--upstream", str(root / "upstream.json"),
                "--casbin-version", VERSION, "--modules", str(root / "modules.txt"),
                "--module-graph", str(root / "module-graph.txt"), "--exceptions", str(root / "exceptions.json"),
            ], capture_output=True, text=True)

    def test_zero_findings_requires_complete_protocol_and_pinned_module(self):
        result = evaluate(scan(), [], [], VERSION)
        self.assertEqual(result["package_or_symbol_findings"], [])
        self.assertEqual(result["module_only_findings"], [])
        self.assertEqual(result["casbin_version"], VERSION)

    def test_module_only_is_retained_not_hidden_or_mislabeled_clean_module(self):
        finding = {"finding": {"osv": "GO-2026-5932", "trace": [{"module": "golang.org/x/crypto", "version": "v0.58.0"}]}}
        result = evaluate(scan({"osv": {"id": "GO-2026-5932"}}, finding), [], [], VERSION)
        self.assertEqual(result["module_only_findings"], ["GO-2026-5932"])

    def test_imported_package_or_symbol_blocks_even_json_process_exit_zero(self):
        for frame in [{"module": CASBIN, "package": CASBIN}, {"module": CASBIN, "package": CASBIN, "function": "Enforce"}]:
            with self.subTest(frame=frame), self.assertRaises(GateError):
                evaluate(scan({"osv": {"id": "GO-2026-9999"}}, {"finding": {"osv": "GO-2026-9999", "trace": [frame]}}), [], [], VERSION)

    def test_malformed_present_scope_cannot_be_downgraded_to_module_only(self):
        scopes = [{key: ""} for key in ("package", "function", "receiver")]
        scopes += [{"position": pos} for pos in ({}, False, [], "", None, {"filename": "", "line": 0}, {"filename": "x.go", "line": -1}, {"line": 1}, {"filename": "x.go", "line": "1"})]
        for scope in scopes:
            with self.subTest(scope=scope), self.assertRaises(GateError):
                evaluate(scan({"osv": {"id": "GO-2026-9999"}}, {"finding": {"osv": "GO-2026-9999", "trace": [dict(scope, module=CASBIN)]}}), [], [], VERSION)

    def test_each_advisory_source_blocks_without_an_automatic_exception(self):
        item = advisory("< v2.136.0")
        for affected, upstream in [([item], []), ([], [item])]:
            with self.subTest(affected=affected, upstream=upstream), self.assertRaises(GateError):
                evaluate(scan(), affected, upstream, VERSION)

    def test_fixed_before_locked_version_does_not_block_and_keeps_range_evidence(self):
        item = advisory("< 2.135.0")
        result = evaluate(scan(), [item], [], VERSION)
        self.assertEqual(result["advisory_evaluations"][0]["evaluation"], "not_affected")
        self.assertEqual(result["advisory_evaluations"][0]["range"], "< 2.135.0")

    def test_future_range_does_not_block_locked_version(self):
        item = advisory(">= v2.136.0, < v2.137.0")
        self.assertEqual(evaluate(scan(), [], [item], VERSION)["upstream_advisories_returned"], 1)

    def test_unrelated_published_package_is_retained_without_blocking_casbin(self):
        item = advisory("< v2.0.0", package="github.com/casbin/gorm-adapter/v3")
        result = evaluate(scan(), [], [item], VERSION)
        self.assertEqual(result["advisory_evaluations"][0]["evaluation"], "unrelated_package")

    def test_range_boundaries_and_prerelease_ordering(self):
        self.assertFalse(evaluate(scan(), [advisory("< v2.135.0")], [], VERSION)["advisory_evaluations"][0]["evaluation"] == "affected")
        with self.assertRaises(GateError):
            evaluate(scan(), [advisory("<= v2.135.0")], [], VERSION)
        prerelease_scan = scan(casbin_version="v2.136.0-rc.1")
        with self.assertRaises(GateError) as exc_info:
            evaluate(prerelease_scan, [advisory(">= v2.136.0-0, < v2.136.0")], [], "v2.136.0-rc.1")
        self.assertEqual(exc_info.exception.report["advisory_evaluations"][0]["evaluation"], "affected")

    def test_or_ranges_are_supported(self):
        with self.assertRaises(GateError) as exc_info:
            evaluate(scan(), [advisory("< v2.134.0 || >= v2.135.0, < v2.136.0")], [], VERSION)
        self.assertEqual(exc_info.exception.report["advisory_evaluations"][0]["evaluation"], "affected")

    def test_missing_malformed_or_unsupported_relevant_ranges_block(self):
        for expression in (None, "", "~ v2.0.0", ">= v2.0.0,", "v2.0.0"):
            item = advisory(expression)
            with self.subTest(expression=expression), self.assertRaises(GateError):
                evaluate(scan(), [item], [], VERSION)

    def test_reviewed_exception_is_exact_and_recorded(self):
        exception = {"ghsa_id": "GHSA-test-test-test", "module": CASBIN, "version": VERSION, "rationale": "Reviewed mitigation", "evidence": "https://github.com/metrum-ai/router/issues/856", "revalidation": "Recheck before each release", "reviewer": "release owner", "approved_at": "2026-11-16"}
        document = dict(EMPTY_EXCEPTIONS, exceptions=[exception])
        result = evaluate(scan(), [advisory("< v2.136.0")], [], VERSION, document)
        self.assertEqual(result["advisory_evaluations"][0]["evaluation"], "reviewed_exception")
        with self.assertRaises(GateError):
            evaluate(scan(), [], [], VERSION, document)

    def test_exception_schema_rejects_broad_duplicate_or_unreviewed_entries(self):
        valid = {"ghsa_id": "GHSA-test-test-test", "module": CASBIN, "version": VERSION, "rationale": "Reviewed mitigation", "evidence": "issue reference", "revalidation": "Recheck before each release", "reviewer": "release owner", "approved_at": "2026-11-16"}
        invalid = [
            {},
            dict(EMPTY_EXCEPTIONS, exceptions=[{"ghsa_id": "GHSA-test-test-test", "module": CASBIN, "version": "*", "rationale": "x", "evidence": "x", "revalidation": "x", "reviewer": "x", "approved_at": "2026-11-16"}]),
            dict(EMPTY_EXCEPTIONS, exceptions=[{"ghsa_id": "GHSA-test-test-test", "module": CASBIN, "version": VERSION, "rationale": "x", "evidence": "x", "revalidation": "x", "reviewer": "x", "approved_at": "bad"}]),
            dict(EMPTY_EXCEPTIONS, exceptions=[dict(valid, module="github.com/casbin/gorm-adapter/v3")]),
            dict(EMPTY_EXCEPTIONS, exceptions=[dict(valid, approved_at="2026-99-99")]),
            dict(EMPTY_EXCEPTIONS, exceptions=[{key: value for key, value in valid.items() if key != "reviewer"}]),
            dict(EMPTY_EXCEPTIONS, exceptions=[valid, valid]),
        ]
        for document in invalid:
            with self.subTest(document=document), self.assertRaises(GateError):
                validate_exceptions(document)

    def test_api_error_object_is_not_an_empty_advisory_list(self):
        for affected, upstream in [({"message": "rate limited"}, []), ([], {"message": "forbidden"}), ([None], [])]:
            with self.subTest(affected=affected, upstream=upstream), self.assertRaises(GateError):
                evaluate(scan(), affected, upstream, VERSION)

    def test_cli_preserves_range_evidence_on_success_and_failure(self):
        fixed = self.run_cli([advisory("< v2.135.0")])
        self.assertEqual(fixed.returncode, 0, fixed.stdout + fixed.stderr)
        self.assertEqual(json.loads(fixed.stdout)["advisory_evaluations"][0]["evaluation"], "not_affected")
        affected = self.run_cli([advisory("< v2.136.0")])
        self.assertEqual(affected.returncode, 1)
        self.assertEqual(json.loads(affected.stdout)["advisory_evaluations"][0]["evaluation"], "affected")

    def test_cli_api_rate_limit_error_fails_closed(self):
        result = self.run_cli({"message": "rate limited"})
        self.assertEqual(result.returncode, 1)
        report = json.loads(result.stdout)
        self.assertEqual(report["status"], "blocked")
        self.assertEqual(report["reason"], "advisory API did not return an advisory array")

    def test_missing_truncated_malformed_or_wrong_scope_scan_fails_closed(self):
        config, sbom = [json.loads(part) for part in scan().splitlines()]
        cases = ["", "{}", "{", json.dumps(config), json.dumps(sbom), scan() + "{", scan() + '\n{"unknown":{}}']
        for field, value in [("scan_mode", "binary"), ("scan_level", "module"), ("protocol_version", "v2"), ("scanner_name", "other"), ("db", "http://empty.invalid")]:
            bad = copy.deepcopy(config)
            bad["config"][field] = value
            cases.append(json.dumps(bad) + "\n" + json.dumps(sbom))
        for raw in cases:
            with self.subTest(raw=raw), self.assertRaises(GateError):
                evaluate(raw, [], [], VERSION)

    def test_version_mismatch_and_incomplete_finding_fail_closed(self):
        with self.assertRaises(GateError):
            evaluate(scan(), [], [], "v2.999.0")
        for finding in [{}, {"osv": "GO-2026-9999", "trace": []}, {"osv": "GO-2026-9999", "trace": [{}]}]:
            with self.subTest(finding=finding), self.assertRaises(GateError):
                evaluate(scan({"finding": finding}), [], [], VERSION)


class AdapterInventoryTests(unittest.TestCase):
    modules = "github.com/metrum-ai/router\n" + CASBIN + " " + VERSION + "\ngithub.com/casbin/govaluate v1.3.0\n"
    graph = "github.com/metrum-ai/router " + CASBIN + "@" + VERSION + "\n" + CASBIN + "@" + VERSION + " github.com/casbin/govaluate@v1.3.0\n"

    def test_current_manual_loader_has_no_external_adapter(self):
        inventory = evaluate_inventory(self.modules, self.graph, VERSION)
        self.assertEqual(inventory["adapter_candidates"], [])
        self.assertEqual(inventory["casbin_related_modules"]["github.com/casbin/govaluate"], "v1.3.0")

    def test_new_external_casbin_consumer_cannot_skip_upstream_review(self):
        for module in ("github.com/casbin/gorm-adapter/v3", "github.com/other-owner/sql-adapter"):
            with self.subTest(module=module), self.assertRaises(GateError):
                evaluate_inventory(self.modules + module + " v1.0.0\n", self.graph + module + "@v1.0.0 " + CASBIN + "@v2.100.0\n", VERSION)

    def test_missing_malformed_replaced_or_inconsistent_inventory_blocks(self):
        for modules, graph in [("", self.graph), (self.modules, ""), (self.modules, self.graph + "broken\n"), (self.modules.replace(VERSION, VERSION + " replaced"), self.graph), (self.modules.replace(VERSION, "v2.999.0"), self.graph)]:
            with self.subTest(modules=modules, graph=graph), self.assertRaises(GateError):
                evaluate_inventory(modules, graph, VERSION)


class RequiredAggregateTests(unittest.TestCase):
    def test_advisory_uses_heavy_bounded_go_runtime(self):
        workflow = (Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml").read_text()
        self.assertIn("\n  admin-authz-advisories:\n", workflow)
        job = workflow.split("\n  admin-authz-advisories:\n", 1)[1].split("\n  ci:\n", 1)[0]
        self.assertIn("runs-on: ${{ vars.ROUTER_HEAVY_RUNNER || 'router-arc' }}", job)
        self.assertNotIn("ROUTER_LIGHT_RUNNER", job)
        self.assertIn("GOMAXPROCS: '2'", job)
        self.assertIn("GOMEMLIMIT: 8GiB", job)
        self.assertIn("timeout-minutes: 25", job)
        self.assertIn("govulncheck@v1.9.0 -json ./...", job)
        self.assertIn("github-affected-status.txt", job)
        self.assertIn("github-upstream-status.txt", job)
        self.assertIn("--paginate --jq '.[]'", job)
        self.assertIn("--exceptions advisory-evidence/exceptions.json", job)

    def aggregate(self):
        workflow = (Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml").read_text()
        return workflow.split("\n  ci:\n", 1)[1]

    def test_required_ci_depends_on_advisory_job(self):
        needs = re.search(r"needs: \[([^\]]+)\]", self.aggregate()).group(1)
        self.assertIn("admin-authz-advisories", [name.strip() for name in needs.split(",")])

    def test_advisory_rejection_cannot_leave_required_ci_green(self):
        script = textwrap.dedent(self.aggregate().split("run: |", 1)[1])
        for event in ("pull_request", "merge_group", "workflow_dispatch", "push"):
            lanes = {name: "success" for name in ("ROUTER_RESULT", "OTHER_GO_RESULT", "OTHER_RESULT", "K8S_SECURITY_RESULT", "PARALLEL_TOOLS_RESULT", "TLS_ACME_RESULT")}
            if event == "push":
                lanes = {name: "skipped" for name in lanes}
            lanes.update(WARM_RESULT="success" if event == "push" else "skipped", SBOM_RESULT="success" if event in ("merge_group", "workflow_dispatch") else "skipped")
            for result in ("success", "failure", "cancelled", "skipped"):
                with self.subTest(event=event, result=result):
                    run = subprocess.run(["bash", "-e", "-c", script], env=dict(
                        lanes, CI_EVENT=event, ADVISORY_RESULT=result, ENGINE_ADVISORY_RESULT="success"
                    ), capture_output=True, text=True)
                    self.assertEqual(run.returncode == 0, result == "success", run.stderr)


if __name__ == "__main__":
    unittest.main()
