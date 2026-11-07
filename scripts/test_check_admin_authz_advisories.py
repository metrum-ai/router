# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import copy
import json
from pathlib import Path
import re
import subprocess
import textwrap
import unittest

from check_admin_authz_advisories import GateError, evaluate, evaluate_inventory

CASBIN = "github.com/casbin/casbin/v2"
VERSION = "v2.135.0"


def scan(*extra):
    events = [
        {"config": {"protocol_version": "v1.0.0", "scanner_name": "govulncheck", "scanner_version": "v1.9.0", "db": "https://vuln.go.dev", "scan_mode": "source", "scan_level": "symbol", "go_version": "go1.27.3"}},
        {"SBOM": {"go_version": "go1.27.3", "modules": [{"path": CASBIN, "version": VERSION}]}},
    ]
    return "\n".join(json.dumps(event) for event in events + list(extra))


class AdvisoryGateTests(unittest.TestCase):
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
        for affected, upstream in [([{"ghsa_id": "GHSA-test-test-test"}], []), ([], [{"ghsa_id": "GHSA-test-test-test"}])]:
            with self.subTest(affected=affected, upstream=upstream), self.assertRaises(GateError):
                evaluate(scan(), affected, upstream, VERSION)

    def test_api_error_object_is_not_an_empty_advisory_list(self):
        for affected, upstream in [({"message": "rate limited"}, []), ([], {"message": "forbidden"}), ([None], [])]:
            with self.subTest(affected=affected, upstream=upstream), self.assertRaises(GateError):
                evaluate(scan(), affected, upstream, VERSION)

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
        self.assertIn("govulncheck@v1.9.0 -json ./...", job)

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
                    run = subprocess.run(["bash", "-e", "-c", script], env=dict(lanes, CI_EVENT=event, ADVISORY_RESULT=result), capture_output=True, text=True)
                    self.assertEqual(run.returncode == 0, result == "success", run.stderr)


if __name__ == "__main__":
    unittest.main()
