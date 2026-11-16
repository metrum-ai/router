# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import subprocess
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

    def test_timeout_does_not_include_captured_output(self):
        timeout = subprocess.TimeoutExpired(["migration"], 1, output="secret-like-output")
        with patch.object(MODULE.subprocess, "run", side_effect=timeout):
            with self.assertRaisesRegex(RuntimeError, "exceeded its timeout") as caught:
                MODULE.run(["migration"], timeout=1)
        self.assertNotIn("secret-like-output", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
