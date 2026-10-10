#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Tests for scripts/pi_tool_continuation_smoke.py (Metrum AI Router)."""

from __future__ import annotations

import contextlib
import io
import json
import os
import sqlite3
import sys
import tempfile
import textwrap
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

import pi_tool_continuation_smoke as smoke  # noqa: E402

GROUP = "big-coder"
UPSTREAM = "gpt-6-luna"


def pi_lines(token: str, response_model: str, done: bool = True, tools: tuple[str, ...] = ("write", "read")) -> list[str]:
    events: list[dict] = [{"type": "session"}, {"type": "agent_start"}]
    for tool in tools:
        events.append({
            "type": "message_end",
            "message": {"role": "assistant", "content": [{"type": "toolCall", "name": tool}], "model": GROUP, "responseModel": response_model},
        })
        events.append({"type": "tool_execution_end", "toolName": tool, "isError": False})
    final = f"DONE {token}" if done else "I could not do it"
    events.append({
        "type": "message_end",
        "message": {"role": "assistant", "content": [{"type": "text", "text": final}], "model": GROUP, "responseModel": response_model},
    })
    events.append({"type": "agent_settled", "aborted": False})
    return [json.dumps(e) for e in events] + ["not json"]


def test_parse_pi_events() -> None:
    parsed = smoke.parse_pi_events(pi_lines("PROOF-abc", GROUP))
    assert parsed["tools"] == ["write", "read"], parsed
    assert parsed["final_text"] == "DONE PROOF-abc", parsed
    assert parsed["models"] == [GROUP] * 3, parsed
    upstream = smoke.parse_pi_events(pi_lines("PROOF-abc", UPSTREAM))
    assert upstream["models"] == [UPSTREAM] * 3, upstream
    # pi omits responseModel when it equals the requested model.
    same = smoke.parse_pi_events([json.dumps({"type": "message_end", "message": {"role": "assistant", "content": "hi", "model": GROUP}})])
    assert same["models"] == [GROUP], same
    assert parsed["bad_lines"] == 1, parsed
    errored = smoke.parse_pi_events([json.dumps({"type": "tool_execution_end", "toolName": "read", "isError": True})])
    assert errored["tools"] == [] and errored["tool_errors"] == ["read"], errored


def test_check_pi_result() -> None:
    token = "PROOF-abc"
    ok = smoke.parse_pi_events(pi_lines(token, GROUP))
    assert smoke.check_pi_result(0, ok, token, token + "\n") == []
    split = smoke.parse_pi_events([json.dumps({"type": "message_end", "message": {"role": "assistant", "content": f"DONE\n{token}"}})])
    assert "final assistant text" not in "\n".join(smoke.check_pi_result(0, split, token, token)), split
    failures = smoke.check_pi_result(1, smoke.parse_pi_events(pi_lines(token, GROUP, done=False, tools=("write",))), token, "other")
    joined = "\n".join(failures)
    for want in ("pi exited 1", "read tool", "contents do not match", "DONE <token>"):
        assert want in joined, failures
    assert "was not created" in "\n".join(smoke.check_pi_result(0, ok, token, None))


def test_parse_sse_models() -> None:
    body = "\n".join([
        'data: {"id":"c1","model":"big-coder","choices":[]}',
        "",
        ": keepalive",
        'data: {"id":"c1","model":"big-coder","choices":[],"usage":{}}',
        "data: [DONE]",
    ])
    models, chunks = smoke.parse_sse_models(body)
    assert models == [GROUP, GROUP] and chunks == 2, (models, chunks)
    models, _ = smoke.parse_sse_models("data: {broken")
    assert models == ["<invalid-json>"], models


def test_model_matches() -> None:
    assert smoke.model_matches("requested_group", GROUP, GROUP, UPSTREAM)
    assert not smoke.model_matches("requested_group", GROUP, UPSTREAM, UPSTREAM)
    assert smoke.model_matches("upstream", GROUP, UPSTREAM, UPSTREAM)
    assert smoke.model_matches("upstream", GROUP, UPSTREAM + "-2026-10-01", UPSTREAM)
    assert not smoke.model_matches("upstream", GROUP, GROUP, UPSTREAM)
    assert not smoke.model_matches("upstream", GROUP, UPSTREAM, "")
    assert not smoke.model_matches("upstream", GROUP, "gpt-6-lunar", UPSTREAM)
    assert smoke.check_models("requested_group", GROUP, [], [], "x") == ["x: no model values seen"]
    assert smoke.check_models("upstream", GROUP, [UPSTREAM, "deepseek"], [UPSTREAM, "deepseek"], "pi") == []


def test_check_usage_rows() -> None:
    rows = [
        {"request_id": "a", "status": 200, "requested_model": GROUP},
        {"request_id": "b", "status": 502, "requested_model": GROUP},
        {"request_id": "c", "status": 200, "requested_model": "other"},
    ]
    failures = smoke.check_usage_rows(rows, GROUP, 4)
    joined = "\n".join(failures)
    assert "at least 4" in joined and "b status 502" in joined and "c requested_model" in joined, failures
    assert smoke.check_usage_rows(rows[:1], GROUP, 1) == []


def test_build_pi_models_uses_token_file_not_token() -> None:
    cfg = smoke.build_pi_models("http://127.0.0.1:1/v1/", Path("/tmp/tok"), GROUP)
    provider = cfg["providers"]["metrum"]
    assert provider["baseUrl"] == "http://127.0.0.1:1/v1", provider
    assert provider["apiKey"] == "!cat /tmp/tok", provider
    assert [m["id"] for m in provider["models"]] == [GROUP], provider


