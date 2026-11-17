#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline installer tests for scripts/install_crane.sh."""

from __future__ import annotations

import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
INSTALLER = ROOT / "scripts" / "install_crane.sh"


class CraneInstallerTests(unittest.TestCase):
    def _run_installer(self, temp: Path, destination: Path | None = None) -> Path:
        fake_bin = temp / "fake-bin"
        fake_bin.mkdir()
        (fake_bin / "curl").write_text(
            "#!/bin/sh\n"
            "while [ $# -gt 0 ]; do\n"
            "  if [ \"$1\" = --output ]; then : >\"$2\"; exit 0; fi\n"
            "  shift\n"
            "done\n"
            "exit 2\n", encoding="utf-8",
        )
        (fake_bin / "sha256sum").write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        (fake_bin / "tar").write_text(
            "#!/bin/sh\n"
            "while [ $# -gt 0 ]; do\n"
            "  if [ \"$1\" = -C ]; then out=$2; shift 2; else shift; fi\n"
            "done\n"
            "mkdir -p \"$out\"\n"
            "cat >\"$out/crane\" <<'CRANE'\n"
            "#!/bin/sh\n"
            "printf 'crane version v0.22.1\\n'\n"
            "CRANE\n"
            "chmod 0755 \"$out/crane\"\n", encoding="utf-8",
        )
        for tool in fake_bin.iterdir():
            tool.chmod(0o755)
        env = dict(os.environ, PATH=f"{fake_bin}{os.pathsep}{os.defpath}", RUNNER_TEMP=str(temp))
        args = ["bash", str(INSTALLER)]
        if destination is not None:
            args.append(str(destination))
        completed = subprocess.run(args, env=env, capture_output=True, text=True, check=False, timeout=10)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        expected = destination or temp / "crane"
        self.assertTrue(expected.is_file())
        self.assertTrue(os.access(expected, os.X_OK))
        return expected

    def test_default_destination_does_not_move_the_extracted_file_onto_itself(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            self._run_installer(Path(raw))

    def test_custom_destination_installs_from_separate_extraction_directory(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            self._run_installer(root, root / "nested" / "bin" / "crane")


if __name__ == "__main__":
    unittest.main()
