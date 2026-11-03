#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Read one per-hop usage row for the layered-deployment check (#434).

The local `verify.sh --local` mode has no psql, so it reads the hop's SQLite
usage store through this helper. Compose mode reads the same columns with psql.
"""

from __future__ import annotations

import argparse
import sqlite3
import sys


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sqlite", required=True, help="path to the hop's SQLite usage store")
    parser.add_argument("sql", help="one usage query returning a single row")
    args = parser.parse_args()
    if not args.sql.lstrip().lower().startswith("select"):
        print("only SELECT statements are accepted", file=sys.stderr)
        return 2
    connection = sqlite3.connect(f"file:{args.sqlite}?mode=ro", uri=True, timeout=10)
    try:
        row = connection.execute(args.sql).fetchone()
    finally:
        connection.close()
    if row is None:
        return 1
    print("|".join("" if value is None else str(value) for value in row))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
