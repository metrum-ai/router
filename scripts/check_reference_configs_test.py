#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Tests for scripts/check_reference_configs.py."""

from __future__ import annotations

import importlib.util
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REFERENCE = ROOT / "deploy" / "reference"
CHECKER = ROOT / "scripts" / "check_reference_configs.py"

# Imported for the per-tier helpers; the module's own main() stays untouched.
_spec = importlib.util.spec_from_file_location("check_reference_configs", CHECKER)
check_reference_configs = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(check_reference_configs)


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

    def test_rocm_server_without_explicit_entrypoint_is_rejected(self) -> None:
        path = self.root / "amd-mi350p-datacenter" / "compose.yaml"
        text = path.read_text(encoding="utf-8")
        path.write_text(text.replace("    entrypoint: [\"vllm\", \"serve\"]\n", "", 1), encoding="utf-8")
        result = run(self.root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("does not set an explicit vllm serve entrypoint", result.stderr)

    def test_literal_provider_key_is_rejected(self) -> None:
        path = self.root / "nvidia-datacenter" / "config.yaml"
        path.write_text(
            path.read_text(encoding="utf-8").replace("api_key: ${LOCAL_VLLM_API_KEY}", "api_key: sk-literal", 1),
            encoding="utf-8",
        )
        result = run(self.root)
        self.assertEqual(result.returncode, 1)
        self.assertIn("api_key must be a ${VAR} reference", result.stderr)

class DeferredValidationTest(unittest.TestCase):
    """A tier that claims to be unvalidated must say who validates it and where."""

    DEFERRED = {
        "amd-mi350p-datacenter": ("Not validated on MI350P hardware", "#785"),
        "nvidia-gb10-edge": ("verified for the", "#763"),
    }

    def test_each_deferred_tier_links_its_follow_up(self) -> None:
        for tier, (phrase, issue) in self.DEFERRED.items():
            readme = (REFERENCE / tier / "README.md").read_text(encoding="utf-8")
            self.assertIn(phrase, readme, f"{tier} must state its limit")
            self.assertIn(issue, readme,
                          f"{tier} must link the issue that owns the deferred validation ({issue})")


class ContainerListenTest(unittest.TestCase):
    """A tier's router runs in a container: listen all interfaces, publish loopback.

    The ports are read from the router service, so neither a wrong container port nor
    an unrelated 127.0.0.1 elsewhere in the file can satisfy the check.
    """

    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.tier = self.tmp / "tier"
        self.tier.mkdir()
        for name in ("README.md", "SIZING.md", "config.yaml", "compose.yaml", "images.lock"):
            (self.tier / name).write_text("", encoding="utf-8")

    def write(self, listen: str, publish: str, extra: str = "") -> None:
        (self.tier / "config.yaml").write_text(f"server:\n  listen: {listen}\n", encoding="utf-8")
        (self.tier / "compose.yaml").write_text(
            "services:\n  router:\n    image: router:1\n"
            f"    ports:\n      - {publish}\n{extra}", encoding="utf-8")

    def problems(self) -> list[str]:
        return check_reference_configs.check_container_listen(self.tier)

    def test_container_loopback_is_rejected(self) -> None:
        self.write("127.0.0.1:8080", "127.0.0.1:8080:8080")
        self.assertTrue(any("must bind all" in p for p in self.problems()), self.problems())

    def test_all_interfaces_with_loopback_publish_passes(self) -> None:
        for listen in (":8080", "0.0.0.0:8080"):
            self.write(listen, "127.0.0.1:${ROUTER_HTTP_PORT:-8080}:8080")
            self.assertEqual(self.problems(), [], listen)

    def test_publish_of_a_different_container_port_is_rejected(self) -> None:
        """127.0.0.1:8080:9000 forwards host 8080 to container 9000 - nothing answers."""
        self.write(":8080", "127.0.0.1:8080:9000")
        self.assertTrue(any("must be the one the router binds" in p for p in self.problems()), self.problems())

    def test_loopback_elsewhere_in_the_file_does_not_count(self) -> None:
        """A healthcheck URL is not a publish: 0.0.0.0:8080:8080 is still wide open."""
        self.write(
            ":8080",
            "0.0.0.0:8080:8080",
            extra="    healthcheck:\n      test: ['CMD', 'curl', 'http://127.0.0.1:8080/readyz']\n",
        )
        self.assertTrue(any("must\n" not in p and "bind 127.0.0.1" in p for p in self.problems()), self.problems())

    def test_publish_without_a_host_ip_is_rejected(self) -> None:
        self.write(":8080", "8080:8080")
        self.assertTrue(any("bind 127.0.0.1" in p for p in self.problems()), self.problems())

    def test_unpinned_port_variable_uses_its_default(self) -> None:
        self.write(":8080", "${ROUTER_HTTP_PORT:-9090}:8080")
        self.assertTrue(any("bind 127.0.0.1" in p for p in self.problems()), self.problems())


if __name__ == "__main__":
    unittest.main()
