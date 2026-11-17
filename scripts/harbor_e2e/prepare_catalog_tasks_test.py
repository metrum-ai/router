# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import subprocess
import importlib.util
import os
import shutil
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
                dockerfile = (task / "environment" / "Dockerfile").read_text()
                self.assertIn("pi-coding-agent@1.4.0", dockerfile)
                self.assertIn("FROM node:22.19.0-bookworm-slim", dockerfile)
                self.assertIn("--engine-strict", dockerfile)
                self.assertIn("useradd --uid 10001", dockerfile)
                self.assertIn("ENV HOME=/home/harbor", dockerfile)
                self.assertIn("COPY --chown=10001:10001 workspace/ /app/", dockerfile)
                self.assertIn("USER 10001:10001", dockerfile)
                self.assertTrue((task / "tests" / "verify.py").is_file())
                test_sh = task / "tests" / "test.sh"
                self.assertTrue(test_sh.stat().st_mode & 0o111)
                self.assertIn("score:.0f", test_sh.read_text())
                subprocess.run(["bash", "-n", str(test_sh)], check=True)
                self.assertFalse((task / "solution").exists())

    def test_generated_harbor_verifier_accepts_solution_and_rejects_wrong_answer(self):
        source_root = Path(__file__).resolve().parents[2] / "tests" / "harbor" / "tasks"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "catalog"
            prepare(output)
            for task_id, folder in TASKS.items():
                source = source_root / folder
                task = output / folder
                for mode, expected in (("solution", "1"), ("wrong", "0")):
                    workspace = root / f"{task_id}-{mode}"
                    shutil.copytree(task / "environment" / "workspace", workspace)
                    spec = importlib.util.spec_from_file_location(
                        f"{task_id}_{mode}", source / "solution" / "apply.py")
                    module = importlib.util.module_from_spec(spec)
                    spec.loader.exec_module(module)
                    module.apply(workspace, mode)
                    reward_dir = root / f"reward-{task_id}-{mode}"
                    subprocess.run(["sh", str(task / "tests" / "test.sh")], check=True,
                                   env={**os.environ,
                                        "HARBOR_WORKSPACE": str(workspace),
                                        "HARBOR_TESTS_DIR": str(task / "tests"),
                                        "HARBOR_REWARD_DIR": str(reward_dir)},
                                   capture_output=True, text=True)
                    self.assertEqual((reward_dir / "reward.txt").read_text().strip(), expected,
                                     f"{task_id} {mode} verifier result")

    def test_refuses_to_overwrite_an_existing_task_tree(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "catalog"
            output.mkdir()
            (output / "keep").touch()
            with self.assertRaisesRegex(FileExistsError, "refusing to reuse"):
                prepare(output)


if __name__ == "__main__":
    unittest.main()
