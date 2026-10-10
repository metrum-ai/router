# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""STREAM P1/P2 limit, terminal, retry-ceiling and concurrency contracts (issue #94).

STREAM-08 (size/line/heartbeat/idle bounds), STREAM-09 (duplicate terminals and
additive fields), STREAM-11 (SDK + router attempt ceiling) and STREAM-12
(concurrent mixed streams and cancel cycles) run against a profile router that
shares one loopback upstream whose per-request behavior is set by each test.
STREAM-10 documents the router's HTTP/1.1-only listener surface. Metrum AI.
"""

from __future__ import annotations

import json
import os
import socket
import sqlite3
import subprocess
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from http.server import ThreadingHTTPServer
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
    request,
    spawn_router,
    stop_router,
)
from harness.oracles import EXPECTED_CHAT_USAGE, json_dumps
from harness.scripted_upstream import FakeUpstream
from harness.sse import sse_events
from harness.stream_faults import (
    first_chat_content_event,
    open_stream,
    read_sse_until,
    safe_close,
)

# Server-wide stream byte ceiling for this profile (server.upstream.max_response_bytes).
MAX_RESPONSE_BYTES = 1 << 20
# Attempt deadline for the idle/heartbeat group; the router has no separate idle timer.
IDLE_ATTEMPT_TIMEOUT_MS = 1500


class _LimitUpstream(FakeUpstream):
    """Isolated upstream whose response is produced by a per-test ``behavior``."""

    calls: list = []
    scenario = None
    chat_include_usage_empty_choices: bool = False
    _attempt_counter: int = 0
    _counter_lock = threading.Lock()
    behavior: Callable[["_LimitUpstream", dict[str, Any]], None] | None = None
    write_failures: list[str] = []

    @classmethod
    def reset_state(cls) -> None:
        super().reset_state()
        cls.behavior = None
        cls.write_failures = []

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads((self.rfile.read(length) if length else b"{}").decode() or "{}")
        cls = self.__class__
        with cls._counter_lock:
            cls.calls.append({"path": self.path, "body": body, "attempt_id": f"attempt-{len(cls.calls) + 1}"})
        behavior = cls.behavior
        if behavior is None:
            self._serve_default(body)
            return
        behavior(self, body)

    # --- raw writers -------------------------------------------------------
    def start_sse(self) -> None:
        # Frame-per-write streams must not stall on Nagle + delayed ACK.
        self.connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()

    def emit(self, data: str | bytes) -> bool:
        """Write and flush; return False (and record) once the router hung up."""
        try:
            self.wfile.write(data.encode() if isinstance(data, str) else data)
            self.wfile.flush()
            return True
        except (BrokenPipeError, ConnectionResetError, OSError) as exc:
            self.__class__.write_failures.append(type(exc).__name__)
            return False


class _BacklogServer(ThreadingHTTPServer):
    """Loopback upstream with a listen backlog sized for the STREAM-12 burst."""

    daemon_threads = True
    request_queue_size = 128


def _chat_chunk(delta: dict[str, Any], *, finish: str | None = None, extra: dict | None = None) -> str:
    chunk: dict[str, Any] = {
        "id": "chatcmpl_limits",
        "object": "chat.completion.chunk",
        "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
    }
    if extra:
        chunk.update(extra)
    return f"data: {json.dumps(chunk, separators=(',', ':'))}\n\n"


def _chat_usage_chunk(usage: dict[str, int] | None = None) -> str:
    usage = usage or EXPECTED_CHAT_USAGE
    return f'data: {{"id":"chatcmpl_limits","object":"chat.completion.chunk","choices":[],"usage":{json_dumps(usage)}}}\n\n'


def _event(name: str, payload: dict[str, Any]) -> str:
    return f"event: {name}\ndata: {json.dumps(payload, separators=(',', ':'))}\n\n"


def _chat_text(raw: bytes) -> str:
    text = ""
    for _, payload in sse_events(raw):
        if not isinstance(payload, dict):
            continue
        for choice in payload.get("choices") or []:
            content = (choice.get("delta") or {}).get("content")
            if isinstance(content, str):
                text += content
    return text


def _post(base: str, path: str, payload: dict, *, timeout: float = 20.0) -> tuple[int, dict, bytes]:
    headers = {"Authorization": f"Bearer {CALLER}", "Content-Type": "application/json"}
    try:
        with urlopen(Request(base + path, data=json.dumps(payload).encode(), headers=headers), timeout=timeout) as resp:
            return resp.status, dict(resp.headers), resp.read()
    except HTTPError as error:
        return error.code, dict(error.headers), error.read()


def _chat(model: str = "chat", content: str = PROMPT_CANARY) -> dict:
    return {
        "model": model,
        "messages": [{"role": "user", "content": content}],
        "stream": True,
        "stream_options": {"include_usage": True},
    }


@pytest.fixture(scope="module")
def limit_upstream_url():
    _LimitUpstream.reset_state()
    upstream = _BacklogServer(("127.0.0.1", 0), _LimitUpstream)
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{upstream.server_port}"
    finally:
        upstream.shutdown()
        thread.join(timeout=5)


def _limits_config(work, upstream_url: str, usage_db=None) -> str:
    usage_block = (
        "  usage_db:\n    enabled: true\n    driver: sqlite\n"
        f'    path: "{usage_db}"\n    migration_policy: auto-safe\n'
        if usage_db is not None
        else "  usage_db: {enabled: false}\n"
    )
    return f'''server:
  listen: "127.0.0.1:{{port}}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {{enabled: false}}
  upstream: {{max_response_bytes: {MAX_RESPONSE_BYTES}}}
{usage_block}  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  chat: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
  chat_backup: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
  responses: {{base_url: "{upstream_url}/v1", dialect: openai-responses}}
  messages: {{base_url: "{upstream_url}/anthropic", dialect: anthropic}}
models:
  chat: {{strategy: static, targets: [{{provider: chat, model: synthetic-chat}}]}}
  chat-idle: {{strategy: static, attempt_timeout_ms: {IDLE_ATTEMPT_TIMEOUT_MS}, targets: [{{provider: chat, model: synthetic-chat}}]}}
  chat-retry:
    strategy: static
    targets:
      - {{provider: chat, model: synthetic-primary}}
      - {{provider: chat_backup, model: synthetic-backup}}
  responses: {{strategy: static, targets: [{{provider: responses, model: synthetic-responses}}]}}
  messages: {{strategy: static, targets: [{{provider: messages, model: synthetic-messages, request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}}}]}}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [chat, chat-idle, chat-retry, responses, messages]
'''


def _limits_router(router, tmp_path_factory, upstream_url: str, *, usage: bool):
    work = tmp_path_factory.mktemp("stream-limits-usage" if usage else "stream-limits")
    usage_db = work / "usage.sqlite" if usage else None
    handle = spawn_router(router, work, _limits_config(work, upstream_url, usage_db))
    handle["usage_db"] = usage_db
    handle["upstream"] = _LimitUpstream
    try:
        yield handle
    finally:
        stop_router(handle)
        assert_generated_artifacts_redacted({"work": work})


@pytest.fixture(scope="module")
def limits_router(router, tmp_path_factory, limit_upstream_url):
    """No usage DB: synchronous SQLite settlement would dominate concurrency timing."""
    yield from _limits_router(router, tmp_path_factory, limit_upstream_url, usage=False)


@pytest.fixture(scope="module")
def usage_limits_router(router, tmp_path_factory, limit_upstream_url):
    yield from _limits_router(router, tmp_path_factory, limit_upstream_url, usage=True)


@pytest.fixture(scope="module", autouse=True)
def race_build(tmp_path_factory):
    """Build a -race router in the background so STREAM-12 can run under the detector.

    Started when the module begins so the build overlaps the other STREAM tests.
    The binary lives in its own directory outside every redaction-scanned work dir.
    """
    out_dir = tmp_path_factory.mktemp("stream-race-build")
    log = (out_dir / "build.log").open("w", encoding="utf-8")
    env = os.environ | {"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "CGO_ENABLED": "1"}
    proc = subprocess.Popen(
        ["go", "build", "-race", "-tags", "dev_no_license", "-o", str(out_dir / "router"), "./cmd/metrum-ai-router"],
        cwd=Path(__file__).parents[3],
        env=env,
        stdout=log,
        stderr=subprocess.STDOUT,
    )
    try:
        yield {"dir": out_dir, "process": proc, "log": out_dir / "build.log"}
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait(timeout=10)
        log.close()


@pytest.fixture(scope="module")
def race_router(race_build, tmp_path_factory, limit_upstream_url):
    rc = race_build["process"].wait(timeout=600)
    assert rc == 0, race_build["log"].read_text()[-4000:]
    work = tmp_path_factory.mktemp("stream-race")
    race_log = work / "race"
    handle = spawn_router(
        {"work": race_build["dir"]},
        work,
        _limits_config(work, limit_upstream_url),
        env={"GORACE": f"halt_on_error=1 log_path={race_log}"},
    )
    handle["upstream"] = _LimitUpstream
    handle["race_log"] = race_log
    try:
        yield handle
    finally:
        stop_router(handle)
        assert_generated_artifacts_redacted({"work": work})


@pytest.fixture(autouse=True)
def _reset_limit_upstream():
    _LimitUpstream.reset_state()
    yield


def _usage_rows(usage_db) -> list[tuple]:
    conn = sqlite3.connect(usage_db)
    try:
        return conn.execute(
            "SELECT input_tokens, output_tokens, total_tokens, stream, status FROM request_usage ORDER BY ts"
        ).fetchall()
    except sqlite3.OperationalError:
        return []
    finally:
        conn.close()


def _wait_usage_count(usage_db, count: int, timeout_s: float = 5.0) -> list[tuple]:
    deadline = time.monotonic() + timeout_s
    rows: list[tuple] = []
    while time.monotonic() < deadline:
        rows = _usage_rows(usage_db)
        if len(rows) >= count:
            return rows
        time.sleep(0.05)
    raise AssertionError(f"expected {count} usage rows, saw {len(rows)}")


# --------------------------------------------------------------------------- STREAM-08


def test_stream_08_size_line_heartbeat_and_idle_bounds(limits_router):
    """STREAM-08: huge line, tiny frames, size limit, heartbeat-only and idle stall stay bounded."""
    base = limits_router["base"]
    Upstream = limits_router["upstream"]

    # --- Many tiny frames: 600 one-character deltas written one frame per flush. ---
    alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
    expected = "".join(alphabet[i % len(alphabet)] for i in range(600))

    def tiny_frames(handler, _body):
        handler.start_sse()
        handler.emit(_chat_chunk({"role": "assistant", "content": ""}))
        for ch in expected:
            handler.emit(_chat_chunk({"content": ch}))
        handler.emit(_chat_chunk({}, finish="stop"))
        handler.emit(_chat_usage_chunk())
        handler.emit("data: [DONE]\n\n")

    Upstream.behavior = tiny_frames
    status, headers, raw = _post(base, "/v1/chat/completions", _chat())
    assert status == 200 and "text/event-stream" in headers["Content-Type"]
    assert _chat_text(raw) == expected
    assert raw.count(b"data: [DONE]") == 1
    assert len(Upstream.calls) == 1

    # --- Huge single data line before any frame: rejected before commit, not replayed. ---
    Upstream.reset_state()
    huge = "x" * (MAX_RESPONSE_BYTES + 4096)

    def huge_first_line(handler, _body):
        handler.start_sse()
        handler.emit(_chat_chunk({"role": "assistant", "content": huge}))
        handler.emit("data: [DONE]\n\n")

    Upstream.behavior = huge_first_line
    status, headers, raw = _post(base, "/v1/chat/completions", _chat())
    assert status == 502, raw[:300]
    error = json.loads(raw)["error"]
    assert error["details"]["error_class"] == "upstream_response_too_large"
    assert huge.encode()[:64] not in raw
    assert len(raw) < 4096
    assert len(Upstream.calls) == 1

    # --- Size limit crossed after commitment: stream stops, no invented terminal. ---
    Upstream.reset_state()

    def huge_after_commit(handler, _body):
        handler.start_sse()
        handler.emit(_chat_chunk({"role": "assistant", "content": "before-limit"}))
        handler.emit(_chat_chunk({"content": huge}))
        handler.emit(_chat_chunk({}, finish="stop"))
        handler.emit("data: [DONE]\n\n")

    Upstream.behavior = huge_after_commit
    status, headers, raw = _post(base, "/v1/chat/completions", _chat())
    assert status == 200 and "text/event-stream" in headers["Content-Type"]
    assert _chat_text(raw) == "before-limit"
    assert len(raw) < MAX_RESPONSE_BYTES
    assert b"[DONE]" not in raw and b'"finish_reason":"stop"' not in raw
    assert len(Upstream.calls) == 1

    # --- Heartbeat-only stream: comments never become content; deadline ends it. ---
    for label, first in (("heartbeat", None), ("idle", "idle-first")):
        Upstream.reset_state()
        hung_up = threading.Event()

        def heartbeat(handler, _body, first=first, hung_up=hung_up):
            handler.start_sse()
            if first is not None:
                handler.emit(_chat_chunk({"role": "assistant", "content": first}))
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                # Idle mode sends nothing further; heartbeat mode sends SSE comments.
                payload = ": keepalive\n\n" if first is None else b""
                if payload and not handler.emit(payload):
                    break
                if not payload:
                    try:
                        handler.connection.settimeout(0.1)
                        if handler.connection.recv(1, socket.MSG_PEEK) == b"":
                            break
                    except (TimeoutError, socket.timeout, BlockingIOError):
                        pass
                    except OSError:
                        break
                time.sleep(0.05)
            hung_up.set()

        Upstream.behavior = heartbeat
        started = time.monotonic()
        status, headers, raw = _post(base, "/v1/chat/completions", _chat(model="chat-idle"), timeout=15)
        elapsed = time.monotonic() - started
        assert status == 200 and "text/event-stream" in headers["Content-Type"], (label, raw[:200])
        assert b"[DONE]" not in raw and b'"finish_reason":"' not in raw, label
        assert _chat_text(raw) == (first or ""), label
        # The attempt deadline bounds the stream; generous slack for CI load.
        assert IDLE_ATTEMPT_TIMEOUT_MS / 1000 * 0.8 <= elapsed < 10, (label, elapsed)
        # Router closed the upstream socket rather than leaving the writer running.
        assert hung_up.wait(timeout=10), f"{label}: upstream writer never observed router hang-up"
        assert len(Upstream.calls) == 1, label


# --------------------------------------------------------------------------- STREAM-09


def test_stream_09_duplicate_terminals_trailing_data_and_additive_fields(usage_limits_router):
    """STREAM-09: duplicate terminal / trailing data never double content or usage."""
    base = usage_limits_router["base"]
    Upstream = usage_limits_router["upstream"]
    usage_db = usage_limits_router["usage_db"]
    rows_before = len(_usage_rows(usage_db))

    # --- Chat: duplicate finish+usage chunk, obfuscation field, trailing data after [DONE]. ---
    def chat_dupes(handler, _body):
        handler.start_sse()
        handler.emit(_chat_chunk({"role": "assistant", "content": "dup-ok"}, extra={"obfuscation": "q1Zr"}))
        handler.emit('event: vendor.additive\ndata: {"nonce":"stream09"}\n\n')
        handler.emit(_chat_chunk({}, finish="stop"))
        handler.emit(_chat_usage_chunk())
        handler.emit(_chat_usage_chunk())  # duplicated terminal usage
        handler.emit("data: [DONE]\n\n")
        handler.emit(_chat_chunk({"content": "AFTER-DONE"}))
        handler.emit("data: [DONE]\n\n")

    Upstream.behavior = chat_dupes
    status, _, raw = _post(base, "/v1/chat/completions", _chat())
    assert status == 200
    assert _chat_text(raw) == "dup-ok"
    assert b"AFTER-DONE" not in raw
    assert raw.count(b"[DONE]") == 1
    # Unknown additive field and event pass through transparently.
    assert b'"obfuscation":"q1Zr"' in raw
    assert b"vendor.additive" in raw

    # --- Responses: completed twice plus trailing delta. ---
    completed = {
        "type": "response.completed",
        "sequence_number": 4,
        "response": {
            "id": "resp_dupe",
            "object": "response",
            "status": "completed",
            "usage": {"input_tokens": 3, "output_tokens": 2, "total_tokens": 5},
        },
    }

    def responses_dupes(handler, _body):
        handler.start_sse()
        handler.emit(_event("response.created", {"type": "response.created", "sequence_number": 1, "response": {"id": "resp_dupe", "object": "response", "status": "in_progress", "output": []}}))
        handler.emit(_event("response.output_text.delta", {"type": "response.output_text.delta", "sequence_number": 2, "item_id": "msg_d", "output_index": 0, "content_index": 0, "delta": "resp-dup-ok", "obfuscation": "Zx"}))
        handler.emit(_event("response.output_text.done", {"type": "response.output_text.done", "sequence_number": 3, "item_id": "msg_d", "output_index": 0, "content_index": 0, "text": "resp-dup-ok"}))
        handler.emit(_event("response.completed", completed))
        handler.emit(_event("response.completed", completed))
        handler.emit(_event("response.output_text.delta", {"type": "response.output_text.delta", "sequence_number": 5, "delta": "AFTER-COMPLETED"}))

    Upstream.behavior = responses_dupes
    status, _, raw = _post(base, "/v1/responses", {"model": "responses", "input": PROMPT_CANARY, "stream": True})
    assert status == 200
    names = [name for name, _ in sse_events(raw)]
    assert names.count("response.completed") == 1
    assert b"AFTER-COMPLETED" not in raw
    assert b'"obfuscation":"Zx"' in raw

    # --- Messages: message_stop twice plus trailing delta. ---
    def messages_dupes(handler, _body):
        handler.start_sse()
        handler.emit(_event("message_start", {"type": "message_start", "message": {"id": "msg_dupe", "type": "message", "role": "assistant", "model": "synthetic-messages", "content": [], "usage": {"input_tokens": 3, "output_tokens": 0}}}))
        handler.emit(_event("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}))
        handler.emit(_event("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "msg-dup-ok"}}))
        handler.emit(_event("content_block_stop", {"type": "content_block_stop", "index": 0}))
        handler.emit(_event("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 2}}))
        handler.emit(_event("message_stop", {"type": "message_stop"}))
        handler.emit(_event("message_stop", {"type": "message_stop"}))
        handler.emit(_event("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "AFTER-STOP"}}))

    Upstream.behavior = messages_dupes
    status, _, raw = _post(
        base,
        "/anthropic/v1/messages",
        {"model": "messages", "max_tokens": 64, "stream": True, "messages": [{"role": "user", "content": PROMPT_CANARY}]},
    )
    assert status == 200
    names = [name for name, _ in sse_events(raw)]
    assert names.count("message_stop") == 1
    assert b"AFTER-STOP" not in raw

    # --- Exactly one settlement per request, with single (not doubled) usage. ---
    rows = _wait_usage_count(usage_db, rows_before + 3)
    new_rows = rows[rows_before:]
    assert len(new_rows) == 3, new_rows
    for input_tokens, output_tokens, total_tokens, stream, row_status in new_rows:
        assert (input_tokens, output_tokens, total_tokens) == (3, 2, 5)
        assert stream in (1, True)
        assert int(row_status) == 200
    assert len(Upstream.calls) == 3


# --------------------------------------------------------------------------- STREAM-10


def test_stream_10_router_listener_is_http11_without_h2c(limits_router):
    """STREAM-10 (partial): router serves SSE over HTTP/1.1 chunked; h2c is not spoken.

    HTTP/2 and reverse-proxy buffering exist only at a deployment ingress, so this
    test pins the router side of that contract and the row stays blocked.
    """
    base = limits_router["base"]
    Upstream = limits_router["upstream"]
    port = limits_router["port"]

    def simple(handler, _body):
        handler.start_sse()
        handler.emit(_chat_chunk({"role": "assistant", "content": "h1-ok"}))
        handler.emit(_chat_chunk({}, finish="stop"))
        handler.emit("data: [DONE]\n\n")

    Upstream.behavior = simple
    body = json.dumps(_chat()).encode()
    with socket.create_connection(("127.0.0.1", port), timeout=5) as sock:
        sock.sendall(
            b"POST /v1/chat/completions HTTP/1.1\r\nHost: 127.0.0.1\r\n"
            + f"Authorization: Bearer {CALLER}\r\nContent-Type: application/json\r\n".encode()
            + f"Content-Length: {len(body)}\r\nConnection: close\r\n\r\n".encode()
            + body
        )
        raw = b""
        while chunk := sock.recv(65536):
            raw += chunk
    head, _, _ = raw.partition(b"\r\n\r\n")
    lines = head.decode("latin-1").split("\r\n")
    assert lines[0].startswith("HTTP/1.1 200")
    hdrs = {k.strip().lower(): v.strip() for k, _, v in (line.partition(":") for line in lines[1:])}
    assert hdrs.get("content-type", "").startswith("text/event-stream")
    assert hdrs.get("transfer-encoding") == "chunked"
    assert "content-length" not in hdrs
    assert hdrs.get("cache-control") == "no-cache"
    assert b"h1-ok" in raw

    # HTTP/2 prior-knowledge (h2c) preface is not upgraded to an HTTP/2 session.
    with socket.create_connection(("127.0.0.1", port), timeout=5) as sock:
        sock.sendall(b"PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
        reply = sock.recv(4096)
    # An h2 server would answer with a binary SETTINGS frame (type 0x04 at byte 3).
    assert not (len(reply) >= 9 and reply[3] == 0x04), reply[:32]
    assert reply == b"" or reply.startswith(b"HTTP/1.1 ")
    assert len(Upstream.calls) == 1


# --------------------------------------------------------------------------- STREAM-11


def test_stream_11_sdk_and_router_retry_ceiling(limits_router):
    """STREAM-11: total upstream attempts == (SDK retries + 1) x eligible targets."""
    from openai import APIStatusError, OpenAI

    base = limits_router["base"]
    Upstream = limits_router["upstream"]

    for upstream_status in (503, 429):

        def always_fail(handler, _body, upstream_status=upstream_status):
            raw = json.dumps({"error": {"message": "synthetic overload", "type": "overloaded"}}).encode()
            handler.send_response(upstream_status)
            handler.send_header("Content-Type", "application/json")
            handler.send_header("Content-Length", str(len(raw)))
            handler.end_headers()
            handler.emit(raw)

        for sdk_retries in (0, 2):
            Upstream.reset_state()
            Upstream.behavior = always_fail
            client = OpenAI(base_url=base + "/v1", api_key=CALLER, max_retries=sdk_retries, timeout=20)
            started = time.monotonic()
            with pytest.raises(APIStatusError) as excinfo:
                stream = client.chat.completions.create(
                    model="chat-retry",
                    messages=[{"role": "user", "content": PROMPT_CANARY}],
                    stream=True,
                )
                for _ in stream:
                    pass
            elapsed = time.monotonic() - started
            client.close()
            assert excinfo.value.status_code >= 400
            # Router: one attempt per eligible target, never a per-target retry loop.
            expected = (sdk_retries + 1) * 2
            assert len(Upstream.calls) == expected, (upstream_status, sdk_retries, len(Upstream.calls))
            paths = [call["body"]["model"] for call in Upstream.calls]
            assert paths == ["synthetic-primary", "synthetic-backup"] * (sdk_retries + 1)
            # Bounded wall time: no hidden retry storm or unbounded Retry-After wait.
            assert elapsed < 30, elapsed

    # Post-commit failure: SDK sees a committed stream, router never re-attempts.
    Upstream.reset_state()

    def commit_then_drop(handler, _body):
        handler.start_sse()
        handler.emit(_chat_chunk({"role": "assistant", "content": "committed"}))

    Upstream.behavior = commit_then_drop
    client = OpenAI(base_url=base + "/v1", api_key=CALLER, max_retries=2, timeout=20)
    text = ""
    finish = None
    stream = client.chat.completions.create(
        model="chat-retry", messages=[{"role": "user", "content": PROMPT_CANARY}], stream=True
    )
    for chunk in stream:
        for choice in chunk.choices:
            text += choice.delta.content or ""
            finish = choice.finish_reason or finish
    client.close()
    assert text == "committed" and finish is None
    assert len(Upstream.calls) == 1


# --------------------------------------------------------------------------- STREAM-12


def _echo_behavior(handler, body):
    """Echo a per-request nonce in the caller dialect, split across several frames."""
    path = handler.path
    if path.endswith("/chat/completions"):
        nonce = body["messages"][-1]["content"]
        handler.start_sse()
        handler.emit(_chat_chunk({"role": "assistant", "content": ""}))
        for part in (nonce[:6], nonce[6:12], nonce[12:]):
            time.sleep(0.005)
            handler.emit(_chat_chunk({"content": part}))
        handler.emit(_chat_chunk({}, finish="stop"))
        handler.emit(_chat_usage_chunk())
        handler.emit("data: [DONE]\n\n")
    elif path.endswith("/responses"):
        nonce = body["input"]
        handler.start_sse()
        handler.emit(_event("response.created", {"type": "response.created", "sequence_number": 1, "response": {"id": "resp_" + nonce, "object": "response", "status": "in_progress", "output": []}}))
        seq = 2
        for part in (nonce[:6], nonce[6:12], nonce[12:]):
            time.sleep(0.005)
            handler.emit(_event("response.output_text.delta", {"type": "response.output_text.delta", "sequence_number": seq, "item_id": "msg_" + nonce, "output_index": 0, "content_index": 0, "delta": part}))
            seq += 1
        handler.emit(_event("response.completed", {"type": "response.completed", "sequence_number": seq, "response": {"id": "resp_" + nonce, "object": "response", "status": "completed", "usage": {"input_tokens": 3, "output_tokens": 2, "total_tokens": 5}}}))
    elif path.endswith("/messages"):
        nonce = body["messages"][-1]["content"]
        handler.start_sse()
        handler.emit(_event("message_start", {"type": "message_start", "message": {"id": "msg_" + nonce, "type": "message", "role": "assistant", "model": "synthetic-messages", "content": [], "usage": {"input_tokens": 3, "output_tokens": 0}}}))
        handler.emit(_event("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}))
        for part in (nonce[:6], nonce[6:12], nonce[12:]):
            time.sleep(0.005)
            handler.emit(_event("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": part}}))
        handler.emit(_event("content_block_stop", {"type": "content_block_stop", "index": 0}))
        handler.emit(_event("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 2}}))
        handler.emit(_event("message_stop", {"type": "message_stop"}))
    else:
        handler.send_error(404)


def _stream_text(dialect: str, raw: bytes) -> str:
    text = ""
    for _, payload in sse_events(raw):
        if not isinstance(payload, dict):
            continue
        if dialect == "chat":
            for choice in payload.get("choices") or []:
                text += (choice.get("delta") or {}).get("content") or ""
        elif dialect == "responses" and payload.get("type") == "response.output_text.delta":
            text += payload.get("delta") or ""
        elif dialect == "messages" and payload.get("type") == "content_block_delta":
            text += (payload.get("delta") or {}).get("text") or ""
    return text


def _run_mixed(base: str, count: int, salt: str) -> None:
    jobs = []
    for i in range(count):
        dialect = ("chat", "responses", "messages")[i % 3]
        nonce = f"n{salt}{i:04d}-{os.urandom(4).hex()}"
        if dialect == "chat":
            jobs.append((dialect, nonce, "/v1/chat/completions", _chat(content=nonce)))
        elif dialect == "responses":
            jobs.append((dialect, nonce, "/v1/responses", {"model": "responses", "input": nonce, "stream": True}))
        else:
            jobs.append(
                (
                    dialect,
                    nonce,
                    "/anthropic/v1/messages",
                    {"model": "messages", "max_tokens": 32, "stream": True, "messages": [{"role": "user", "content": nonce}]},
                )
            )

    def run(job):
        dialect, nonce, path, payload = job
        status, _, raw = _post(base, path, payload)
        return dialect, nonce, status, raw

    with ThreadPoolExecutor(max_workers=16) as pool:
        results = list(pool.map(run, jobs))
    all_nonces = {nonce for _, nonce, _, _ in jobs}
    for dialect, nonce, status, raw in results:
        assert status == 200, (dialect, raw[:200])
        assert _stream_text(dialect, raw) == nonce, (dialect, nonce, raw[:400])
        # No other request's nonce leaked into this stream.
        others = [n for n in all_nonces if n != nonce and n.encode() in raw]
        assert not others, (dialect, nonce, others)


def _fd_count(pid: int) -> int:
    return len(os.listdir(f"/proc/{pid}/fd"))


def _settled_fd_count(pid: int, ceiling: int, timeout_s: float = 10.0) -> int:
    deadline = time.monotonic() + timeout_s
    count = _fd_count(pid)
    while time.monotonic() < deadline and count > ceiling:
        time.sleep(0.1)
        count = _fd_count(pid)
    return count


def test_stream_12_concurrent_mixed_streams_and_cancel_cycles(race_router):
    """STREAM-12: concurrent mixed streams and cancels stay isolated, bounded and race-free.

    Runs against a router built with ``-race`` and ``GORACE=halt_on_error=1``: any
    data race reported during the load makes the process exit and fails the test.
    """
    limits_router = race_router
    base = limits_router["base"]
    Upstream = limits_router["upstream"]
    pid = limits_router["process"].pid

    Upstream.behavior = _echo_behavior
    _run_mixed(base, 30, "warm")
    baseline_fds = _settled_fd_count(pid, ceiling=10**6, timeout_s=0)
    time.sleep(0.5)
    baseline_fds = min(baseline_fds, _fd_count(pid))

    # --- Repeated connect/cancel cycles: upstream sees each hang-up; no fd growth. ---
    cycles = 20
    hangups: list[threading.Event] = []
    first_written: list[threading.Event] = []

    def stall_after_first(handler, _body):
        idx = len(handler.__class__.calls) - 1
        handler.start_sse()
        handler.emit(_chat_chunk({"role": "assistant", "content": f"cancel-{idx}"}))
        first_written[idx].set()
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if not handler.emit(": keepalive\n\n"):
                break
            time.sleep(0.02)
        hangups[idx].set()

    Upstream.reset_state()
    Upstream.behavior = stall_after_first
    for i in range(cycles):
        hangups.append(threading.Event())
        first_written.append(threading.Event())
        resp = open_stream(base, "/v1/chat/completions", _chat(), token=CALLER)
        try:
            partial = read_sse_until(resp, predicate=first_chat_content_event, timeout_s=5)
            assert f"cancel-{i}".encode() in partial
        finally:
            safe_close(resp)
        assert hangups[i].wait(timeout=10), f"cycle {i}: cancel did not reach upstream"
    assert len(Upstream.calls) == cycles

    # --- Mixed concurrent load again after the cancel cycles. ---
    Upstream.reset_state()
    Upstream.behavior = _echo_behavior
    _run_mixed(base, 60, "post")

    # Router process is still healthy and its fd table returned to the warm baseline.
    assert request(base, "/readyz", token=CALLER)[0] == 200
    slack = 8
    settled = _settled_fd_count(pid, ceiling=baseline_fds + slack)
    assert settled <= baseline_fds + slack, (baseline_fds, settled)
    assert limits_router["process"].poll() is None, "router exited (race detector halt?)"
    race_reports = sorted(Path(limits_router["work"]).glob("race*"))
    assert not race_reports, [p.read_text()[:2000] for p in race_reports]
