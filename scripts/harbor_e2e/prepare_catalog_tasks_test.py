# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import subprocess
import tempfile
import unittest
from pathlib import Path

from prepare_catalog_tasks import TASKS, prepare


class PrepareCatalogTasksTest(unittest.TestCase):
    def test_stages_only_bespoke_live_catalog_tasks_without_solutions(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "catalog"
            prepare(output)
            self.assertEqual(len(list(output.iterdir())), 5)
            for task_id, folder in TASKS.items():
                task = output / folder
                self.assertIn(task_id, (task / "task.toml").read_text())
                self.assertTrue((task / "instruction.md").is_file())
                self.assertTrue((task / "environment" / "workspace").is_dir())
                self.assertIn("pi-coding-agent@1.4.0", (task / "environment" / "Dockerfile").read_text())
                self.assertTrue((task / "tests" / "verify.py").is_file())
                test_sh = task / "tests" / "test.sh"
                self.assertTrue(test_sh.stat().st_mode & 0o111)
                self.assertIn("score:.0f", test_sh.read_text())
                subprocess.run(["bash", "-n", str(test_sh)], check=True)
                self.assertFalse((task / "solution").exists())

    def test_refuses_to_overwrite_an_existing_task_tree(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "catalog"
            output.mkdir()
            (output / "keep").touch()
            with self.assertRaisesRegex(FileExistsError, "refusing to reuse"):
                prepare(output)


if __name__ == "__main__":
    unittest.main()
