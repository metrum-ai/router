# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import json
import os
import shutil
from pathlib import Path

import httpx
import pytest
from lrp.collect import DataError, read_rows
from lrp.judge import Sandbox, cache_identity, judge_requests
from test_collect import request, write
from test_fanout import completion, pricing

ANCHOR = ("openrouter", "synthetic/anchor")
JUDGE = "synthetic/judge"


def response(model, **changes):
    row = {
        "schema_version": "lrp.response.v1",
        "request_id": "synthetic-1",
        "source": "synthetic",
        "target": {"provider": "openrouter", "model": model},
        "started_at": "2026-09-09T00:00:00Z",
        "duration_ms": 1.0,
        "status": "ok",
        "content": "4",
    }
    row.update(changes)
    return row


def inputs(tmp_path, req=None, anchor_changes=None):
    requests = write(tmp_path / "requests", [req or request()])
    responses = write(
        tmp_path / "responses",
        [
            response("synthetic/candidate"),
            response(ANCHOR[1], **(anchor_changes or {})),
        ],
    )
    return requests, responses


def mock_judge(verdicts, seen):
    def handler(req):
        if req.method == "GET":
            return httpx.Response(200, json=pricing([JUDGE]))
        seen.append(json.loads(req.content))
        value = verdicts.pop(0)
        return httpx.Response(
            200, json=completion(choices=[{"message": {"content": value}}])
        )

    return httpx.MockTransport(handler)


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "votes,quality,agree",
    [
        (["A", "B"], 1.0, True),
        (["B", "A"], 0.0, True),
        (["A", "A"], 0.5, False),
        (["TIE", "TIE"], 1.0, True),
    ],
)
async def test_position_swapped_pairwise(tmp_path, votes, quality, agree):
    reqs, resps = inputs(tmp_path)
    seen = []
    async with httpx.AsyncClient(
        transport=mock_judge(
            [
                json.dumps({"winner": v, "reason": "private reason discarded"})
                for v in votes
            ],
            seen,
        )
    ) as client:
        await judge_requests(
            requests=reqs,
            responses=resps,
            out=tmp_path / "out",
            anchor=ANCHOR,
            judge_model=JUDGE,
            client=client,
        )
    rows = list(read_rows(tmp_path / "out"))
    assert rows[0]["quality"] == quality and rows[0]["detail"]["swapped_agree"] is agree
    assert rows[1]["quality"] == 1
    assert len(seen) == 2
    first, second = [json.loads(r["messages"][1]["content"]) for r in seen]
    assert first["A"] == second["B"] and first["B"] == second["A"]
    assert "private reason" not in (tmp_path / "out").read_text()


@pytest.mark.asyncio
async def test_strict_parse_retry_uncertain_excluded(tmp_path):
    reqs, resps = inputs(tmp_path)
    seen = []
    invalid = '{"winner":"A","reason":"x","extra":"discard"}'
    async with httpx.AsyncClient(
        transport=mock_judge(["garbage", invalid, "[]", "{}"], seen)
    ) as client:
        result = await judge_requests(
            requests=reqs,
            responses=resps,
            out=tmp_path / "out",
            anchor=ANCHOR,
            judge_model=JUDGE,
            client=client,
        )
    row = next(iter(read_rows(tmp_path / "out")))
    assert row.get("quality") is None and row["detail"]["parse_failed"]
    assert result["uncertain"] == 1 and len(seen) == 4
    assert row["judge_cost_usd"] == pytest.approx(4 * 0.000028)


@pytest.mark.asyncio
async def test_failed_anchor_uses_absolute_rubric(tmp_path):
    reqs, resps = inputs(tmp_path, anchor_changes={"status": "timeout"})
    seen = []
    async with httpx.AsyncClient(
        transport=mock_judge(['{"score":7,"reason":"adequate"}'], seen)
    ) as client:
        await judge_requests(
            requests=reqs,
            responses=resps,
            out=tmp_path / "out",
            anchor=ANCHOR,
            judge_model=JUDGE,
            client=client,
        )
    rows = list(read_rows(tmp_path / "out"))
    assert rows[0]["quality"] == 0.7 and rows[0]["method"] == "absolute_rubric:v1"
    assert rows[1]["quality"] == 0 and len(seen) == 1


