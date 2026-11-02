#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
import unittest

from validate_trivy_k8s_ignores import validate


class TrivyIgnoreTest(unittest.TestCase):
    def test_empty_and_scoped(self):
        validate('# comment\n{"misconfigurations": []}')
        validate(json.dumps({"misconfigurations": [{"id": "AVD-KSV-0014", "paths": ["example.yaml"], "statement": "Reviewed test-only exception"}]}))

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
