# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import unittest

from check_release_environment import validate


class ReleaseEnvironmentTest(unittest.TestCase):
    def setUp(self) -> None:
        self.environment = {
            "name": "release",
            "protection_rules": [
                {"type": "required_reviewers", "reviewers": [{"type": "User", "reviewer": {"id": 7}}]},
                {"type": "branch_policy"},
            ],
            "deployment_branch_policy": {"protected_branches": False, "custom_branch_policies": True},
        }
        self.policies = {"branch_policies": [{"name": "v*", "type": "tag"}]}

    def test_accepts_required_reviewer_and_tag_policy(self) -> None:
        self.assertEqual(validate(self.environment, self.policies), [])

    def test_rejects_missing_reviewer(self) -> None:
        env = dict(self.environment, protection_rules=[{"type": "branch_policy"}])
        self.assertIn("release environment has no required reviewer", validate(env, self.policies))

    def test_rejects_unrestricted_or_wrong_deployment_patterns(self) -> None:
        env = dict(self.environment, deployment_branch_policy={"custom_branch_policies": False})
        policies = {"branch_policies": [{"name": "*", "type": "branch"}]}
        errors = validate(env, policies)
        self.assertTrue(any("custom tag policies" in error for error in errors))
        self.assertTrue(any("only tags matching v*" in error for error in errors))


if __name__ == "__main__":
    unittest.main()