@pytest.mark.asyncio
async def test_verifier_precedes_judge_and_unavailable_is_not_failure(tmp_path):
    reqs, resps = inputs(
        tmp_path, request(verifier={"kind": "exact", "spec": {"expected": "4"}})
    )

    def never(req):
        pytest.fail("Verifier path must not call LLM")

    async with httpx.AsyncClient(transport=httpx.MockTransport(never)) as client:
        result = await judge_requests(
            requests=reqs,
            responses=resps,
            out=tmp_path / "out",
            anchor=ANCHOR,
            judge_model=JUDGE,
            client=client,
        )
    assert result["uncertain"] == 2
    assert all(
        r.get("quality") is None and r["detail"]["unsupported"]
        for r in read_rows(tmp_path / "out")
    )


@pytest.mark.asyncio
@pytest.mark.parametrize("passed", [True, False])
async def test_verifier_result_mapping_without_llm(tmp_path, passed):
    # Orchestration fake only; real isolation is tested separately below.
    class FakeSandbox:
        rootfs = Path("/synthetic/rootfs")
        timeout_s = 30

        def verify(self, verifier, content):
            return float(passed), {"passed": passed}

    reqs, resps = inputs(
        tmp_path, request(verifier={"kind": "exact", "spec": {"expected": "4"}})
    )
    await judge_requests(
        requests=reqs,
        responses=resps,
        out=tmp_path / "out",
        anchor=ANCHOR,
        judge_model=JUDGE,
        sandbox=FakeSandbox(),
    )
    assert all(r["quality"] == float(passed) for r in read_rows(tmp_path / "out"))


@pytest.mark.asyncio
async def test_cache_resume_and_content_invalidation(tmp_path):
    reqs, resps = inputs(tmp_path)
    seen = []
    async with httpx.AsyncClient(
        transport=mock_judge(
            ['{"winner":"A","reason":"ok"}', '{"winner":"B","reason":"ok"}'], seen
        )
    ) as client:
        options = {
            "requests": reqs,
            "responses": resps,
            "anchor": ANCHOR,
            "judge_model": JUDGE,
            "client": client,
            "cache": tmp_path / "cache",
        }
        await judge_requests(**options, out=tmp_path / "first")
        assert (await judge_requests(**options, out=tmp_path / "first"))["skipped"] == 2
        assert (await judge_requests(**options, out=tmp_path / "second"))["cached"] == 2
        assert len(seen) == 2
        write(
            resps,
            [response("synthetic/candidate", content="changed"), response(ANCHOR[1])],
        )
        with pytest.raises(DataError, match="resume_judgment_input_changed"):
            await judge_requests(**options, out=tmp_path / "first")
    assert (tmp_path / "cache").stat().st_mode & 0o777 == 0o600


def test_cache_identity_includes_model_settings_content_verifier():
    req, resp, ref = request(), response("synthetic/candidate"), response(ANCHOR[1])
    baseline = cache_identity(req, resp, ref, JUDGE, {})
    assert baseline != cache_identity(req, {**resp, "content": "5"}, ref, JUDGE, {})
    assert baseline != cache_identity(req, resp, {**ref, "content": "5"}, JUDGE, {})
    assert baseline != cache_identity(req, resp, ref, "synthetic/new-judge", {})
    assert baseline != cache_identity(req, resp, ref, JUDGE, {"temperature": 1})
    assert baseline != cache_identity(
        {**req, "verifier": {"kind": "exact", "spec": {"expected": "5"}}},
        resp,
        ref,
        JUDGE,
        {},
    )


@pytest.mark.skipif(not shutil.which("bwrap"), reason="requires optional bubblewrap executable")
def test_sandbox_command_is_real_namespace_isolation_and_missing_root_fails_closed(
    tmp_path,
):
    rootfs = tmp_path / "rootfs"
    rootfs.mkdir(mode=0o700)
    sandbox = Sandbox(rootfs)
    command = sandbox.command()
    assert "--unshare-all" in command and "--clearenv" in command
    assert "--disable-userns" in command
    assert command[command.index("--size") + 1] == "16777216"
    assert command[command.index("--uid") + 1] == "65534"
    assert command[command.index("--ro-bind") + 1 : command.index("--ro-bind") + 3] == [
        str(rootfs),
        "/",
    ]
    quality, detail = sandbox.verify(
        {"kind": "pytest", "spec": {"tests": "raise AssertionError"}}, ""
    )
    assert quality is None and detail["unsupported"]
    assert not (rootfs / "test_solution.py").exists()


