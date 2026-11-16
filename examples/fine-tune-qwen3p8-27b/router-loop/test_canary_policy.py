# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest

from canary_policy import route


TARGETS = [{"provider": "teacher", "weight": 1}, {"provider": "ft-canary", "weight": 1}]


class CanaryPolicyTests(unittest.TestCase):
    def test_canary_requires_both_project_and_token_id(self):
        canary = route({"caller": {"project": "acme-support", "tokenId": "rtr_acme-support"}, "targets": TARGETS})
        self.assertEqual(canary["targetIndex"], 1)
        self.assertEqual(canary["classLabel"], "support-key-canary")

        for caller in (
            {"project": "acme-it", "tokenId": "rtr_acme-support"},
            {"project": "acme-support", "tokenId": "rtr_acme-it"},
        ):
            with self.subTest(caller=caller):
                result = route({"caller": caller, "targets": TARGETS})
                self.assertEqual(result["targetIndex"], 0)
                self.assertEqual(result["classLabel"], "teacher")

    def test_missing_bound_target_fails_closed(self):
        with self.assertRaisesRegex(ValueError, "required bound target unavailable"):
            route({"caller": {"project": "acme-support", "tokenId": "rtr_acme-support"}, "targets": TARGETS[:1]})


if __name__ == "__main__":
    unittest.main()
