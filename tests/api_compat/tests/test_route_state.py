# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""ROUTE-06..10: stateful Chat→Responses continuation and bridge encoder fidelity.

Stateful bridge expectations come from the documented contract in
docs-site/docs/reference/api-compatibility.md ("Chat To Responses Bridge"): the
router stores only the upstream Responses ``id`` under a hashed caller, model
group, target, bridge and session scope, injects it as ``previous_response_id``
on the next request in that scope, and retries once without it only after a
compatible stale-state 4xx. Backend misses and errors continue stateless.

Shared-state profiles use a loopback-only RESP2 fake Redis defined here so the
offline suite never needs a Redis binary, Docker, or network. Metrum AI.
"""

from __future__ import annotations

import json
import socketserver
import threading
import time

import pytest

from conftest import (
    PROMPT_CANARY,
    assert_generated_artifacts_redacted,
    request,
    spawn_router,
    stop_router as _stop_router,
)
from harness import CALLER, CALLER_DIGEST, DENIED_CALLER, DENIED_CALLER_DIGEST
from harness.scripted_upstream import ScriptedScenario

SESSION_HEADER = "X-Router-Session"
TTL_SECONDS = 30


class FakeRedisHandler(socketserver.StreamRequestHandler):
    """Minimal RESP2 server: enough of the protocol for go-redis GET/SET/DEL."""

    def handle(self) -> None:
        store = self.server.store
        while True:
            try:
                args = self._read_command()
            except (ConnectionError, OSError, ValueError):
                return
            if args is None:
                return
            if self.server.down.is_set():
                return
            name = args[0].upper()
            self.server.commands.append(name)
            if name == "HELLO":
                # Force go-redis' documented RESP2 fallback.
                self._write(b"-ERR unknown command 'HELLO'\r\n")
            elif name == "PING":
                self._write(b"+PONG\r\n")
            elif name in {"CLIENT", "SELECT", "AUTH"}:
                self._write(b"+OK\r\n")
            elif name == "GET":
                with self.server.lock:
                    entry = store.get(args[1])
                    if entry is not None and entry[1] is not None and entry[1] <= time.monotonic():
                        store.pop(args[1], None)
                        entry = None
                if entry is None:
                    self._write(b"$-1\r\n")
                else:
                    value = entry[0].encode()
                    self._write(b"$%d\r\n%s\r\n" % (len(value), value))
            elif name == "SET":
                expires = None
                ttl_ms = None
                options = [a.upper() for a in args[3:]]
                if "PX" in options:
                    ttl_ms = int(args[3 + options.index("PX") + 1])
                elif "EX" in options:
                    ttl_ms = int(args[3 + options.index("EX") + 1]) * 1000
                if ttl_ms is not None:
                    expires = time.monotonic() + ttl_ms / 1000
                with self.server.lock:
                    store[args[1]] = (args[2], expires)
                    self.server.ttls_ms.append(ttl_ms)
                self._write(b"+OK\r\n")
            elif name == "DEL":
                removed = 0
                with self.server.lock:
                    for key in args[1:]:
                        removed += 1 if store.pop(key, None) is not None else 0
                self._write(b":%d\r\n" % removed)
            else:
                self._write(b"-ERR unknown command\r\n")

    def _read_line(self) -> bytes | None:
        line = self.rfile.readline()
        if not line:
            return None
        return line.rstrip(b"\r\n")

    def _read_command(self) -> list[str] | None:
        header = self._read_line()
        if header is None:
            return None
        if not header.startswith(b"*"):
            raise ValueError("inline commands are not supported")
        args = []
        for _ in range(int(header[1:])):
            size_line = self._read_line()
            if size_line is None or not size_line.startswith(b"$"):
                raise ValueError("bad bulk header")
            size = int(size_line[1:])
            data = self.rfile.read(size + 2)
            args.append(data[:size].decode())
        return args

    def _write(self, raw: bytes) -> None:
        self.wfile.write(raw)
        self.wfile.flush()


class FakeRedis(socketserver.ThreadingTCPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self) -> None:
        super().__init__(("127.0.0.1", 0), FakeRedisHandler)
        self.store: dict[str, tuple[str, float | None]] = {}
        self.lock = threading.Lock()
        self.down = threading.Event()
        self.commands: list[str] = []
        self.ttls_ms: list[int | None] = []
        self.thread = threading.Thread(target=self.serve_forever, daemon=True)
        self.thread.start()

    @property
    def address(self) -> str:
        return f"127.0.0.1:{self.server_address[1]}"

    def expire_all(self) -> None:
        with self.lock:
            self.store.clear()

    def stop(self) -> None:
        self.shutdown()
        self.server_close()


def stop_router(handle):
    _stop_router(handle)
    assert_generated_artifacts_redacted({"work": handle["work"]})


def _config(router, work, *, sessions: str, model: str = "synthetic-bridge-responses") -> str:
    upstream_port = router["server"].server_address[1]
    upstream_url = f"http://127.0.0.1:{upstream_port}"
    return f'''server:
  listen: "127.0.0.1:{{port}}"
  identifiers: {{mode: passthrough}}
  default_model_group: stateful
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  responses: {{base_url: "{upstream_url}/v1", dialect: openai-responses}}
models:
  stateful:
    strategy: static
    targets:
      - provider: responses
        model: {model}
        tool_support: {{openai_responses: [function, tool_choice]}}
        bridges:
          chat_to_responses:
            enabled: true
            text: true
            tools: true
            tool_choice: true
            stateful_sessions:
{sessions}
  other-stateful:
    strategy: static
    targets:
      - provider: responses
        model: {model}
        bridges:
          chat_to_responses:
            enabled: true
            text: true
            stateful_sessions:
{sessions}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [stateful, other-stateful]
  - id: synthetic-second
    token_sha256: {DENIED_CALLER_DIGEST}
    allow: [stateful]
'''


def _memory_sessions() -> str:
    return (
        "              enabled: true\n"
        "              backend: memory\n"
        f"              session_header: {SESSION_HEADER}\n"
        f"              ttl_seconds: {TTL_SECONDS}\n"
    )


def _redis_sessions(redis: FakeRedis) -> str:
    return (
        "              enabled: true\n"
        "              backend: redis\n"
        f"              session_header: {SESSION_HEADER}\n"
        f"              ttl_seconds: {TTL_SECONDS}\n"
        "              redis:\n"
        f'                address: "{redis.address}"\n'
        "                namespace: api-compat-route\n"
        "                connect_timeout_ms: 300\n"
        "                read_timeout_ms: 300\n"
        "                write_timeout_ms: 300\n"
    )


def _chat(base, text, *, session=None, token=CALLER, model="stateful"):
    headers = {SESSION_HEADER: session} if session else {}
    return request_with_headers(
        base,
        "/v1/chat/completions",
        {"model": model, "messages": [{"role": "user", "content": text}]},
        token=token,
        headers=headers,
    )


def request_with_headers(base, path, payload, *, token=CALLER, headers=None):
    from urllib.error import HTTPError
    from urllib.request import Request, urlopen

    all_headers = {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}
    all_headers.update(headers or {})
    req = Request(base + path, data=json.dumps(payload).encode(), headers=all_headers)
    try:
        with urlopen(req, timeout=10) as response:
            return response.status, response.headers, response.read()
    except HTTPError as error:
        return error.code, error.headers, error.read()


def _responses_reply(response_id: str, text: str = "stateful reply") -> dict:
    return {
        "id": response_id,
        "object": "response",
        "model": "synthetic-bridge-responses",
        "status": "completed",
        "output": [
            {
                "type": "message",
                "id": f"msg_{response_id}",
                "role": "assistant",
                "content": [{"type": "output_text", "text": text}],
            }
        ],
        "usage": {"input_tokens": 3, "output_tokens": 2, "total_tokens": 5},
    }


def _expect_turn(scenario, response_id, *, previous=None, status=200, body=None, text=PROMPT_CANARY):
    def validate(call):
        try:
            _validate(call)
        except AssertionError as error:
            # Record so assert_complete fails even when the router masks the
            # dropped upstream connection behind an error status.
            scenario.failures.append(f"{response_id}: {error}")
            raise

    def _validate(call):
        sent = call["body"]
        if previous is None:
            assert "previous_response_id" not in sent, sent.get("previous_response_id")
        else:
            assert sent.get("previous_response_id") == previous
        # The router injects only the stored id; the caller's messages are not merged
        # with any stored history (no doubled or lost turns at the router layer).
        assert sent["input"] == [{"role": "user", "content": [{"type": "input_text", "text": text}]}]

    scenario.expect(
        path="/v1/responses",
        validate=validate,
        response=body if body is not None else _responses_reply(response_id),
        status=status,
    )


@pytest.fixture
def fake_redis():
    server = FakeRedis()
    yield server
    server.stop()


@pytest.fixture
def memory_router(router, tmp_path):
    handle = spawn_router(router, tmp_path / "memory", _config(router, tmp_path / "memory", sessions=_memory_sessions()))
    yield handle
    stop_router(handle)


def _install(router) -> ScriptedScenario:
    scenario = ScriptedScenario()
    router["upstream"].reset_state()
    router["upstream"].scenario = scenario
    return scenario


def test_route_06_redis_continuation_across_two_processes_and_restart(router, tmp_path, fake_redis):
    """ROUTE-06: two routers sharing Redis continue one session; restart keeps it."""
    work_a, work_b = tmp_path / "a", tmp_path / "b"
    a = spawn_router(router, work_a, _config(router, work_a, sessions=_redis_sessions(fake_redis)))
    b = spawn_router(router, work_b, _config(router, work_b, sessions=_redis_sessions(fake_redis)))
    try:
        scenario = _install(router)
        _expect_turn(scenario, "resp_turn_1", previous=None)
        _expect_turn(scenario, "resp_turn_2", previous="resp_turn_1")
        _expect_turn(scenario, "resp_turn_3", previous="resp_turn_2")
        # No header: stateless even though the backend has an entry.
        _expect_turn(scenario, "resp_unscoped", previous=None)

        status, _, raw = _chat(a["base"], PROMPT_CANARY, session="case-route-06")
        assert status == 200, raw
        assert json.loads(raw)["choices"][0]["message"]["content"] == "stateful reply"
        status, _, raw = _chat(b["base"], PROMPT_CANARY, session="case-route-06")
        assert status == 200, raw

        # Restart router A between turns; the next turn must continue from turn 2.
        stop_router(a)
        a = spawn_router(router, tmp_path / "a2", _config(router, tmp_path / "a2", sessions=_redis_sessions(fake_redis)))
        status, _, raw = _chat(a["base"], PROMPT_CANARY, session="case-route-06")
        assert status == 200, raw
        status, _, raw = _chat(b["base"], PROMPT_CANARY)
        assert status == 200, raw
        scenario.assert_complete()

        # Only the latest id is stored, under one hashed key that never contains
        # the raw session header value.
        assert len(fake_redis.store) == 1
        (key, (value, _)), = fake_redis.store.items()
        assert value == "resp_turn_3"
        assert "case-route-06" not in key
        assert key.startswith("metrum-ai-router:bridge-session:api-compat-route:")
    finally:
        stop_router(a)
        stop_router(b)


def test_route_07_stale_continuation_retries_once_stateless(router, memory_router):
    """ROUTE-07: only a designated stale-state 4xx triggers one stateless retry."""
    base = memory_router["base"]
    stale = {"error": {"type": "invalid_request_error", "message": "previous_response_id was not found"}}
    scenario = _install(router)
    _expect_turn(scenario, "resp_a1")
    _expect_turn(scenario, "unused", previous="resp_a1", status=400, body=stale)
    _expect_turn(scenario, "resp_a2", previous=None)
    _expect_turn(scenario, "resp_a3", previous="resp_a2")

    assert _chat(base, PROMPT_CANARY, session="route-07-stale")[0] == 200
    status, _, raw = _chat(base, PROMPT_CANARY, session="route-07-stale")
    assert status == 200, raw
    # Stale mapping was replaced by the retry's new id.
    assert _chat(base, PROMPT_CANARY, session="route-07-stale")[0] == 200
    scenario.assert_complete()


def test_route_07_stale_retry_is_bounded_to_one(router, memory_router):
    """ROUTE-07: a second stale error after the stateless retry is terminal."""
    base = memory_router["base"]
    stale = {"error": {"message": "previous_response_id was not found"}}
    scenario = _install(router)
    _expect_turn(scenario, "resp_b1")
    _expect_turn(scenario, "unused", previous="resp_b1", status=400, body=stale)
    _expect_turn(scenario, "unused", previous=None, status=400, body=stale)

    assert _chat(base, PROMPT_CANARY, session="route-07-bounded")[0] == 200
    status, _, raw = _chat(base, PROMPT_CANARY, session="route-07-bounded")
    assert 400 <= status < 600 and status != 200
    assert "error" in json.loads(raw)
    scenario.assert_complete()
    assert len(router["upstream"].calls) == 3


@pytest.mark.parametrize(
    "status_code,message",
    [
        (400, "invalid tool schema: missing required field"),
        (401, "previous_response_id belongs to another account"),
        (400, "model not found"),
    ],
)
def test_route_07_unrelated_errors_do_not_replay(router, memory_router, status_code, message):
    """ROUTE-07: unrelated 400s and auth errors never replay history statelessly."""
    base = memory_router["base"]
    session = f"route-07-unrelated-{status_code}-{len(message)}"
    scenario = _install(router)
    _expect_turn(scenario, "resp_c1")
    _expect_turn(scenario, "unused", previous="resp_c1", status=status_code, body={"error": {"message": message}})

    assert _chat(base, PROMPT_CANARY, session=session)[0] == 200
    status, _, raw = _chat(base, PROMPT_CANARY, session=session)
    assert status != 200
    assert "error" in json.loads(raw)
    scenario.assert_complete()
    assert len(router["upstream"].calls) == 2


def test_route_07_ttl_expiry_and_redis_outage_continue_stateless(router, tmp_path, fake_redis):
    """ROUTE-07: expired mapping and a Redis outage both continue stateless, never reuse."""
    work = tmp_path / "ttl"
    handle = spawn_router(router, work, _config(router, work, sessions=_redis_sessions(fake_redis)))
    try:
        scenario = _install(router)
        _expect_turn(scenario, "resp_d1")
        _expect_turn(scenario, "resp_d2", previous=None)
        _expect_turn(scenario, "resp_d3", previous=None)
        base = handle["base"]

        assert _chat(base, PROMPT_CANARY, session="route-07-ttl")[0] == 200
        # The router writes the configured TTL with every mapping.
        assert fake_redis.ttls_ms and fake_redis.ttls_ms[-1] == TTL_SECONDS * 1000
        # TTL boundary: the backend drops the entry → next turn is stateless.
        fake_redis.expire_all()
        assert _chat(base, PROMPT_CANARY, session="route-07-ttl")[0] == 200
        # Backend outage mid-session: request still succeeds without a stale id.
        fake_redis.down.set()
        status, _, raw = _chat(base, PROMPT_CANARY, session="route-07-ttl")
        assert status == 200, raw
        scenario.assert_complete()
    finally:
        stop_router(handle)


def test_route_08_session_keys_isolated_by_caller_and_group(router, memory_router):
    """ROUTE-08: identical session labels never cross callers or model groups."""
    base = memory_router["base"]
    scenario = _install(router)
    _expect_turn(scenario, "resp_caller_a1")
    _expect_turn(scenario, "resp_caller_b1", previous=None)
    _expect_turn(scenario, "resp_group_1", previous=None)
    _expect_turn(scenario, "resp_caller_a2", previous="resp_caller_a1")
    _expect_turn(scenario, "resp_caller_b2", previous="resp_caller_b1")

    shared = "shared-session-label"
    assert _chat(base, PROMPT_CANARY, session=shared)[0] == 200
    assert _chat(base, PROMPT_CANARY, session=shared, token=DENIED_CALLER)[0] == 200
    assert _chat(base, PROMPT_CANARY, session=shared, model="other-stateful")[0] == 200
    assert _chat(base, PROMPT_CANARY, session=shared)[0] == 200
    assert _chat(base, PROMPT_CANARY, session=shared, token=DENIED_CALLER)[0] == 200
    scenario.assert_complete()


def test_route_08_simultaneous_turns_are_last_writer_wins(router, memory_router):
    """ROUTE-08: concurrent turns in one session settle on a single stored id."""
    base = memory_router["base"]
    upstream = router["upstream"]
    upstream.reset_state()
    counter = {"n": 0}
    lock = threading.Lock()

    def factory(_body):
        with lock:
            counter["n"] += 1
            return _responses_reply(f"resp_parallel_{counter['n']}")

    scenario = ScriptedScenario()
    for _ in range(4):
        scenario.expect(path="/v1/responses", validate=lambda call: None, response=factory)
    upstream.scenario = scenario
    results = []
    threads = [
        threading.Thread(target=lambda: results.append(_chat(base, PROMPT_CANARY, session="parallel")[0]))
        for _ in range(4)
    ]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join(timeout=15)
    assert results == [200] * 4
    scenario.assert_complete()
    issued = {f"resp_parallel_{i}" for i in range(1, 5)}
    for call in upstream.calls:
        previous = call["body"].get("previous_response_id")
        assert previous is None or previous in issued

    follow = ScriptedScenario()
    seen = {}
    follow.expect(path="/v1/responses", validate=lambda call: seen.update(prev=call["body"].get("previous_response_id")),
                  response=_responses_reply("resp_parallel_next"))
    upstream.reset_state()
    upstream.scenario = follow
    assert _chat(base, PROMPT_CANARY, session="parallel")[0] == 200
    follow.assert_complete()
    assert seen["prev"] in issued


def test_route_09_target_reconfiguration_never_forwards_foreign_ids(router, tmp_path, fake_redis):
    """ROUTE-09: a target swap between turns resets continuation instead of reusing ids."""
    work = tmp_path / "before"
    handle = spawn_router(router, work, _config(router, work, sessions=_redis_sessions(fake_redis)))
    try:
        scenario = _install(router)
        _expect_turn(scenario, "resp_old_target")
        assert _chat(handle["base"], PROMPT_CANARY, session="route-09")[0] == 200
        scenario.assert_complete()
    finally:
        stop_router(handle)
    assert len(fake_redis.store) == 1

    # Same group/session, but the group's target now points at another upstream model.
    work = tmp_path / "after"
    handle = spawn_router(
        router,
        work,
        _config(router, work, sessions=_redis_sessions(fake_redis), model="synthetic-bridge-responses-v2"),
    )
    try:
        scenario = _install(router)
        _expect_turn(scenario, "resp_new_target", previous=None)
        _expect_turn(scenario, "resp_new_target_2", previous="resp_new_target")
        assert _chat(handle["base"], PROMPT_CANARY, session="route-09")[0] == 200
        assert _chat(handle["base"], PROMPT_CANARY, session="route-09")[0] == 200
        scenario.assert_complete()
        assert all(call["body"]["model"] == "synthetic-bridge-responses-v2" for call in router["upstream"].calls)
    finally:
        stop_router(handle)


def _route10_config(router, work) -> str:
    upstream_port = router["server"].server_address[1]
    upstream_url = f"http://127.0.0.1:{upstream_port}"
    return f'''server:
  listen: "127.0.0.1:{{port}}"
  identifiers: {{mode: passthrough}}
  default_model_group: c2r
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  chat: {{base_url: "{upstream_url}/v1", dialect: openai-chat}}
  responses: {{base_url: "{upstream_url}/v1", dialect: openai-responses}}
models:
  c2r:
    strategy: static
    targets:
      - provider: responses
        model: synthetic-bridge-responses
        honors_max_tokens: true
        tool_support: {{openai_responses: [function, tool_choice]}}
        bridges: {{chat_to_responses: {{enabled: true, text: true, tools: true, tool_choice: true, parallel_tool_calls: true}}}}
  r2c:
    strategy: static
    targets:
      - provider: chat
        model: synthetic-bridge-chat
        honors_max_tokens: true
        tool_support: {{openai_chat: [tools, tool_choice]}}
        responses_to_chat: {{enabled: true, text: true, function_tools: true, tool_choice: true, validation_status: passed}}
callers:
  - id: synthetic-allowed
    token_sha256: {CALLER_DIGEST}
    allow: [c2r, r2c]
'''


LOOKUP_CHAT = {"type": "function", "function": {"name": "lookup", "parameters": {"type": "object"}}}
LOOKUP_RESPONSES = {"type": "function", "name": "lookup", "parameters": {"type": "object"}}


def test_route_10_bridge_encoders_preserve_null_false_zero_distinctions(router, tmp_path):
    """ROUTE-10: omitted vs explicit false/zero/null survive both stateless bridges."""
    upstream = router["upstream"]
    handle = spawn_router(router, tmp_path / "route10", _route10_config(router, tmp_path / "route10"))
    base = handle["base"]
    api = lambda path, payload: request(base, path, payload)  # noqa: E731
    try:
        # Chat→Responses: omitted controls stay absent upstream.
        scenario = _install(router)
        scenario.expect(path="/v1/responses")
        status, _, raw = api(
            "/v1/chat/completions",
            {"model": "c2r", "messages": [{"role": "user", "content": PROMPT_CANARY}]},
        )
        assert status == 200, raw
        sent = upstream.calls[-1]["body"]
        for key in ("temperature", "top_p", "max_output_tokens", "parallel_tool_calls", "tool_choice", "tools", "text", "previous_response_id", "store"):
            assert key not in sent, key
        scenario.assert_complete()

        # Chat→Responses: explicit zero/false are forwarded, not dropped as falsy.
        scenario = _install(router)
        scenario.expect(path="/v1/responses")
        status, _, raw = api(
            "/v1/chat/completions",
            {
                "model": "c2r",
                "messages": [{"role": "user", "content": PROMPT_CANARY}],
                "temperature": 0,
                "top_p": 0,
                "parallel_tool_calls": False,
                "tools": [LOOKUP_CHAT],
                "tool_choice": "auto",
                "max_tokens": 7,
            },
        )
        assert status == 200, raw
        sent = upstream.calls[-1]["body"]
        assert sent["temperature"] == 0
        assert sent["top_p"] == 0
        assert sent["parallel_tool_calls"] is False
        assert sent["max_output_tokens"] == 7
        assert sent["tool_choice"] == "auto"
        scenario.assert_complete()

        # Explicit null tool_choice cannot be preserved by either bridge, so it is
        # rejected before upstream (api-compatibility.md "Tool Calls").
        upstream.reset_state()
        status, _, raw = api(
            "/v1/chat/completions",
            {
                "model": "c2r",
                "messages": [{"role": "user", "content": PROMPT_CANARY}],
                "tools": [LOOKUP_CHAT],
                "tool_choice": None,
            },
        )
        assert status == 502, raw
        assert json.loads(raw)["error"]["type"] == "no-eligible-target"
        status, _, raw = api(
            "/v1/responses",
            {"model": "r2c", "input": PROMPT_CANARY, "tools": [LOOKUP_RESPONSES], "tool_choice": None},
        )
        assert status == 502, raw
        assert json.loads(raw)["error"]["type"] == "no-eligible-target"
        assert upstream.calls == []

        # Responses→Chat: omitted stays absent; explicit zero is forwarded.
        scenario = _install(router)
        scenario.expect(path="/v1/chat/completions").expect(path="/v1/chat/completions")
        status, _, raw = api("/v1/responses", {"model": "r2c", "input": PROMPT_CANARY})
        assert status == 200, raw
        sent = upstream.calls[-1]["body"]
        for key in ("temperature", "top_p", "max_tokens", "max_completion_tokens", "tool_choice", "tools", "store"):
            assert key not in sent, key
        status, _, raw = api(
            "/v1/responses",
            {"model": "r2c", "input": PROMPT_CANARY, "temperature": 0, "top_p": 0, "max_output_tokens": 9},
        )
        assert status == 200, raw
        sent = upstream.calls[-1]["body"]
        assert sent["temperature"] == 0
        assert sent["top_p"] == 0
        assert sent.get("max_tokens", sent.get("max_completion_tokens")) == 9
        scenario.assert_complete()
    finally:
        stop_router(handle)