@pytest.mark.skipif(
    not os.environ.get("LRP_TEST_ROOTFS")
    and not os.environ.get("LRP_REQUIRE_SANDBOX_TESTS"),
    reason="requires operator-provisioned isolated Python/pytest rootfs and namespace support",
)
def test_real_sandbox_network_host_denial_and_wall_timeout(tmp_path, monkeypatch):
    assert os.environ.get("LRP_TEST_ROOTFS"), (
        "mandatory sandbox test requires LRP_TEST_ROOTFS"
    )
    sandbox = Sandbox(Path(os.environ["LRP_TEST_ROOTFS"]))
    assert sandbox.verify({"kind": "exact", "spec": {"expected": "4"}}, "4")[0] == 1
    assert sandbox.verify({"kind": "exact", "spec": {"expected": "4"}}, "5")[0] == 0
    assert (
        sandbox.verify({"kind": "regex", "spec": {"pattern": r"[0-9]+"}}, "123")[0] == 1
    )
    assert (
        sandbox.verify(
            {"kind": "json_schema", "spec": {"schema": {"type": "integer"}}}, "123"
        )[0]
        == 1
    )
    assert (
        sandbox.verify(
            {"kind": "json_schema", "spec": {"schema": {"type": "integer"}}}, '"bad"'
        )[0]
        == 0
    )
    host_file = tmp_path / "host-only"
    host_file.write_text("synthetic sentinel")
    host_network = os.readlink("/proc/self/ns/net")
    monkeypatch.setenv("LRP_SYNTHETIC_HOST_SECRET", "must-not-reach-worker")
    tests = f"""import os, socket, pathlib, pytest
import solution
def test_isolation():
    assert solution.value == 42
    assert not pathlib.Path('/home').exists()
    assert not pathlib.Path({str(host_file)!r}).exists()
    assert 'LRP_SYNTHETIC_HOST_SECRET' not in os.environ
    assert os.getuid() == 65534
    assert os.readlink('/proc/self/ns/net') != {host_network!r}
    assert len(pathlib.Path('/proc/net/route').read_text().splitlines()) <= 1
    with pytest.raises(OSError):
        socket.create_connection(('192.0.2.1', 443), timeout=1)

def test_scratch_limit():
    assert os.statvfs('/work').f_blocks * os.statvfs('/work').f_frsize <= 16777216
    with pytest.raises(OSError):
        for i in range(32):
            pathlib.Path('/work/fill-' + str(i)).write_bytes(b'x' * 1048576)
"""
    assert (
        sandbox.verify({"kind": "pytest", "spec": {"tests": tests}}, "value = 42")[0]
        == 1
    )
    short = Sandbox(sandbox.rootfs, timeout_s=0.2)
    quality, detail = short.verify(
        {"kind": "pytest", "spec": {"tests": "import time; time.sleep(60)"}}, ""
    )
    assert quality == 0 and detail["timeout"]


def test_contracts_allowlist_sql_and_plugin():
    from lrp.judge.contracts import normalize_verifier

    sql = normalize_verifier(
        {
            "kind": "sql_result",
            "spec": {
                "expected_rows": [{"id": 1, "name": "a"}],
                "columns": ["id", "name"],
                "ignore_row_order": True,
            },
        }
    )
    assert sql["version"] == "sql_result.v1"
    plugin = normalize_verifier(
        {"kind": "plugin", "spec": {"plugin_id": "contains_v1", "params": {"needle": "OK"}}}
    )
    assert plugin["version"] == "plugin.v1"
    with pytest.raises(DataError, match="unsupported_plugin_id"):
        normalize_verifier(
            {"kind": "plugin", "spec": {"plugin_id": "eval_v1", "params": {}}}
        )
    with pytest.raises(DataError, match="unsupported_verifier_contract"):
        normalize_verifier({"kind": "exact", "version": "exact.v0", "spec": {"expected": "1"}})


def test_plugins_runtime_allowlisted_only():
    from lrp.judge.plugins_runtime import run_plugin

    assert run_plugin("contains_v1", {"needle": "OK"}, "say OK please") is True
    assert run_plugin("contains_v1", {"needle": "OK"}, "nope") is False
    assert run_plugin("json_equals_v1", {"expected": {"a": 1}}, '{"a":1}') is True
    assert run_plugin("numeric_equals_v1", {"expected": 4, "abs_tol": 0}, "4") is True
    with pytest.raises(ValueError, match="unknown_plugin"):
        run_plugin("os_system_v1", {}, "")


