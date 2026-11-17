# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Tests for the narrowly scoped Harbor credential permission contract."""

import unittest
from pathlib import Path

from credential_permissions import valid_credential_permissions
from harbor_config import ROUTER_TOKEN_CONTAINER_PATH, router_token_bind_mount


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

    def test_router_token_mount_uses_adapter_container_path(self):
        self.assertEqual(ROUTER_TOKEN_CONTAINER_PATH, "/run/harbor-e2e/router-token")
        self.assertEqual(
            router_token_bind_mount(Path("/dev/shm/harbor-e2e-123/router-token")),
            {
                "type": "bind",
                "source": "/dev/shm/harbor-e2e-123/router-token",
                "target": ROUTER_TOKEN_CONTAINER_PATH,
                "read_only": True,
            },
        )

    def test_unknown_credentials_are_rejected(self):
        self.assertFalse(valid_credential_permissions("other-token", 0o600, 10001))


if __name__ == "__main__":
    unittest.main()
