# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import hashlib
import json
import json
import os
import stat
import subprocess
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch
from pathlib import Path


SPEC = importlib.util.spec_from_file_location(
    "release_upgrade_rollback", Path(__file__).with_name("release_upgrade_rollback.py")
)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class StrictJSONTests(unittest.TestCase):
    def test_evidence_directory_is_writable_by_service_user(self):
        with tempfile.TemporaryDirectory() as raw:
            evidence = Path(raw) / "evidence"
            service = SimpleNamespace(pw_uid=1234, pw_gid=5678)
            with patch.object(MODULE.os, "geteuid", return_value=0), patch.object(MODULE.os, "chown") as chown:
                self.assertEqual(MODULE.service_owned_directory(evidence, service), evidence)
            chown.assert_called_once_with(evidence, 1234, 5678)
            self.assertEqual(stat.S_IMODE(evidence.stat().st_mode), 0o700)

    def test_evidence_directory_rejects_unprivileged_wrong_owner(self):
        with tempfile.TemporaryDirectory() as raw:
            evidence = Path(raw) / "evidence"
            service = SimpleNamespace(pw_uid=os.geteuid() + 1, pw_gid=os.getegid())
            with patch.object(MODULE.os, "geteuid", return_value=os.geteuid()):
                with self.assertRaisesRegex(RuntimeError, "root or as the service user"):
                    MODULE.service_owned_directory(evidence, service)

    def _run_job_with_migration_stub(self, stub: str) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            executable = root / "metrum-ai-router-migrate"
            executable.write_text(stub, encoding="utf-8")
            executable.chmod(0o755)
            script = MODULE.migration_job_example_script().replace(
                "/app/bin/metrum-ai-router-migrate", str(executable),
            )
            env = dict(os.environ,
                       MIGRATION_ACTION="apply",
                       BACKUP_EVIDENCE_REF="release-check-approved-snapshot",
                       ROUTER_USAGE_DB_DSN="unused-usage-dsn",
                       ROUTER_CONFIG_DB_DSN="unused-config-dsn",
                       FAKE_MIGRATION_STATE=str(root / "calls"))
            return subprocess.run(["/bin/sh", "-ec", script], env=env,
                                  capture_output=True, text=True, check=False, timeout=10)

    def test_kubernetes_job_propagates_unrecognized_online_failure(self):
        completed = self._run_job_with_migration_stub(
            "#!/bin/sh\necho 'unexpected online failure'\nexit 7\n",
        )
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("unexpected online failure", completed.stderr)

    def test_kubernetes_job_propagates_unrecognized_maintenance_failure(self):
        stub = """#!/bin/sh
case "$*" in
  *--action=apply*)
    calls=0
    [ ! -f "$FAKE_MIGRATION_STATE" ] || calls=$(cat "$FAKE_MIGRATION_STATE")
    calls=$((calls + 1))
    printf '%s' "$calls" > "$FAKE_MIGRATION_STATE"
    if [ "$calls" -eq 1 ]; then
      echo 'migration apply: migration sample requires explicit maintenance runner'
      exit 1
    fi
    echo '{"Compatible":true}'
    exit 0
    ;;
  *--action=maintenance*) echo 'unexpected maintenance failure'; exit 9 ;;
  *) echo '{"Compatible":true}' ;;
esac
"""
        completed = self._run_job_with_migration_stub(stub)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("unexpected maintenance failure", completed.stderr)

    def test_expected_router_config_schema_includes_authentication_counters(self):
        self.assertEqual(MODULE.ROUTER_CONFIG_SCHEMA_VERSION, 21)

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

    def test_script_upgrade_applies_only_to_pre_v5_baselines(self):
        for version, expected in [("v4.7.1", True), ("v4.8.0", True), ("v5.0.0", False), ("v10.0.0", False)]:
            with self.subTest(version=version):
                self.assertEqual(MODULE.needs_script_upgrade(version), expected)

    def test_external_policy_result_requires_a_single_recorded_pass(self):
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / "candidate-smoke.json"
            for values, expected in [([True], True), ([False], False), ([], False), ([True, False], False)]:
                with self.subTest(values=values):
                    checks = [{"check": "external-policy-served", "passed": value} for value in values]
                    report.write_text(json.dumps({"checks": checks}), encoding="utf-8")
                    self.assertEqual(MODULE.external_policy_served(report), expected)

    def test_script_migration_requires_specific_startup_rejection(self):
        spec = importlib.util.spec_from_file_location(
            "local_launch_path_smoke", Path(__file__).with_name("local_launch_path_smoke.py")
        )
        assert spec and spec.loader
        launch = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(launch)

        for code, diagnostic, accepted in [
            (1, b"script was removed in v5.0.0; migrate to strategy: external", True),
            (0, b"script was removed in v5.0.0; migrate to strategy: external", False),
            (1, b"failed to open config", False),
        ]:
            with self.subTest(code=code, diagnostic=diagnostic), patch.object(
                subprocess, "run", return_value=subprocess.CompletedProcess([], code, stderr=diagnostic)
            ):
                if accepted:
                    launch.require_script_migration(Path("router"), Path("config.json"))
                else:
                    with self.assertRaises(RuntimeError):
                        launch.require_script_migration(Path("router"), Path("config.json"))

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