def test_sandbox_rejects_non_allowlisted_plugin_without_host_exec(tmp_path):
    rootfs = tmp_path / "rootfs"
    rootfs.mkdir(mode=0o700)
    sandbox = Sandbox(rootfs)
    quality, detail = sandbox.verify(
        {"kind": "plugin", "spec": {"plugin_id": "os_system_v1", "params": {}}},
        "ignored",
    )
    assert quality is None and detail["error_class"] == "unsupported_plugin_id"


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "verifier,content,passed",
    [
        (
            {
                "kind": "sql_result",
                "spec": {
                    "expected_rows": [{"n": 1}, {"n": 2}],
                    "ignore_row_order": True,
                },
            },
            '[{"n":2},{"n":1}]',
            True,
        ),
        (
            {
                "kind": "sql_result",
                "spec": {"expected_rows": [{"n": 1}], "columns": ["n"]},
            },
            '[{"n":9}]',
            False,
        ),
        (
            {"kind": "plugin", "spec": {"plugin_id": "contains_v1", "params": {"needle": "PASS"}}},
            "result PASS",
            True,
        ),
        (
            {
                "kind": "plugin",
                "spec": {"plugin_id": "numeric_equals_v1", "params": {"expected": 2.5, "abs_tol": 0.1}},
            },
            "2.55",
            True,
        ),
    ],
)
async def test_sql_and_plugin_verifier_orchestration(tmp_path, verifier, content, passed):
    class RecordingSandbox:
        rootfs = Path("/synthetic/rootfs")
        timeout_s = 30

        def verify(self, received, text):
            from lrp.judge.contracts import normalize_verifier
            from lrp.judge.plugins_runtime import run_plugin

            normalized = normalize_verifier(received)
            assert text == content
            if normalized["kind"] == "sql_result":
                expected = normalized["spec"]["expected_rows"]
                import json

                actual = json.loads(text)
                if normalized["spec"].get("ignore_row_order"):
                    ok = sorted(
                        tuple(sorted(row.items())) for row in expected
                    ) == sorted(tuple(sorted(row.items())) for row in actual)
                else:
                    ok = expected == actual
                return float(ok), {"passed": ok}
            ok = run_plugin(
                normalized["spec"]["plugin_id"],
                normalized["spec"].get("params", {}),
                text,
            )
            return float(ok), {"passed": ok}

    reqs, resps = inputs(
        tmp_path,
        request(verifier=verifier),
        anchor_changes={"content": content},
    )
    # candidate content must match
    write(
        resps,
        [
            response("synthetic/candidate", content=content),
            response(ANCHOR[1], content=content),
        ],
    )
    await judge_requests(
        requests=reqs,
        responses=resps,
        out=tmp_path / "out",
        anchor=ANCHOR,
        judge_model=JUDGE,
        sandbox=RecordingSandbox(),
    )
    rows = list(read_rows(tmp_path / "out"))
    assert all(r["quality"] == float(passed) for r in rows)
    assert all(r["method"].startswith("verifier:") for r in rows)


@pytest.mark.asyncio
async def test_human_audit_sample_during_judge_excludes_content(tmp_path):
    reqs, resps = inputs(tmp_path)
    seen = []
    async with httpx.AsyncClient(
        transport=mock_judge(
            ['{"winner":"A","reason":"secret reason"}', '{"winner":"B","reason":"secret reason"}'],
            seen,
        )
    ) as client:
        result = await judge_requests(
            requests=reqs,
            responses=resps,
            out=tmp_path / "out",
            anchor=ANCHOR,
            judge_model=JUDGE,
            client=client,
            audit_queue=tmp_path / "queue",
            audit_sample_rate=1.0,
            audit_seed="synthetic-audit",
        )
    assert result["audit_sampled"] >= 1
    queue_text = (tmp_path / "queue").read_text()
    assert "secret reason" not in queue_text
    assert "What is 2+2" not in queue_text
    assert '"content_included":false' in queue_text.replace(" ", "")


