#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Backup release package tarballs from DIST_DIR to an operator-configured restic repo.

Credentials come from ignored ops.env.json (preferred), env.json (legacy mixed),
or the process environment:
  BACKUP_USER, BACKUP_PASS, RESTIC_PASSWORD

Repository (one of):
  RESTIC_REPOSITORY  full restic repo URL
  RESTIC_REPO_HOST + RESTIC_REPO_PATH  with BACKUP_USER/BACKUP_PASS
    (no compiled-in host or path defaults; both host and path are required)

Local dist/ artifacts keep the version/hash in their filenames. Before upload,
this script stages one complete release set under stable basenames so restic
snapshots always replace the same paths:

  metrum-ai-router-linux-amd64.tar.gz
  metrum-ai-router-linux-arm64.tar.gz
  metrum-ai-router-docker-linux-amd64.tar.gz
  metrum-ai-router-docker-linux-arm64.tar.gz

Version identity is recorded in restic tags (version:<id>). Customer license
payloads are never packaged and are not included in this backup.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import urllib.parse
from collections import defaultdict
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_OPS_ENV_JSON = ROOT / "ops.env.json"
DEFAULT_ENV_JSON = ROOT / "env.json"
# Fixed staging path so restic snapshot paths stay identical across uploads.
# tmp/ is gitignored; local hashed dist/ filenames are left unchanged.
STABLE_STAGE_DIR = ROOT / "tmp" / "restic-dist-upload"
PACKAGE_GLOB = "*.tar.gz"
DOCKER_PACKAGE_RE = re.compile(
    r"^metrum-ai-router-.+-docker-linux-(amd64|arm64)\.tar\.gz$"
)
BINARY_PACKAGE_RE = re.compile(
    r"^metrum-ai-router-.+-linux-(amd64|arm64)\.tar\.gz$"
)
REQUIRED_KINDS = (
    ("binary", "amd64"),
    ("binary", "arm64"),
    ("docker", "amd64"),
    ("docker", "arm64"),
)
STABLE_NAMES = {
    ("binary", "amd64"): "metrum-ai-router-linux-amd64.tar.gz",
    ("binary", "arm64"): "metrum-ai-router-linux-arm64.tar.gz",
    ("docker", "amd64"): "metrum-ai-router-docker-linux-amd64.tar.gz",
    ("docker", "arm64"): "metrum-ai-router-docker-linux-arm64.tar.gz",
}


def is_docker_package(name: str) -> bool:
    return DOCKER_PACKAGE_RE.match(name) is not None


def is_binary_package(name: str) -> bool:
    return BINARY_PACKAGE_RE.match(name) is not None and not is_docker_package(name)


def is_release_package(name: str) -> bool:
    return is_binary_package(name) or is_docker_package(name)


def package_arch(name: str) -> str | None:
    match = re.search(r"linux-(amd64|arm64)\.tar\.gz$", name)
    return match.group(1) if match else None


def package_kind(name: str) -> str | None:
    if is_docker_package(name):
        return "docker"
    if is_binary_package(name):
        return "binary"
    return None


def package_version(name: str) -> str | None:
    if is_docker_package(name):
        match = re.match(
            r"^metrum-ai-router-(.+)-docker-linux-(?:amd64|arm64)\.tar\.gz$",
            name,
        )
    elif is_binary_package(name):
        match = re.match(
            r"^metrum-ai-router-(.+)-linux-(?:amd64|arm64)\.tar\.gz$",
            name,
        )
    else:
        return None
    return match.group(1) if match else None


def stable_package_name(path: Path) -> str:
    kind = package_kind(path.name)
    arch = package_arch(path.name)
    if kind is None or arch is None:
        raise SystemExit(f"unable to map stable name for {path.name}")
    return STABLE_NAMES[(kind, arch)]


def list_release_packages(dist_dir: Path) -> list[Path]:
    if not dist_dir.is_dir():
        raise SystemExit(f"dist directory not found: {dist_dir}")
    return sorted(
        path
        for path in dist_dir.glob(PACKAGE_GLOB)
        if path.is_file() and is_release_package(path.name)
    )


def missing_required_packages(packages: list[Path]) -> list[str]:
    present = {
        (package_kind(path.name), package_arch(path.name))
        for path in packages
        if package_kind(path.name) and package_arch(path.name)
    }
    missing: list[str] = []
    for kind, arch in REQUIRED_KINDS:
        if (kind, arch) not in present:
            missing.append(f"{kind} linux-{arch}")
    return missing


