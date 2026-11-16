#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Emit a salted caller verifier for a structured token read from stdin."""
import base64
import hashlib
import json
import secrets
import sys


def verifier(token: str) -> tuple[str, str]:
    parts = token.split("_")
    if len(parts) != 7 or parts[:2] != ["rtr", "metrum"] or not all(parts):
        raise ValueError("token must use rtr_metrum_<user>_<project>_<env>_<key>_<secret>")
    token_id = "_".join(parts[:6])
    salt = secrets.token_bytes(16)
    key = hashlib.pbkdf2_hmac("sha3_512", token.encode(), salt, 20000, 64)
    enc = lambda value: base64.urlsafe_b64encode(value).decode().rstrip("=")
    return f"pbkdf2-sha3-512:i=20000:{enc(salt)}:{enc(key)}", token_id


if __name__ == "__main__":
    try:
        hashed, token_id = verifier(sys.stdin.read().strip())
    except ValueError as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(2)
    print(json.dumps({"token_hash": hashed, "token_id": token_id}))
