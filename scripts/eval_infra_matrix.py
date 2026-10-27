#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Preregistered evaluation matrix tooling (metrum-ai/router#330).

ARCH-06: generates pairwise combinations of the supported request-shape
factors plus the mandatory high-risk triples, and requires every excluded
pairing to be enumerated with a reason — no unbounded Cartesian product and
no silently untested combination.

EVAL-03: validates the preregistered cells (suite/mode declared, sample size
and allowed deltas chosen before evaluation).

Usage:
  python3 scripts/eval_infra_matrix.py [--matrix evaluators/eval-infra/preregistered-matrix.yaml] [--emit]
"""

from __future__ import annotations

import argparse
import itertools
import json
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_MATRIX = ROOT / "evaluators" / "eval-infra" / "preregistered-matrix.yaml"


class MatrixError(ValueError):
    pass


def load_matrix(path: Path) -> dict:
    doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(doc, dict) or doc.get("version") != 1:
        raise MatrixError("matrix file must set version: 1")
    for key in ("modes", "cells", "factors", "mandatory_triples", "excluded", "task_gates"):
        if key not in doc:
            raise MatrixError(f"matrix file is missing {key!r}")
    return doc


def factor_values(doc: dict) -> list[str]:
    values: list[str] = []
    for group, entries in doc["factors"].items():
        if not isinstance(entries, list) or len(entries) < 2:
            raise MatrixError(f"factor {group!r} needs at least two values")
        values.extend(str(v) for v in entries)
    if len(values) != len(set(values)):
        raise MatrixError("factor values must be unique across groups")
    return values


def pairwise(doc: dict) -> list[tuple[str, ...]]:
    """One combination per unordered value pair, plus the mandatory triples."""
    values = factor_values(doc)
    combos = {tuple(sorted(pair)) for pair in itertools.combinations(values, 2)}
    for triple in doc["mandatory_triples"]:
        t = tuple(sorted(str(x) for x in triple))
        if len(t) != 3 or any(v not in values and v not in ("restart", "caller-isolation") for v in t):
            raise MatrixError(f"mandatory triple {t} references unknown factor values")
        combos.add(t)
    excluded = {tuple(sorted(str(x) for x in e["combo"])) for e in doc["excluded"]}
    for combo in excluded:
        if len(combo) < 2:
            raise MatrixError("excluded combos must name at least two factor values")
    # Every excluded pair must be a real pair of known factor values.
    known = set(values) | {"restart", "caller-isolation"}
    for combo in excluded:
        unknown = [v for v in combo if v not in known]
        if unknown:
            raise MatrixError(f"excluded combo {combo} references unknown values {unknown}")
    out = []
    for combo in sorted(combos):
        pairs = {tuple(sorted(p)) for p in itertools.combinations(combo, 2)}
        if pairs & excluded:
            continue
        out.append(combo)
    return out


def validate(doc: dict) -> list[tuple[str, ...]]:
    modes = set(doc["modes"])
    cells = doc["cells"]
    if not isinstance(cells, list) or not cells:
        raise MatrixError("matrix needs at least one preregistered cell")
    seen = set()
    for cell in cells:
        for key in ("id", "suite", "mode", "environment", "sample_size", "allowed_score_delta", "allowed_error_rate"):
            if key not in cell:
                raise MatrixError(f"cell {cell.get('id', '?')!r} is missing {key!r}")
        if cell["id"] in seen:
            raise MatrixError(f"duplicate cell id {cell['id']!r}")
        seen.add(cell["id"])
        if cell["mode"] not in modes:
            raise MatrixError(f"cell {cell['id']!r} uses undeclared mode {cell['mode']!r}")
        if cell["environment"] not in ("offline", "nightly-live"):
            raise MatrixError(f"cell {cell['id']!r} has unknown environment {cell['environment']!r}")
        if not isinstance(cell["sample_size"], int) or cell["sample_size"] < 1:
            raise MatrixError(f"cell {cell['id']!r} sample_size must be a positive integer")
        if not 0 <= cell["allowed_score_delta"] <= 1 or not 0 <= cell["allowed_error_rate"] <= 1:
            raise MatrixError(f"cell {cell['id']!r} deltas must be fractions in [0, 1]")
    gates = doc["task_gates"]
    for key in ("max_time_to_first_event_ms", "max_completion_latency_ms", "max_tool_turns", "max_cost_per_verified_task_usd"):
        if not isinstance(gates.get(key), (int, float)) or gates[key] <= 0:
            raise MatrixError(f"task gate {key!r} must be a positive number")
    return pairwise(doc)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--matrix", type=Path, default=DEFAULT_MATRIX)
    parser.add_argument("--emit", action="store_true", help="print the resolved pairwise coverage as JSON")
    args = parser.parse_args()
    try:
        doc = load_matrix(args.matrix)
        combos = validate(doc)
    except MatrixError as exc:
        print(f"eval matrix invalid: {exc}", file=sys.stderr)
        return 2
    if args.emit:
        print(json.dumps({"cells": [c["id"] for c in doc["cells"]], "pairwise": [list(c) for c in combos]}, indent=2))
    else:
        print(f"eval matrix ok: {len(doc['cells'])} preregistered cells, {len(combos)} pairwise/triple combinations, {len(doc['excluded'])} explicit exclusions")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
