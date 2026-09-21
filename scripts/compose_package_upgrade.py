#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Deterministic Docker Compose package upgrade for an existing install root.

Upgrades copy live runtime forward from a timestamped backup. They never glob-move
from `/`, never stop or reboot the host, and never invoke EKS tooling.
"""

from __future__ import annotations

import argparse
import datetime as dt
import os
import re
import shutil
import subprocess
import sys
import tarfile
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Sequence


PACKAGE_FILE_RE = re.compile(
    r"^metrum-ai-router-(?P<version>.+)-docker-linux-(?P<arch>amd64|arm64)\.tar\.gz$"
)
PACKAGE_DIR_RE = re.compile(
    r"^metrum-ai-router-(?P<version>.+)-docker-linux-(?P<arch>amd64|arm64)$"
)
IMAGE_TAR_RE = re.compile(r"^metrum-ai-router-.+-linux-(amd64|arm64)\.tar$")
FORBIDDEN_PATHS = {"/", "/usr", "/bin", "/opt", "/etc", "/var", "/home", "/root"}
RUNTIME_DIRS = ("compose/config", "compose/state", "compose/logs")
RUNTIME_ENV = Path("compose/.env")
TOKEN_NAME_RE = re.compile(r"^ROUTER_TOKEN.*\.txt$")
RUNTIME_UID = 65532
RUNTIME_GID = 65532
POSTGRES_OVERRIDE = "docker-compose.postgres-localhost.yml"


@dataclass(frozen=True)
class PackageInfo:
    path: Path
    version: str
    arch: str
    top_dir: str
    image_tag: str
    version_env: str


class UpgradeError(RuntimeError):
    pass


def utc_stamp(now: dt.datetime | None = None) -> str:
    stamp = now or dt.datetime.now(dt.timezone.utc)
    return stamp.astimezone(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")


def resolved(path: Path) -> Path:
    return path.expanduser().resolve()


def reject_forbidden(path: Path, *, label: str) -> Path:
    expanded = path.expanduser()
    resolved_path = expanded.resolve()
    names = {str(path), str(expanded), str(resolved_path)}
    if names & FORBIDDEN_PATHS:
        raise UpgradeError(f"{label} {resolved_path} is a forbidden path")
    return resolved_path


def parse_package_filename(path: Path) -> tuple[str, str]:
    match = PACKAGE_FILE_RE.match(path.name)
    if not match:
        raise UpgradeError(
            f"package name {path.name} must match metrum-ai-router-<version>-docker-linux-<arch>.tar.gz"
        )
    return match.group("version"), match.group("arch")


def package_top_dir(package: Path) -> str:
    with tarfile.open(package, "r:gz") as archive:
        names = [member.name for member in archive.getmembers() if member.name]
    tops = {name.split("/", 1)[0] for name in names if name not in (".",)}
    if len(tops) != 1:
        raise UpgradeError(f"package {package} must contain exactly one top-level directory, found {sorted(tops)}")
    top = next(iter(tops))
    if not PACKAGE_DIR_RE.match(top):
        raise UpgradeError(f"package top-level directory {top} is not a Docker Compose package directory")
    return top


def inspect_package(package: Path) -> PackageInfo:
    path = resolved(package)
    if not path.is_file():
        raise UpgradeError(f"package {path} does not exist")
    version, arch = parse_package_filename(path)
    top = package_top_dir(path)
    top_match = PACKAGE_DIR_RE.match(top)
    if top_match and (top_match.group("version"), top_match.group("arch")) != (version, arch):
        raise UpgradeError(f"package top-level directory {top} does not match filename {path.name}")
    return PackageInfo(
        path=path,
        version=version,
        arch=arch,
        top_dir=top,
        image_tag=f"metrum-ai-router:{version}-linux-{arch}",
        version_env=f"{version}-linux-{arch}",
    )


def unpack_package(package: Path, staging: Path) -> None:
    staging = reject_forbidden(staging, label="staging")
    if staging.exists():
        raise UpgradeError(f"staging directory {staging} already exists")
    staging.mkdir(parents=True, exist_ok=False)
    completed = subprocess.run(
        ["tar", "-C", str(staging), "--strip-components=1", "-xzf", str(package)],
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    if completed.returncode != 0:
        raise UpgradeError(f"tar unpack failed: {completed.stderr.strip() or completed.stdout.strip()}")


def image_tar_in(staging: Path, arch: str) -> Path:
    images = staging / "images"
    if not images.is_dir():
        raise UpgradeError(f"staging {staging} is missing images/")
    matches = [
        path
        for path in images.iterdir()
        if path.is_file() and IMAGE_TAR_RE.match(path.name) and path.name.endswith(f"-linux-{arch}.tar")
    ]
    if len(matches) != 1:
        raise UpgradeError(f"staging {staging} must contain exactly one linux-{arch} image tar, found {[p.name for p in matches]}")
    return matches[0]


def validate_staging(staging: Path, info: PackageInfo) -> Path:
    staging = reject_forbidden(staging, label="staging")
    compose_file = staging / "compose" / "docker-compose.yml"
    if not compose_file.is_file():
        raise UpgradeError(f"staging {staging} is missing compose/docker-compose.yml")
    return image_tar_in(staging, info.arch)


def token_files(compose_dir: Path) -> list[Path]:
    if not compose_dir.is_dir():
        return []
    return sorted(
        path
        for path in compose_dir.iterdir()
        if path.is_file() and TOKEN_NAME_RE.match(path.name)
    )


def require_runtime(root: Path, *, label: str) -> None:
    missing = [rel for rel in ("compose/config", "compose/.env") if not (root / rel).exists()]
    if missing:
        raise UpgradeError(f"{label} {root} is missing required runtime paths: {', '.join(missing)}")


def copy_runtime(backup: Path, install_root: Path) -> list[str]:
    copied: list[str] = []
    require_runtime(backup, label="backup")
    for rel in RUNTIME_DIRS:
        source = backup / rel
        dest = install_root / rel
        if not source.exists():
            continue
        if dest.exists():
            shutil.rmtree(dest)
        shutil.copytree(source, dest, symlinks=True)
        copied.append(rel)
    env_source = backup / RUNTIME_ENV
    dest_env = install_root / RUNTIME_ENV
    dest_env.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(env_source, dest_env)
    copied.append(str(RUNTIME_ENV))
    for token in token_files(backup / "compose"):
        shutil.copy2(token, install_root / "compose" / token.name)
        copied.append(f"compose/{token.name}")
    return copied


def pin_version_env(env_path: Path, version_env: str) -> None:
    if not env_path.is_file():
        raise UpgradeError(f"{env_path} is missing")
    lines = env_path.read_text(encoding="utf-8").splitlines()
    replaced = False
    out: list[str] = []
    for line in lines:
        if line.startswith("SMART_LLMROUTER_VERSION="):
            out.append(f"SMART_LLMROUTER_VERSION={version_env}")
            replaced = True
        else:
            out.append(line)
    if not replaced:
        out.append(f"SMART_LLMROUTER_VERSION={version_env}")
    env_path.write_text("\n".join(out) + "\n", encoding="utf-8")


def backup_path(install_root: Path, suffix: str, stamp: str) -> Path:
    if not suffix or "/" in suffix or ".." in suffix:
        raise UpgradeError("backup suffix must be a single path-safe token")
    return install_root.parent / f"{install_root.name}.backup-{suffix}-{stamp}"


def staging_path(install_root: Path, stamp: str) -> Path:
    return install_root.parent / f".{install_root.name}-upgrade-staging-{stamp}"


def postgres_override_enabled(env_path: Path) -> bool:
    if not env_path.is_file():
        return False
    for line in env_path.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if stripped.startswith("ROUTER_USAGE_DB_DSN=") and not stripped.startswith("#"):
            return True
    return False


def compose_file_args(install_root: Path) -> list[str]:
    args = ["-f", "docker-compose.yml"]
    override = install_root / "compose" / POSTGRES_OVERRIDE
    if postgres_override_enabled(install_root / RUNTIME_ENV) and override.is_file():
        args.extend(["-f", POSTGRES_OVERRIDE])
    return args


def chown_runtime(install_root: Path) -> None:
    for rel in RUNTIME_DIRS:
        target = install_root / rel
        if not target.exists():
            continue
        for dirpath, dirnames, filenames in os.walk(target, topdown=False):
            os.chown(dirpath, RUNTIME_UID, RUNTIME_GID)
            for name in dirnames + filenames:
                os.chown(os.path.join(dirpath, name), RUNTIME_UID, RUNTIME_GID)
        os.chown(target, RUNTIME_UID, RUNTIME_GID)
    env_json = install_root / "compose" / "config" / "env.json"
    if env_json.is_file():
        os.chmod(env_json, 0o400)
    config_dir = install_root / "compose" / "config"
    if config_dir.is_dir():
        os.chmod(config_dir, 0o750)


def compose_run(
    run: Callable[..., subprocess.CompletedProcess[str]],
    install_root: Path,
    args: Sequence[str],
) -> subprocess.CompletedProcess[str]:
    compose_dir = install_root / "compose"
    return run(["docker", "compose", *compose_file_args(install_root), *args], cwd=str(compose_dir))


def run_compose(install_root: Path, info: PackageInfo, *, skip_compose: bool, runner: Callable[..., subprocess.CompletedProcess[str]] | None = None) -> list[str]:
    if skip_compose:
        return ["compose skipped"]
    chown_runtime(install_root)
    run = runner or (lambda args, **kwargs: subprocess.run(args, check=False, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs))
    image_tar = image_tar_in(install_root, info.arch)
    load = run(["docker", "load", "-i", str(image_tar)])
    if load.returncode != 0:
        raise UpgradeError(f"docker load failed: {load.stderr.strip() or load.stdout.strip()}")
    config = compose_run(run, install_root, ["config"])
    if config.returncode != 0:
        raise UpgradeError(f"docker compose config failed: {config.stderr.strip() or config.stdout.strip()}")
    up = compose_run(run, install_root, ["up", "-d"])
    if up.returncode != 0:
        raise UpgradeError(f"docker compose up failed: {up.stderr.strip() or up.stdout.strip()}")
    ps = compose_run(run, install_root, ["ps", "--format", "{{.Name}} {{.Image}} {{.Status}}"])
    if ps.returncode != 0:
        raise UpgradeError(f"docker compose ps failed: {ps.stderr.strip() or ps.stdout.strip()}")
    return [line for line in ps.stdout.splitlines() if line.strip()]


def plan_upgrade(info: PackageInfo, install_root: Path | None, suffix: str, stamp: str) -> dict[str, object]:
    copy_list = list(RUNTIME_DIRS) + [str(RUNTIME_ENV), "compose/ROUTER_TOKEN*.txt"]
    planned: dict[str, object] = {
        "package": str(info.path),
        "version": info.version,
        "arch": info.arch,
        "image_tag": info.image_tag,
        "version_env": info.version_env,
        "top_dir": info.top_dir,
        "unpack": "tar --strip-components=1",
        "copy": copy_list,
        "skip_instance_reboot": True,
    }
    if install_root is not None:
        root = reject_forbidden(install_root, label="install-root")
        planned["install_root"] = str(root)
        planned["backup"] = str(backup_path(root, suffix, stamp))
        planned["staging"] = str(staging_path(root, stamp))
        if root.exists():
            planned["existing_tokens"] = [path.name for path in token_files(root / "compose")]
            planned["postgres_override"] = postgres_override_enabled(root / RUNTIME_ENV)
    return planned


def apply_local(
    info: PackageInfo,
    install_root: Path,
    suffix: str,
    stamp: str,
    *,
    skip_compose: bool = False,
) -> dict[str, object]:
    root = reject_forbidden(install_root, label="install-root")
    if not root.is_dir():
        raise UpgradeError(f"install-root {root} does not exist")
    require_runtime(root, label="install-root")
    backup = backup_path(root, suffix, stamp)
    staging = staging_path(root, stamp)
    if backup.exists() or staging.exists():
        raise UpgradeError("backup or staging path already exists")
    unpack_package(info.path, staging)
    try:
        image = validate_staging(staging, info)
        shutil.move(str(root), str(backup))
        shutil.move(str(staging), str(root))
        copied = copy_runtime(backup, root)
        pin_version_env(root / RUNTIME_ENV, info.version_env)
        if not skip_compose:
            chown_runtime(root)
        services = run_compose(root, info, skip_compose=skip_compose)
    except Exception:
        if staging.exists() and root.exists():
            shutil.rmtree(staging)
        raise
    return {
        "backup": str(backup),
        "install_root": str(root),
        "image_tar": image.name,
        "copied": copied,
        "version_env": info.version_env,
        "services": services,
    }


def rollback_local(backup: Path, install_root: Path, stamp: str, *, skip_compose: bool = False) -> dict[str, object]:
    root = reject_forbidden(install_root, label="install-root")
    source = reject_forbidden(backup, label="backup")
    if not source.is_dir():
        raise UpgradeError(f"backup {source} does not exist")
    require_runtime(source, label="backup")
    replaced = None
    if root.exists():
        replaced = root.parent / f"{root.name}.replaced-{stamp}"
        shutil.move(str(root), str(replaced))
    shutil.move(str(source), str(root))
    services = ["compose skipped"]
    if not skip_compose:
        chown_runtime(root)
        files = compose_file_args(root)
        ps = subprocess.run(
            ["docker", "compose", *files, "up", "-d"],
            cwd=str(root / "compose"),
            check=False,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        if ps.returncode != 0:
            raise UpgradeError(f"docker compose up failed: {ps.stderr.strip() or ps.stdout.strip()}")
        listing = subprocess.run(
            ["docker", "compose", *files, "ps", "--format", "{{.Name}} {{.Image}} {{.Status}}"],
            cwd=str(root / "compose"),
            check=False,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        services = [line for line in listing.stdout.splitlines() if line.strip()]
    result: dict[str, object] = {"install_root": str(root), "services": services}
    if replaced is not None:
        result["replaced"] = str(replaced)
    return result


def ssh_args(identity: Path | None) -> list[str]:
    args = ["ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"]
    if identity is not None:
        args.extend(["-i", str(identity)])
    return args


def scp_args(identity: Path | None) -> list[str]:
    args = ["scp", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"]
    if identity is not None:
        args.extend(["-i", str(identity)])
    return args


def run_remote(remote: str, identity: Path | None, argv: Sequence[str], files: Sequence[Path]) -> int:
    for path in files:
        completed = subprocess.run([*scp_args(identity), str(path), f"{remote}:/tmp/{path.name}"], check=False)
        if completed.returncode != 0:
            raise UpgradeError(f"scp {path.name} to {remote} failed")
    remote_cmd = [
        "sudo",
        "python3",
        f"/tmp/{Path(argv[0]).name}",
        *argv[1:],
        "--package",
        f"/tmp/{files[0].name}",
        "--cleanup-remote-files",
    ]
    completed = subprocess.run([*ssh_args(identity), remote, *remote_cmd], check=False)
    return completed.returncode


def print_plan(plan: dict[str, object]) -> None:
    for key in (
        "package",
        "version",
        "arch",
        "image_tag",
        "version_env",
        "top_dir",
        "unpack",
        "install_root",
        "backup",
        "staging",
        "copy",
        "existing_tokens",
        "postgres_override",
        "skip_instance_reboot",
    ):
        if key in plan:
            print(f"{key}: {plan[key]}")


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    sub = parser.add_subparsers(dest="command", required=True)

    def add_common(cmd: argparse.ArgumentParser, *, require_package: bool) -> None:
        if require_package:
            cmd.add_argument("--package", required=True, type=Path)
        cmd.add_argument("--install-root", type=Path, default=Path("/opt/metrum-ai-router"))
        cmd.add_argument("--backup-suffix", default="compose-upgrade")
        cmd.add_argument("--utc", default=None, help="UTC stamp override YYYYMMDDTHHMMSSZ")
        cmd.add_argument("--remote", default=None)
        cmd.add_argument("--ssh-identity", type=Path, default=None)
        cmd.add_argument("--skip-compose", action="store_true")
        cmd.add_argument("--cleanup-remote-files", action="store_true")

    add_common(sub.add_parser("plan", help="inspect package and print the upgrade plan"), require_package=True)
    add_common(sub.add_parser("apply", help="backup, unpack, copy runtime, load image, compose up"), require_package=True)
    rollback = sub.add_parser("rollback", help="restore a timestamped backup and compose up")
    rollback.add_argument("--backup", required=True, type=Path)
    rollback.add_argument("--install-root", type=Path, default=Path("/opt/metrum-ai-router"))
    rollback.add_argument("--utc", default=None)
    rollback.add_argument("--remote", default=None)
    rollback.add_argument("--ssh-identity", type=Path, default=None)
    rollback.add_argument("--skip-compose", action="store_true")
    return parser.parse_args(argv)


def cleanup_remote_files(paths: Sequence[Path]) -> None:
    for path in paths:
        try:
            path.unlink(missing_ok=True)
        except OSError:
            pass


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    stamp = args.utc or utc_stamp()
    try:
        if args.command == "plan":
            info = inspect_package(args.package)
            print_plan(plan_upgrade(info, args.install_root, args.backup_suffix, stamp))
            return 0
        if args.command == "apply" and args.remote:
            script = Path(__file__).resolve()
            remote_argv = [
                script.name,
                "apply",
                "--install-root",
                str(args.install_root),
                "--backup-suffix",
                args.backup_suffix,
                "--utc",
                stamp,
            ]
            if args.skip_compose:
                remote_argv.append("--skip-compose")
            return run_remote(args.remote, args.ssh_identity, remote_argv, (resolved(args.package), script))
        if args.command == "apply":
            info = inspect_package(args.package)
            result = apply_local(
                info,
                args.install_root,
                args.backup_suffix,
                stamp,
                skip_compose=args.skip_compose,
            )
            print(f"backup: {result['backup']}")
            print(f"install_root: {result['install_root']}")
            print(f"image_tar: {result['image_tar']}")
            print(f"copied: {result['copied']}")
            print(f"version_env: {result['version_env']}")
            print("services:")
            for line in result["services"]:
                print(f"  {line}")
            if args.cleanup_remote_files:
                cleanup_remote_files(
                    [
                        resolved(args.package),
                        Path("/tmp") / Path(__file__).name,
                    ]
                )
            return 0
        if args.command == "rollback" and args.remote:
            script = Path(__file__).resolve()
            completed = subprocess.run(
                [*scp_args(args.ssh_identity), str(script), f"{args.remote}:/tmp/{script.name}"],
                check=False,
            )
            if completed.returncode != 0:
                raise UpgradeError("scp of upgrade script failed")
            remote_cmd = [
                "sudo",
                "python3",
                f"/tmp/{script.name}",
                "rollback",
                "--backup",
                str(args.backup),
                "--install-root",
                str(args.install_root),
                "--utc",
                stamp,
            ]
            if args.skip_compose:
                remote_cmd.append("--skip-compose")
            return subprocess.run([*ssh_args(args.ssh_identity), args.remote, *remote_cmd], check=False).returncode
        if args.command == "rollback":
            result = rollback_local(args.backup, args.install_root, stamp, skip_compose=args.skip_compose)
            print(f"install_root: {result['install_root']}")
            if "replaced" in result:
                print(f"replaced: {result['replaced']}")
            print("services:")
            for line in result["services"]:
                print(f"  {line}")
            return 0
        raise UpgradeError(f"unknown command {args.command}")
    except UpgradeError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
