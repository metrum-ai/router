# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest
from pathlib import Path
import check_go_toolchain as check

ROOT = Path(__file__).resolve().parents[1]


class GoPatchFloorTests(unittest.TestCase):
    def test_obsolete_and_prerelease_toolchains_fail(self):
        for version in ("go1.25.99", "go1.26.10", "go1.27.0", "go1.27.2", "go1.27.3rc1", "devel go1.28", "", "go1.27", "go1.27.3 extra"):
            with self.subTest(version=version):
                self.assertFalse(check.supported(version))

    def test_fixed_stable_versions_pass(self):
        for version in ("go1.27.3", "go1.27.4", "go1.28.0", "go1.30.0"):
            with self.subTest(version=version):
                self.assertTrue(check.supported(version))

    def test_build_producers_use_fixed_floor(self):
        self.assertIn("\ngo 1.27.3\n", (ROOT / "go.mod").read_text())
        self.assertIn("golang:1.27.3-alpine", (ROOT / "Dockerfile").read_text())
        self.assertIn("ARG GO_VERSION=1.27.3\n", (ROOT / "deploy/github-runners-router/Dockerfile").read_text())

    def test_security_note_is_in_unreleased(self):
        text = (ROOT / "CHANGELOG.md").read_text()
        unreleased = text.split("## [Unreleased]\n", 1)[1].split("\n## [", 1)[0]
        self.assertIn("### Security", unreleased)
        self.assertIn("v4.8.0 release binaries are built with Go 1.27.3", unreleased)

    def test_ci_and_release_builds_assert_actual_runtime(self):
        for name in ("ci.yml", "release.yml", "release-compat.yml"):
            text = (ROOT / ".github/workflows" / name).read_text()
            with self.subTest(workflow=name):
                self.assertIn("python3 scripts/check_go_toolchain.py", text)
                self.assertIn("go-version-file: go.mod", text)


if __name__ == "__main__":
    unittest.main()