def group_packages_by_version(packages: list[Path]) -> dict[str, list[Path]]:
    groups: dict[str, list[Path]] = defaultdict(list)
    for path in packages:
        version = package_version(path.name)
        if version is None:
            raise SystemExit(f"unable to parse package version from {path.name}")
        groups[version].append(path)
    return dict(groups)


def select_release_set(
    packages: list[Path],
    requested_version: str | None = None,
) -> tuple[str, list[Path]]:
    if not packages:
        raise SystemExit(
            "no release package tarballs found; "
            "run `make package-all package-docker-all` first"
        )
    groups = group_packages_by_version(packages)
    complete: dict[str, list[Path]] = {}
    for version, members in groups.items():
        # One artifact per kind/arch; reject duplicate kind/arch for a version.
        keyed: dict[tuple[str, str], Path] = {}
        for path in members:
            kind = package_kind(path.name)
            arch = package_arch(path.name)
            if kind is None or arch is None:
                continue
            key = (kind, arch)
            if key in keyed:
                raise SystemExit(
                    f"duplicate {kind} linux-{arch} packages for version {version}: "
                    f"{keyed[key].name} and {path.name}"
                )
            keyed[key] = path
        selected = [keyed[key] for key in REQUIRED_KINDS if key in keyed]
        if not missing_required_packages(selected):
            complete[version] = [keyed[key] for key in REQUIRED_KINDS]

    if requested_version:
        if requested_version not in complete:
            available = ", ".join(sorted(complete)) or "none"
            raise SystemExit(
                f"complete release set for VERSION={requested_version} not found "
                f"under dist/; complete versions available: {available}"
            )
        return requested_version, complete[requested_version]

    if not complete:
        raise SystemExit(
            "incomplete release set for CTO backup; need binary+docker "
            "linux-amd64 and linux-arm64 for one VERSION. "
            "Build with `make package-all package-docker-all`."
        )

    # Prefer the complete set with the newest member mtime when files exist.
    def set_mtime(item: str) -> float:
        mtimes = []
        for path in complete[item]:
            try:
                mtimes.append(path.stat().st_mtime)
            except OSError:
                continue
        return max(mtimes) if mtimes else 0.0

    version = max(complete, key=set_mtime)
    return version, complete[version]


def version_tags(version: str, packages: list[Path]) -> list[str]:
    tags = {
        "cto",
        "metrum-ai-router",
        "release-packages",
        f"version:{version}",
    }
    for path in packages:
        kind = package_kind(path.name)
        if kind == "docker":
            tags.add("customer-docker")
        elif kind == "binary":
            tags.add("release-binary")
    return sorted(tags)


def stage_stable_packages(packages: list[Path], stage_dir: Path) -> list[Path]:
    if stage_dir.exists():
        shutil.rmtree(stage_dir)
    stage_dir.mkdir(parents=True, exist_ok=True)
    staged: list[Path] = []
    for path in packages:
        dest = stage_dir / stable_package_name(path)
        try:
            os.link(path, dest)
        except OSError:
            shutil.copy2(path, dest)
        staged.append(dest)
    return staged


def load_env_json(path: Path) -> dict[str, str]:
    if not path.is_file():
        return {}
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        raise SystemExit(f"{path}: must contain a JSON object")
    out: dict[str, str] = {}
    for key, value in data.items():
        if isinstance(value, str) and value != "":
            out[str(key)] = value
    return out


def merge_credentials(env_file: dict[str, str]) -> dict[str, str]:
    merged = dict(env_file)
    for key in (
        "BACKUP_USER",
        "BACKUP_PASS",
        "RESTIC_PASSWORD",
        "RESTIC_REPOSITORY",
        "RESTIC_REPO_HOST",
        "RESTIC_REPO_PATH",
    ):
        value = os.environ.get(key)
        if value:
            merged[key] = value
    return merged


def require_credential(creds: dict[str, str], key: str) -> str:
    value = creds.get(key, "").strip()
    if not value:
        raise SystemExit(
            f"{key} is required in ignored ops.env.json (or legacy env.json) "
            "or the process environment"
        )
    return value


