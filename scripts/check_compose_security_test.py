#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Ensure Compose security-check env files satisfy required Compose variables."""

from __future__ import annotations

import re
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
import canonical_product as product

ROOT = Path(__file__).resolve().parents[1]
COMPOSE_DIR = ROOT / "deploy"
SECURITY_SCRIPT = ROOT / "scripts" / "check_compose_security.sh"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def required_variables(path: Path) -> set[str]:
    text = path.read_text(encoding="utf-8")
    return set(re.findall(r"\$\{([A-Za-z_][A-Za-z0-9_]*):\?", text))


def written_env_keys(script: str) -> dict[str, set[str]]:
    heredoc = re.compile(
        r"cat\s+>>?\s*\"?\$(BASE_ENV|OPT_IN_ENV)\"?\s+<<'ENV'\n(.*?)\nENV",
        re.DOTALL,
    )
    keys: dict[str, set[str]] = {"BASE_ENV": set(), "OPT_IN_ENV": set()}
    for match in heredoc.finditer(script):
        target, contents = match.groups()
        keys[target].update(
            line.split("=", 1)[0]
            for line in contents.splitlines()
            if line and "=" in line
        )
    return keys


def main() -> None:
    script = SECURITY_SCRIPT.read_text(encoding="utf-8")
    keys = written_env_keys(script)
    compose_files = sorted(COMPOSE_DIR.glob("docker-compose*.yml"))
    require(bool(compose_files), "no deploy/docker-compose*.yml files found")

    base_required = required_variables(COMPOSE_DIR / "docker-compose.yml")
    all_required = set().union(*(required_variables(path) for path in compose_files))
    require(
        product.VERSION_ENV in base_required,
        f"base Compose file must require canonical {product.VERSION_ENV}",
    )
    require(
        not any(not keys[target] for target in keys),
        "security validator must write both base and PostgreSQL env files",
    )
    require(
        product.VERSION_ENV_LEGACY not in keys["BASE_ENV"] | keys["OPT_IN_ENV"],
        f"security validator must not write legacy {product.VERSION_ENV_LEGACY}",
    )

    missing_base = base_required - keys["BASE_ENV"]
    require(
        not missing_base,
        f"base env in {SECURITY_SCRIPT.relative_to(ROOT)} is missing required Compose variables: {sorted(missing_base)}",
    )
    missing_postgres = all_required - keys["OPT_IN_ENV"]
    require(
        not missing_postgres,
        f"PostgreSQL env in {SECURITY_SCRIPT.relative_to(ROOT)} is missing required Compose variables: {sorted(missing_postgres)}",
    )
    print("compose security validator env regression test passed")


if __name__ == "__main__":
    main()
