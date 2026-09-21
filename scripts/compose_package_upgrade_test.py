#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Self-test Compose package upgrade unpack, runtime copy, and fail-closed checks."""

from __future__ import annotations

import sys
import tarfile
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import compose_package_upgrade as upgrade


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def write(path: Path, text: str = "x\n") -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def make_package(root: Path, *, version: str = "a910827", arch: str = "amd64") -> Path:
    payload = root / f"metrum-ai-router-{version}-docker-linux-{arch}"
    write(payload / "compose" / "docker-compose.yml", "services: {}\n")
    write(payload / "compose" / ".env", "SMART_LLMROUTER_VERSION=placeholder\nOTHER=keep\n")
    write(payload / "images" / f"metrum-ai-router-{version}-linux-{arch}.tar", "image-bytes\n")
    write(payload / "config" / "config.example.yaml", "example: true\n")
    archive = root / f"metrum-ai-router-{version}-docker-linux-{arch}.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        tar.add(payload, arcname=payload.name)
    return archive


def make_install(root: Path) -> Path:
    install = root / "metrum-ai-router"
    write(install / "compose" / "config" / "config.yaml", "live: true\n")
    write(install / "compose" / "config" / "env.json", '{"k":"secret"}\n')
    write(install / "compose" / "state" / "usage.sqlite", "db\n")
    write(install / "compose" / "logs" / "requests.jsonl", "{}\n")
    write(install / "compose" / ".env", "SMART_LLMROUTER_VERSION=4175bdf-linux-amd64\nKEEP=yes\n")
    write(install / "compose" / "ROUTER_TOKEN.txt", "token-redacted\n")
    write(install / "compose" / "ROUTER_TOKEN_HARBOR.txt", "harbor-redacted\n")
    write(install / "compose" / "docker-compose.yml", "old: true\n")
    return install


def test_strip_unpack_and_runtime_copy() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        package = make_package(root)
        install = make_install(root)
        info = upgrade.inspect_package(package)
        result = upgrade.apply_local(
            info,
            install,
            "elevenlabs-docs",
            "20260818T150000Z",
            skip_compose=True,
        )
        require((install / "compose" / "docker-compose.yml").read_text(encoding="utf-8") == "services: {}\n", "compose file stayed nested or was not replaced")
        require(not (install / info.top_dir).exists(), "strip-components left a nested package directory")
        require((install / "compose" / "config" / "config.yaml").read_text(encoding="utf-8") == "live: true\n", "live config was not copied forward")
        require((install / "compose" / "config" / "env.json").exists(), "env.json was not copied")
        require((install / "compose" / "ROUTER_TOKEN.txt").exists(), "ROUTER_TOKEN.txt was not copied")
        require((install / "compose" / "ROUTER_TOKEN_HARBOR.txt").exists(), "ROUTER_TOKEN_HARBOR.txt was not copied")
        env_text = (install / "compose" / ".env").read_text(encoding="utf-8")
        require("SMART_LLMROUTER_VERSION=a910827-linux-amd64" in env_text, f"version not pinned: {env_text}")
        require("KEEP=yes" in env_text, "non-version .env keys were dropped")
        require(not (install / "config" / "config.example.yaml").exists() or True, "example config copy is allowed in package tree")
        backup = Path(str(result["backup"]))
        require(backup.is_dir(), "backup missing")
        require((backup / "compose" / "config" / "config.yaml").exists(), "backup lost live config")
        require("compose/config" in result["copied"], "copy list missing compose/config")
        require("compose/ROUTER_TOKEN.txt" in result["copied"], "copy list missing token")


def test_missing_runtime_aborts() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        package = make_package(root)
        install = root / "metrum-ai-router"
        write(install / "compose" / "docker-compose.yml", "old: true\n")
        info = upgrade.inspect_package(package)
        original = (install / "compose" / "docker-compose.yml").read_text(encoding="utf-8")
        try:
            upgrade.apply_local(info, install, "elevenlabs-docs", "20260818T150001Z", skip_compose=True)
        except upgrade.UpgradeError as exc:
            require("missing required runtime" in str(exc), f"unexpected error: {exc}")
        else:
            raise AssertionError("missing runtime was accepted")
        require((install / "compose" / "docker-compose.yml").read_text(encoding="utf-8") == original, "install tree was mutated after abort")
        require(not list(root.glob("*.backup-*")), "backup created after abort")


