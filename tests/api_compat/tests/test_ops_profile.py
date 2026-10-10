# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""OPS-02..09 operational contracts against an ops-profile router (issue #94).

The profile enables the SQLite usage DB, the response cache, caller quotas and
static failover groups, all pointed at an isolated loopback upstream whose
behavior each test programs directly. Metrum AI.
"""

from __future__ import annotations

import contextlib
import hashlib
import http.client
import json
import os
import shutil
import socket
import sqlite3
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable
from urllib.error import HTTPError
from urllib.request import Request, urlopen

import pytest

from conftest import (
    CALLER,
    CALLER_DIGEST,
    PROMPT_CANARY,
    assert_generated_artifacts_redacted,
    spawn_router,
    stop_router,
)

QUOTA_CALLER = "synthetic-api-compat-ops-quota"
OTHER_CALLER = "synthetic-api-compat-ops-other"
QUOTA_DIGEST = hashlib.sha256(QUOTA_CALLER.encode()).hexdigest()
OTHER_DIGEST = hashlib.sha256(OTHER_CALLER.encode()).hexdigest()
# Synthetic provider-side secret and host that must never reach the caller.
UPSTREAM_SECRET_CANARY = "sk-ops-upstream-secret-canary-0000"
UPSTREAM_HOST_CANARY = "internal-provider.ops-canary.invalid"
QUOTA_DAY_TOKENS = 1000

Handler = Callable[["OpsUpstream", dict[str, Any]], None]


class OpsUpstream(BaseHTTPRequestHandler):
    """Programmable loopback upstream. Bodies stay in ephemeral memory only."""

    protocol_version = "HTTP/1.1"
    calls: list[dict[str, Any]] = []
    handler: Handler | None = None
    lock = threading.Lock()

    def log_message(self, _format: str, *args: Any) -> None:
        pass

    @classmethod
    def reset(cls, handler: Handler | None = None) -> None:
        with cls.lock:
            cls.calls = []
            cls.handler = handler

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length) or b"{}")
        with self.__class__.lock:
            self.__class__.calls.append({"path": self.path, "body": body, "model": body.get("model")})
        handler = self.__class__.handler or default_ok
        handler(self, body)

    # Helpers used by handlers.
    def send_json(self, status: int, payload: Any, headers: dict[str, str] | None = None) -> None:
        raw = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def start_sse(self) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True

    def write_frames(self, frames: list[str]) -> None:
        for frame in frames:
            self.wfile.write(frame.encode())
            self.wfile.flush()


def chat_ok(usage: Any = "default", content: str = "ops chat", model: str = "") -> dict[str, Any]:
    body: dict[str, Any] = {
        "id": "chatcmpl_ops",
        "object": "chat.completion",
        "model": model,
        "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
    }
    if usage == "default":
        body["usage"] = {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
    elif usage is not _OMIT:
        body["usage"] = usage
    return body


_OMIT = object()


def responses_ok() -> dict[str, Any]:
    return {
        "id": "resp_ops",
        "object": "response",
        "status": "completed",
        "output": [{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "ops responses"}]}],
        "usage": {"input_tokens": 3, "output_tokens": 2, "total_tokens": 5},
    }


def messages_ok() -> dict[str, Any]:
    return {
        "id": "msg_ops",
        "type": "message",
        "role": "assistant",
        "model": "synthetic",
        "stop_reason": "end_turn",
        "content": [{"type": "text", "text": "ops messages"}],
        "usage": {"input_tokens": 3, "output_tokens": 2},
    }


def default_ok(handler: OpsUpstream, body: dict[str, Any]) -> None:
    if handler.path.endswith("/chat/completions"):
        handler.send_json(200, chat_ok())
    elif handler.path.endswith("/responses"):
        handler.send_json(200, responses_ok())
    else:
        handler.send_json(200, messages_ok())


@pytest.fixture(scope="module")
def ops_upstream():
    OpsUpstream.reset()
    server = ThreadingHTTPServer(("127.0.0.1", 0), OpsUpstream)
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    yield {"server": server, "url": f"http://127.0.0.1:{server.server_port}"}
    server.shutdown()
    thread.join(timeout=5)


def ops_config(work: Path, upstream_url: str, *, usage_db: Path | None = None, cache: bool = True) -> str:
    usage = (
        f'{{enabled: true, driver: sqlite, path: "{usage_db}", migration_policy: auto-safe}}'
        if usage_db is not None
        else "{enabled: false}"
    )
    cache_cfg = "{enabled: true, max_bytes: 1048576, default_ttl: 10m}" if cache else "{enabled: false}"
    chat_tools = "tool_support: {openai_chat: [tools, tool_choice, structured_outputs]}"
    return f'''server:
  listen: "127.0.0.1:{{port}}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {cache_cfg}
  usage_db: {usage}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  chat: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
  chat_backup: {{base_url: "{upstream_url}/backup/v1", dialect: openai-chat}}
  responses: {{base_url: "{upstream_url}/v1", dialect: openai-responses}}
  messages: {{base_url: "{upstream_url}/anthropic", dialect: anthropic}}
  unreachable: {{base_url: "http://127.0.0.1:{{dead_port}}/v1", dialect: openai-chat}}
models:
  chat: {{strategy: static, targets: [{{provider: chat, model: ops-chat, {chat_tools}, input_modalities: [text, image], reasoning: {{supported: true, mode: opt_in, control: effort_enum}}}}]}}
  chat-fallback: {{strategy: failover, targets: [{{provider: chat, model: ops-primary, {chat_tools}}}, {{provider: chat_backup, model: ops-backup, {chat_tools}}}]}}
  chat-priced-fallback: {{strategy: failover, targets: [{{provider: chat, model: ops-p-primary, input_price_per_million_usd: 1.0, output_price_per_million_usd: 2.0, cached_input_price_per_million_usd: 0.1}}, {{provider: chat_backup, model: ops-p-backup, input_price_per_million_usd: 3.0, output_price_per_million_usd: 6.0, cached_input_price_per_million_usd: 0.3}}]}}
  chat-dead-first: {{strategy: failover, targets: [{{provider: unreachable, model: ops-dead}}, {{provider: chat, model: ops-alive}}]}}
  chat-timeout: {{strategy: static, attempt_timeout_ms: 400, targets: [{{provider: chat, model: ops-slow}}]}}
  responses-fallback: {{strategy: failover, targets: [{{provider: responses, model: ops-r-primary}}, {{provider: responses, model: ops-r-backup}}]}}
  messages-fallback: {{strategy: failover, targets: [{{provider: messages, model: ops-m-primary, request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}}}, {{provider: messages, model: ops-m-backup, request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}}}]}}
  messages: {{strategy: static, targets: [{{provider: messages, model: ops-messages, input_price_per_million_usd: 2.0, output_price_per_million_usd: 4.0, cached_input_price_per_million_usd: 0.2, tool_support: {{anthropic_messages: [client_tools]}}, request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}}}]}}
callers:
  - id: ops-main
    token_sha256: {CALLER_DIGEST}
    allow: [chat, chat-fallback, chat-priced-fallback, chat-dead-first, chat-timeout, responses-fallback, messages-fallback, messages]
  - id: ops-other
    token_sha256: {OTHER_DIGEST}
    allow: [chat]
  - id: ops-quota
    token_sha256: {QUOTA_DIGEST}
    allow: [chat, chat-timeout]
    quota: {{day: {{tokens: {QUOTA_DAY_TOKENS}}}}}
'''


def _dead_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


@contextlib.contextmanager
def fast_workdir(tmp_path_factory, name: str):
    """Router work dir on tmpfs when available.

    Every request writes its usage rows synchronously; on a disk-backed /tmp each
    SQLite commit costs a full fsync, which made this module several times slower
    than the rest of the suite. The directory is removed afterwards.
    """
    shm = Path("/dev/shm")
    if shm.is_dir() and os.access(shm, os.W_OK):
        path = Path(tempfile.mkdtemp(prefix=f"{name}-", dir=shm))
        try:
            yield path
        finally:
            shutil.rmtree(path, ignore_errors=True)
    else:
        yield tmp_path_factory.mktemp(name)


@pytest.fixture(scope="module")
def ops_router(router, ops_upstream, tmp_path_factory):
    with fast_workdir(tmp_path_factory, "api-compat-ops") as work:
        usage_db = work / "usage.sqlite"
        config = ops_config(work, ops_upstream["url"], usage_db=usage_db).replace("{dead_port}", str(_dead_port()))
        handle = spawn_router(router, work, config)
        handle["usage_db"] = usage_db
        yield handle
        stop_router(handle)
        assert_generated_artifacts_redacted({"work": work})


@pytest.fixture(autouse=True)
def _reset_ops_upstream():
    OpsUpstream.reset()
    yield
    OpsUpstream.reset()


def call(base: str, path: str, payload: Any = None, *, token: str | None = CALLER, headers: dict[str, str] | None = None, timeout: float = 10):
    hdrs = {"Content-Type": "application/json"}
    if token is not None:
        hdrs["Authorization"] = f"Bearer {token}"
    hdrs.update(headers or {})
    data = None if payload is None else json.dumps(payload).encode()
    try:
        with urlopen(Request(base + path, data=data, headers=hdrs), timeout=timeout) as resp:
            return resp.status, dict(resp.headers), resp.read()
    except HTTPError as error:
        return error.code, dict(error.headers), error.read()


def chat_body(model: str = "chat", **extra: Any) -> dict[str, Any]:
    body = {"model": model, "messages": [{"role": "user", "content": PROMPT_CANARY}]}
    body.update(extra)
    return body


def usage_rows(usage_db: Path, request_id: str, *, timeout_s: float = 5.0, expect: int = 1) -> list[sqlite3.Row]:
    deadline = time.monotonic() + timeout_s
    rows: list[sqlite3.Row] = []
    while time.monotonic() < deadline:
        if usage_db.is_file():
            conn = sqlite3.connect(usage_db)
            conn.row_factory = sqlite3.Row
            try:
                rows = conn.execute("SELECT * FROM request_usage WHERE request_id = ?", (request_id,)).fetchall()
            except sqlite3.OperationalError:
                rows = []
            finally:
                conn.close()
            if len(rows) >= expect:
                return rows
        time.sleep(0.05)
    return rows


def attempt_rows(usage_db: Path, request_id: str) -> list[sqlite3.Row]:
    conn = sqlite3.connect(usage_db)
    conn.row_factory = sqlite3.Row
    try:
        return conn.execute(
            "SELECT * FROM request_attempts WHERE request_id = ? ORDER BY attempt_index", (request_id,)
        ).fetchall()
    finally:
        conn.close()


def estimate_row(usage_db: Path, request_id: str) -> sqlite3.Row | None:
    conn = sqlite3.connect(usage_db)
    conn.row_factory = sqlite3.Row
    try:
        return conn.execute("SELECT * FROM request_token_estimates WHERE request_id = ?", (request_id,)).fetchone()
    finally:
        conn.close()


def caller_usage(base: str, token: str) -> dict[str, Any]:
    status, _, raw = call(base, "/v1/usage", token=token)
    assert status == 200, raw
    return json.loads(raw)


def jsonl_record(work: Path, request_id: str, *, timeout_s: float = 5.0) -> dict[str, Any]:
    deadline = time.monotonic() + timeout_s
    path = Path(work) / "router.jsonl"
    while time.monotonic() < deadline:
        if path.is_file():
            for line in path.read_text().splitlines():
                try:
                    record = json.loads(line)
                except ValueError:
                    continue
                if record.get("request_id") == request_id or record.get("id") == request_id:
                    return record
        time.sleep(0.05)
    raise AssertionError(f"no router.jsonl record for {request_id}")


def chat_frame(delta: dict[str, Any] | None = None, *, finish: str | None = None, usage: dict[str, Any] | None = None, empty_choices: bool = False) -> str:
    payload: dict[str, Any] = {"id": "chatcmpl_ops", "object": "chat.completion.chunk"}
    if empty_choices:
        payload["choices"] = []
    else:
        payload["choices"] = [{"index": 0, "delta": delta or {}, "finish_reason": finish}]
    if usage is not None:
        payload["usage"] = usage
    return f"data: {json.dumps(payload)}\n\n"


def stream_open(base: str, payload: dict[str, Any], token: str = CALLER):
    host, port = base.removeprefix("http://").split(":")
    conn = http.client.HTTPConnection(host, int(port), timeout=10)
    conn.request(
        "POST",
        "/v1/chat/completions",
        body=json.dumps(payload),
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
    )
    return conn, conn.getresponse()


def _settled_total(rows: list[sqlite3.Row]) -> int:
    return sum(int(r["total_tokens"]) for r in rows)


def test_ops02_settlement_exactly_once_across_failure_classes(ops_router):
    """OPS-02: missing/null/zero usage, provider error, timeout, truncation, abort and late usage settle once."""
    base, db, work = ops_router["base"], ops_router["usage_db"], ops_router["work"]
    before = caller_usage(base, CALLER)
    request_ids: list[str] = []

    # 1-3. Unary 200 with omitted, null and all-zero usage: the router settles its
    # own estimate exactly once and marks the provenance as estimated, never as
    # a silent zero or as provider-reported usage.
    for usage in (_OMIT, None, {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}):
        OpsUpstream.reset(lambda h, b, usage=usage: h.send_json(200, chat_ok(usage=usage)))
        status, headers, raw = call(base, "/v1/chat/completions", chat_body())
        assert status == 200, raw
        rid = headers["X-Request-Id"]
        request_ids.append(rid)
        caller_visible = json.loads(raw)["usage"]
        estimate = estimate_row(db, rid) or {}
        rows = usage_rows(db, rid)
        assert len(rows) == 1
        assert rows[0]["total_tokens"] > 0, "unknown usage must not settle as silent zero"
        assert rows[0]["input_tokens"] == caller_visible["prompt_tokens"]
        if estimate:
            assert rows[0]["input_tokens"] == estimate["estimated_total_input_tokens"]
        assert "usage-estimated" in (jsonl_record(work, rid).get("warnings") or [])
        assert len(OpsUpstream.calls) == 1

    # 4. Provider 5xx with no usage: one failure row, no token settlement.
    OpsUpstream.reset(lambda h, b: h.send_json(500, {"error": {"message": "synthetic overload"}}))
    status, headers, raw = call(base, "/v1/chat/completions", chat_body())
    assert status == 502, raw
    rid = headers["X-Request-Id"]
    request_ids.append(rid)
    rows = usage_rows(db, rid)
    assert len(rows) == 1 and rows[0]["status"] == 502 and rows[0]["total_tokens"] == 0
    assert "usage-estimated" not in (jsonl_record(work, rid).get("warnings") or [])

    # 5. Attempt timeout: 504 upstream-timeout, one row, nothing settled.
    def slow(h: OpsUpstream, b: dict[str, Any]) -> None:
        time.sleep(1.5)
        try:
            h.send_json(200, chat_ok())
        except OSError:
            pass

    OpsUpstream.reset(slow)
    status, headers, raw = call(base, "/v1/chat/completions", chat_body("chat-timeout"))
    assert status == 504, raw
    assert json.loads(raw)["error"]["type"] == "upstream-timeout"
    rid = headers["X-Request-Id"]
    request_ids.append(rid)
    rows = usage_rows(db, rid)
    assert len(rows) == 1 and rows[0]["status"] == 504 and rows[0]["total_tokens"] == 0
    assert [a["timed_out"] for a in attempt_rows(db, rid)] == [1]

    # 6. Late usage: finish chunk first, usage-only empty-choices chunk after it.
    # Provider-reported totals settle once (not doubled, not replaced by estimate).
    late_frames = [
        chat_frame({"role": "assistant", "content": "late usage"}),
        chat_frame(finish="stop"),
        chat_frame(empty_choices=True, usage={"prompt_tokens": 7, "completion_tokens": 4, "total_tokens": 11}),
        "data: [DONE]\n\n",
    ]

    def late(h: OpsUpstream, b: dict[str, Any]) -> None:
        h.start_sse()
        h.write_frames(late_frames)

    OpsUpstream.reset(late)
    conn, resp = stream_open(base, chat_body(stream=True, stream_options={"include_usage": True}))
    body = resp.read()
    rid = resp.getheader("X-Request-Id")
    conn.close()
    assert resp.status == 200 and b"[DONE]" in body
    request_ids.append(rid)
    rows = usage_rows(db, rid)
    assert len(rows) == 1
    assert (rows[0]["input_tokens"], rows[0]["output_tokens"], rows[0]["total_tokens"]) == (7, 4, 11)
    assert "usage-estimated" not in (jsonl_record(work, rid).get("warnings") or [])

    # 7. Truncation after commitment: EOF after one content frame, no usage.
    def truncated(h: OpsUpstream, b: dict[str, Any]) -> None:
        h.start_sse()
        h.write_frames([chat_frame({"role": "assistant", "content": "partial"})])

    OpsUpstream.reset(truncated)
    conn, resp = stream_open(base, chat_body(stream=True))
    body = resp.read()
    rid = resp.getheader("X-Request-Id")
    conn.close()
    assert b"partial" in body
    assert b"[DONE]" not in body, "truncated upstream must not gain a synthetic success sentinel"
    request_ids.append(rid)
    rows = usage_rows(db, rid)
    assert len(rows) == 1 and rows[0]["status"] != 200
    # No usage arrived before EOF: the settled estimate is marked as estimated.
    assert rows[0]["total_tokens"] > 0
    assert "usage-estimated" in (jsonl_record(work, rid).get("warnings") or [])
    assert len(OpsUpstream.calls) == 1, "no replay after downstream commitment"

    # 8. Client abort mid-stream while the upstream is still producing.
    gate = threading.Event()

    def gated(h: OpsUpstream, b: dict[str, Any]) -> None:
        h.start_sse()
        h.write_frames([chat_frame({"role": "assistant", "content": "before-abort"})])
        gate.wait(timeout=10)
        try:
            h.write_frames([chat_frame(finish="stop", usage={"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}), "data: [DONE]\n\n"])
        except OSError:
            pass

    OpsUpstream.reset(gated)
    conn, resp = stream_open(base, chat_body(stream=True))
    rid = resp.getheader("X-Request-Id")
    assert b"before-abort" in resp.fp.readline() + resp.fp.readline()
    conn.sock.shutdown(socket.SHUT_RDWR)
    conn.close()
    request_ids.append(rid)
    rows = usage_rows(db, rid)
    gate.set()
    assert len(rows) == 1 and rows[0]["status"] == 499
    assert "usage-estimated" in (jsonl_record(work, rid).get("warnings") or [])
    assert len(OpsUpstream.calls) == 1

    # Exactly one settlement per request: caller counters moved by exactly the
    # sum of the per-request rows, and every request is counted once.
    after = caller_usage(base, CALLER)
    all_rows = [usage_rows(db, rid)[0] for rid in request_ids]
    assert after["day"]["requests"] - before["day"]["requests"] == len(request_ids)
    assert after["day"]["tokens"] - before["day"]["tokens"] == _settled_total(all_rows)


def _wait_for(predicate: Callable[[], bool], timeout_s: float = 5.0) -> bool:
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(0.02)
    return predicate()


def test_ops03_parallel_admissions_at_quota_boundary(ops_router):
    """OPS-03: concurrent reservations cannot overshoot the day token budget or leak."""
    base, db = ops_router["base"], ops_router["usage_db"]
    start = caller_usage(base, QUOTA_CALLER)
    assert start["day"]["tokens"] == 0
    gate = threading.Event()

    def gated_ok(h: OpsUpstream, b: dict[str, Any]) -> None:
        gate.wait(timeout=15)
        h.send_json(200, chat_ok())

    OpsUpstream.reset(gated_ok)
    # Each request reserves its input estimate plus a 450-token output cap, so
    # at most two can be in flight against a 1000-token day budget.
    payload = chat_body(max_tokens=450)
    results: list[tuple[int, dict[str, str], bytes]] = []
    lock = threading.Lock()

    def worker() -> None:
        out = call(base, "/v1/chat/completions", payload, token=QUOTA_CALLER, timeout=20)
        with lock:
            results.append(out)

    threads = [threading.Thread(target=worker) for _ in range(6)]
    for t in threads:
        t.start()
    # Rejections answer immediately; admitted requests park on the gate.
    assert _wait_for(lambda: len(results) == 4 and len(OpsUpstream.calls) == 2, 10), (
        len(results),
        len(OpsUpstream.calls),
    )
    rejected = list(results)
    for status, _, raw in rejected:
        assert status == 429, raw
        assert json.loads(raw)["error"]["type"] == "quota-exhausted"
    gate.set()
    for t in threads:
        t.join(timeout=20)
    statuses = sorted(r[0] for r in results)
    assert statuses == [200, 200, 429, 429, 429, 429]
    assert len(OpsUpstream.calls) == 2, "rejected admissions must never reach the upstream"

    # Reservations reconcile to provider-reported usage (5 tokens each).
    after = caller_usage(base, QUOTA_CALLER)
    assert after["day"]["tokens"] == 10
    for status, headers, _ in results:
        rows = usage_rows(db, headers["X-Request-Id"])
        assert len(rows) == 1
        assert rows[0]["total_tokens"] == (5 if status == 200 else 0)

    # Cancel racing completion: abort a gated request; its reservation must be
    # released, so a near-budget follow-up is still admitted afterwards.
    gate2 = threading.Event()

    def gated_abort(h: OpsUpstream, b: dict[str, Any]) -> None:
        gate2.wait(timeout=15)
        try:
            h.send_json(200, chat_ok())
        except OSError:
            pass

    OpsUpstream.reset(gated_abort)
    host, port = base.removeprefix("http://").split(":")
    conn = http.client.HTTPConnection(host, int(port), timeout=10)
    conn.request(
        "POST",
        "/v1/chat/completions",
        body=json.dumps(chat_body(max_tokens=900)),
        headers={"Authorization": f"Bearer {QUOTA_CALLER}", "Content-Type": "application/json"},
    )
    assert _wait_for(lambda: len(OpsUpstream.calls) == 1)
    # While the 900-token reservation is in flight, a second large request is rejected.
    status, _, raw = call(base, "/v1/chat/completions", chat_body(max_tokens=450), token=QUOTA_CALLER)
    assert status == 429, raw
    conn.sock.shutdown(socket.SHUT_RDWR)
    conn.close()
    time.sleep(0.3)
    gate2.set()
    OpsUpstream.reset()
    assert _wait_for(
        lambda: call(base, "/v1/chat/completions", chat_body(max_tokens=900), token=QUOTA_CALLER)[0] == 200, 5
    ), "aborted request leaked its reservation"
    final = caller_usage(base, QUOTA_CALLER)
    assert final["day"]["tokens"] <= QUOTA_DAY_TOKENS
    assert final["day"]["tokens"] == 15


CALLER_SURFACES = {
    # caller dialect -> (path, fallback group, request body builder, primary/backup models)
    "openai-chat": (
        "/v1/chat/completions",
        lambda: chat_body("chat-fallback"),
        ("ops-primary", "ops-backup"),
    ),
    "openai-responses": (
        "/v1/responses",
        lambda: {"model": "responses-fallback", "input": PROMPT_CANARY},
        ("ops-r-primary", "ops-r-backup"),
    ),
    "anthropic": (
        "/anthropic/v1/messages",
        lambda: {"model": "messages-fallback", "max_tokens": 64, "messages": [{"role": "user", "content": PROMPT_CANARY}]},
        ("ops-m-primary", "ops-m-backup"),
    ),
}

# (upstream status, upstream body, expected attempt class, fallback expected,
#  terminal caller status/code when every target fails the same way).
ERROR_CLASS_MATRIX = [
    (400, {"error": {"message": "bad param"}}, "upstream_bad_request", False, 502, "upstream-failed"),
    (401, {"error": {"code": "invalid_api_key"}}, "upstream_auth_failed", False, 503, "upstream-access-denied"),
    (402, {"error": {"code": "insufficient_quota"}}, "upstream_quota_exhausted", True, 503, "upstream-quota-exhausted"),
    (403, {"error": {"code": "not_entitled"}}, "upstream_entitlement_failed", True, 503, "upstream-access-denied"),
    (404, {"error": {"code": "model_not_found"}}, "upstream_model_access_denied", True, 503, "upstream-access-denied"),
    (408, {"error": {"message": "timeout"}}, "upstream_timeout", True, 504, "upstream-timeout"),
    (413, {"error": {"message": "too large"}}, "upstream_request_too_large", False, 502, "upstream-failed"),
    (429, {"error": {"message": "slow down"}}, "upstream_rate_limited", True, 503, "upstream-rate-limited"),
    (429, {"error": {"code": "insufficient_quota"}}, "upstream_quota_exhausted", True, 503, "upstream-quota-exhausted"),
    (500, {"error": {"message": "boom"}}, "upstream_status_5xx", True, 502, "upstream-failed"),
    (503, {"error": {"type": "overloaded_error"}}, "upstream_status_5xx", True, 502, "upstream-failed"),
]


def _error_type(raw: bytes) -> str:
    payload = json.loads(raw)
    return payload["error"]["type"]


@pytest.mark.parametrize("dialect", sorted(CALLER_SURFACES))
def test_ops07_error_classes_retry_policy_across_dialects(ops_router, dialect):
    """OPS-07: each upstream class keeps its retry/fallback policy for every caller dialect."""
    base, db = ops_router["base"], ops_router["usage_db"]
    path, build, (primary, backup) = CALLER_SURFACES[dialect]
    for status, body, klass, fallback, all_fail_status, all_fail_code in ERROR_CLASS_MATRIX:
        # Primary fails with the class, backup succeeds.
        def primary_fails(h: OpsUpstream, b: dict[str, Any], status=status, body=body) -> None:
            if b.get("model") == primary:
                h.send_json(status, body)
            else:
                default_ok(h, b)

        OpsUpstream.reset(primary_fails)
        code, headers, raw = call(base, path, build())
        models = [c["model"] for c in OpsUpstream.calls]
        rid = headers["X-Request-Id"]
        attempts = attempt_rows(db, rid) if usage_rows(db, rid) else []
        where = f"{dialect} {status} {klass}"
        assert attempts and attempts[0]["error_class"] == klass, where
        if fallback:
            assert code == 200, (where, raw)
            assert models == [primary, backup], where
            assert [a["selected"] for a in attempts] == [0, 1], where
            assert attempts[0]["fallback_reason"] == klass, where
            assert usage_rows(db, rid)[0]["fallback_used"] == 1, where
        else:
            assert (code, _error_type(raw)) == (all_fail_status, all_fail_code), (where, raw)
            assert models == [primary], f"{where}: non-retryable class must not fall back"
            details = json.loads(raw)["error"]["details"]
            assert details["error_class"] == klass and details["attempts"] == 1, where
            assert details["retryable"] is False, where
            assert headers.get("X-Upstream-Status") == str(status), where

        # Every target fails the same way: terminal status/code per class, bounded attempts.
        OpsUpstream.reset(lambda h, b, status=status, body=body: h.send_json(status, body))
        code, headers, raw = call(base, path, build())
        assert (code, _error_type(raw)) == (all_fail_status, all_fail_code), (where, raw)
        assert len(OpsUpstream.calls) == (2 if fallback else 1), where
        details = json.loads(raw)["error"]["details"]
        assert details["error_class"] == klass and details["attempts"] == len(OpsUpstream.calls), where


def test_ops08_retry_after_redirect_and_unreachable_fallback_bounded(ops_router):
    """OPS-08: Retry-After variants, upstream redirects and dead targets stay bounded and sanitized."""
    base, db = ops_router["base"], ops_router["usage_db"]
    far_future = "Wed, 01 Jan 2120 00:00:00 GMT"
    cases = [
        ("3600", 3_600_000),  # very large delta-seconds
        (far_future, None),  # HTTP-date far in the future
        ("not-a-date", 0),  # invalid
        ("-5", 0),  # negative
    ]
    for header, expected_ms in cases:
        OpsUpstream.reset(
            lambda h, b, header=header: h.send_json(
                429,
                {"error": {"message": f"rate limited; key={UPSTREAM_SECRET_CANARY} host={UPSTREAM_HOST_CANARY}"}},
                headers={"Retry-After": header, "X-Provider-Debug": UPSTREAM_SECRET_CANARY},
            )
        )
        started = time.monotonic()
        code, headers, raw = call(base, "/v1/chat/completions", chat_body("chat-fallback"))
        elapsed = time.monotonic() - started
        assert code == 503 and _error_type(raw) == "upstream-rate-limited", raw
        # One attempt per target and no sleeping on the provider's Retry-After.
        assert len(OpsUpstream.calls) == 2
        assert elapsed < 5, f"Retry-After {header!r} stalled the request for {elapsed:.1f}s"
        # Provider secrets, hosts and debug headers never reach the caller.
        flat = raw.decode() + json.dumps(headers)
        assert UPSTREAM_SECRET_CANARY not in flat
        assert UPSTREAM_HOST_CANARY not in flat
        assert "X-Provider-Debug" not in headers
        # The safe diagnostic category survives sanitization.
        details = json.loads(raw)["error"]["details"]
        assert details["error_class"] == "upstream_rate_limited" and details["upstream_status"] == 429
        assert headers.get("X-Router-Error-Class") == "upstream_rate_limited"
        recorded = [a["retry_after_ms"] for a in attempt_rows(db, headers["X-Request-Id"])]
        if expected_ms is None:
            assert all(ms > 0 for ms in recorded), recorded
        else:
            assert recorded == [expected_ms, expected_ms], (header, recorded)

    # Upstream redirect: never followed (no egress to the Location), not retried.
    def redirect(h: OpsUpstream, b: dict[str, Any]) -> None:
        h.send_response(307)
        h.send_header("Location", f"http://{UPSTREAM_HOST_CANARY}/v1/chat/completions")
        h.send_header("Content-Length", "0")
        h.end_headers()

    OpsUpstream.reset(redirect)
    code, headers, raw = call(base, "/v1/chat/completions", chat_body())
    assert code == 502 and _error_type(raw) == "upstream-failed", raw
    assert len(OpsUpstream.calls) == 1
    assert "Location" not in headers and UPSTREAM_HOST_CANARY not in raw.decode()
    assert json.loads(raw)["error"]["details"]["upstream_status"] == 307

    # Unreachable first target: bounded network failure, then eligible fallback.
    OpsUpstream.reset()
    started = time.monotonic()
    code, headers, raw = call(base, "/v1/chat/completions", chat_body("chat-dead-first"))
    assert code == 200, raw
    assert time.monotonic() - started < 5
    assert [c["model"] for c in OpsUpstream.calls] == ["ops-alive"]
    attempts = attempt_rows(db, headers["X-Request-Id"]) if usage_rows(db, headers["X-Request-Id"]) else []
    assert [(a["error_class"], a["selected"]) for a in attempts] == [("upstream_network_error", 0), ("", 1)]
    assert "127.0.0.1" not in raw.decode()

    # Persisted diagnostics are sanitized too: no provider secret or host canary.
    time.sleep(0.5)
    for path in Path(ops_router["work"]).rglob("*"):
        if path.is_file():
            data = path.read_bytes()
            assert UPSTREAM_SECRET_CANARY.encode() not in data, path.name
            assert UPSTREAM_HOST_CANARY.encode() not in data, path.name



def _counting_chat_upstream():
    counter = {"n": 0}

    def handler(h: OpsUpstream, b: dict[str, Any]) -> None:
        counter["n"] += 1
        h.send_json(200, chat_ok(content=f"cache-probe-{counter['n']}"))

    return handler


def _content(raw: bytes) -> str:
    return json.loads(raw)["choices"][0]["message"]["content"]


def test_ops05_cache_keys_never_collide_across_semantic_variants(ops_router):
    """OPS-05: requests differing only in one semantic field never share a cache entry."""
    base, db = ops_router["base"], ops_router["usage_db"]
    OpsUpstream.reset(_counting_chat_upstream())
    base_body = chat_body(temperature=0)

    def send(body: dict[str, Any], token: str = CALLER) -> tuple[str, str]:
        code, headers, raw = call(base, "/v1/chat/completions", body, token=token)
        assert code == 200, raw
        rows = usage_rows(db, headers["X-Request-Id"])
        return _content(raw), rows[0]["cache"]

    first, state = send(base_body)
    assert state == "miss"
    again, state = send(base_body)
    assert (again, state) == (first, "hit"), "deterministic baseline must be served from cache"
    assert len(OpsUpstream.calls) == 1

    image_a = {"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgo="}}
    image_b = {"type": "image_url", "image_url": {"url": "data:image/png;base64,R0lGODlhAQABAAAAACw="}}
    cacheable_variants = {
        "stop": {"stop": ["END"]},
        "max_tokens": {"max_tokens": 77},
        "reasoning_effort": {"reasoning_effort": "low"},
        "seed": {"seed": 7},
        "top_p": {"top_p": 0.5},
    }
    seen = {first}
    for name, extra in cacheable_variants.items():
        body = base_body | extra
        content, state = send(body)
        assert state == "miss", f"{name}: variant must not hit the baseline entry"
        assert content not in seen, f"{name}: collided with an earlier cache entry"
        seen.add(content)
        repeat, state = send(body)
        assert (repeat, state) == (content, "hit"), f"{name}: variant must reuse only its own entry"

    # Caller scope: an identical body from another caller never reads this caller's entry.
    content, state = send(base_body, token=OTHER_CALLER)
    assert state == "miss" and content not in seen
    seen.add(content)

    # Tool-bearing, tool_choice, schema-constrained and image requests bypass the
    # cache entirely, including image-order variants: every one reaches upstream.
    tool = {"type": "function", "function": {"name": "lookup", "parameters": {"type": "object"}}}
    schema = {"type": "json_schema", "json_schema": {"name": "s", "schema": {"type": "object"}}}
    bypass_variants = {
        "tools": {"tools": [tool]},
        "tool_choice_required": {"tools": [tool], "tool_choice": "required"},
        "response_format": {"response_format": schema},
        "image_order_ab": {"messages": [{"role": "user", "content": [{"type": "text", "text": PROMPT_CANARY}, image_a, image_b]}]},
        "image_order_ba": {"messages": [{"role": "user", "content": [{"type": "text", "text": PROMPT_CANARY}, image_b, image_a]}]},
    }
    for name, extra in bypass_variants.items():
        for _ in range(2):
            calls_before = len(OpsUpstream.calls)
            code, headers, raw = call(base, "/v1/chat/completions", base_body | extra)
            assert code == 200, (name, raw)
            assert len(OpsUpstream.calls) == calls_before + 1, f"{name}: must bypass the response cache"
            assert usage_rows(db, headers["X-Request-Id"])[0]["cache"] == "bypass", name

    # Streaming and non-deterministic sampling also bypass.
    for name, extra in {"temperature_omitted": {"temperature": None}, "temperature_0_7": {"temperature": 0.7}}.items():
        body = {k: v for k, v in (base_body | extra).items() if v is not None}
        calls_before = len(OpsUpstream.calls)
        send(body)
        send(body)
        assert len(OpsUpstream.calls) == calls_before + 2, name


def test_ops05_choice_count_is_part_of_cache_identity(ops_router):
    """OPS-05: n (choice count) changes the response shape and must not share a cache entry."""
    base, db = ops_router["base"], ops_router["usage_db"]
    OpsUpstream.reset(_counting_chat_upstream())
    body = chat_body(temperature=0, messages=[{"role": "user", "content": PROMPT_CANARY + " n-probe"}])
    code, headers, raw = call(base, "/v1/chat/completions", body | {"n": 1})
    assert code == 200
    code, headers, raw = call(base, "/v1/chat/completions", body | {"n": 2})
    assert code == 200
    assert usage_rows(db, headers["X-Request-Id"])[0]["cache"] != "hit"
    assert len(OpsUpstream.calls) == 2


def _approx(a: float, b: float) -> bool:
    return abs(a - b) < 1e-9


def test_ops04_cache_and_reasoning_details_attribute_to_serving_target(ops_router):
    """OPS-04: provider cache/reasoning details, cost and identity follow the target that served."""
    base, db = ops_router["base"], ops_router["usage_db"]
    usage = {
        "prompt_tokens": 100,
        "completion_tokens": 50,
        "total_tokens": 150,
        "prompt_tokens_details": {"cached_tokens": 40},
        "completion_tokens_details": {"reasoning_tokens": 12},
    }

    def primary_down(h: OpsUpstream, b: dict[str, Any]) -> None:
        if b.get("model") == "ops-p-primary":
            h.send_json(503, {"error": {"message": "overloaded"}})
        else:
            h.send_json(200, chat_ok(usage=usage))

    OpsUpstream.reset(primary_down)
    code, headers, raw = call(base, "/v1/chat/completions", chat_body("chat-priced-fallback"))
    assert code == 200, raw
    visible = json.loads(raw)["usage"]
    assert visible["prompt_tokens"] == 100 and visible["completion_tokens"] == 50
    assert visible["prompt_tokens_details"]["cached_tokens"] == 40
    assert visible["completion_tokens_details"]["reasoning_tokens"] == 12
    rid = headers["X-Request-Id"]
    row = usage_rows(db, rid)[0]
    # Serving identity is the backup, not the configured primary.
    assert (row["target_provider"], row["target_model"]) == ("chat_backup", "ops-p-backup")
    assert row["attempts"] == 2 and row["fallback_used"] == 1
    assert (row["input_tokens"], row["output_tokens"], row["total_tokens"]) == (100, 50, 150)
    assert (row["cached_input_tokens"], row["reasoning_tokens"]) == (40, 12)
    # Cost uses the serving target's prices: 60 uncached @3.0 + 40 cached @0.3, 50 out @6.0.
    assert (row["input_price_per_million_usd"], row["output_price_per_million_usd"]) == (3.0, 6.0)
    assert row["cached_input_price_per_million_usd"] == 0.3
    assert _approx(row["input_cost_usd"], (60 * 3.0 + 40 * 0.3) / 1e6)
    assert _approx(row["output_cost_usd"], 50 * 6.0 / 1e6)
    assert _approx(row["total_cost_usd"], row["input_cost_usd"] + row["output_cost_usd"])
    # Provider prompt cache is not the router response cache.
    assert row["cache"] != "hit"
    attempts = attempt_rows(db, rid)
    assert [(a["provider"], a["model"], a["selected"], a["error_class"]) for a in attempts] == [
        ("chat", "ops-p-primary", 0, "upstream_status_5xx"),
        ("chat_backup", "ops-p-backup", 1, ""),
    ]
    assert attempts[1]["reasoning_tokens"] == 12 and attempts[0]["reasoning_tokens"] is None

    # Omitted provider details stay unknown (NULL), not reported zero.
    OpsUpstream.reset(lambda h, b: h.send_json(200, chat_ok()))
    code, headers, raw = call(base, "/v1/chat/completions", chat_body("chat-priced-fallback"))
    row = usage_rows(db, headers["X-Request-Id"])[0]
    assert (row["cached_input_tokens"], row["reasoning_tokens"]) == (None, None)
    assert (row["target_model"], row["attempts"], row["fallback_used"]) == ("ops-p-primary", 1, 0)

    # Responses details map onto the same canonical columns.
    OpsUpstream.reset(
        lambda h, b: h.send_json(
            200,
            responses_ok()
            | {
                "usage": {
                    "input_tokens": 30,
                    "output_tokens": 20,
                    "total_tokens": 50,
                    "input_tokens_details": {"cached_tokens": 10},
                    "output_tokens_details": {"reasoning_tokens": 5},
                }
            },
        )
    )
    code, headers, raw = call(base, "/v1/responses", {"model": "responses-fallback", "input": PROMPT_CANARY})
    assert code == 200, raw
    row = usage_rows(db, headers["X-Request-Id"])[0]
    assert (row["input_tokens"], row["output_tokens"], row["cached_input_tokens"], row["reasoning_tokens"]) == (30, 20, 10, 5)

    # A router response-cache hit is attributed as a hit and consumes no quota.
    OpsUpstream.reset(lambda h, b: h.send_json(200, chat_ok(usage=usage)))
    body = chat_body("chat-priced-fallback", temperature=0)
    call(base, "/v1/chat/completions", body)
    before = caller_usage(base, CALLER)
    code, headers, raw = call(base, "/v1/chat/completions", body)
    assert code == 200 and len(OpsUpstream.calls) == 1
    row = usage_rows(db, headers["X-Request-Id"])[0]
    assert row["cache"] == "hit"
    assert caller_usage(base, CALLER)["day"]["tokens"] == before["day"]["tokens"]
    # No upstream attempt, and the cached response's provider prompt-cache count
    # is not replayed as prompt-cache evidence for this request.
    assert row["attempts"] == 0
    assert row["cached_input_tokens"] is None


ANTH_TOOL = {
    "name": "lookup",
    "description": "ops cache-control tool",
    "input_schema": {"type": "object", "properties": {"q": {"type": "string"}}},
    "cache_control": {"type": "ephemeral"},
}


def _anthropic_cached_body(turns: list[dict[str, Any]], ttl_block: dict[str, Any] | None = None) -> dict[str, Any]:
    return {
        "model": "messages",
        "max_tokens": 64,
        "system": [
            {"type": "text", "text": "ops system preamble"},
            {"type": "text", "text": "ops cached system block", "cache_control": {"type": "ephemeral"}},
        ],
        "tools": [ANTH_TOOL],
        "messages": turns,
    }


def test_ops06_anthropic_cache_control_breakpoints_and_accounting(ops_router):
    """OPS-06: cache_control breakpoints survive and cache read/write accounting stays consistent."""
    base, db = ops_router["base"], ops_router["usage_db"]
    user_turn = {
        "role": "user",
        "content": [
            {"type": "text", "text": PROMPT_CANARY},
            {"type": "text", "text": "long cached history", "cache_control": {"type": "ephemeral", "ttl": "1h"}},
        ],
    }
    usages = [
        # Turn 1 writes the cache; turn 2 reads it back.
        {"input_tokens": 10, "cache_creation_input_tokens": 200, "cache_read_input_tokens": 0, "output_tokens": 5},
        {"input_tokens": 12, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 200, "output_tokens": 6},
        # Provider omitted cache fields entirely.
        {"input_tokens": 9, "output_tokens": 4},
    ]
    seq = {"i": 0}

    def upstream(h: OpsUpstream, b: dict[str, Any]) -> None:
        usage = usages[seq["i"]]
        seq["i"] += 1
        h.send_json(200, messages_ok() | {"usage": usage})

    OpsUpstream.reset(upstream)
    turns = [user_turn]
    rows = []
    visible_usages = []
    for i in range(3):
        code, headers, raw = call(base, "/anthropic/v1/messages", _anthropic_cached_body(turns))
        assert code == 200, raw
        visible_usages.append(json.loads(raw)["usage"])
        rows.append(usage_rows(db, headers["X-Request-Id"])[0])
        sent = OpsUpstream.calls[-1]["body"]
        # Every breakpoint reaches the native upstream exactly as sent, in place.
        assert sent["system"][1]["cache_control"] == {"type": "ephemeral"}
        assert "cache_control" not in sent["system"][0]
        assert sent["tools"][0]["cache_control"] == {"type": "ephemeral"}
        assert sent["messages"][0]["content"][1]["cache_control"] == {"type": "ephemeral", "ttl": "1h"}
        assert "cache_control" not in sent["messages"][0]["content"][0]
        assert len(sent["messages"]) == len(turns)
        turns = turns + [
            {"role": "assistant", "content": [{"type": "text", "text": f"turn {i}"}]},
            {"role": "user", "content": [{"type": "text", "text": f"next {i}"}]},
        ]

    # Canonical input includes cache reads and writes; cached = cache reads only.
    assert [(r["input_tokens"], r["output_tokens"], r["total_tokens"]) for r in rows] == [
        (210, 5, 215),
        (212, 6, 218),
        (9, 4, 13),
    ]
    # Reported zero stays zero; an omitted metric stays unknown, never a fabricated hit.
    assert [r["cached_input_tokens"] for r in rows] == [0, 200, None]
    # Cost: cached reads at the cached price, everything else at the input price.
    assert _approx(rows[1]["input_cost_usd"], (12 * 2.0 + 200 * 0.2) / 1e6)
    assert _approx(rows[0]["input_cost_usd"], 210 * 2.0 / 1e6)
    # The caller sees the provider's native cache fields unchanged (no fabricated keys).
    assert visible_usages == usages

    # Invalid TTL is provider-owned validation: forwarded unchanged once, then the
    # provider rejection propagates sanitized as a non-retryable bad request.
    OpsUpstream.reset(
        lambda h, b: h.send_json(
            400, {"type": "error", "error": {"type": "invalid_request_error", "message": "cache_control.ttl invalid"}}
        )
    )
    bad = _anthropic_cached_body(
        [{"role": "user", "content": [{"type": "text", "text": "x", "cache_control": {"type": "ephemeral", "ttl": "7d"}}]}]
    )
    code, headers, raw = call(base, "/anthropic/v1/messages", bad)
    assert len(OpsUpstream.calls) == 1
    assert OpsUpstream.calls[0]["body"]["messages"][0]["content"][0]["cache_control"]["ttl"] == "7d"
    assert code == 502, raw
    details = json.loads(raw)["error"]["details"]
    assert details["error_class"] == "upstream_bad_request" and details["attempts"] == 1


def _count_rows(usage_db: Path) -> int:
    conn = sqlite3.connect(usage_db)
    try:
        # /v1/usage lookups are recorded too; count model requests only.
        return conn.execute("SELECT COUNT(*) FROM request_usage WHERE inbound_dialect != 'usage'").fetchone()[0]
    finally:
        conn.close()


def test_ops09_restart_and_locked_usage_db_do_not_duplicate_settlement(router, ops_upstream, tmp_path_factory):
    """OPS-09 (partial): restart keeps rows/quota exactly once; a locked usage DB never double-settles."""
    with fast_workdir(tmp_path_factory, "api-compat-ops-restart") as work:
        _ops09_restart_and_lock(router, ops_upstream, work)
        assert_generated_artifacts_redacted({"work": work})


def _ops09_restart_and_lock(router, ops_upstream, work: Path) -> None:
    usage_db = work / "usage.sqlite"
    config = ops_config(work, ops_upstream["url"], usage_db=usage_db, cache=False).replace("{dead_port}", str(_dead_port()))
    handle = spawn_router(router, work, config)
    ids = []
    try:
        for _ in range(2):
            code, headers, raw = call(handle["base"], "/v1/chat/completions", chat_body(), token=QUOTA_CALLER)
            assert code == 200, raw
            ids.append(headers["X-Request-Id"])
        for rid in ids:
            assert len(usage_rows(usage_db, rid)) == 1
        assert caller_usage(handle["base"], QUOTA_CALLER)["day"] == {"requests": 2, "tokens": 10}
    finally:
        stop_router(handle)

    # Restart on the same state and usage DB: nothing is replayed or re-settled.
    handle = spawn_router(router, work, config)
    try:
        assert _count_rows(usage_db) == 2
        assert caller_usage(handle["base"], QUOTA_CALLER)["day"] == {"requests": 2, "tokens": 10}
        code, headers, raw = call(handle["base"], "/v1/chat/completions", chat_body(), token=QUOTA_CALLER)
        assert code == 200, raw
        assert len(usage_rows(usage_db, headers["X-Request-Id"])) == 1
        assert _count_rows(usage_db) == 3
        assert caller_usage(handle["base"], QUOTA_CALLER)["day"] == {"requests": 3, "tokens": 15}

        # Usage DB briefly unavailable for writes (exclusive lock held ~1s): the
        # request is still served, quota settles exactly once, and the usage row
        # is written at most once once the lock clears (never duplicated).
        before = caller_usage(handle["base"], QUOTA_CALLER)["day"]
        lock = sqlite3.connect(usage_db, timeout=0, isolation_level=None, check_same_thread=False)
        lock.execute("BEGIN EXCLUSIVE")

        def release() -> None:
            lock.execute("ROLLBACK")
            lock.close()

        timer = threading.Timer(1.0, release)
        timer.start()
        try:
            code, headers, raw = call(handle["base"], "/v1/chat/completions", chat_body(), token=QUOTA_CALLER, timeout=120)
        finally:
            timer.join()
        assert code == 200, raw
        locked_rid = headers["X-Request-Id"]
        assert len(usage_rows(usage_db, locked_rid, timeout_s=2)) <= 1
        after = caller_usage(handle["base"], QUOTA_CALLER)["day"]
        assert after["tokens"] - before["tokens"] == 5
        assert jsonl_record(work, locked_rid)["usage"]["total_tokens"] == 5
    finally:
        stop_router(handle)