def create_usage_db(path: Path) -> None:
    with sqlite3.connect(path) as conn:
        conn.execute(
            "CREATE TABLE request_usage (request_id TEXT PRIMARY KEY, status INTEGER, "
            "requested_model TEXT, target_provider TEXT, target_model TEXT)"
        )


def insert_usage(db: Path, request_id: str, model: str = UPSTREAM) -> None:
    with sqlite3.connect(db) as conn:
        conn.execute(
            "INSERT INTO request_usage VALUES (?, 200, ?, 'openai', ?)",
            (request_id, GROUP, model),
        )


class FakeRouter(BaseHTTPRequestHandler):
    db: Path
    reported: str
    counter = 0

    def log_message(self, *args) -> None:  # noqa: D401
        pass

    def do_POST(self) -> None:  # noqa: N802
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        assert self.headers["Authorization"] == "Bearer caller-secret"
        FakeRouter.counter += 1
        request_id = f"req_fake_{FakeRouter.counter}"
        insert_usage(self.db, request_id)
        self.send_response(200)
        self.send_header("X-Request-Id", request_id)
        if body.get("stream"):
            payload = "".join(
                f'data: {{"model":"{self.reported}","choices":[]}}\n\n' for _ in range(3)
            ) + "data: [DONE]\n\n"
            self.send_header("Content-Type", "text/event-stream")
        else:
            payload = json.dumps({"model": self.reported, "choices": []})
            self.send_header("Content-Type", "application/json")
        data = payload.encode()
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def write_fake_pi(path: Path, db: Path, response_model: str) -> None:
    path.write_text(
        textwrap.dedent(
            f"""\
            #!{sys.executable}
            import json, os, pathlib, re, sqlite3, sys
            agent = pathlib.Path(os.environ["PI_CODING_AGENT_DIR"])
            cfg = json.loads((agent / "models.json").read_text())
            assert cfg["providers"]["metrum"]["apiKey"].startswith("!cat ")
            assert str(pathlib.Path.home() / ".pi") not in str(agent)
            token = re.search(r"exactly: (\\S+) ", sys.argv[-1]).group(1)
            pathlib.Path("proof.txt").write_text(token)
            with sqlite3.connect({str(db)!r}) as conn:
                for i in range(3):
                    conn.execute("INSERT INTO request_usage VALUES (?, 200, 'big-coder', 'openai', 'gpt-6-luna')", (f"req_pi_{{i}}",))
            def msg(content):
                return {{"type": "message_end", "message": {{"role": "assistant", "content": content, "model": "big-coder", "responseModel": {response_model!r}}}}}
            for tool in ("write", "read"):
                print(json.dumps(msg([{{"type": "toolCall", "name": tool}}])))
                print(json.dumps({{"type": "tool_execution_end", "toolName": tool, "isError": False}}))
            print(json.dumps(msg([{{"type": "text", "text": "DONE " + token}}])))
            """
        ),
        encoding="utf-8",
    )
    path.chmod(0o755)


def run_main(identity: str, reported: str) -> tuple[int, dict, str]:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        db = root / "usage.sqlite"
        create_usage_db(db)
        insert_usage(db, "req_before_start")
        token_file = root / "METRUM_API_KEY"
        token_file.write_text("caller-secret\n", encoding="utf-8")
        fake_pi = root / "pi"
        write_fake_pi(fake_pi, db, reported)
        FakeRouter.db = db
        FakeRouter.reported = reported
        server = ThreadingHTTPServer(("127.0.0.1", 0), FakeRouter)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        out = io.StringIO()
        try:
            with contextlib.redirect_stdout(out):
                code = smoke.main([
                    "--base-url", f"http://127.0.0.1:{server.server_address[1]}/v1",
                    "--token-file", str(token_file),
                    "--usage-db", str(db),
                    "--expect-model-identity", identity,
                    "--pi", str(fake_pi),
                    "--timeout", "60",
                ])
        finally:
            server.shutdown()
            server.server_close()
        text = out.getvalue()
        return code, json.loads(text), text


def test_main_requested_group_passes() -> None:
    code, summary, text = run_main("requested_group", GROUP)
    assert code == 0, summary["failures"]
    assert summary["ok"] and summary["pi"]["tools"] == ["write", "read"], summary
    assert summary["chat"]["stream"]["chunks"] == 3 and summary["chat"]["stream"]["models"] == [GROUP], summary
    assert summary["chat"]["unary"]["target_model"] == UPSTREAM, summary
    assert len(summary["pi"]["usage_rows"]) == 3, summary
    assert "req_before_start" not in text, "rows before start must be ignored"
    assert "caller-secret" not in text, "caller token must never be printed"


def test_main_upstream_passes_and_mismatch_fails() -> None:
    code, summary, _ = run_main("upstream", UPSTREAM)
    assert code == 0, summary["failures"]
    code, summary, _ = run_main("requested_group", UPSTREAM)
    assert code == 1, summary
    assert any("does not match requested_group" in f for f in summary["failures"]), summary["failures"]
    code, summary, _ = run_main("upstream", GROUP)
    assert code == 1 and any("does not match upstream" in f for f in summary["failures"]), summary["failures"]


if __name__ == "__main__":
    os.environ.pop("PI_CODING_AGENT_DIR", None)
    test_parse_pi_events()
    test_check_pi_result()
    test_parse_sse_models()
    test_model_matches()
    test_check_usage_rows()
    test_build_pi_models_uses_token_file_not_token()
    test_main_requested_group_passes()
    test_main_upstream_passes_and_mismatch_fails()
    print("pi_tool_continuation_smoke_test: ok")
