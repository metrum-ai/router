#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
import unittest
from pathlib import Path

from validate_trivy_k8s_ignores import validate


class TrivyIgnoreTest(unittest.TestCase):
    def test_empty_and_scoped(self):
        validate('# comment\n{"misconfigurations": []}')
        validate(json.dumps({"misconfigurations": [{"id": "AVD-KSV-0014", "paths": ["example.yaml"], "statement": "Reviewed test-only exception"}]}))

    def test_registry_trust_ignores_are_scoped_to_rendered_outputs(self):
        path = Path(__file__).resolve().parents[1] / ".github/trivy-k8s-ignore.yaml"
        document = json.loads("\n".join(
            line for line in path.read_text(encoding="utf-8").splitlines()
            if not line.lstrip().startswith("#")
        ))
        entries = [entry for entry in document["misconfigurations"] if entry["id"] == "AVD-KSV-0125"]
        self.assertEqual(
            {entry["paths"][0] for entry in entries},
            {"admin-plane.yaml", "example.yaml", "k3s-amd-instinct-local-serving.yaml",
             "linkerd-example.yaml", "nvidia-local-serving.yaml", "sqlite-bootstrap.yaml"},
        )
        self.assertEqual(len(entries), 6)
        self.assertTrue(all("placeholder" in entry["statement"].lower() or "upstream" in entry["statement"].lower()
                            for entry in entries))

    def test_reject_broad_or_unexplained(self):
        base = {"id": "AVD-DS-0002", "paths": ["Dockerfile"], "statement": "Reviewed reason"}
        invalid = [
            {}, {"vulnerabilities": []},
            {"misconfigurations": [{**base, "paths": []}]},
            {"misconfigurations": [{**base, "paths": ["*.yaml"]}]},
            {"misconfigurations": [{**base, "paths": ["../Dockerfile"]}]},
            {"misconfigurations": [{**base, "statement": "  "}]},
            {"misconfigurations": [{**base, "id": "AVD-KSV-*"}]},
            {"misconfigurations": [{**base, "extra": "ignored"}]},
        ]
        for document in invalid:
            with self.subTest(document=document), self.assertRaises(ValueError):
                validate(json.dumps(document))


if __name__ == "__main__":
    unittest.main()
