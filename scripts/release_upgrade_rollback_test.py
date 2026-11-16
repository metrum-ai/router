# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import json
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from pathlib import Path


SPEC = importlib.util.spec_from_file_location(
    "release_upgrade_rollback", Path(__file__).with_name("release_upgrade_rollback.py")
)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class StrictJSONTests(unittest.TestCase):
    def test_workflow_compares_the_full_candidate_commit_sha(self):
        workflow = Path(__file__).parents[1] / ".github/workflows/release-upgrade-check.yml"
        self.assertIn('--candidate-commit "$(git rev-parse HEAD)"', workflow.read_text(encoding="utf-8"))

    def test_upgrade_check_executes_both_scopes_from_the_checked_in_job(self):
        contract = MODULE.migration_job_example_script()
        for fragment in (
            "apply_scope()", "apply_scope usage ROUTER_USAGE_DB_DSN",
            "apply_scope router-config ROUTER_CONFIG_DB_DSN", "--action=plan",
            "--action=maintenance", "--action=verify", "--dsn-env=\"$dsn_env\"", "--json",
        ):
            self.assertIn(fragment, contract)

    def test_upgrade_check_runs_the_manifest_shell_script_directly(self):
        completed = subprocess.CompletedProcess(args=[], returncode=0, stdout='{"Compatible":true}\n', stderr="")
        with tempfile.TemporaryDirectory() as raw:
            workspace = Path(raw) / "job"
            with patch.object(MODULE, "run_as", return_value=completed) as run_as:
                statuses = MODULE.run_migration_job_example(
                    Path("/bin/true"), Path(raw) / "usage.sqlite", Path(raw) / "config.sqlite",
                    "router-test", "apply", workspace,
                )
        self.assertEqual(statuses, [{"Compatible": True}])
        command = run_as.call_args.args[1]
        self.assertEqual(command[-2], "-ec")
        self.assertIn("apply_scope usage ROUTER_USAGE_DB_DSN", command[-1])
        self.assertIn("apply_scope router-config ROUTER_CONFIG_DB_DSN", command[-1])
        self.assertIn(str(workspace / "metrum-ai-router-migrate"), command[-1])

    def test_accepts_one_object(self):
        self.assertEqual(MODULE.parse_single_json('{"state":"current"}'), {"state": "current"})

    def test_rejects_sql_prefix(self):
        with self.assertRaises(ValueError):
            MODULE.parse_single_json('SELECT * FROM schema_migrations;\n{"state":"current"}')

    def test_rejects_multiple_documents(self):
        with self.assertRaises(ValueError):
            MODULE.parse_single_json('{"state":"current"}\n{"state":"current"}')

    def test_detects_sql_diagnostics(self):
        self.assertTrue(MODULE.contains_sql_text('[0.3ms] SELECT * FROM schema_migrations'))
        self.assertFalse(MODULE.contains_sql_text("migration verify-serving failed"))

    def test_v48_baseline_must_reproduce_json_regression(self):
        self.assertFalse(MODULE.previous_json_status("v4.8.0", 'SELECT * FROM schema_migrations;\n{"state":"current"}'))
        with self.assertRaisesRegex(RuntimeError, "did not reproduce"):
            MODULE.previous_json_status("v4.8.0", '{"state":"current"}')

    def test_later_baseline_can_have_valid_json(self):
        self.assertTrue(MODULE.previous_json_status("v5.0.0", '{"state":"current"}'))

    def test_timeout_does_not_include_captured_output(self):
        timeout = subprocess.TimeoutExpired(["migration"], 1, output="secret-like-output")
        with patch.object(MODULE.subprocess, "run", side_effect=timeout):
            with self.assertRaisesRegex(RuntimeError, "exceeded its timeout") as caught:
                MODULE.run(["migration"], timeout=1)
        self.assertNotIn("secret-like-output", str(caught.exception))

    def test_smoke_failure_summary_reports_safe_check_names_only(self):
        with tempfile.TemporaryDirectory() as raw:
            evidence = Path(raw) / "smoke.json"
            evidence.write_text(json.dumps({
                "checks": [
                    {"check": "version", "passed": True},
                    {"check": "authenticated-text", "passed": False, "diagnostic": "secret value"},
                ]
            }), encoding="utf-8")
            message = MODULE.smoke_failure_summary(evidence)
        self.assertEqual(message, "serving smoke checks failed: authenticated-text")
        self.assertNotIn("secret", message)

    def test_smoke_failure_summary_handles_missing_evidence(self):
        with tempfile.TemporaryDirectory() as raw:
            evidence = Path(raw) / "missing.json"
            self.assertEqual(
                MODULE.smoke_failure_summary(evidence),
                "serving smoke failed before writing safe evidence",
            )


if __name__ == "__main__":
    unittest.main()
