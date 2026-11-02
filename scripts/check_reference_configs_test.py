#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Tests for scripts/check_reference_configs.py."""

from __future__ import annotations

import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REFERENCE = ROOT / "deploy" / "reference"
CHECKER = ROOT / "scripts" / "check_reference_configs.py"


def run(root: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(CHECKER), "--root", str(root)],
        capture_output=True,
        text=True,
        check=False,
    )


class ReferenceConfigTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.root = self.tmp / "reference"
        shutil.copytree(REFERENCE, self.root)

    def test_committed_tree_is_valid(self) -> None:
        result = run(REFERENCE)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("reference configurations", result.stdout)

    def test_compose_digest_must_be_pinned(self) -> None:
        path = self.root / "nvidia-datacenter" / "compose.yaml"
        text = path.read_text(encoding="utf-8")
        path.write_text(
            text.replace("@sha256:d825baf9ce4b1b6bfc5ecc7b79880c7e6748c65a72d697ce3d47607aafa84c6e", "", 1),
            encoding="utf-8",
        )
        result = run(self.root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("must be pinned by digest", result.stderr)

    def test_lock_digest_must_match_compose(self) -> None:
        path = self.root / "amd-mi350p-datacenter" / "images.lock"
        path.write_text(path.read_text(encoding="utf-8").replace("3b20f0a3", "4b20f0a3"), encoding="utf-8")
        result = run(self.root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("digest disagrees with images.lock", result.stderr)

    def test_non_official_model_server_image_is_rejected(self) -> None:
        path = self.root / "amd-strix-halo-edge" / "compose.yaml"
        path.write_text(
            path.read_text(encoding="utf-8").replace("rocm/vllm:", "someoneelse/vllm:", 1),
            encoding="utf-8",
        )
        result = run(self.root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("not an official model-server image", result.stderr)

    def test_latest_tag_is_rejected(self) -> None:
        path = self.root / "nvidia-gb10-edge" / "images.lock"
        path.write_text(
            path.read_text(encoding="utf-8").replace(
                "    image: vllm/vllm-openai:v0.33.0", "    image: vllm/vllm-openai:latest"
            ),
            encoding="utf-8",
        )
        result = run(self.root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("resolves to latest", result.stderr)

    def test_missing_readme_section_is_rejected(self) -> None:
        path = self.root / "amd-strix-halo-edge" / "README.md"
        path.write_text(path.read_text(encoding="utf-8").replace("## Actual cost", "## Costs"), encoding="utf-8")
        result = run(self.root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("## Actual cost", result.stderr)

    def test_missing_required_file_is_rejected(self) -> None:
        (self.root / "nvidia-datacenter" / "SIZING.md").unlink()
        result = run(self.root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing SIZING.md", result.stderr)

    def test_unexpected_extra_file_is_rejected(self) -> None:
        (self.root / "nvidia-gb10-edge" / "notes.txt").write_text("x", encoding="utf-8")
        result = run(self.root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("unexpected file notes.txt", result.stderr)

    def test_caller_group_must_exist(self) -> None:
        path = self.root / "nvidia-datacenter" / "config.yaml"
        path.write_text(
            path.read_text(encoding="utf-8").replace("      - local-tiny\n", "      - local-tiny\n      - local-ghost\n", 1),
            encoding="utf-8",
        )
        result = run(self.root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("unknown group local-ghost", result.stderr)

    def test_literal_provider_key_is_rejected(self) -> None:
        path = self.root / "nvidia-datacenter" / "config.yaml"
        path.write_text(
            path.read_text(encoding="utf-8").replace("api_key: ${LOCAL_VLLM_API_KEY}", "api_key: sk-literal", 1),
            encoding="utf-8",
        )
        result = run(self.root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("api_key must be a ${VAR} reference", result.stderr)


if __name__ == "__main__":
    unittest.main()
