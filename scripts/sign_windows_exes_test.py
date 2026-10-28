#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline regression for scripts/sign_windows_exes.py (#332)."""
from __future__ import annotations

import importlib.util
import os
import struct
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("sign_windows_exes", ROOT / "scripts/sign_windows_exes.py")
assert SPEC and SPEC.loader
SW = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SW)


def require(ok: bool, message: str) -> None:
    if not ok:
        raise AssertionError(message)


def make_pe(path: Path, *, with_signature: bool) -> None:
    """Build a minimal but structurally valid PE32+ file."""
    data = bytearray(0x400)
    data[:2] = b"MZ"
    pe_offset = 0x80
    struct.pack_into("<I", data, 0x3C, pe_offset)
    data[pe_offset:pe_offset + 4] = b"PE\0\0"
    optional_offset = pe_offset + 24
    struct.pack_into("<H", data, optional_offset, 0x20B)  # PE32+ magic
    dir_base = optional_offset + 112
    if with_signature:
        struct.pack_into("<II", data, dir_base + 4 * 8, 0x300, 0x100)
    path.write_bytes(bytes(data))


def run_script(bin_dir: Path, env: dict[str, str]) -> tuple[int, str]:
    merged = {k: v for k, v in os.environ.items() if k not in SW.REQUIRED_SECRETS and k not in ("ROUTER_SIGN_WINDOWS", "ROUTER_REQUIRE_WINDOWS_SIGNING", "JSIGN_JAR")}
    merged.update(env)
    proc = subprocess.run(
        [sys.executable, str(ROOT / "scripts/sign_windows_exes.py"), str(bin_dir)],
        capture_output=True, text=True, env=merged,
    )
    return proc.returncode, proc.stdout + proc.stderr


def main() -> int:
    # PE security directory parsing.
    with tempfile.TemporaryDirectory() as tmp:
        signed_pe = Path(tmp) / "signed.exe"
        unsigned_pe = Path(tmp) / "unsigned.exe"
        make_pe(signed_pe, with_signature=True)
        make_pe(unsigned_pe, with_signature=False)
        require(SW.pe_security_directory(str(signed_pe)) == (0x300, 0x100), "signed PE must report its security directory")
        require(SW.pe_security_directory(str(unsigned_pe)) == (0, 0), "unsigned PE must report no security directory")
        try:
            SW.verify_signed(str(unsigned_pe))
        except SystemExit as exc:
            require("no Authenticode signature block" in str(exc), str(exc))
        else:
            raise AssertionError("verify_signed must fail on an unsigned PE")
        SW.verify_signed(str(signed_pe))

    # Gate off: no-op, exit 0, no jsign needed.
    with tempfile.TemporaryDirectory() as tmp:
        code, out = run_script(Path(tmp), {})
        require(code == 0 and "not requested" in out, f"unsigned gate must no-op: {code} {out}")

    # Gate on, all secrets absent: no-op, exit 0 (pre-provisioning phase).
    with tempfile.TemporaryDirectory() as tmp:
        code, out = run_script(Path(tmp), {"ROUTER_SIGN_WINDOWS": "1"})
        require(code == 0 and "secrets absent" in out, f"missing secrets must no-op: {code} {out}")

    # Required mode (WINDOWS_SIGNING_REQUIRED provisioned): absent secrets fail.
    with tempfile.TemporaryDirectory() as tmp:
        code, out = run_script(Path(tmp), {"ROUTER_SIGN_WINDOWS": "1", "ROUTER_REQUIRE_WINDOWS_SIGNING": "1"})
        require(code == 2 and "refusing to ship unsigned" in out, f"required mode with absent secrets must fail: {code} {out}")
    # Required mode, partial secrets: still the partial-set failure.
    with tempfile.TemporaryDirectory() as tmp:
        code, out = run_script(Path(tmp), {"ROUTER_SIGN_WINDOWS": "1", "ROUTER_REQUIRE_WINDOWS_SIGNING": "1", "AZURE_TENANT_ID": "t"})
        require(code == 2 and "missing" in out, f"required mode with partial secrets must fail: {code} {out}")

    # Gate on, partial secrets: fail with the missing names.
    with tempfile.TemporaryDirectory() as tmp:
        code, out = run_script(Path(tmp), {"ROUTER_SIGN_WINDOWS": "1", "AZURE_TENANT_ID": "t"})
        require(code == 2 and "missing" in out, f"partial secrets must fail loudly: {code} {out}")
        require("AZURE_CLIENT_SECRET" in out and "TRUSTED_SIGNING_ENDPOINT" in out, "missing list must name the absent secrets")

    # Gate on, all secrets, no jsign jar: fail (signing was requested).
    with tempfile.TemporaryDirectory() as tmp:
        full = {name: "x" for name in SW.REQUIRED_SECRETS}
        full["ROUTER_SIGN_WINDOWS"] = "1"
        code, out = run_script(Path(tmp), full)
        require(code != 0 and "JSIGN_JAR" in out, f"full secrets without a jar must fail: {code} {out}")

    # Empty bin dir with a jar + fake java is a loud failure, not a silent pass.
    with tempfile.TemporaryDirectory() as tmp:
        fake_jar = Path(tmp) / "jsign.jar"
        fake_jar.write_bytes(b"PK")
        full = {name: "x" for name in SW.REQUIRED_SECRETS}
        full.update({"ROUTER_SIGN_WINDOWS": "1", "JSIGN_JAR": str(fake_jar), "PATH": "/usr/bin:/bin"})
        if not shutil_which_java():
            full.pop("PATH")
        code, out = run_script(Path(tmp), full)
        require(code != 0, f"no exe files must fail when signing is requested: {code} {out}")

    print("sign_windows_exes tests passed")
    return 0


def shutil_which_java() -> bool:
    import shutil
    return shutil.which("java") is not None


if __name__ == "__main__":
    raise SystemExit(main())
