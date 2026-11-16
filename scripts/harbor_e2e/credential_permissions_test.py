# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Tests for the narrowly scoped Harbor credential permission contract."""

import unittest

from credential_permissions import valid_credential_permissions


class CredentialPermissionsTests(unittest.TestCase):
    def test_provider_secret_remains_owner_only(self):
        self.assertTrue(valid_credential_permissions("provider-secrets.json", 0o600, 10001))
        self.assertFalse(valid_credential_permissions("provider-secrets.json", 0o640, 10001))
        self.assertFalse(valid_credential_permissions("provider-secrets.json", 0o640, 0))

    def test_router_token_is_readable_only_by_container_group(self):
        self.assertTrue(valid_credential_permissions("router-token", 0o640, 10001))
        self.assertFalse(valid_credential_permissions("router-token", 0o640, 1000))
        self.assertFalse(valid_credential_permissions("router-token", 0o660, 10001))
        self.assertFalse(valid_credential_permissions("router-token", 0o644, 10001))
        self.assertFalse(valid_credential_permissions("router-token", 0o600, 10001))

    def test_unknown_credentials_are_rejected(self):
        self.assertFalse(valid_credential_permissions("other-token", 0o600, 10001))


if __name__ == "__main__":
    unittest.main()
