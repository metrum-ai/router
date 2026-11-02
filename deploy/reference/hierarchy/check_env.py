#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Check that the layered reference configs and env.example agree.

A `${VAR}` in either router config that is missing from env.example is a
deployment that fails at startup with a value the operator never saw. This
check is offline and runs from verify.sh before anything starts.
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

PLACEHOLDER = re.compile(r"\$\{([A-Z][A-Z0-9_]*)\}")
ENV_LINE = re.compile(r"^([A-Z][A-Z0-9_]*)=", re.MULTILINE)
# Compose substitutes these itself and they are not router config values.
COMPOSE_ONLY = {"EDGE_LISTEN_PORT", "DC_LISTEN_PORT"}


def variables(path: Path) -> set[str]:
    return set(PLACEHOLDER.findall(path.read_text(encoding="utf-8")))


def declared(path: Path) -> set[str]:
    return set(ENV_LINE.findall(path.read_text(encoding="utf-8")))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, action="append", required=True)
    parser.add_argument("--env", type=Path, required=True)
    parser.add_argument("--compose", type=Path, default=None,
                        help="also check that compose-referenced variables are declared")
    args = parser.parse_args()

    used: set[str] = set()
    for config in args.config:
        used |= variables(config)
    declared_vars = declared(args.env)
    missing = sorted(used - declared_vars)
    failures = 0
    for name in missing:
        print(f"FAIL {name} is used by a router config but not declared in {args.env.name}", file=sys.stderr)
        failures += 1
    if args.compose:
        compose = args.compose.read_text(encoding="utf-8")
        compose_used = set(PLACEHOLDER.findall(compose))
        # Compose passes env_file; only flag variables compose interpolates and
        # the router never reads, because those have no other place to live.
        undeclared = sorted(compose_used - declared_vars - set(used))
        for name in undeclared:
            print(f"FAIL {name} is interpolated by compose but not declared in {args.env.name}", file=sys.stderr)
            failures += 1
    if failures:
        return 1
    print(f"ok: {len(used)} router variables are declared in {args.env.name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