def load_ops_credentials(env_json: Path) -> dict[str, str]:
    """Prefer ops.env.json; fall back to --env-json / legacy env.json."""
    merged: dict[str, str] = {}
    if DEFAULT_OPS_ENV_JSON.is_file():
        merged.update(load_env_json(DEFAULT_OPS_ENV_JSON))
    if env_json.is_file():
        # Legacy mixed env.json fills only keys still missing.
        for key, value in load_env_json(env_json).items():
            merged.setdefault(key, value)
    return merged


def build_repository_url(creds: dict[str, str]) -> str:
    explicit = creds.get("RESTIC_REPOSITORY", "").strip()
    if explicit:
        return explicit
    user = urllib.parse.quote(require_credential(creds, "BACKUP_USER"), safe="")
    password = urllib.parse.quote(require_credential(creds, "BACKUP_PASS"), safe="")
    host = require_credential(creds, "RESTIC_REPO_HOST")
    path = require_credential(creds, "RESTIC_REPO_PATH").strip().strip("/")
    if not path:
        raise SystemExit(
            "RESTIC_REPO_PATH is required in ignored ops.env.json (or legacy env.json) "
            "or the process environment"
        )
    return f"rest:https://{user}:{password}@{host}/{path}"


def run_restic(
    repository: str,
    restic_password: str,
    args: list[str],
    *,
    dry_run: bool,
    cwd: Path | None = None,
) -> None:
    if shutil.which("restic") is None:
        raise SystemExit("restic is required on PATH")
    env = os.environ.copy()
    env["RESTIC_REPOSITORY"] = repository
    env["RESTIC_PASSWORD"] = restic_password
    # Prefer env-based repo so argv does not echo the HTTP password.
    cmd = ["restic", *args]
    if dry_run:
        where = f" (cwd={cwd})" if cwd is not None else ""
        print(f"dry-run: restic {' '.join(args)}{where}")
        return
    subprocess.run(cmd, check=True, env=env, cwd=cwd)


def backup_packages(
    packages: list[Path],
    version: str,
    creds: dict[str, str],
    *,
    dry_run: bool,
) -> None:
    repository = build_repository_url(creds)
    restic_password = require_credential(creds, "RESTIC_PASSWORD")
    tags = version_tags(version, packages)
    tag_args: list[str] = []
    for tag in tags:
        tag_args.extend(["--tag", tag])

    print(f"backing up release {version} ({len(packages)} package(s)) with stable restic names:")
    for path in packages:
        kind = "customer-docker" if is_docker_package(path.name) else "release-binary"
        print(f"  - {path.name} -> {stable_package_name(path)} ({kind})")

    staged = stage_stable_packages(packages, STABLE_STAGE_DIR)
    try:
        # Backup the fixed staging directory so every snapshot replaces the same
        # absolute paths under tmp/restic-dist-upload/.
        run_restic(
            repository,
            restic_password,
            ["backup", *tag_args, "--", str(STABLE_STAGE_DIR)],
            dry_run=dry_run,
        )
        print(f"staged {len(staged)} stable package path(s) under {STABLE_STAGE_DIR}")
    finally:
        if STABLE_STAGE_DIR.exists():
            shutil.rmtree(STABLE_STAGE_DIR, ignore_errors=True)
    print("restic backup complete")


