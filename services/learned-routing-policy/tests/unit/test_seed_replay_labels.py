# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import json

import httpx
import pytest
from lrp.collect import DataError
from lrp.fanout import run_fanout
from lrp.judge.contracts import normalize_verifier
from lrp.judge.plugins_runtime import run_plugin
from lrp.seed.extract import extract_teacher_forced_turns
from lrp.seed.labels import (
    LABEL_COLUMNS,
    candidate_payload,
    label_verifier_columns,
    quality_blend_rule,
)
from lrp.seed.parquet_manifest import build_manifest, write_manifest, write_seed_parquet
from lrp.seed.replay import (
    build_replay_request,
    estimate_replay_spend_usd,
    guard_spend_estimate,
    run_seed_replay,
)
from lrp.seed.targets import fanout_target_dicts, iteration1_targets
from seed_fixtures import sample_trajectory_row
from test_collect import write
from test_fanout import completion, pricing


def test_iteration1_targets_have_dated_prices_without_secrets():
    targets = iteration1_targets()
    assert len(targets) == 4
    models = {row["model"] for row in targets}
    assert "qwen/qwen3.5-9b" in models
    assert "qwen/qwen3.8-27b" in models
    assert "gpt-5.6-sol" in models
    assert "gpt-5.4-mini" in models
    blob = json.dumps(targets)
    assert "sk-" not in blob
    assert "OPENAI_API_KEY" not in blob
    openai = next(row for row in targets if row["model"] == "gpt-5.6-sol")
    assert openai["pass1_controls"]["prompt_cache_options"]["mode"] == "explicit"
    assert openai["pricing"]["as_of"] == "2026-09-13"


@pytest.mark.asyncio
async def test_openai_only_fields_not_sent_to_qwen(tmp_path):
    bodies: list[dict] = []

    def handler(req: httpx.Request) -> httpx.Response:
        if req.method == "GET":
            return httpx.Response(200, json=pricing(("qwen/qwen3.5-9b",)))
        bodies.append(json.loads(req.content))
        return httpx.Response(
            200,
            json=completion(),
            headers={"x-request-id": "router-req-1"},
        )

    turn = extract_teacher_forced_turns(sample_trajectory_row())[0]
    request = build_replay_request(turn, max_tokens=32)
    source = write(tmp_path / "requests", [request])
    target = fanout_target_dicts(
        [row for row in iteration1_targets() if row["model"] == "qwen/qwen3.5-9b"]
    )[0]
    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        await run_fanout(
            requests=source,
            out=tmp_path / "out",
            targets=[target],
            via="router",
            base_url="http://127.0.0.1:8080/v1",
            client=client,
            approved_content=True,
        )
    assert bodies
    assert "prompt_cache_options" not in bodies[0]


@pytest.mark.asyncio
async def test_openai_target_without_catalog_price_is_ineligible(tmp_path):
    bodies: list[dict] = []

    def handler(req: httpx.Request) -> httpx.Response:
        if req.method == "GET":
            return httpx.Response(200, json={"data": []})
        bodies.append(json.loads(req.content))
        return httpx.Response(200, json=completion())

    turn = extract_teacher_forced_turns(sample_trajectory_row())[0]
    request = build_replay_request(turn, max_tokens=32)
    source = write(tmp_path / "requests", [request])
    target = fanout_target_dicts(
        [row for row in iteration1_targets() if row["model"] == "gpt-5.6-sol"]
    )[0]
    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        await run_fanout(
            requests=source,
            out=tmp_path / "out",
            targets=[target],
            via="router",
            base_url="http://127.0.0.1:8080/v1",
            client=client,
            approved_content=True,
        )
    assert bodies == []


