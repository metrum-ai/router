#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Tests for env example secret guardrails."""

from __future__ import annotations

import unittest

import check_env_example_secrets as checker


class SecretKeyErrorsTest(unittest.TestCase):
    def test_identifier_keys(self) -> None:
        for name in ("ROUTER_ID_TRANSFORM_KEY", "ROUTER_ID_TRANSFORM_KEY_PREVIOUS", "ROUTER_ID_TRANSFORM_KEY_EXTRA"):
            self.assertTrue(list(checker.secret_key_errors(checker.ROOT / "env.example.json", {name: "ab" * 64})))
            self.assertEqual([], list(checker.secret_key_errors(checker.ROOT / "env.example.json", {name: ""})))

    def test_allows_empty_sensitive_values(self) -> None:
        errors = list(
            checker.secret_key_errors(
                checker.ROOT / "env.example.json",
                {"UNKNOWN_PROVIDER_API_KEY": "", "PASSWORD": ""},
            )
        )

        self.assertEqual([], errors)

    def test_rejects_concrete_password_value(self) -> None:
        errors = list(
            checker.secret_key_errors(
                checker.ROOT / "env.example.json",
                {"PASSWORD": "super-secret-value"},
            )
        )

        self.assertIn("env.example.json:PASSWORD must be empty or a placeholder", errors[0])

    def test_rejects_unknown_provider_concrete_key_value(self) -> None:
        errors = list(
            checker.secret_key_errors(
                checker.ROOT / "env.example.json",
                {"UNKNOWN_PROVIDER_API_KEY": "super-secret-value"},
            )
        )

        self.assertIn(
            "env.example.json:UNKNOWN_PROVIDER_API_KEY must be empty or a placeholder",
            errors[0],
        )

    def test_allows_placeholder_values(self) -> None:
        errors = list(
            checker.secret_key_errors(
                checker.ROOT / "env.example.json",
                {
                    "OPENAI_API_KEY": "YOUR_OPENAI_API_KEY",
                    "ROUTER_TOKEN": "<router-token>",
                },
            )
        )

        self.assertEqual([], errors)


    def test_retired_agent_credential_paths_are_ignored(self) -> None:
        self.assertEqual([], list(checker.retired_agent_ignore_errors()))

    def test_local_credential_paths_are_ignored(self) -> None:
        self.assertEqual([], list(checker.local_credential_ignore_errors()))

    def test_detects_stripe_secret_patterns(self) -> None:
        errors = list(
            checker.live_pattern_errors(
                checker.ROOT / "commerce.env.json",
                'STRIPE_SECRET_KEY=sk_test_51ExampleStripeTestKeyValueXXXX',
            )
        )
        self.assertTrue(any("sk_test_" in error for error in errors))


if __name__ == "__main__":
    unittest.main()
