# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest

from policy_service import route


TARGETS = [{"provider": name, "weight": 1} for name in ("biomedical", "finance", "math", "coding")]


class PolicyServiceTests(unittest.TestCase):
    def test_domain_routes_ignore_caller_persona(self):
        for text, domain in (
            ("Find the gene and protein", "biomedical"),
            ("Find financial revenue", "finance"),
            ("Calculate the arithmetic sum", "math"),
            ("Implement a Python function", "coding"),
        ):
            with self.subTest(domain=domain):
                result = route({"text": text, "targets": TARGETS, "caller": {"project": "wrong"}})
                self.assertEqual(TARGETS[result["targetIndex"]]["provider"], domain)
                self.assertEqual(result["classLabel"], f"domain:{domain}")
                self.assertEqual(result["fallbackIndexes"], [])

    def test_unknown_and_tied_domains_use_coding_fallback(self):
        for text in ("What is the weather?", "Explain a gene and a math problem.", "A general zipcode."):
            result = route({"text": text, "targets": TARGETS})
            self.assertEqual(result["classLabel"], "domain:unknown")
            self.assertEqual(TARGETS[result["targetIndex"]]["provider"], "coding")

    def test_unavailable_domain_fails_closed(self):
        targets = [dict(target, weight=0 if target["provider"] == "finance" else target["weight"]) for target in TARGETS]
        with self.assertRaisesRegex(ValueError, "required expert unavailable"):
            route({"text": "financial revenue", "targets": targets})


if __name__ == "__main__":
    unittest.main()
