# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Stratified, session-disjoint sampling for the iteration-1 seed corpus."""

from __future__ import annotations

import hashlib
import random
from collections import defaultdict
from collections.abc import Callable
from typing import Any

from ..collect import DataError
from ..features import session_split
from . import DEFAULT_SAMPLE_SEED, SAMPLE_N
from .extract import estimate_prompt_tokens

TokenCounter = Callable[[list[dict[str, Any]]], int]


def turn_index_bucket(assistant_index: int) -> str:
    if assistant_index <= 5:
        return "1-5"
    if assistant_index <= 20:
        return "6-20"
    return "21+"


def prompt_token_bucket(token_count: int) -> str:
    if token_count < 4_000:
        return "0-4K"
    if token_count < 16_000:
        return "4-16K"
    if token_count < 32_000:
        return "16-32K"
    return "32K+"


def stratum_key(turn: dict[str, Any], *, token_counter: TokenCounter) -> tuple[str, str, str]:
    tokens = turn.get("prompt_tokens_cl100k")
    if tokens is None:
        tokens = token_counter(list(turn["messages"]))
    if type(tokens) is not int or tokens < 0:
        raise DataError("invalid_prompt_tokens")
    return (
        turn_index_bucket(int(turn["assistant_index"])),
        prompt_token_bucket(tokens),
        str(turn["turn_kind"]),
    )


def _stable_rank(seed: int, turn_id: str) -> int:
    return int.from_bytes(
        hashlib.sha256(f"{seed}\0{turn_id}".encode()).digest()[:8], "big"
    )


def stratified_sample(
    turns: list[dict[str, Any]],
    *,
    n: int = SAMPLE_N,
    seed: int = DEFAULT_SAMPLE_SEED,
    token_counter: TokenCounter = estimate_prompt_tokens,
) -> dict[str, Any]:
    """Select up to ``n`` turns with balanced strata and session-disjoint splits.

    Sessions stay entirely in train, valid, or test via ``session_split``.
    """
    if n < 1:
        raise DataError("invalid_sample_n")
    if not turns:
        raise DataError("empty_turn_pool")

    enriched: list[dict[str, Any]] = []
    for turn in turns:
        row = dict(turn)
        tokens = row.get("prompt_tokens_cl100k")
        if tokens is None:
            tokens = token_counter(list(row["messages"]))
        row["prompt_tokens_cl100k"] = tokens
        row["stratum"] = {
            "turn_index_bucket": turn_index_bucket(int(row["assistant_index"])),
            "prompt_token_bucket": prompt_token_bucket(int(tokens)),
            "turn_kind": row["turn_kind"],
        }
        row["split"] = session_split(str(row["session_key"]), seed)
        enriched.append(row)

    by_stratum: dict[tuple[str, str, str], list[dict[str, Any]]] = defaultdict(list)
    for row in enriched:
        key = (
            row["stratum"]["turn_index_bucket"],
            row["stratum"]["prompt_token_bucket"],
            row["stratum"]["turn_kind"],
        )
        by_stratum[key].append(row)

    available = {str(key): len(values) for key, values in sorted(by_stratum.items())}
    for values in by_stratum.values():
        values.sort(key=lambda item: (_stable_rank(seed, item["turn_id"]), item["turn_id"]))

    selected: list[dict[str, Any]] = []
    selected_sessions: dict[str, str] = {}
    rng = random.Random(seed)
    stratum_keys = list(by_stratum.keys())
    rng.shuffle(stratum_keys)
    # Round-robin strata until n or exhaustion.
    exhausted: set[tuple[str, str, str]] = set()
    while len(selected) < n and len(exhausted) < len(stratum_keys):
        progress = False
        for key in stratum_keys:
            if len(selected) >= n or key in exhausted:
                continue
            pool = by_stratum[key]
            while pool:
                candidate = pool.pop(0)
                session = str(candidate["session_key"])
                split = candidate["split"]
                prior = selected_sessions.get(session)
                if prior is not None and prior != split:
                    raise DataError("session_split_conflict")
                if prior is None:
                    # Keep session membership consistent with hash split.
                    selected_sessions[session] = split
                selected.append(candidate)
                progress = True
                break
            else:
                exhausted.add(key)
        if not progress:
            break

    selected.sort(key=lambda item: item["turn_id"])
    selected_counts: dict[str, int] = defaultdict(int)
    for row in selected:
        key = (
            row["stratum"]["turn_index_bucket"],
            row["stratum"]["prompt_token_bucket"],
            row["stratum"]["turn_kind"],
        )
        selected_counts[str(key)] += 1

    return {
        "schema_version": "lrp.seed.sample.v1",
        "seed": seed,
        "requested_n": n,
        "selected_n": len(selected),
        "available_stratum_counts": available,
        "selected_stratum_counts": dict(selected_counts),
        "selected_turn_ids": [row["turn_id"] for row in selected],
        "turns": selected,
        "split_counts": {
            name: sum(1 for row in selected if row["split"] == name)
            for name in ("train", "valid", "test")
        },
    }