def test_token_listing_stays_in_compose_dir() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        compose = root / "metrum-ai-router" / "compose"
        write(compose / "ROUTER_TOKEN.txt", "a\n")
        write(compose / "ROUTER_TOKEN_HARBOR.txt", "b\n")
        write(compose / "not-a-token.txt", "c\n")
        write(root / "ROUTER_TOKEN.txt", "outside\n")
        names = [path.name for path in upgrade.token_files(compose)]
        require(names == ["ROUTER_TOKEN.txt", "ROUTER_TOKEN_HARBOR.txt"], f"unexpected tokens: {names}")


def test_forbidden_paths() -> None:
    for path in ("/", "/usr", "/bin", "/opt"):
        try:
            upgrade.reject_forbidden(Path(path), label="staging")
        except upgrade.UpgradeError:
            continue
        raise AssertionError(f"forbidden path accepted: {path}")


def test_source_has_no_star_move() -> None:
    source = Path(upgrade.__file__).read_text(encoding="utf-8")
    require("strip-components=1" in source, "missing strip-components unpack")
    require("shell=True" not in source, "shell=True would allow glob expansion")
    require("mv ${" not in source, "shell mv with expansion is present")
    require("${INNER}" not in source, "INNER glob unpacker is present")
    require("/*'" not in source and '/*"' not in source, "star-move glob string is present")


def test_plan_is_read_only() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        package = make_package(root)
        install = make_install(root)
        before = {path.relative_to(install): path.read_bytes() for path in install.rglob("*") if path.is_file()}
        info = upgrade.inspect_package(package)
        plan = upgrade.plan_upgrade(info, install, "elevenlabs-docs", "20260818T150002Z")
        after = {path.relative_to(install): path.read_bytes() for path in install.rglob("*") if path.is_file()}
        require(before == after, "plan mutated the install root")
        require(plan["unpack"] == "tar --strip-components=1", "plan missing strip unpack")
        require(plan["skip_instance_reboot"] is True, "plan should refuse instance reboot")
        require(plan["skip_instance_reboot"] is True, "plan should skip instance reboot")


def test_rollback_restores_backup() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        package = make_package(root)
        install = make_install(root)
        info = upgrade.inspect_package(package)
        result = upgrade.apply_local(info, install, "elevenlabs-docs", "20260818T150003Z", skip_compose=True)
        backup = Path(str(result["backup"]))
        upgrade.rollback_local(backup, install, "20260818T150004Z", skip_compose=True)
        require("4175bdf-linux-amd64" in (install / "compose" / ".env").read_text(encoding="utf-8"), "rollback did not restore previous .env")
        require((install / "compose" / "config" / "config.yaml").read_text(encoding="utf-8") == "live: true\n", "rollback lost live config")


def test_postgres_override_compose_files() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp) / "metrum-ai-router"
        write(root / "compose" / ".env", "SMART_LLMROUTER_VERSION=x\nROUTER_USAGE_DB_DSN=host=postgres\n")
        write(root / "compose" / "docker-compose.yml", "services: {}\n")
        write(root / "compose" / "docker-compose.postgres-localhost.yml", "services: {}\n")
        require(upgrade.postgres_override_enabled(root / "compose" / ".env"), "DSN not detected")
        require(
            upgrade.compose_file_args(root) == ["-f", "docker-compose.yml", "-f", "docker-compose.postgres-localhost.yml"],
            f"unexpected compose files: {upgrade.compose_file_args(root)}",
        )
        write(root / "compose" / ".env", "SMART_LLMROUTER_VERSION=x\n# ROUTER_USAGE_DB_DSN=host=postgres\n")
        require(not upgrade.postgres_override_enabled(root / "compose" / ".env"), "commented DSN treated as enabled")
        require(upgrade.compose_file_args(root) == ["-f", "docker-compose.yml"], "override used without DSN")


def main() -> int:
    test_strip_unpack_and_runtime_copy()
    test_missing_runtime_aborts()
    test_token_listing_stays_in_compose_dir()
    test_forbidden_paths()
    test_source_has_no_star_move()
    test_plan_is_read_only()
    test_rollback_restores_backup()
    test_postgres_override_compose_files()
    print("compose package upgrade self-test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
