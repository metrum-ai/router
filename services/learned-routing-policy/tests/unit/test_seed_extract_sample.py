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


def test_example_sample_n_is_one_hundred():
    from lrp.seed import EXAMPLE_MAX_TRACES, SAMPLE_N

    assert EXAMPLE_MAX_TRACES == 100
    assert SAMPLE_N == 100


def test_iter_trajectory_rows_respects_max_traces():
    import tempfile
    from pathlib import Path

    import pyarrow as pa
    import pyarrow.parquet as pq

    from lrp.seed.extract import iter_trajectory_rows

    # Keep parquet outside the repo and outside /tmp (a stray /tmp/.git on some
    # hosts makes protected_path treat all pytest basetemp paths as in-repo).
    rows = [{"trajectory_id": f"s{i}", "trajectory": [{"role": "user", "content": "x"}]} for i in range(5)]
    with tempfile.TemporaryDirectory(dir=Path.home()) as tmp:
        path = Path(tmp) / "trajectories.parquet"
        pq.write_table(pa.Table.from_pylist(rows), path)
        got = list(iter_trajectory_rows(path, max_traces=2, batch_size=1))
    assert len(got) == 2
    assert got[0]["trajectory_id"] == "s0"