def test_human_audit_agreement_gate(tmp_path):
    from lrp.judge.audit import (
        build_agreement_report,
        sample_judgments,
        write_agreement_report,
    )

    judgments = write(
        tmp_path / "judgments",
        [
            {
                "schema_version": "lrp.judgment.v1",
                "request_id": f"synthetic-{index}",
                "source": "synthetic",
                "target": {"provider": "openrouter", "model": "synthetic/candidate"},
                "method": "pairwise_vs_anchor:v1",
                "quality": 1.0 if index < 8 else 0.0,
                "detail": {"votes": ["A", "B"], "swapped_agree": True},
                "judge_model": JUDGE,
                "judge_cost_usd": 0.0,
                "judged_at": "2026-09-09T00:00:00Z",
                "cache_key": f"synthetic-cache-{index}",
            }
            for index in range(10)
        ],
    )
    queue = tmp_path / "queue"
    stats = sample_judgments(judgments=judgments, out=queue, rate=1.0, seed="gate")
    assert stats["written"] == 10
    reviews = write(
        tmp_path / "reviews",
        [
            {
                "schema_version": "lrp.human_audit_review.v1",
                "request_id": f"synthetic-{index}",
                "target": {"provider": "openrouter", "model": "synthetic/candidate"},
                "human_quality": 1.0 if index < 8 else 0.0,
                "reviewer_id_hash": "sha256:synthetic-reviewer",
                "reviewed_at": "2026-09-09T01:00:00Z",
                "content_included": False,
            }
            for index in range(10)
        ],
    )
    report = build_agreement_report(
        queue=queue, reviews=reviews, min_agreement=0.8, min_coverage=0.5, min_reviews=5
    )
    assert report["gate_passed"] is True
    assert report["agreement_rate"] == 1.0
    assert report["content_included"] is False
    write_agreement_report(report, tmp_path / "report.json")
    disagree = write(
        tmp_path / "reviews-bad",
        [
            {
                "schema_version": "lrp.human_audit_review.v1",
                "request_id": f"synthetic-{index}",
                "target": {"provider": "openrouter", "model": "synthetic/candidate"},
                "human_quality": 0.0,
                "reviewer_id_hash": "sha256:synthetic-reviewer",
                "reviewed_at": "2026-09-09T01:00:00Z",
                "content_included": False,
            }
            for index in range(10)
        ],
    )
    failed = build_agreement_report(queue=queue, reviews=disagree, min_reviews=5)
    assert failed["gate_passed"] is False
    assert "agreement_below_floor" in failed["gate_reasons"]
    thin = build_agreement_report(
        queue=queue,
        reviews=write(
            tmp_path / "reviews-thin",
            [
                {
                    "schema_version": "lrp.human_audit_review.v1",
                    "request_id": "synthetic-0",
                    "target": {"provider": "openrouter", "model": "synthetic/candidate"},
                    "human_quality": 1.0,
                    "reviewer_id_hash": "sha256:synthetic-reviewer",
                    "reviewed_at": "2026-09-09T01:00:00Z",
                    "content_included": False,
                }
            ],
        ),
        min_reviews=5,
    )
    assert "insufficient_reviews" in thin["gate_reasons"]
    with pytest.raises(DataError, match="human_review_content_forbidden"):
        build_agreement_report(
            queue=queue,
            reviews=write(
                tmp_path / "reviews-leak",
                [
                    {
                        "schema_version": "lrp.human_audit_review.v1",
                        "request_id": "synthetic-0",
                        "target": {"provider": "openrouter", "model": "synthetic/candidate"},
                        "human_quality": 1.0,
                        "reviewer_id_hash": "sha256:synthetic-reviewer",
                        "reviewed_at": "2026-09-09T01:00:00Z",
                        "notes": "must not persist",
                    }
                ],
            ),
        )


def test_request_accepts_sql_result_and_plugin_verifiers():
    from lrp.collect import validate_record

    sql = request(
        verifier={
            "kind": "sql_result",
            "spec": {"expected_rows": [{"x": 1}], "ignore_row_order": False},
        }
    )
    assert validate_record(sql, "request")["verifier"]["kind"] == "sql_result"
    plugin = request(
        verifier={"kind": "plugin", "spec": {"plugin_id": "json_equals_v1", "params": {"expected": 1}}}
    )
    assert validate_record(plugin, "request")["verifier"]["kind"] == "plugin"


@pytest.mark.skipif(
    not os.environ.get("LRP_TEST_ROOTFS")
    and not os.environ.get("LRP_REQUIRE_SANDBOX_TESTS"),
    reason="requires operator-provisioned isolated Python/pytest rootfs and namespace support",
)
def test_real_sandbox_sql_result_and_plugin(tmp_path):
    assert os.environ.get("LRP_TEST_ROOTFS"), (
        "mandatory sandbox test requires LRP_TEST_ROOTFS"
    )
    sandbox = Sandbox(Path(os.environ["LRP_TEST_ROOTFS"]))
    assert (
        sandbox.verify(
            {
                "kind": "sql_result",
                "spec": {
                    "expected_rows": [{"id": 1}, {"id": 2}],
                    "columns": ["id"],
                    "ignore_row_order": True,
                },
            },
            '[{"id":2},{"id":1}]',
        )[0]
        == 1
    )
    assert (
        sandbox.verify(
            {"kind": "plugin", "spec": {"plugin_id": "contains_v1", "params": {"needle": "ok"}}},
            "looks ok",
        )[0]
        == 1
    )
    assert (
        sandbox.verify(
            {"kind": "plugin", "spec": {"plugin_id": "contains_v1", "params": {"needle": "ok"}}},
            "fail",
        )[0]
        == 0
    )
