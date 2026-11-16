# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Permission contract for Harbor host-to-container credentials."""


def valid_credential_permissions(name: str, mode: int, gid: int) -> bool:
    if name == "provider-secrets.json":
        return mode == 0o600
    if name == "router-token":
        return mode == 0o640 and gid == 10001
    return False
