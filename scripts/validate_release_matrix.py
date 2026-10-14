#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Run the release validation matrix checks that do not require live secrets."""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def command_output(cmd: list[str], fallback: str) -> str:
    try:
        return subprocess.check_output(cmd, cwd=ROOT, text=True, stderr=subprocess.DEVNULL).strip() or fallback
    except (FileNotFoundError, subprocess.CalledProcessError):
        return fallback


def seed_make_defaults() -> None:
    defaults = {
        "VERSION": command_output(["git", "describe", "--tags", "--always", "--dirty"], "dev"),
        "COMMIT": command_output(["git", "rev-parse", "--short", "HEAD"], "unknown"),
        "BUILD_DATE": command_output(["date", "-u", "+%Y-%m-%dT%H:%M:%SZ"], "unknown"),
        "DIST_DIR": "dist",
        "PKG_NAME": "metrum-ai-router",
        "GOOS": "linux",
        "GOARCH": command_output(["go", "env", "GOARCH"], "amd64"),
        "IMAGE_NAME": "metrum-ai-router",
        "IMAGE_TAG": os.environ.get("VERSION") or command_output(["git", "describe", "--tags", "--always", "--dirty"], "dev") + "-linux-" + command_output(["go", "env", "GOARCH"], "amd64"),
    }
    for key, value in defaults.items():
        os.environ.setdefault(key, value)


def run(label: str, cmd: list[str], *, optional: bool = False) -> bool:
    print(f"==> {label}")
    try:
        subprocess.run(cmd, cwd=ROOT, check=True)
        return True
    except FileNotFoundError:
        if optional:
            print(f"SKIP {label}: executable not found: {cmd[0]}")
            return True
        print(f"FAIL {label}: executable not found: {cmd[0]}", file=sys.stderr)
        return False
    except subprocess.CalledProcessError as exc:
        if optional:
            print(f"SKIP {label}: command exited {exc.returncode}")
            return True
        print(f"FAIL {label}: command exited {exc.returncode}", file=sys.stderr)
        return False


def list_package_archives(dist_dir: Path) -> list[Path]:
    return sorted([*dist_dir.glob("metrum-ai-router-*.tar.gz"), *dist_dir.glob("metrum-ai-router-*.zip")])


def check_artifacts() -> bool:
    dist_dir = ROOT / "dist"
    archives = list_package_archives(dist_dir)
    if not archives:
        print("SKIP artifact content validation: no release archives found")
        return True
    return run(
        "artifact content validation",
        [
            sys.executable,
            "scripts/validate_package_contents.py",
            "--allowlist",
            "scripts/package_docs_allowlist.txt",
            *[str(path.relative_to(ROOT)) for path in archives],
        ],
    )


def check_kubernetes() -> bool:
    overlay = ROOT / "deploy/kubernetes/overlays/example"
    if not overlay.exists():
        print("SKIP kubernetes validation: deploy/kubernetes/overlays/example is absent")
        return True
    if shutil.which("kubectl"):
        return run(
            "kubernetes kustomize overlay",
            ["kubectl", "kustomize", "deploy/kubernetes/overlays/example"],
        )
    if shutil.which("kustomize"):
        return run(
            "kubernetes kustomize overlay",
            ["kustomize", "build", "deploy/kubernetes/overlays/example"],
        )
    print("SKIP kubernetes validation: neither kubectl nor kustomize is installed")
    return True


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--include-artifacts",
        action="store_true",
        help="validate existing dist/*.tar.gz and dist/*.zip artifacts when present",
    )
    args = parser.parse_args()
    seed_make_defaults()

    checks = [
        ("build metadata validator", [sys.executable, "scripts/validate_build_metadata.py"]),
        ("release clean-tree validator", [sys.executable, "scripts/validate_release_clean_test.py"]),
        ("package content validator self-test", [sys.executable, "scripts/validate_package_contents_test.py"]),
        ("release artifact inventory self-test", [sys.executable, "scripts/release_artifact_inventory_test.py"]),
        ("release security evidence self-test", [sys.executable, "scripts/release_security_evidence_test.py"]),
        ("release matrix artifact selection self-test", [sys.executable, "scripts/validate_release_matrix_test.py"]),
        ("docker context validator", [sys.executable, "scripts/validate_docker_context.py"]),
        ("compose security validator", ["bash", "scripts/check_compose_security.sh"]),
    ]

    ok = True
    for label, cmd in checks:
        ok = run(label, cmd) and ok
    ok = check_kubernetes() and ok
    if args.include_artifacts:
        ok = check_artifacts() and ok

    if ok:
        print("release validation matrix passed")
        return 0
    print("release validation matrix failed", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
