# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import base64
import hashlib

CALLER_ID = "rtr_api_compat_allowed"
CALLER = CALLER_ID + "_secret"
DENIED_CALLER_ID = "rtr_api_compat_denied"
DENIED_CALLER = DENIED_CALLER_ID + "_secret"

def _token_hash(token: str, salt: bytes) -> str:
    derived = hashlib.pbkdf2_hmac("sha3_512", token.encode(), salt, 10000)
    enc = lambda value: base64.urlsafe_b64encode(value).decode().rstrip("=")
    return f"pbkdf2-sha3-512:i=10000:{enc(salt)}:{enc(derived)}"

CALLER_DIGEST = _token_hash(CALLER, b"api-compat-caller")
DENIED_CALLER_DIGEST = _token_hash(DENIED_CALLER, b"api-compat-denied")
PROMPT_CANARY = "api-compat-prompt-canary"
TOOL_CANARY = "api-compat-tool-canary"
FORBIDDEN_ARTIFACT_VALUES = {
    CALLER,
    CALLER_ID,
    DENIED_CALLER,
    DENIED_CALLER_ID,
    CALLER_DIGEST,
    DENIED_CALLER_DIGEST,
    "Authorization:",
    PROMPT_CANARY,
    TOOL_CANARY,
}

DISPOSITIONS = frozenset(
    {
        "native_supported",
        "supported_translation",
        "documented_normalization",
        "policy_rejection",
        "unsupported",
        "blocked",
    }
)
