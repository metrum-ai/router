#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Fail closed unless publish-time tools support the release commands we use."""

from __future__ import annotations

import argparse
import re
import shlex
import subprocess
import sys
from pathlib import Path

GH_MINIMUM = (2, 104, 0)
COSIGN_VERSION = "v3.1.4"
CRANE_VERSION = "0.22.1"
SYFT_VERSION = "1.57.1"
JSIGN_VERSION = "7.6"


def capture(command: list[str]) -> str:
    result = subprocess.run(command, capture_output=True, text=True)
    output = result.stdout + result.stderr
    if result.returncode:
        raise RuntimeError(f"command failed ({result.returncode}): {shlex.join(command)}")
    return output


def require_help(binary: list[str], args: list[str], required: tuple[str, ...]) -> None:
    output = capture([*binary, *args, "--help"])
    require_tokens([*binary, *args], output, required)


def require_tokens(command: list[str], output: str, required: tuple[str, ...]) -> None:
    missing = [flag for flag in required if flag not in output]
    if missing:
        raise RuntimeError(f"{shlex.join(command)} help lacks required flags: {', '.join(missing)}")


def version_tuple(value: str) -> tuple[int, ...]:
    parts = re.findall(r"\d+", value)
    if not parts:
        raise RuntimeError(f"cannot parse tool version: {value.strip()}")
    return tuple(int(part) for part in parts[:3])


def verify_gh(gh: list[str]) -> None:
    output = capture([*gh, "version"])
    match = re.search(r"gh version (\d+(?:\.\d+){1,2})", output)
    if match is None or version_tuple(match.group(1)) < GH_MINIMUM:
        raise RuntimeError(f"GitHub CLI must be at least 2.104.0; got {output.splitlines()[0] if output else 'no version output'}")
    commands = {
        ("api",): (),
        ("release", "list"): ("--repo", "--limit", "--json"),
        ("release", "download"): ("-R", "--pattern", "--dir"),
        ("release", "view"): ("--json",),
        ("release", "create"): ("--verify-tag", "--title", "--notes-file", "--prerelease", "--latest"),
        ("attestation", "verify"): ("--repo", "--signer-workflow", "--source-ref"),
    }
    for path, flags in commands.items():
        require_help(gh, list(path), flags)


def verify_crane(crane: list[str]) -> None:
    output = capture([*crane, "version"])
    if CRANE_VERSION not in output:
        raise RuntimeError(f"crane must be {CRANE_VERSION}; got {output.splitlines()[0] if output else 'no version output'}")
    checks = {
        ("auth", "login"): ("--username", "--password-stdin"),
        ("mutate",): ("--append", "--platform", "--entrypoint", "--cmd", "--user", "--workdir", "--exposed-ports", "--set-platform", "--label", "--output"),
        ("digest",): ("--tarball",),
        ("manifest",): (),
        ("push",): (),
        ("index", "append"): ("-m", "--manifest", "-t", "--tag"),
    }
    for path, flags in checks.items():
        require_help(crane, list(path), flags)


def verify_cosign(cosign: list[str]) -> None:
    output = capture([*cosign, "version"])
    if COSIGN_VERSION not in output:
        raise RuntimeError(f"cosign must be {COSIGN_VERSION}; got {output.splitlines()[0] if output else 'no version output'}")
    checks = {
        ("attach", "sbom"): ("--sbom",),
        ("sign-blob",): ("--yes", "--bundle"),
        ("verify-blob",): ("--bundle", "--certificate-identity", "--certificate-oidc-issuer"),
        ("sign",): ("--yes",),
        ("verify",): ("--certificate-identity", "--certificate-oidc-issuer"),
    }
    for path, flags in checks.items():
        require_help(cosign, list(path), flags)


def verify_syft(syft: list[str]) -> None:
    output = capture([*syft, "version"])
    if SYFT_VERSION not in output:
        raise RuntimeError(f"syft must be {SYFT_VERSION}; got {output.splitlines()[0] if output else 'no version output'}")
    require_help(syft, [], ("-o", "--output"))


def verify_java_and_jsign(java: list[str], jsign_jar: Path) -> None:
    output = capture([*java, "-version"])
    match = re.search(r'version "(\d+)', output)
    if match is None or int(match.group(1)) != 21:
        raise RuntimeError(f"Java 21 is required; got {output.splitlines()[0] if output else 'no version output'}")
    if not jsign_jar.is_file():
        raise RuntimeError("pinned jsign jar is missing")
    version_output = capture([*java, "-jar", str(jsign_jar), "--version"])
    if JSIGN_VERSION not in version_output:
        raise RuntimeError(f"jsign must be {JSIGN_VERSION}; got {version_output.splitlines()[0] if version_output else 'no version output'}")
    help_output = capture([*java, "-jar", str(jsign_jar), "--help"])
    for flag in ("--storetype", "--keystore", "--storepass", "--alias", "--tsaurl", "--tsmode"):
        if flag not in help_output:
            raise RuntimeError(f"jsign help lacks required flag: {flag}")


def verify_host_commands() -> None:
    require_tokens(["curl", "--help", "all"], capture(["curl", "--help", "all"]), ("--fail", "--location", "--retry", "--output", "--silent", "--show-error"))
    require_help(["sha256sum"], [], ("--check",))
    require_help(["tar"], [], ("--extract", "--gzip", "--file", "--strip-components"))
    require_help(["find"], [], ("-maxdepth", "-type", "-name"))
    require_help(["sort"], [], ())
    capture(["git", "tag", "-l", "--format=%(refname:short)"])
    capture(["python3", "--version"])


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gh", required=True, type=Path)
    parser.add_argument("--crane", required=True, type=Path)
    parser.add_argument("--cosign", default="cosign")
    parser.add_argument("--syft", required=True, help="Syft executable or command")
    parser.add_argument("--java", default="java")
    parser.add_argument("--jsign-jar", required=True, type=Path)
    args = parser.parse_args()
    try:
        verify_gh([str(args.gh)])
        verify_crane([str(args.crane)])
        verify_cosign(shlex.split(args.cosign))
        verify_syft(shlex.split(args.syft))
        verify_java_and_jsign(shlex.split(args.java), args.jsign_jar)
        verify_host_commands()
    except (OSError, RuntimeError, subprocess.SubprocessError) as exc:
        print(f"publish tool contract failed: {exc}", file=sys.stderr)
        return 2
    print("publish tool contract passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