@pytest.mark.asyncio
async def test_spend_estimate_aborts_without_approval_and_dry_run_skips_paid(tmp_path):
    turns = extract_teacher_forced_turns(sample_trajectory_row())
    for turn in turns:
        turn["prompt_tokens_cl100k"] = 10_000_000
    estimate = estimate_replay_spend_usd(turns)
    assert estimate["estimate_usd"] > estimate["abort_threshold_usd"]
    assert estimate["abort_threshold_usd"] == 100.0
    with pytest.raises(DataError, match="spend_estimate_requires_approval"):
        guard_spend_estimate(estimate, approve_over_threshold=False)
    guard_spend_estimate(estimate, approve_over_threshold=True)
    result = await run_seed_replay(
        requests=tmp_path / "unused-requests",
        out=tmp_path / "unused-out",
        base_url="http://127.0.0.1:8080/v1",
        spend_estimate=estimate,
        approve_spend_over_threshold=True,
        execute=False,
    )
    assert result["status"] == "dry_run"
    assert result["note"] == "paid replay not executed"


def test_verifier_plugins_and_separate_columns():
    turn = extract_teacher_forced_turns(sample_trajectory_row())[0]
    reference = turn["reference_assistant"]
    matching = {
        "status": "ok",
        "content": "",
        "tool_calls": reference["tool_calls"],
    }
    labels = label_verifier_columns(turn=turn, response=matching)
    for column in LABEL_COLUMNS:
        assert column in labels
    assert labels["verifier_tool_presence"] == 1.0
    assert labels["verifier_tool_name"] == 1.0
    assert labels["verifier_normalized_args"] == 1.0
    mismatched = {
        "status": "ok",
        "content": "hello",
        "tool_calls": [],
    }
    missing = label_verifier_columns(turn=turn, response=mismatched)
    assert missing["verifier_tool_presence"] == 0.0
    assert quality_blend_rule()["default_applied"] is False

    content = candidate_payload(matching)
    assert run_plugin("tool_call_presence_v1", {"reference": reference}, content)
    assert run_plugin("tool_name_match_v1", {"reference": reference}, content)
    normalize_verifier(
        {
            "kind": "plugin",
            "spec": {
                "plugin_id": "tool_normalized_arg_match_v1",
                "params": {"reference": reference},
            },
        }
    )


def test_normalized_arg_match_collapses_path_and_shell_syntax():
    reference = {
        "content": "",
        "tool_calls": [
            {
                "id": "1",
                "type": "function",
                "function": {
                    "name": "read_file",
                    "arguments": '{"path":"src/main.py"}',
                },
            }
        ],
    }
    candidate = {
        "content": "",
        "tool_calls": [
            {
                "id": "1",
                "type": "function",
                "function": {
                    "name": "read_file",
                    "arguments": '{"path":"src//main.py/"}',
                },
            }
        ],
    }
    assert run_plugin(
        "tool_normalized_arg_match_v1",
        {"reference": reference},
        json.dumps(candidate),
    )


def test_parquet_and_manifest(tmp_path):
    rows = [
        {
            "turn_id": "t1",
            "session_id": "s1",
            "split": "train",
            "source_dataset": "nebius/SWE-rebench-openhands-trajectories",
            "source_revision": "35455389ab51bf5e2306bfd436ef72d0f98bf882",
            "target_id": "openrouter-qwen3.5-9b",
            "provider": "openrouter",
            "model": "qwen/qwen3.5-9b",
            "cache_pass": "1",
            "router_request_id": "req-1",
            "eligible": True,
            "eligibility_reason": None,
            "prompt_tokens_cl100k": 100,
            "input_tokens": 90,
            "cached_input_tokens": None,
            "cache_write_tokens": None,
            "reasoning_tokens": None,
            "output_tokens": 10,
            "cost_usd": 0.01,
            "latency_ms": 12.5,
            "verifier_tool_presence": 1.0,
            "verifier_tool_name": 1.0,
            "verifier_args_schema": 1.0,
            "verifier_normalized_args": 0.0,
            "judge_quality": None,
            "human_quality": None,
        }
    ]
    meta = write_seed_parquet(tmp_path / "seed.parquet", rows)
    assert meta["rows"] == 1
    assert meta["sha256"]
    manifest = build_manifest(
        [{"path": "seed.parquet", "sha256": meta["sha256"]}],
        inputs=[{"path": "README.md", "sha256": "a" * 64}],
        extra={"paid_replay": "not_run"},
    )
    path = write_manifest(tmp_path / "manifest.json", manifest)
    assert path.exists()
    assert manifest["manifest_sha256"]
