#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Authenticode-sign Windows release binaries with Azure Trusted Signing (#332).

Runs on the Linux release runners between the Windows `.exe` builds and the
deterministic zip packaging, so checksums and the release inventory are always
computed over the *signed* binaries.

Behavior:

- No-op (with a log line) when signing is not requested
  (ROUTER_SIGN_WINDOWS != "1") or when all six Trusted Signing secrets are
  absent. PR and merge-queue builds therefore keep working, and releases stay
  unsigned until the credentials are provisioned (tracked on #332).
- Fails the build when only some of the secrets are set (misconfiguration),
  and fails the release when any sign or verify step fails once all secrets
  are present: unsigned Windows assets must never ship from a signing-enabled
  run.
- When ROUTER_REQUIRE_WINDOWS_SIGNING=1 (set from the repository variable
  WINDOWS_SIGNING_REQUIRED in release.yml once signing is provisioned), a
  missing or partial secret set on a tag build fails the release instead of
  shipping unsigned binaries. The default stays a no-op.

Required secrets (expected as environment variables, never on the command
line; the client secret is passed to the token request body only):

    AZURE_TENANT_ID
    AZURE_CLIENT_ID
    AZURE_CLIENT_SECRET        (secret)
    TRUSTED_SIGNING_ENDPOINT   (e.g. https://eus.codesigning.azure.net)
    TRUSTED_SIGNING_ACCOUNT
    TRUSTED_SIGNING_CERT_PROFILE

The signer is the pinned, checksum-verified jsign fat jar (see
.github/workflows/release.yml); its path comes from JSIGN_JAR. java must be on
PATH (actions/setup-java). Timestamping uses the Azure RFC 3161 timestamp
service so signatures stay valid after the certificate expires.

Verification: after signing, each .exe must carry a non-empty PE security
directory (the embedded Authenticode signature block). When osslsigncode is
available it additionally runs `osslsigncode verify` per binary. Subject and
timestamp validation on a real Windows host is part of the release dry-run
checklist on #332 (Get-AuthenticodeSignature).

Metrum AI.
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import struct
import subprocess
import sys
import time
import urllib.parse
import urllib.request

REQUIRED_SECRETS = (
    "AZURE_TENANT_ID",
    "AZURE_CLIENT_ID",
    "AZURE_CLIENT_SECRET",
    "TRUSTED_SIGNING_ENDPOINT",
    "TRUSTED_SIGNING_ACCOUNT",
    "TRUSTED_SIGNING_CERT_PROFILE",
)

TOKEN_SCOPE = "https://codesigning.azure.net/.default"
TSA_URL = "http://timestamp.acs.microsoft.com"


def log(message: str) -> None:
    print(f"[sign-windows] {message}", flush=True)


def required_env() -> dict[str, str] | None:
    """Return the secret set, None when all are absent, or die on a partial set."""
    present = {name: os.environ.get(name, "").strip() for name in REQUIRED_SECRETS}
    set_names = [name for name, value in present.items() if value]
    if not set_names:
        return None
    if len(set_names) != len(REQUIRED_SECRETS):
        missing = sorted(set(REQUIRED_SECRETS) - set(set_names))
        log(f"FATAL: partial Trusted Signing configuration; missing: {', '.join(missing)}")
        raise SystemExit(2)
    return present


def acquire_token(env: dict[str, str]) -> str:
    body = urllib.parse.urlencode({
        "grant_type": "client_credentials",
        "client_id": env["AZURE_CLIENT_ID"],
        "client_secret": env["AZURE_CLIENT_SECRET"],
        "scope": TOKEN_SCOPE,
    }).encode()
    req = urllib.request.Request(
        f"https://login.microsoftonline.com/{env['AZURE_TENANT_ID']}/oauth2/v2.0/token",
        data=body,
    )
    with urllib.request.urlopen(req, timeout=60) as resp:
        payload = json.load(resp)
    token = payload.get("access_token", "")
    if not token:
        raise SystemExit("[sign-windows] FATAL: token response carried no access_token")
    return token


def pe_security_directory(path: str) -> tuple[int, int]:
    """Return the PE IMAGE_DIRECTORY_ENTRY_SECURITY (rva, size); (0, 0) when absent."""
    with open(path, "rb") as handle:
        data = handle.read()
    if len(data) < 0x40 or data[:2] != b"MZ":
        raise SystemExit(f"[sign-windows] FATAL: {path} is not a PE binary")
    (pe_offset,) = struct.unpack_from("<I", data, 0x3C)
    if data[pe_offset:pe_offset + 4] != b"PE\0\0":
        raise SystemExit(f"[sign-windows] FATAL: {path} has no PE header")
    optional_offset = pe_offset + 24
    (magic,) = struct.unpack_from("<H", data, optional_offset)
    if magic == 0x20B:
        dir_base = optional_offset + 112
    elif magic == 0x10B:
        dir_base = optional_offset + 96
    else:
        raise SystemExit(f"[sign-windows] FATAL: {path} has an unknown PE optional header magic {magic:#x}")
    rva, size = struct.unpack_from("<II", data, dir_base + 4 * 8)
    return rva, size


def verify_signed(path: str) -> None:
    rva, size = pe_security_directory(path)
    if rva == 0 or size == 0:
        raise SystemExit(f"[sign-windows] FATAL: {path} has no Authenticode signature block after signing")
    osslsigncode = shutil.which("osslsigncode")
    if osslsigncode:
        proc = subprocess.run([osslsigncode, "verify", path], capture_output=True, text=True)
        if proc.returncode != 0:
            raise SystemExit(f"[sign-windows] FATAL: osslsigncode verify failed for {path}: {proc.stdout.strip()} {proc.stderr.strip()}")
    log(f"verified {os.path.basename(path)}: signature block {size} bytes at 0x{rva:x}")


def sign_all(bin_dir: str, env: dict[str, str], jar: str) -> int:
    exes = sorted(
        os.path.join(bin_dir, name)
        for name in os.listdir(bin_dir)
        if name.endswith(".exe")
    )
    if not exes:
        raise SystemExit(f"[sign-windows] FATAL: no .exe files found in {bin_dir}")
    token = acquire_token(env)
    alias = f"{env['TRUSTED_SIGNING_ACCOUNT']}/{env['TRUSTED_SIGNING_CERT_PROFILE']}"
    child_env = dict(os.environ)
    child_env["JSIGN_STOREPASS"] = token
    signed = 0
    for exe in exes:
        # Password material is passed by env reference (env:JSIGN_STOREPASS),
        # never on the jsign command line.
        cmd = [
            "java", "-jar", jar,
            "--storetype", "TRUSTEDSIGNING",
            "--keystore", env["TRUSTED_SIGNING_ENDPOINT"],
            "--storepass", "env:JSIGN_STOREPASS",
            "--alias", alias,
            "--tsaurl", TSA_URL,
            "--tsmode", "RFC3161",
            exe,
        ]
        proc = subprocess.run(cmd, env=child_env, capture_output=True, text=True)
        if proc.returncode != 0:
            raise SystemExit(f"[sign-windows] FATAL: jsign failed for {exe}: {proc.stdout.strip()} {proc.stderr.strip()}")
        verify_signed(exe)
        signed += 1
        log(f"signed {os.path.basename(exe)} with RFC 3161 timestamp ({alias})")
    return signed


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: sign_windows_exes.py <bin_dir>")
    bin_dir = sys.argv[1]
    if os.environ.get("ROUTER_SIGN_WINDOWS", "") != "1":
        log("signing not requested (ROUTER_SIGN_WINDOWS != 1); leaving Windows binaries unsigned")
        return 0
    required = os.environ.get("ROUTER_REQUIRE_WINDOWS_SIGNING", "") == "1"
    env = required_env()
    if env is None:
        if required:
            log("FATAL: WINDOWS_SIGNING_REQUIRED is set but the Trusted Signing secrets are absent; refusing to ship unsigned Windows binaries")
            return 2
        log("Trusted Signing secrets absent; skipping Windows signing (no-op until provisioned, #332)")
        return 0
    jar = os.environ.get("JSIGN_JAR", "").strip()
    if not jar or not os.path.isfile(jar):
        raise SystemExit("[sign-windows] FATAL: signing requested but JSIGN_JAR does not name the pinned jsign jar")
    if shutil.which("java") is None:
        raise SystemExit("[sign-windows] FATAL: signing requested but java is not on PATH")
    started = time.monotonic()
    count = sign_all(bin_dir, env, jar)
    log(f"signed {count} Windows binaries in {time.monotonic() - started:.1f}s")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
