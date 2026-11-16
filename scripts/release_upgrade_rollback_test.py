# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import hashlib
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
    def test_v48_legacy_caller_config_migrates_with_real_commands(self):
        root = Path(__file__).parents[1]
        token_id = "rtr_metrum_migration_prod_v48_k1"
        token = token_id + "_existing-held-token-secret"
        digest = hashlib.sha256(token.encode()).hexdigest()
        second_id = "rtr_metrum_migration_prod_v48_k2"
        second_token = second_id + "_second-held-token-secret"
        second_digest = hashlib.sha256(second_token.encode()).hexdigest()
        with tempfile.TemporaryDirectory() as raw_dir:
            directory = Path(raw_dir)
            config = directory / "config.yaml"
            token_file = directory / "caller.token"
            second_token_file = directory / "second-caller.token"
            token_file.write_text(token + "\n", encoding="utf-8")
            token_file.chmod(0o600)
            second_token_file.write_text(second_token + "\n", encoding="utf-8")
            second_token_file.chmod(0o600)
            config.write_text(
                "server:\n  listen: ':8080'\ncallers:\n"
                "  - id: production-api\n"
                f"    token_sha256: {digest}\n"
                f"    token_id: {token_id}\n"
                "  - id: production-worker\n"
                f"    token_sha256: {second_digest}\n"
                f"    token_id: {second_id}\n",
                encoding="utf-8",
            )
            migrated = subprocess.run(
                ["go", "run", "./cmd/metrum-ai-routerctl", "callers", "migrate-legacy",
                 "--config", str(config), "--id", "production-api", "--token-file", str(token_file)],
                cwd=root, text=True, capture_output=True, timeout=90, check=False,
            )
            self.assertEqual(migrated.returncode, 0, migrated.stderr)
            migrated = subprocess.run(
                ["go", "run", "./cmd/metrum-ai-routerctl", "callers", "migrate-legacy",
                 "--config", str(config), "--id", "production-worker", "--token-file", str(second_token_file)],
                cwd=root, text=True, capture_output=True, timeout=90, check=False,
            )
            self.assertEqual(migrated.returncode, 0, migrated.stderr)
            updated = config.read_text(encoding="utf-8")
            self.assertNotIn("token_sha256", updated)
            self.assertIn("token_hash: pbkdf2-sha3-512:i=20000:", updated)
            verified = subprocess.run(
                ["go", "run", "./cmd/metrum-ai-routerctl", "callers", "verify",
                 "--config", str(config), "--id", "production-api", "--token-file", str(token_file)],
                cwd=root, text=True, capture_output=True, timeout=90, check=False,
            )
            self.assertEqual(verified.returncode, 0, verified.stderr)
            self.assertIn('"match": true', verified.stdout)

    def test_workflow_compares_the_full_candidate_commit_sha(self):
        workflow = Path(__file__).parents[1] / ".github/workflows/release-upgrade-check.yml"
        self.assertIn('--candidate-commit "$(git rev-parse HEAD)"', workflow.read_text(encoding="utf-8"))

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