def self_test() -> None:
    with_creds = {
        "BACKUP_USER": "backup-user",
        "BACKUP_PASS": "example-pass",
        "RESTIC_PASSWORD": "example-restic",
        "RESTIC_REPO_HOST": "example.com",
        "RESTIC_REPO_PATH": "backups/metrum-ai-router",
    }
    url = build_repository_url(with_creds)
    expected = "rest:https://backup-user:example-pass@example.com/backups/metrum-ai-router"
    if url != expected:
        raise AssertionError(f"repository URL mismatch: {url!r}")

    encoded = build_repository_url(
        {
            "BACKUP_USER": "u/n",
            "BACKUP_PASS": "p@ss:word",
            "RESTIC_PASSWORD": "x",
            "RESTIC_REPO_HOST": "example.com",
            "RESTIC_REPO_PATH": "backups/metrum-ai-router",
        }
    )
    if "u%2Fn" not in encoded or "p%40ss%3Aword" not in encoded:
        raise AssertionError(f"credentials were not URL-encoded: {encoded!r}")

    try:
        build_repository_url(
            {
                "BACKUP_USER": "backup-user",
                "BACKUP_PASS": "example-pass",
                "RESTIC_PASSWORD": "example-restic",
            }
        )
    except SystemExit:
        pass
    else:
        raise AssertionError("expected missing RESTIC_REPO_HOST/PATH to fail")

    explicit = build_repository_url({"RESTIC_REPOSITORY": "rest:https://example/repo"})
    if explicit != "rest:https://example/repo":
        raise AssertionError("explicit RESTIC_REPOSITORY override failed")

    names = [
        Path("metrum-ai-router-v1-linux-amd64.tar.gz"),
        Path("metrum-ai-router-v1-linux-arm64.tar.gz"),
        Path("metrum-ai-router-v1-docker-linux-amd64.tar.gz"),
        Path("metrum-ai-router-v1-docker-linux-arm64.tar.gz"),
    ]
    if missing_required_packages(names):
        raise AssertionError("complete set reported missing packages")
    if not missing_required_packages(names[:2]):
        raise AssertionError("incomplete set should report missing docker packages")
    if missing_required_packages(names[2:]) != ["binary linux-amd64", "binary linux-arm64"]:
        raise AssertionError("docker-only set should report missing binary packages")
    if is_binary_package("metrum-ai-router-v1-docker-linux-amd64.tar.gz"):
        raise AssertionError("docker package must not classify as binary")
    if package_version("metrum-ai-router-v1-docker-linux-amd64.tar.gz") != "v1":
        raise AssertionError("docker package version parse failed")

    if stable_package_name(names[0]) != "metrum-ai-router-linux-amd64.tar.gz":
        raise AssertionError("binary stable name mismatch")
    if stable_package_name(names[2]) != "metrum-ai-router-docker-linux-amd64.tar.gz":
        raise AssertionError("docker stable name mismatch")

    version, selected = select_release_set(names, requested_version="v1")
    if version != "v1" or len(selected) != 4:
        raise AssertionError("select_release_set failed for explicit version")

    mixed = names + [
        Path("metrum-ai-router-old-linux-amd64.tar.gz"),
        Path("metrum-ai-router-old-linux-arm64.tar.gz"),
    ]
    version, selected = select_release_set(mixed)
    if version != "v1" or len(selected) != 4:
        raise AssertionError("select_release_set should prefer the only complete set")

    tags = version_tags("v1", names)
    for required in (
        "cto",
        "metrum-ai-router",
        "release-packages",
        "release-binary",
        "customer-docker",
        "version:v1",
    ):
        if required not in tags:
            raise AssertionError(f"missing tag {required!r} in {tags}")
    if "version:v1-docker" in tags:
        raise AssertionError(f"docker suffix leaked into version tag: {tags}")

    with tempfile.TemporaryDirectory() as tmp:
        stage = Path(tmp)
        source_dir = stage / "src"
        source_dir.mkdir()
        sources = []
        for name in names:
            src = source_dir / name.name
            src.write_bytes(b"pkg")
            sources.append(src)
        staged = stage_stable_packages(sources, stage / "out")
        got = sorted(path.name for path in staged)
        expected_names = sorted(STABLE_NAMES.values())
        if got != expected_names:
            raise AssertionError(f"staged names mismatch: {got} != {expected_names}")

    print("backup_dist_restic self-test passed")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist-dir", default=os.environ.get("DIST_DIR", "dist"))
    parser.add_argument("--env-json", type=Path, default=DEFAULT_ENV_JSON)
    parser.add_argument(
        "--version",
        default=os.environ.get("VERSION"),
        help="Release version/hash to archive (default: VERSION env or newest complete set)",
    )
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()

    if args.self_test:
        self_test()
        return 0

    dist_dir = Path(args.dist_dir)
    if not dist_dir.is_absolute():
        dist_dir = ROOT / dist_dir

    creds = merge_credentials(load_ops_credentials(args.env_json))
    packages = list_release_packages(dist_dir)
    version, selected = select_release_set(packages, requested_version=args.version)
    backup_packages(selected, version, creds, dry_run=args.dry_run)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except subprocess.CalledProcessError as exc:
        print(f"restic failed with exit code {exc.returncode}", file=sys.stderr)
        raise SystemExit(exc.returncode) from exc
