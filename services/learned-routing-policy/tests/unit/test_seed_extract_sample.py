# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

from lrp.features import session_split
from lrp.seed.extract import extract_teacher_forced_turns
from lrp.seed.sample import stratified_sample
from seed_fixtures import multi_session_pool, sample_trajectory_row


def test_teacher_forced_extraction_keeps_reference_separate():
    turns = extract_teacher_forced_turns(sample_trajectory_row())
    assert len(turns) == 3
    first = turns[0]
    assert first["turn_kind"] == "tool-call"
    assert first["messages"][-1]["role"] == "user"
    assert "reference_assistant" in first
    assert first["reference_assistant"]["tool_calls"][0]["function"]["name"] == "read_file"
    assert isinstance(
        first["reference_assistant"]["tool_calls"][0]["function"]["arguments"], str
    )
    assert first["messages"][-1] != first["reference_assistant"]
    assert turns[2]["turn_kind"] == "text"
    assert turns[1]["messages"][-1]["role"] == "tool"


def test_stratified_sample_is_session_disjoint_and_reproducible():
    pool = []
    for row in multi_session_pool():
        pool.extend(extract_teacher_forced_turns(row))
    first = stratified_sample(pool, n=8, seed=7)
    second = stratified_sample(pool, n=8, seed=7)
    assert first["selected_turn_ids"] == second["selected_turn_ids"]
    assert first["selected_n"] == 8
    sessions: dict[str, str] = {}
    for turn in first["turns"]:
        split = session_split(turn["session_key"], 7)
        assert turn["split"] == split
        prior = sessions.get(turn["session_key"])
        if prior is not None:
            assert prior == split
        sessions[turn["session_key"]] = split
    assert first["available_stratum_counts"]
