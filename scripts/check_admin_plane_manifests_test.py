#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Unit tests for the admin-plane manifest check (#817)."""

from __future__ import annotations

import io
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import check_admin_plane_manifests as check


def write_overlay(directory: Path, *, service_type: str = "ClusterIP", allow_any_admin: bool = False) -> None:
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "config.yaml").write_text(
        "admin:\n"
        "  enabled: true\n"
        "  listen: \"0.0.0.0:8081\"\n"
        "  allow_plaintext_non_loopback: true\n",
        encoding="utf-8",
    )
    (directory / "patch-deployment.yaml").write_text(
        "apiVersion: apps/v1\n"
        "kind: Deployment\n"
        "metadata: {name: metrum-ai-router}\n"
        "spec:\n"
        "  template:\n"
        "    spec:\n"
        "      containers:\n"
        "        - name: router\n"
        "          ports:\n"
        "            - {name: admin, containerPort: 8081}\n",
        encoding="utf-8",
    )
    (directory / "service-admin.yaml").write_text(
        f"apiVersion: v1\nkind: Service\nmetadata: {{name: metrum-ai-router-admin}}\n"
        f"spec:\n  type: {service_type}\n  ports:\n    - {{name: admin, port: 8081, targetPort: admin}}\n",
        encoding="utf-8",
    )
    admin_rule = (
        "    - ports:\n        - {protocol: TCP, port: 8081}\n"
        if allow_any_admin
        else "    - from:\n"
        "        - podSelector:\n            matchLabels: {access.metrum.ai/admin-client: \"true\"}\n"
        "      ports:\n        - {protocol: TCP, port: 8081}\n"
    )
    (directory / "networkpolicy-admin.yaml").write_text(
        "apiVersion: networking.k8s.io/v1\n"
        "kind: NetworkPolicy\n"
        "metadata: {name: default-deny}\n"
        "spec:\n"
        "  podSelector:\n    matchLabels: {app.kubernetes.io/name: metrum-ai-router}\n"
        "  policyTypes: [Ingress]\n"
        "  ingress: []\n"
        "---\n"
        "apiVersion: networking.k8s.io/v1\n"
        "kind: NetworkPolicy\n"
        "metadata: {name: allow}\n"
        "spec:\n"
        "  podSelector:\n    matchLabels: {app.kubernetes.io/name: metrum-ai-router}\n"
        "  policyTypes: [Ingress]\n"
        "  ingress:\n" + admin_rule,
        encoding="utf-8",
    )


class CheckAdminPlaneManifestsTest(unittest.TestCase):
    def run_check(self, directory: Path) -> tuple[int, str]:
        original = check.OVERLAY
        check.OVERLAY = directory
        try:
            out, err = io.StringIO(), io.StringIO()
            with redirect_stdout(out), redirect_stderr(err):
                code = check.main()
        finally:
            check.OVERLAY = original
        return code, out.getvalue() + err.getvalue()

    def test_repository_overlay_passes(self) -> None:
        code, output = self.run_check(check.OVERLAY)
        self.assertEqual(code, 0, output)

    def test_node_port_service_fails(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw)
            write_overlay(directory, service_type="NodePort")
            code, output = self.run_check(directory)
            self.assertEqual(code, 1)
            self.assertIn("ClusterIP", output)

    def test_admin_port_open_to_everyone_fails(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            directory = Path(raw)
            write_overlay(directory, allow_any_admin=True)
            code, output = self.run_check(directory)
            self.assertEqual(code, 1)
            self.assertIn("every source", output)


if __name__ == "__main__":
    unittest.main()
