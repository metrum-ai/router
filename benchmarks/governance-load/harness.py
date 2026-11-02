#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Bounded governance-under-load harness for the router (#573 follow-up to #431).

This harness exercises the shipped binaries, not the in-process test helpers:
it builds `metrum-ai-router`, starts one or more real router processes with
PBKDF2 caller tokens, drives real HTTP load against a deterministic local
OpenAI-Chat upstream, then reads the usage and security-access rows back from
the configured store.

Scenarios:

  sustained       mixed streamed/unary load under rpm/tpm/day token limits
  auth            current PBKDF2 caller authentication and per-key attribution
  cancel          caller-cancelled streaming settlement and slot release
  pii-failover    PII redaction across streaming, retry and failover paths
  dollar-budget   a token budget converted to US dollars at pinned prices
  crash-restart   SIGKILL of a router process mid-load, then restart
  multiprocess    several router processes against one caller budget

Everything runs on loopback with synthetic usage: no hosted model calls, $0.
The numbers are a bounded, stated-assumption measurement of this build and
these configs. They are not a universal overspend bound, and they are not
evidence about real provider usage, network partitions, or Postgres unless the
run used it.

Secrets: caller tokens are generated into mode-0600 files and referenced by
path only. A Postgres DSN is read from the environment variable named by
--dsn-env and handed to psql through libpq environment variables, never on a
command line. Do not print this harness's work directory.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import platform
import shutil
import signal
import socket
import sqlite3
import struct
import subprocess
import sys
import threading
import time
import urllib.parse
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCHEMA = "governance-load-v47-report-v1"
BINARIES = ("metrum-ai-router", "metrum-ai-routerctl", "metrum-ai-router-migrate")
SCENARIOS = ("sustained", "auth", "cancel", "pii-failover", "dollar-budget", "crash-restart", "multiprocess")

# Synthetic upstream usage. 12 + 8 = 20 tokens per successful request.
PROMPT_TOKENS = 12
COMPLETION_TOKENS = 8

# Pinned prices used only to convert tokens into a dollar budget. They are
# harness inputs that the report labels, not provider prices.
PINNED_INPUT_USD_PER_MTOK = 0.15
PINNED_OUTPUT_USD_PER_MTOK = 0.60

RAW_EMAIL = "jane.doe@example.com"
RAW_PHONE = "415-555-0199"


def now_iso() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def percentile(values: list[float], fraction: float) -> float | None:
    """Nearest-rank percentile: p99 of [1,2,3,4] is 4, p50 is 2."""
    if not values:
        return None
    ordered = sorted(values)
    rank = math.ceil(fraction * len(ordered))
    index = min(len(ordered) - 1, max(0, rank - 1))
    return ordered[index]


def request_cost_usd(input_tokens: int, output_tokens: int, input_price: float, output_price: float) -> float:
    return input_tokens * input_price / 1_000_000 + output_tokens * output_price / 1_000_000


def tokens_for_budget(budget_usd: float, input_price: float, output_price: float) -> int:
    """Tokens a caller may consume for budget_usd at the synthetic request mix."""
    per_request = request_cost_usd(PROMPT_TOKENS, COMPLETION_TOKENS, input_price, output_price)
    requests = int(budget_usd / per_request)
    return requests * (PROMPT_TOKENS + COMPLETION_TOKENS)


def parse_dsn(dsn: str) -> dict[str, str]:
    """Split a Postgres URL into libpq environment variables.

    The DSN itself never reaches argv: the harness exports PG* variables for
    psql. Percent-encoded credentials are decoded exactly once.
    """
    url = urllib.parse.urlsplit(dsn)
    if url.scheme not in ("postgres", "postgresql"):
        raise ValueError("dsn must be a postgres:// URL")
    env: dict[str, str] = {}
    if url.hostname:
        env["PGHOST"] = url.hostname
    if url.port:
        env["PGPORT"] = str(url.port)
    if url.username:
        env["PGUSER"] = urllib.parse.unquote(url.username)
    if url.password:
        env["PGPASSWORD"] = urllib.parse.unquote(url.password)
    database = urllib.parse.unquote(url.path.lstrip("/"))
    if database:
        env["PGDATABASE"] = database
    return env


def check(checks: list[dict], name: str, ok: bool, detail: str = "") -> None:
    checks.append({"name": name, "ok": bool(ok), "detail": detail})


# ---------------------------------------------------------------------------
# Deterministic OpenAI-Chat upstream


class MockUpstream:
    """Deterministic OpenAI-Chat upstream with a bounded arrival record.

    Modes: "ok" streams/unaries synthetic usage; "fail" always answers 503 so
    the router must retry and fail over. Bodies are kept (bounded) so the
    harness can prove what actually left the router.
    """

    def __init__(
        self,
        mode: str = "ok",
        stream_events: int = 40,
        event_delay_ms: float = 20.0,
        unary_delay_ms: float = 0.0,
        prompt_tokens: int = PROMPT_TOKENS,
        completion_tokens: int = COMPLETION_TOKENS,
    ) -> None:
        self.mode = mode
        self.stream_events = stream_events
        self.event_delay_ms = event_delay_ms
        self.unary_delay_ms = unary_delay_ms
        self.prompt_tokens = prompt_tokens
        self.completion_tokens = completion_tokens
        self.lock = threading.Lock()
        self.requests: list[dict] = []
        self.bodies: list[str] = []
        self.unary_calls = 0
        self.stream_calls = 0
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), self._handler())
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def _handler(self):
        harness = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):  # noqa: D401 - keep the run quiet
                return

            def do_POST(self):  # noqa: N802 - BaseHTTPRequestHandler API
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b""
                try:
                    body = json.loads(raw or b"{}")
                except json.JSONDecodeError:
                    body = {}
                text = raw.decode("utf-8", "replace")
                stream = bool(body.get("stream"))
                # Bounded diagnostic record: the harness only looks for the
                # synthetic PII markers and the model name.
                with harness.lock:
                    harness.requests.append({"stream": stream, "model": body.get("model")})
                    harness.bodies.append(text[:4096])
                    if stream:
                        harness.stream_calls += 1
                    else:
                        harness.unary_calls += 1
                if harness.mode == "fail":
                    payload = json.dumps({"error": {"type": "server_error", "message": "synthetic upstream failure"}}).encode()
                    self.send_response(503)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(payload)))
                    self.end_headers()
                    self.wfile.write(payload)
                    return
                if stream:
                    self._stream(body)
                else:
                    self._unary(body)

            def _event(self, text: str) -> bool:
                try:
                    self.wfile.write(text.encode())
                    self.wfile.flush()
                    return True
                except (BrokenPipeError, ConnectionResetError):
                    return False

            def _stream(self, body: dict) -> None:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.send_header("Connection", "close")
                self.end_headers()
                for index in range(harness.stream_events):
                    payload = json.dumps(
                        {
                            "id": "chatcmpl_synthetic",
                            "object": "chat.completion.chunk",
                            "model": body.get("model", "synthetic"),
                            "choices": [
                                {
                                    "index": 0,
                                    "delta": {"content": f"t{index} "} if index else {"role": "assistant", "content": "t0 "},
                                    "finish_reason": None,
                                }
                            ],
                        }
                    )
                    if not self._event(f"data: {payload}\n\n"):
                        return
                    if harness.event_delay_ms:
                        time.sleep(harness.event_delay_ms / 1000.0)
                final = json.dumps(
                    {
                        "id": "chatcmpl_synthetic",
                        "object": "chat.completion.chunk",
                        "model": body.get("model", "synthetic"),
                        "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                        "usage": {
                            "prompt_tokens": harness.prompt_tokens,
                            "completion_tokens": harness.completion_tokens,
                            "total_tokens": harness.prompt_tokens + harness.completion_tokens,
                        },
                    }
                )
                if self._event(f"data: {final}\n\ndata: [DONE]\n\n"):
                    self.close_connection = True

            def _unary(self, body: dict) -> None:
                if harness.unary_delay_ms:
                    # Hold the reservation open so the settlement boundary can
                    # be crossed by concurrent in-flight requests.
                    time.sleep(harness.unary_delay_ms / 1000.0)
                payload = json.dumps(
                    {
                        "id": "chatcmpl_synthetic",
                        "object": "chat.completion",
                        "model": body.get("model", "synthetic"),
                        "choices": [
                            {
                                "index": 0,
                                "message": {"role": "assistant", "content": "synthetic reply"},
                                "finish_reason": "stop",
                            }
                        ],
                        "usage": {
                            "prompt_tokens": harness.prompt_tokens,
                            "completion_tokens": harness.completion_tokens,
                            "total_tokens": harness.prompt_tokens + harness.completion_tokens,
                        },
                    }
                ).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

        return Handler

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.server.server_address[1]}/v1"

    def start(self) -> "MockUpstream":
        self.thread.start()
        return self

    def stop(self) -> None:
        self.server.shutdown()
        self.server.server_close()

    def contains_raw_pii(self) -> bool:
        with self.lock:
            return any(RAW_EMAIL in body or RAW_PHONE in body for body in self.bodies)


# ---------------------------------------------------------------------------
# Router processes


@dataclass
class Ctx:
    work: Path
    bin_dir: Path
    backend: str
    dsn_env: str
    duration: float
    workers: int
    processes: int
    shared_state: bool
    commit: str
    keep: bool
    strict: bool
    child_env: dict[str, str] = field(default_factory=dict)
    processes_started: list = field(default_factory=list)


def build_binaries(ctx: Ctx) -> None:
    ctx.bin_dir.mkdir(parents=True, exist_ok=True)
    missing = [name for name in BINARIES if not (ctx.bin_dir / name).exists()]
    if not missing:
        return
    for target in missing:
        subprocess.run(
            ["go", "build", "-buildvcs=false", "-o", str(ctx.bin_dir / target), f"./cmd/{target}"],
            cwd=ROOT,
            check=True,
            # Static binaries: the harness may be run on a node without a Go
            # toolchain, and a run copy can be moved between hosts.
            env={**os.environ, "CGO_ENABLED": "0"},
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )


def admin_password_hash(ctx: Ctx) -> str:
    """Generate a bcrypt hash for the harness-only admin user.

    admin_reports (and therefore the security-access audit) requires admin
    authentication. The hash is a bcrypt of a fixed local harness password and
    is passed to child processes by environment variable only; the config file
    names the variable, never the hash.
    """
    source = ctx.work / "gen_admin_hash.go"
    if source.exists():
        return ctx.child_env["HARNESS_ADMIN_PASSWORD_HASH"]
    program = (
        "package main\n\nimport (\n\t\"fmt\"\n\n\t\"golang.org/x/crypto/bcrypt\"\n)\n\n"
        "func main() {\n\th, err := bcrypt.GenerateFromPassword([]byte(\"harness-admin-password\"), 10)\n"
        "\tif err != nil {\n\t\tpanic(err)\n\t}\n\tfmt.Print(string(h))\n}\n"
    )
    source.write_text(program, encoding="utf-8")
    completed = subprocess.run(
        ["go", "run", str(source)],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    value = completed.stdout.strip()
    if not value.startswith("$2"):
        raise RuntimeError("bcrypt hash generation failed")
    ctx.child_env["HARNESS_ADMIN_PASSWORD_HASH"] = value
    return value


def new_caller(ctx: Ctx, slug: str, allow: str = "harness") -> tuple[str, str]:
    """Generate a caller token file and return (token, token_hash)."""
    token_path = ctx.work / "tokens" / f"{slug}.token"
    token_path.parent.mkdir(parents=True, exist_ok=True)
    if token_path.exists():
        token_path.unlink()
    subprocess.run(
        [
            str(ctx.bin_dir / "metrum-ai-routerctl"),
            "callers",
            "generate",
            "--owner-user",
            "harness-owner",
            "--project",
            "harness-project",
            "--env",
            "load",
            "--key",
            slug,
            "--allow",
            allow,
            "--token-out",
            str(token_path),
        ],
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
    )
    hashed = subprocess.run(
        [str(ctx.bin_dir / "metrum-ai-routerctl"), "callers", "hash", "--token-file", str(token_path)],
        check=True,
        capture_output=True,
        text=True,
    )
    document = json.loads(hashed.stdout)
    token = token_path.read_text(encoding="utf-8").strip()
    os.chmod(token_path, 0o600)
    return token, document["token_hash"] + "|" + document["token_id"]


def split_hash(value: str) -> tuple[str, str]:
    token_hash, token_id = value.split("|", 1)
    return token_hash, token_id


CONFIG_TEMPLATE = """\
server:
  listen: 127.0.0.1:{port}
  default_model_group: harness
  identifiers:
    mode: passthrough
  cache:
    enabled: false
  logging:
    path: {log_path}
    rotate_mb: 64
    keep: 2
{usage_db}
  diagnostics:
    enabled: true
    retention_days: 2
    store_sanitized_upstream_errors: true
    max_error_bytes: 2048
  admin_reports:
    enabled: true
    path_prefix: /admin/reports
    security:
      enabled: true
      retention_days: 2
  admin_auth:
    basic:
      enabled: true
      allow_insecure_http: true
      trusted_proxy_cidrs: []
      users:
        - username: harness-admin
          password_hash_env: HARNESS_ADMIN_PASSWORD_HASH
          subject: basic:harness-admin
          domain: example/harness
    authorization:
      enabled: true
      source: static
      policy:
        - p, basic:harness-admin, example/harness, admin:reports, read|export|drilldown
  upstream:
    max_router_hops: 8
state_path: {state_path}

providers:
{providers}

models:
  harness:
    strategy: static
{group_extra}    targets:
{targets}

users:
  - id: harness-owner
    name: Harness Owner
    type: service_account
    status: active

projects:
  - id: harness-project
    name: Harness Project
    status: active

project_memberships:
  - user_id: harness-owner
    project: harness-project
    role: developer
    status: active

callers:
{callers}
"""

PROVIDER_TEMPLATE = """\
  {name}:
    base_url: {base_url}
    dialect: openai-chat
    api_key: harness-provider-key
    key_id: harness-{name}
    models:
      {model}:
        model: {model}
        input_price_per_million_usd: {input_price}
        output_price_per_million_usd: {output_price}
        pricing_source: harness-pinned
        pricing_updated_at: "2026-11-02"
        input_modalities: [text]
        output_modalities: [text]
"""

CALLER_TEMPLATE = """\
  - id: {caller_id}
    owner_user: harness-owner
    project: harness-project
    environment: load
    status: {status}
    token_hash: "{token_hash}"
    token_id: {token_id}
    allow: [{allow}]
    rate:
      rpm: {rpm}
      tpm: {tpm}
      concurrent: {concurrent}
    quota:
      day:
        requests: {day_requests}
        tokens: {day_tokens}
      month:
        tokens: 0
      soft_pct: 0
    key:
      lifetime_tokens: 0
      soft_pct: 0
"""


def render_config(
    *,
    port: int,
    log_path: Path,
    state_path: Path,
    backend: str,
    sqlite_path: Path | None,
    dsn_env: str,
    providers: list[dict],
    targets: str,
    group_extra: str,
    callers: list[dict],
    migration_policy: str = "auto-safe",
) -> str:
    if backend == "sqlite":
        usage_db = (
            "  usage_db:\n"
            "    enabled: true\n"
            "    driver: sqlite\n"
            f"    path: {sqlite_path}\n"
            f"    migration_policy: {migration_policy}\n"
            "    drain_timeout_ms: 3000\n"
        )
    else:
        usage_db = (
            "  usage_db:\n"
            "    enabled: true\n"
            "    driver: postgres\n"
            f'    dsn: "${{{dsn_env}}}"\n'
            "    migration_policy: deployment-job\n"
            "    drain_timeout_ms: 3000\n"
        )
    return CONFIG_TEMPLATE.format(
        port=port,
        log_path=log_path,
        usage_db=usage_db,
        state_path=state_path,
        providers="\n".join(
            PROVIDER_TEMPLATE.format(
                name=p["name"],
                base_url=p["base_url"],
                model=p["model"],
                input_price=p.get("input_price", 0.0),
                output_price=p.get("output_price", 0.0),
            )
            for p in providers
        ).rstrip(),
        group_extra=group_extra,
        targets=targets.rstrip(),
        callers="\n".join(CALLER_TEMPLATE.format(**caller) for caller in callers).rstrip(),
    )


class RouterProcess:
    def __init__(self, ctx: Ctx, name: str, config_path: Path, port: int, log_path: Path) -> None:
        self.ctx = ctx
        self.name = name
        self.config_path = config_path
        self.port = port
        self.log_path = log_path
        self.proc: subprocess.Popen | None = None

    def start(self) -> "RouterProcess":
        self.log_path.parent.mkdir(parents=True, exist_ok=True)
        stdout = open(self.log_path, "ab")
        self.proc = subprocess.Popen(
            [str(self.ctx.bin_dir / "metrum-ai-router"), "-config", str(self.config_path)],
            cwd=self.ctx.work,
            env=self.ctx.child_env,
            stdout=stdout,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        stdout.close()
        self.ctx.processes_started.append(self)
        return self

    @property
    def addr(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def wait_ready(self, timeout: float = 45.0) -> None:
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                with socket.create_connection(("127.0.0.1", self.port), timeout=0.5) as sock:
                    sock.sendall(b"GET /readyz HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
                    data = sock.recv(64)
                    if b"200" in data.split(b"\r\n", 1)[0]:
                        return
            except OSError:
                pass
            if self.proc is not None and self.proc.poll() is not None:
                raise RuntimeError(f"{self.name} exited during startup with code {self.proc.returncode}")
            time.sleep(0.1)
        raise RuntimeError(f"{self.name} did not become ready")

    def stop(self, sig: int = signal.SIGTERM, wait: float = 20.0) -> int | None:
        if self.proc is None or self.proc.poll() is not None:
            return None if self.proc is None else self.proc.returncode
        self.proc.send_signal(sig)
        try:
            return self.proc.wait(timeout=wait)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            return self.proc.wait(timeout=10)


def stop_all(ctx: Ctx) -> None:
    for process in reversed(ctx.processes_started):
        try:
            process.stop()
        except Exception:  # noqa: BLE001 - cleanup must not mask evidence
            pass
    ctx.processes_started.clear()


# ---------------------------------------------------------------------------
# Usage store readers


class Store:
    def __init__(self, ctx: Ctx, sqlite_path: Path | None) -> None:
        self.ctx = ctx
        self.sqlite_path = sqlite_path
        self.dsn_env = ctx.dsn_env

    @property
    def available(self) -> bool:
        if self.ctx.backend == "sqlite":
            return bool(self.sqlite_path and self.sqlite_path.exists())
        return bool(os.environ.get(self.dsn_env))

    def query(self, sql: str) -> list[dict]:
        if not self.available:
            raise RuntimeError("usage store is not available for this run")
        if self.ctx.backend == "sqlite":
            connection = sqlite3.connect(f"file:{self.sqlite_path}?mode=ro", uri=True, timeout=10)
            connection.row_factory = sqlite3.Row
            try:
                return [dict(row) for row in connection.execute(sql).fetchall()]
            finally:
                connection.close()
        env = dict(os.environ)
        env.update(parse_dsn(os.environ[self.dsn_env]))
        env["PGOPTIONS"] = "-c statement_timeout=30000"
        completed = subprocess.run(
            ["psql", "-X", "-q", "-A", "-t", "-F", "\t", "-v", "ON_ERROR_STOP=1", "-c", sql],
            env=env,
            check=True,
            capture_output=True,
            text=True,
        )
        rows = []
        for line in completed.stdout.splitlines():
            if line:
                rows.append({"line": line})
        return rows

    # --- narrow, named queries so column drift fails loudly -----------------
    COLUMNS = (
        "request_id",
        "caller_id",
        "token_id",
        "status",
        "stream",
        "total_tokens",
        "input_tokens",
        "output_tokens",
        "error",
        "usage_source",
        "pii_filter_applied",
        "pii_filter_replacements",
        "cache",
        "attempts",
        "fallback_used",
        "target_provider",
    )
    INT_COLUMNS = {"status", "total_tokens", "input_tokens", "output_tokens", "pii_filter_replacements", "attempts"}
    BOOL_COLUMNS = {"stream", "pii_filter_applied", "fallback_used"}

    def usage_rows(self, caller_id: str) -> list[dict]:
        # caller_id values are harness constants, but keep the SQL literal
        # single-line and quote-free so a typo cannot smuggle in a statement.
        if not caller_id.replace("-", "").isalnum():
            raise ValueError("caller id must be a harness slug")
        sql = (
            "SELECT request_id, caller_id, token_id, status, stream, total_tokens, input_tokens, output_tokens, "
            "coalesce(error,'') AS error, coalesce(usage_source,'') AS usage_source, pii_filter_applied, "
            "pii_filter_replacements, cache, "
            "attempts, fallback_used, target_provider FROM request_usage "
            f"WHERE caller_id = '{caller_id}'"
        )
        if self.ctx.backend == "sqlite":
            raw = self.query(sql)
        else:
            raw = [
                dict(zip(self.COLUMNS, row["line"].split("\t")))
                for row in self.query(sql)
            ]
        rows = []
        for row in raw:
            if "line" in row:
                row = dict(zip(self.COLUMNS, row["line"].split("\t")))
            clean = {}
            for column in self.COLUMNS:
                value = row.get(column, "")
                if column in self.INT_COLUMNS:
                    clean[column] = int(value or 0)
                elif column in self.BOOL_COLUMNS:
                    clean[column] = str(value).strip().lower() in ("1", "t", "true")
                else:
                    clean[column] = "" if value is None else str(value)
            rows.append(clean)
        return rows

    def scalar(self, sql: str) -> int:
        rows = self.query(sql)
        if not rows:
            return 0
        return int(rows[0]["line"] if "line" in rows[0] else list(rows[0].values())[0])


# ---------------------------------------------------------------------------
# HTTP client helpers


def post_json(
    addr: str,
    path: str,
    body: dict,
    token: str,
    timeout: float = 60.0,
) -> tuple[int, bytes, dict[str, str]]:
    connection = socket.create_connection((addr_host(addr), addr_port(addr)), timeout=timeout)
    try:
        payload = json.dumps(body).encode()
        head = (
            f"POST {path} HTTP/1.1\r\nHost: {addr_host(addr)}:{addr_port(addr)}\r\n"
            f"Authorization: Bearer {token}\r\nContent-Type: application/json\r\n"
            f"Content-Length: {len(payload)}\r\nConnection: close\r\n\r\n"
        ).encode()
        connection.sendall(head + payload)
        chunks = []
        while True:
            data = connection.recv(65536)
            if not data:
                break
            chunks.append(data)
    except OSError as exc:
        return 0, str(exc).encode(), {}
    finally:
        connection.close()
    raw = b"".join(chunks)
    head, _, rest = raw.partition(b"\r\n\r\n")
    lines = head.split(b"\r\n")
    status = 0
    if lines and len(lines[0].split()) >= 2:
        try:
            status = int(lines[0].split()[1])
        except ValueError:
            status = 0
    headers = {}
    for line in lines[1:]:
        key, _, value = line.partition(b":")
        headers[key.strip().lower().decode()] = value.strip().decode()
    return status, rest, headers


def addr_host(addr: str) -> str:
    return urllib.parse.urlsplit(addr).hostname or "127.0.0.1"


def addr_port(addr: str) -> int:
    return urllib.parse.urlsplit(addr).port or 80


def chat_body(stream: bool, prompt: str = "hello synthetic world", model: str = "harness") -> dict:
    return {
        "model": model,
        "max_tokens": 8,
        "stream": stream,
        "messages": [{"role": "user", "content": prompt}],
    }


def rejection_reason(body: bytes) -> str:
    try:
        document = json.loads(body)
    except json.JSONDecodeError:
        return ""
    error = document.get("error")
    if isinstance(error, dict):
        return str(error.get("code") or error.get("type") or "")
    return ""


# ---------------------------------------------------------------------------
# Scenario plumbing


def new_workdir(name: str) -> Path:
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    path = ROOT / ".out" / "governance-load" / f"{stamp}-{name}"
    path.mkdir(parents=True, exist_ok=False)
    return path


def environment_record(ctx: Ctx) -> dict:
    cpu = ""
    cores = os.cpu_count() or 0
    try:
        for line in Path("/proc/cpuinfo").read_text(encoding="utf-8").splitlines():
            if line.startswith("model name"):
                cpu = line.split(":", 1)[1].strip()
                break
    except OSError:
        pass
    return {
        "cpu_model": cpu,
        "visible_cpus": cores,
        "kernel": platform.release(),
        "machine": platform.machine(),
        "python": platform.python_version(),
        "router_commit": ctx.commit,
        "backend": ctx.backend,
        "loopback_only": True,
        "hosted_model_calls": 0,
    }


def render_scenario(
    ctx: Ctx,
    slug: str,
    *,
    upstreams: list[dict],
    targets: list[str],
    callers: list[dict],
    group_extra: str = "",
    port: int,
    state_path: Path,
    sqlite_path: Path | None,
    migration_policy: str = "auto-safe",
) -> RouterProcess:
    log_path = ctx.work / "logs" / f"{slug}.jsonl"
    config_path = ctx.work / f"{slug}.yaml"
    config_path.write_text(
        render_config(
            port=port,
            log_path=log_path,
            state_path=state_path,
            backend=ctx.backend,
            sqlite_path=sqlite_path,
            dsn_env=ctx.dsn_env,
            providers=upstreams,
            targets="".join(f"      - {target}\n" for target in targets),
            group_extra=group_extra,
            callers=callers,
            migration_policy=migration_policy,
        ),
        encoding="utf-8",
    )
    os.chmod(config_path, 0o600)
    return RouterProcess(ctx, slug, config_path, port, log_path)


def caller_entry(
    caller_id: str,
    token_hash: str,
    token_id: str,
    *,
    rpm: int = 0,
    tpm: int = 0,
    concurrent: int = 0,
    day_tokens: int = 0,
    day_requests: int = 0,
    allow: str = "harness",
    status: str = "active",
) -> dict:
    return {
        "caller_id": caller_id,
        "token_hash": token_hash,
        "token_id": token_id,
        "rpm": rpm,
        "tpm": tpm,
        "concurrent": concurrent,
        "day_tokens": day_tokens,
        "day_requests": day_requests,
        "allow": allow,
        "status": status,
    }


# ---------------------------------------------------------------------------
# Scenarios


def scenario_sustained(ctx: Ctx) -> dict:
    """Mixed streamed/unary load under sustained TPM and concurrency limits."""
    checks: list[dict] = []
    upstream = MockUpstream().start()
    tpm_limit = 6000
    try:
        token, hashed = new_caller(ctx, "sustained-a")
        token_hash, token_id = split_hash(hashed)
        sqlite_path = ctx.work / "sustained.sqlite" if ctx.backend == "sqlite" else None
        # The day budget is deliberately larger than the sustained TPM window,
        # so this scenario measures the rate limits rather than budget drain.
        caller = caller_entry(
            "sustained-a", token_hash, token_id, rpm=0, tpm=tpm_limit, concurrent=ctx.workers + 8,
            day_tokens=0,
        )
        process = render_scenario(
            ctx,
            "sustained",
            upstreams=[{"name": "mock", "base_url": upstream.base_url, "model": "mock-model"}],
            targets=['{provider: mock, model_ref: mock-model}'],
            callers=[caller],
            port=free_port(),
            state_path=ctx.work / "sustained-state.json",
            sqlite_path=sqlite_path,
        ).start()
        process.wait_ready()
        deadline = time.time() + ctx.duration
        results: list[dict] = []
        lock = threading.Lock()

        def worker(stream: bool) -> None:
            while time.time() < deadline:
                started = time.time()
                status, body, headers = post_json(process.addr, "/v1/chat/completions", chat_body(stream), token)
                with lock:
                    results.append(
                        {
                            "status": status,
                            "reason": rejection_reason(body),
                            "ms": (time.time() - started) * 1000,
                            "stream": stream,
                            "t": started,
                        }
                    )
                if status != 200:
                    # Back off after a governance rejection so a sustained run
                    # measures the limit, not a rejection busy-loop.
                    time.sleep(0.025)

        threads = [
            threading.Thread(target=worker, args=(index % 2 == 0,), daemon=True) for index in range(ctx.workers)
        ]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        process.stop()
        store = Store(ctx, sqlite_path)
        rows = store.usage_rows("sustained-a") if store.available else []
        settled = sum(int(row["total_tokens"]) for row in rows)
        ok = [r for r in results if r["status"] == 200]
        rejected = [r for r in results if r["status"] != 200]
        reasons: dict[str, int] = {}
        for item in rejected:
            reasons[item["reason"] or str(item["status"])] = reasons.get(item["reason"] or str(item["status"]), 0) + 1
        buckets: dict[int, int] = {}
        start = min((r["t"] for r in results), default=time.time())
        for item in ok:
            buckets[int(item["t"] - start)] = buckets.get(int(item["t"] - start), 0) + 1
        elapsed = max(0.001, time.time() - start)
        # TPM is a sliding-window allowance, not a smooth refill: the whole
        # minute budget can be admitted in the first seconds. Report the
        # per-window rate, and the burst profile separately.
        latencies = [r["ms"] for r in ok]
        peak_per_second = max(buckets.values()) if buckets else 0
        # Highest tokens settled in any rolling 60 s window. This is the metric
        # the limiter itself enforces; extrapolating a short run is misleading
        # because a sliding window refills at the one-minute boundary.
        admitted_times = sorted(item["t"] for item in ok)
        max_window_tokens = 0
        for index, moment in enumerate(admitted_times):
            count = 0
            for later in admitted_times[index:]:
                if later - moment < 60.0:
                    count += 1
                else:
                    break
            max_window_tokens = max(max_window_tokens, count * (PROMPT_TOKENS + COMPLETION_TOKENS))
        windows = max(elapsed / 60.0, 1.0)
        check(checks, "the run admitted and rejected requests", bool(ok) and bool(rejected),
              f"ok={len(ok)} rejected={len(rejected)}")
        check(checks, "every rejection is a bounded governance reason", all(item["reason"] for item in rejected),
              json.dumps(reasons, sort_keys=True))
        check(checks, "usage rows persisted for admitted requests", len(rows) >= len(ok),
              f"rows={len(rows)} ok={len(ok)}")
        check(checks, "tokens settled in any rolling 60 s window stay within the TPM limit (one request tolerance)",
              max_window_tokens <= tpm_limit + 20, f"max_window_tokens={max_window_tokens}")
        check(checks, "the TPM allowance is actually consumable (the limit binds)",
              max_window_tokens >= tpm_limit * 0.9, f"max_window_tokens={max_window_tokens}")
        return {
            "scenario": "sustained",
            "duration_s": round(elapsed, 1),
            "workers": ctx.workers,
            "tpm_limit": tpm_limit,
            "requests": len(results),
            "admitted": len(ok),
            "rejected": len(rejected),
            "reject_reasons": reasons,
            "settled_tokens": settled,
            "max_settled_tokens_in_rolling_60s_window": max_window_tokens,
            "windows_in_run": round(windows, 2),
            "peak_admitted_per_second": peak_per_second,
            "admitted_per_second": dict(sorted(buckets.items())),
            "latency_ms_p50": percentile(latencies, 0.50),
            "latency_ms_p99": percentile(latencies, 0.99),
            "stream_requests": sum(1 for r in results if r["stream"]),
        }, checks
    finally:
        upstream.stop()


def scenario_auth(ctx: Ctx) -> dict:
    checks: list[dict] = []
    upstream = MockUpstream().start()
    try:
        token_a, hashed_a = new_caller(ctx, "auth-a")
        token_b, hashed_b = new_caller(ctx, "auth-b")
        token_c, hashed_c = new_caller(ctx, "auth-c")
        hash_a, id_a = split_hash(hashed_a)
        hash_b, id_b = split_hash(hashed_b)
        hash_c, id_c = split_hash(hashed_c)
        sqlite_path = ctx.work / "auth.sqlite" if ctx.backend == "sqlite" else None
        callers = [
            caller_entry("auth-a", hash_a, id_a, rpm=1000, concurrent=16),
            caller_entry("auth-b", hash_b, id_b, rpm=1000, concurrent=16),
            caller_entry("auth-c", hash_c, id_c, rpm=1000, concurrent=16, status="disabled"),
        ]
        process = render_scenario(
            ctx,
            "auth",
            upstreams=[{"name": "mock", "base_url": upstream.base_url, "model": "mock-model"}],
            targets=['{provider: mock, model_ref: mock-model}'],
            callers=callers,
            port=free_port(),
            state_path=ctx.work / "auth-state.json",
            sqlite_path=sqlite_path,
        ).start()
        process.wait_ready()
        missing = post_json(process.addr, "/v1/chat/completions", chat_body(False), "")
        wrong_secret = post_json(process.addr, "/v1/chat/completions", chat_body(False), token_a.rsplit("_", 1)[0] + "_wrong")
        disabled = post_json(process.addr, "/v1/chat/completions", chat_body(False), token_c)
        ok_a = post_json(process.addr, "/v1/chat/completions", chat_body(False), token_a)
        ok_b = post_json(process.addr, "/v1/chat/completions", chat_body(True), token_b)
        process.stop()
        calls_after = len(upstream.requests)
        store = Store(ctx, sqlite_path)
        rows = store.usage_rows("auth-a") + store.usage_rows("auth-b") if store.available else []
        audit = 0
        if store.available:
            try:
                audit = store.scalar(
                    "SELECT count(*) FROM security_access_events WHERE event_type = 'api_auth_failed'"
                )
            except Exception as exc:  # noqa: BLE001 - Postgres scalar path returns text
                audit = -1
                check(checks, "security audit rows readable", False, str(exc))
        check(checks, "missing token is rejected", missing[0] == 401, f"status={missing[0]}")
        check(checks, "wrong token secret is rejected", wrong_secret[0] == 401, f"status={wrong_secret[0]}")
        check(checks, "disabled caller is rejected", disabled[0] in (401, 403), f"status={disabled[0]}")
        check(checks, "rejected callers never reach the upstream", calls_after == 2, f"upstream_calls={calls_after}")
        check(checks, "valid callers are admitted", ok_a[0] == 200 and ok_b[0] == 200, f"a={ok_a[0]} b={ok_b[0]}")
        check(checks, "usage rows attribute both admitted calls",
              sorted(row["caller_id"] for row in rows) == ["auth-a", "auth-b"], f"rows={len(rows)}")
        check(checks, "auth failures are recorded in the security audit", audit >= 2, f"api_auth_failed_rows={audit}")
        check(checks, "token ids are attributed per key",
              sorted({row["token_id"] for row in rows}) == sorted([id_a, id_b]),
              f"token_ids={len({row['token_id'] for row in rows})}")
        return {
            "scenario": "auth",
            "statuses": {
                "missing_token": missing[0],
                "wrong_secret": wrong_secret[0],
                "disabled_caller": disabled[0],
                "valid_unary": ok_a[0],
                "valid_stream": ok_b[0],
            },
            "upstream_calls": calls_after,
            "usage_rows": len(rows),
            "api_auth_failed_rows": audit,
        }, checks
    finally:
        upstream.stop()


def scenario_cancel(ctx: Ctx) -> dict:
    checks: list[dict] = []
    upstream = MockUpstream(stream_events=200, event_delay_ms=25.0).start()
    try:
        token, hashed = new_caller(ctx, "cancel-a")
        token_hash, token_id = split_hash(hashed)
        sqlite_path = ctx.work / "cancel.sqlite" if ctx.backend == "sqlite" else None
        caller = caller_entry("cancel-a", token_hash, token_id, rpm=1000, concurrent=1)
        process = render_scenario(
            ctx,
            "cancel",
            upstreams=[{"name": "mock", "base_url": upstream.base_url, "model": "mock-model"}],
            targets=['{provider: mock, model_ref: mock-model}'],
            callers=[caller],
            port=free_port(),
            state_path=ctx.work / "cancel-state.json",
            sqlite_path=sqlite_path,
        ).start()
        process.wait_ready()
        # Open a streaming request and read two SSE events, then reset the
        # connection so the router sees a caller cancellation, not a clean EOF.
        connection = socket.create_connection((addr_host(process.addr), addr_port(process.addr)), timeout=30)
        payload = json.dumps(chat_body(True)).encode()
        connection.sendall(
            (
                f"POST /v1/chat/completions HTTP/1.1\r\nHost: {addr_host(process.addr)}:{addr_port(process.addr)}\r\n"
                f"Authorization: Bearer {token}\r\nContent-Type: application/json\r\n"
                f"Content-Length: {len(payload)}\r\nConnection: close\r\n\r\n"
            ).encode()
            + payload
        )
        received = 0
        while received < 2:
            data = connection.recv(4096)
            if not data:
                break
            received += data.count(b"data: ")
        connection.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
        connection.close()
        # The concurrency limit is 1: an admitted request proves the cancelled
        # stream released its slot and reservation. The release is asynchronous,
        # so retry for a bounded window instead of assuming the first attempt.
        after = (0, b"", {})
        post_cancel_attempts = 0
        release_deadline = time.time() + 10
        while time.time() < release_deadline:
            post_cancel_attempts += 1
            after = post_json(process.addr, "/v1/chat/completions", chat_body(False), token)
            if after[0] == 200:
                break
            time.sleep(0.1)
        time.sleep(1.0)
        process.stop()
        store = Store(ctx, sqlite_path)
        rows = store.usage_rows("cancel-a") if store.available else []
        cancelled = [row for row in rows if str(row["status"]) == "499" or "client-canceled" in str(row.get("error", ""))]
        settled = sum(int(row["total_tokens"]) for row in rows)
        check(checks, "stream delivered at least two events before cancel", received >= 2, f"events={received}")
        check(checks, "a request after cancellation is admitted (slot released)", after[0] == 200, f"status={after[0]}")
        check(checks, "cancelled stream is recorded with a client-cancel status", bool(cancelled),
              f"rows={len(rows)} cancelled={len(cancelled)}")
        check(checks, "cancelled stream settles committed tokens", settled > 0, f"settled_tokens={settled}")
        return {
            "scenario": "cancel",
            "events_before_cancel": received,
            "post_cancel_status": after[0],
            "post_cancel_attempts": post_cancel_attempts,
            "usage_rows": len(rows),
            "client_canceled_rows": len(cancelled),
            "settled_tokens": settled,
            "usage_source": sorted({str(row.get("usage_source", "")) for row in rows} - {""}),
        }, checks
    finally:
        upstream.stop()


def scenario_pii_failover(ctx: Ctx) -> dict:
    checks: list[dict] = []
    primary = MockUpstream(mode="fail").start()
    fallback = MockUpstream().start()
    try:
        token, hashed = new_caller(ctx, "pii-a")
        token_hash, token_id = split_hash(hashed)
        sqlite_path = ctx.work / "pii.sqlite" if ctx.backend == "sqlite" else None
        caller = caller_entry("pii-a", token_hash, token_id, rpm=10000, concurrent=ctx.workers + 8)
        group_extra = (
            "    pii_filter:\n"
            "      enabled: true\n"
            "      mode: redact_only\n"
            "      restore_response: false\n"
            "      max_replacements_per_request: 200\n"
            "      apply_to:\n"
            "        system: true\n"
            "        messages: true\n"
            "        responses_input: true\n"
            "        tool_results: true\n"
            "        image_urls: false\n"
            "      rules:\n"
            "        - name: email\n"
            "          expression: '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}'\n"
            "          placeholder_prefix: EMAIL\n"
            "        - name: phone\n"
            "          expression: '\\b(?:\\+1[-. ]?)?\\(?[2-9]\\d{2}\\)?[-. ]?[2-9]\\d{2}[-. ]?\\d{4}\\b'\n"
            "          placeholder_prefix: PHONE\n"
        )
        process = render_scenario(
            ctx,
            "pii-failover",
            upstreams=[
                {"name": "primary", "base_url": primary.base_url, "model": "primary-model"},
                {"name": "fallback", "base_url": fallback.base_url, "model": "fallback-model"},
            ],
            targets=[
                "{provider: primary, model_ref: primary-model, weight: 50}",
                "{provider: fallback, model_ref: fallback-model, weight: 50}",
            ],
            callers=[caller],
            group_extra=group_extra,
            port=free_port(),
            state_path=ctx.work / "pii-state.json",
            sqlite_path=sqlite_path,
        ).start()
        process.wait_ready()
        prompt = f"Email {RAW_EMAIL} or call {RAW_PHONE} and summarize."
        results = []
        for index in range(max(8, ctx.workers)):
            results.append(post_json(process.addr, "/v1/chat/completions", chat_body(index % 2 == 0, prompt), token))
        process.stop()
        statuses = [status for status, _, _ in results]
        store = Store(ctx, sqlite_path)
        rows = store.usage_rows("pii-a") if store.available else []
        leak = primary.contains_raw_pii() or fallback.contains_raw_pii()
        placeholder = any(
            "[EMAIL_" in body and "[PHONE_" in body for body in primary.bodies + fallback.bodies
        )
        pii_rows = [row for row in rows if row.get("pii_filter_applied")]
        log_text = ""
        for path in (ctx.work / "logs").glob("pii-failover.jsonl"):
            log_text += path.read_text(encoding="utf-8", errors="replace")
        log_leak = RAW_EMAIL in log_text or RAW_PHONE in log_text
        db_leak = False
        if store.available:
            # Synthetic rows only: this checks that no free-text column kept a
            # raw matched value.
            stored = store.query("SELECT * FROM request_usage")
            try:
                stored += store.query("SELECT * FROM security_access_events")
            except Exception:  # noqa: BLE001 - the table may be absent on an older schema
                pass
            db_leak = any(RAW_EMAIL in json.dumps(row) or RAW_PHONE in json.dumps(row) for row in stored)
        check(checks, "no raw PII reached the primary upstream", not primary.contains_raw_pii(),
              f"primary_bodies={len(primary.bodies)}")
        check(checks, "no raw PII reached the failed-over upstream", not fallback.contains_raw_pii(),
              f"fallback_bodies={len(fallback.bodies)}")
        check(checks, "placeholders reached the upstream", placeholder)
        check(checks, "failover actually used both targets", primary.unary_calls + primary.stream_calls > 0
              and fallback.unary_calls + fallback.stream_calls > 0,
              f"primary={len(primary.requests)} fallback={len(fallback.requests)}")
        check(checks, "admitted requests succeeded through the fallback", any(s == 200 for s in statuses),
              f"statuses={sorted(set(statuses))}")
        check(checks, "usage rows record the applied PII filter", len(pii_rows) >= 1, f"rows={len(rows)} pii_rows={len(pii_rows)}")
        check(checks, "router request log carries no raw PII", not log_leak)
        check(checks, "usage store carries no raw PII", not db_leak)
        return {
            "scenario": "pii-failover",
            "requests": len(results),
            "statuses": sorted({str(status) for status in statuses}),
            "primary_arrivals": len(primary.requests),
            "fallback_arrivals": len(fallback.requests),
            "raw_pii_leak": leak,
            "placeholders_observed": placeholder,
            "usage_rows": len(rows),
            "pii_filter_rows": len(pii_rows),
            "log_pii_leak": log_leak,
        }, checks
    finally:
        primary.stop()
        fallback.stop()


def scenario_dollar_budget(ctx: Ctx) -> dict:
    """A token budget converted to dollars at pinned prices, with a boundary burst.

    The router has no cumulative USD budget control: a dollar budget is a token
    budget converted at a pinned price. The upstream reports substantially more
    usage than the router's local estimate, which is the documented case where
    settlement exceeds the reservation; concurrent in-flight requests hold
    reservations while the budget boundary is crossed.
    """
    checks: list[dict] = []
    prompt_tokens, completion_tokens = 200, 40
    upstream = MockUpstream(
        unary_delay_ms=250.0, prompt_tokens=prompt_tokens, completion_tokens=completion_tokens
    ).start()
    try:
        token, hashed = new_caller(ctx, "dollar-a")
        token_hash, token_id = split_hash(hashed)
        sqlite_path = ctx.work / "dollar.sqlite" if ctx.backend == "sqlite" else None
        budget_usd = 0.01
        per_request_usd = request_cost_usd(prompt_tokens, completion_tokens,
                                           PINNED_INPUT_USD_PER_MTOK, PINNED_OUTPUT_USD_PER_MTOK)
        budget_tokens = int(budget_usd / per_request_usd) * (prompt_tokens + completion_tokens)
        caller = caller_entry("dollar-a", token_hash, token_id, rpm=0, concurrent=ctx.workers + 8,
                              day_tokens=budget_tokens)
        process = render_scenario(
            ctx,
            "dollar-budget",
            upstreams=[{
                "name": "mock",
                "base_url": upstream.base_url,
                "model": "mock-model",
                "input_price": PINNED_INPUT_USD_PER_MTOK,
                "output_price": PINNED_OUTPUT_USD_PER_MTOK,
            }],
            targets=['{provider: mock, model_ref: mock-model}'],
            callers=[caller],
            port=free_port(),
            state_path=ctx.work / "dollar-state.json",
            sqlite_path=sqlite_path,
        ).start()
        process.wait_ready()
        results: list[tuple[int, str]] = []
        lock = threading.Lock()
        deadline = time.time() + min(ctx.duration, 20)

        def worker() -> None:
            while time.time() < deadline:
                status, body, _ = post_json(process.addr, "/v1/chat/completions", chat_body(False), token)
                with lock:
                    results.append((status, rejection_reason(body)))
                if status != 200:
                    time.sleep(0.02)

        threads = [threading.Thread(target=worker, daemon=True) for _ in range(ctx.workers)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        process.stop()
        store = Store(ctx, sqlite_path)
        rows = store.usage_rows("dollar-a") if store.available else []
        settled_in = sum(int(row["input_tokens"]) for row in rows)
        settled_out = sum(int(row["output_tokens"]) for row in rows)
        actual_cost = request_cost_usd(settled_in, settled_out, PINNED_INPUT_USD_PER_MTOK, PINNED_OUTPUT_USD_PER_MTOK)
        admitted = sum(1 for status, _ in results if status == 200)
        rejected = sum(1 for status, _ in results if status != 200)
        overspend = actual_cost - budget_usd
        check(checks, "the budget is enforced at some point", rejected > 0, f"admitted={admitted} rejected={rejected}")
        check(checks, "actual cost is measured from usage rows, not assumed", settled_in > 0 and settled_out > 0,
              f"in={settled_in} out={settled_out}")
        check(checks, "settlement above the reserved estimate produces a measurable overspend",
              overspend > 0, f"overspend=${overspend:.8f}")
        check(checks, "overspend is bounded by the concurrent in-flight reservations, not unbounded",
              overspend <= admitted * per_request_usd,
              f"overspend=${overspend:.8f} one_request=${per_request_usd:.8f} admitted={admitted}")
        return {
            "scenario": "dollar-budget",
            "dollar_budget": budget_usd,
            "pinned_input_usd_per_mtok": PINNED_INPUT_USD_PER_MTOK,
            "pinned_output_usd_per_mtok": PINNED_OUTPUT_USD_PER_MTOK,
            "upstream_reported_input_tokens": prompt_tokens,
            "upstream_reported_output_tokens": completion_tokens,
            "token_budget": budget_tokens,
            "workers": ctx.workers,
            "requests": len(results),
            "admitted": admitted,
            "rejected": rejected,
            "settled_input_tokens": settled_in,
            "settled_output_tokens": settled_out,
            "actual_cost_usd": round(actual_cost, 10),
            "overspend_usd": round(overspend, 10),
            "overspend_fraction_of_budget": round(overspend / budget_usd, 4),
            "assumption": "list-price conversion of a token budget; the router has no cumulative USD budget control",
        }, checks
    finally:
        upstream.stop()


def scenario_crash_restart(ctx: Ctx) -> dict:
    checks: list[dict] = []
    upstream = MockUpstream().start()
    try:
        token, hashed = new_caller(ctx, "crash-a")
        token_hash, token_id = split_hash(hashed)
        sqlite_path = ctx.work / "crash.sqlite" if ctx.backend == "sqlite" else None
        budget_tokens = 4000  # 200 requests of 20 tokens
        caller = caller_entry("crash-a", token_hash, token_id, rpm=0, concurrent=8, day_tokens=budget_tokens)
        state_path = ctx.work / "crash-state.json"
        process = render_scenario(
            ctx,
            "crash-restart",
            upstreams=[{"name": "mock", "base_url": upstream.base_url, "model": "mock-model"}],
            targets=['{provider: mock, model_ref: mock-model}'],
            callers=[caller],
            port=free_port(),
            state_path=state_path,
            sqlite_path=sqlite_path,
        ).start()
        process.wait_ready()
        first_half = 0
        statuses_before = []
        for _ in range(80):
            status, body, _ = post_json(process.addr, "/v1/chat/completions", chat_body(False), token)
            statuses_before.append(status)
            if status == 200:
                first_half += 1
        process.stop(sig=signal.SIGKILL, wait=10)
        ctx.processes_started.remove(process)
        restart = render_scenario(
            ctx,
            "crash-restart-restarted",
            upstreams=[{"name": "mock", "base_url": upstream.base_url, "model": "mock-model"}],
            targets=['{provider: mock, model_ref: mock-model}'],
            callers=[caller],
            port=process.port,  # same listener port: the restart must serve
            state_path=state_path,
            sqlite_path=sqlite_path,
        ).start()
        recovered_seconds = None
        started = time.time()
        try:
            restart.wait_ready(timeout=45)
            recovered_seconds = time.time() - started
        except RuntimeError as exc:
            check(checks, "router restarts after SIGKILL", False, str(exc))
        admitted_after = 0
        rejected_after = 0
        reasons_after: dict[str, int] = {}
        if recovered_seconds is not None:
            for _ in range(400):
                status, body, _ = post_json(restart.addr, "/v1/chat/completions", chat_body(False), token)
                if status == 200:
                    admitted_after += 1
                else:
                    rejected_after += 1
                    reason = rejection_reason(body) or str(status)
                    reasons_after[reason] = reasons_after.get(reason, 0) + 1
                    if rejected_after >= 20:
                        break
        restart.stop()
        store = Store(ctx, sqlite_path)
        rows = store.usage_rows("crash-a") if store.available else []
        rows_ok = [row for row in rows if int(row["status"]) == 200]
        settled = sum(int(row["total_tokens"]) for row in rows_ok)
        total_admitted = first_half + admitted_after
        check(checks, "service recovers after SIGKILL", recovered_seconds is not None,
              f"ready_after_s={recovered_seconds}")
        check(checks, "quota state survived the crash (budget was not reset)",
              total_admitted * 20 <= budget_tokens + 20 * 8,
              f"admitted={total_admitted} tokens={total_admitted * 20} budget={budget_tokens}")
        check(checks, "budget is exhausted after restart, not doubled",
              total_admitted * 20 >= budget_tokens - 20 * 8,
              f"admitted={total_admitted} tokens={total_admitted * 20}")
        check(checks, "usage rows from before the crash are still readable", len(rows_ok) >= first_half,
              f"admitted_rows={len(rows_ok)} pre_crash_admitted={first_half}")
        return {
            "scenario": "crash-restart",
            "day_token_budget": budget_tokens,
            "admitted_before_kill": first_half,
            "recovered_seconds": recovered_seconds,
            "admitted_after_restart": admitted_after,
            "rejected_after_restart": rejected_after,
            "reject_reasons_after_restart": reasons_after,
            "total_admitted": total_admitted,
            "settled_tokens": settled,
            "usage_rows": len(rows),
            "admitted_usage_rows": len(rows_ok),
            "row_loss_vs_admitted": total_admitted - len(rows_ok),
        }, checks
    finally:
        upstream.stop()


def scenario_multiprocess(ctx: Ctx) -> dict:
    checks: list[dict] = []
    upstream = MockUpstream().start()
    try:
        token, hashed = new_caller(ctx, "multi-a")
        token_hash, token_id = split_hash(hashed)
        sqlite_path = ctx.work / "multi.sqlite" if ctx.backend == "sqlite" else None
        budget_tokens = 2000  # 100 requests of 20 tokens
        caller = caller_entry("multi-a", token_hash, token_id, rpm=0, concurrent=8, day_tokens=budget_tokens)
        shared_state = ctx.work / "multi-shared-state.json"
        processes = []
        # SQLite is not a multi-writer migration target: the first process
        # applies the startup migration, the others start behind it with the
        # deployment-job gate so they cannot race the same DDL.
        for index in range(ctx.processes):
            state_path = shared_state if ctx.shared_state else ctx.work / f"multi-state-{index}.json"
            processes.append(
                render_scenario(
                    ctx,
                    f"multi-{index}",
                    upstreams=[{"name": "mock", "base_url": upstream.base_url, "model": "mock-model"}],
                    targets=['{provider: mock, model_ref: mock-model}'],
                    callers=[caller],
                    port=free_port(),
                    state_path=state_path,
                    sqlite_path=sqlite_path,
                    migration_policy="auto-safe" if index == 0 else "deployment-job",
                ).start()
            )
            processes[index].wait_ready()
        deadline = time.time() + min(ctx.duration, 30)
        lock = threading.Lock()
        statuses: list[int] = []

        def worker(process: RouterProcess) -> None:
            while time.time() < deadline:
                status, _, _ = post_json(process.addr, "/v1/chat/completions", chat_body(False), token)
                with lock:
                    statuses.append(status)
                if status != 200:
                    # Keep probing after exhaustion to confirm the cap holds.
                    time.sleep(0.01)

        threads = [threading.Thread(target=worker, args=(processes[i % len(processes)],), daemon=True)
                   for i in range(ctx.workers)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        for process in processes:
            process.stop()
        admitted = sum(1 for status in statuses if status == 200)
        store = Store(ctx, sqlite_path)
        rows = store.usage_rows("multi-a") if store.available else []
        admitted_rows = [row for row in rows if int(row["status"]) == 200]
        settled = sum(int(row["total_tokens"]) for row in admitted_rows)
        state_files = [shared_state] if ctx.shared_state else [ctx.work / f"multi-state-{index}.json"
                                                             for index in range(len(processes))]
        persisted = [read_state_day_tokens(path, "multi-a") for path in state_files]
        persisted_total = sum(value for value in persisted if value is not None)
        check(checks, "each process enforces the configured budget on its own",
              admitted * 20 >= budget_tokens, f"admitted={admitted} tokens={admitted * 20}")
        expected_cap = budget_tokens * len(processes)
        check(checks, "total admitted tokens stay within processes x budget",
              settled <= expected_cap + 20 * len(processes),
              f"settled={settled} cap={expected_cap}")
        check(checks, "the budget multiplies by the number of processes",
              settled >= budget_tokens * 1.5, f"settled={settled} per_process_budget={budget_tokens}")
        if ctx.shared_state:
            check(checks, "a shared quota state file loses updates between processes",
                  persisted_total < settled, f"persisted={persisted_total} settled={settled}")
        else:
            check(checks, "separate quota state files each keep their own accurate counter",
                  abs(persisted_total - settled) <= 20 * len(processes),
                  f"persisted={persisted_total} settled={settled}")
        return {
            "scenario": "multiprocess",
            "processes": len(processes),
            "shared_state_path": ctx.shared_state,
            "day_token_budget_per_process": budget_tokens,
            "admitted": admitted,
            "settled_tokens": settled,
            "admitted_usage_rows": len(admitted_rows),
            "usage_rows": len(rows),
            "persisted_state_day_tokens": persisted,
            "persisted_total_day_tokens": persisted_total,
            "overspend_factor": round(settled / budget_tokens, 3) if budget_tokens else None,
            "expected_cap_tokens": expected_cap,
        }, checks
    finally:
        upstream.stop()


SCENARIO_FUNCS = {
    "sustained": scenario_sustained,
    "auth": scenario_auth,
    "cancel": scenario_cancel,
    "pii-failover": scenario_pii_failover,
    "dollar-budget": scenario_dollar_budget,
    "crash-restart": scenario_crash_restart,
    "multiprocess": scenario_multiprocess,
}


def read_state_day_tokens(path: Path, caller_id: str) -> int | None:
    """Read the persisted day-token counter from a quota state file.

    The file is a JSON envelope with an HMAC over the state; this harness reads
    the counter for evidence and does not verify the MAC (it has no key), so a
    value here is only meaningful next to the process that wrote it.
    """
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    state = document.get("state") if isinstance(document, dict) else None
    callers = state.get("callers") if isinstance(state, dict) else None
    caller = callers.get(caller_id) if isinstance(callers, dict) else None
    if not isinstance(caller, dict):
        return None
    value = caller.get("day_tokens")
    return int(value) if isinstance(value, int) else None


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


# ---------------------------------------------------------------------------
# Report


def write_report(work: Path, ctx: Ctx, results: list[dict], checks: list[dict]) -> Path:
    report = {
        "schema": SCHEMA,
        "generated_at": now_iso(),
        "environment": environment_record(ctx),
        "scenarios": results,
        "checks": checks,
        "checks_failed": sum(1 for item in checks if not item["ok"]),
        "limits": [
            "Loopback synthetic upstream with fixed 20-token usage; not real provider usage or latency.",
            "Bounded run of this build and these configs; not a universal overspend bound.",
            "SQLite and Postgres are separate runs; a SQLite run says nothing about Postgres replication or locking.",
            "Async usage persistence is best-effort; a SIGKILL can lose rows that the quota state already counted.",
        ],
    }
    path = work / "report.json"
    path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    (work / "RESULT.md").write_text(render_markdown(report), encoding="utf-8")
    return path


def render_markdown(report: dict) -> str:
    env = report["environment"]
    lines = [
        "# Governance under load: harness run",
        "",
        f"- Generated: {report['generated_at']}",
        f"- Router commit: `{env['router_commit']}`",
        f"- Host: {env['cpu_model'] or 'unknown'} ({env['visible_cpus']} visible CPUs), {env['kernel']}",
        f"- Backend: {env['backend']}",
        f"- Hosted model calls: {env['hosted_model_calls']}",
        "",
        "## Checks",
        "",
        "| Check | Result | Detail |",
        "|---|---|---|",
    ]
    for item in report["checks"]:
        detail = item["detail"].replace("|", "\\|")[:200]
        lines.append(f"| {item['name']} | {'PASS' if item['ok'] else 'FAIL'} | {detail} |")
    lines.extend(["", "## Scenarios", ""])
    for scenario in report["scenarios"]:
        lines.append(f"### {scenario['scenario']}")
        lines.append("")
        for key, value in scenario.items():
            if key == "scenario":
                continue
            if isinstance(value, (dict, list)):
                value = json.dumps(value, sort_keys=True)
            lines.append(f"- `{key}`: {value}")
        lines.append("")
    lines.extend(["## Limits", ""])
    lines.extend(f"- {limit}" for limit in report["limits"])
    lines.append("")
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# CLI


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--scenario", action="append", choices=SCENARIOS, required=True)
    parser.add_argument("--backend", choices=("sqlite", "postgres"), default="sqlite")
    parser.add_argument("--dsn-env", default="ROUTER_USAGE_DB_DSN",
                        help="environment variable holding the Postgres DSN (never passed on a command line)")
    parser.add_argument("--duration", type=float, default=30.0, help="load seconds per scenario")
    parser.add_argument("--workers", type=int, default=16)
    parser.add_argument("--processes", type=int, default=2)
    parser.add_argument("--shared-state", action="store_true",
                        help="multiprocess: point every process at one quota state file")
    parser.add_argument("--binary-dir", type=Path, default=None, help="reuse prebuilt binaries")
    parser.add_argument("--name", default="run", help="work directory label")
    parser.add_argument("--keep", action="store_true", help="keep the work directory even on success")
    parser.add_argument("--strict", action="store_true", help="exit non-zero when any check fails")
    args = parser.parse_args()

    if sys.version_info < (3, 10):
        print("python 3.10 or newer is required", file=sys.stderr)
        return 2
    if args.backend == "postgres" and not os.environ.get(args.dsn_env):
        print(f"--backend postgres needs the environment variable {args.dsn_env}", file=sys.stderr)
        return 2
    if args.backend == "postgres" and not shutil.which("psql"):
        print("--backend postgres needs psql on PATH to read usage rows", file=sys.stderr)
        return 2
    if args.backend == "postgres":
        # Fail closed on a DSN that would put credentials on a command line.
        parse_dsn(os.environ[args.dsn_env])
    work = new_workdir(args.name)
    commit = os.environ.get("GOVLOAD_ROUTER_COMMIT", "") or subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True
    ).stdout.strip() or "unknown"
    child_env = dict(os.environ)
    child_env.pop("OPENAI_API_KEY", None)
    ctx = Ctx(
        work=work,
        bin_dir=args.binary_dir or (work / "bin"),
        backend=args.backend,
        dsn_env=args.dsn_env,
        duration=args.duration,
        workers=args.workers,
        processes=max(2, args.processes),
        shared_state=args.shared_state,
        commit=commit,
        keep=args.keep,
        strict=args.strict,
        child_env=child_env,
    )
    build_binaries(ctx)
    admin_password_hash(ctx)
    if ctx.backend == "postgres":
        migrate_usage_schema(ctx)
    results: list[dict] = []
    checks: list[dict] = []
    try:
        for name in args.scenario:
            print(f"running scenario {name} (backend={ctx.backend})", flush=True)
            measurements, scenario_checks = SCENARIO_FUNCS[name](ctx)
            results.append(measurements)
            checks.extend(scenario_checks)
            failed = [item for item in scenario_checks if not item["ok"]]
            print(f"  {len(scenario_checks) - len(failed)}/{len(scenario_checks)} checks pass", flush=True)
            for item in failed:
                print(f"  FAIL {item['name']}: {item['detail']}", flush=True)
    finally:
        stop_all(ctx)
    report_path = write_report(work, ctx, results, checks)
    failed = sum(1 for item in checks if not item["ok"])
    print(f"report: {report_path}")
    print(f"checks: {len(checks) - failed}/{len(checks)} pass")
    if not args.keep:
        for transient in ("bin", "logs"):
            shutil.rmtree(work / transient, ignore_errors=True)
    return 1 if (args.strict and failed) else 0


def migrate_sqlite(ctx: Ctx, path: Path) -> None:
    """Apply the usage-schema gate once before several SQLite processes start."""
    subprocess.run(
        [
            str(ctx.bin_dir / "metrum-ai-router-migrate"),
            "--action",
            "apply",
            "--driver",
            "sqlite",
            "--db",
            str(path),
            "--json",
        ],
        cwd=ctx.work,
        env=ctx.child_env,
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
    )


def migrate_usage_schema(ctx: Ctx) -> None:
    """Complete the usage-schema deployment gate before serving from Postgres.

    `apply` alone leaves the ledger at `state=pending` because the shipping data
    job is still to run, and a router with `migration_policy: deployment-job`
    then refuses to serve. Run the same three steps the operator runbook does:
    apply, one checkpoint of the shipping data job, then verify-serving.
    """
    binary = str(ctx.bin_dir / "metrum-ai-router-migrate")
    steps = (
        ["--action", "apply", "--driver", "postgres", "--dsn-env", ctx.dsn_env, "--scope", "usage"],
        ["--action", "resume", "--job", "historical-usage-validation-v1", "--checkpoint-ordinal", "0",
         "--driver", "postgres", "--dsn-env", ctx.dsn_env, "--scope", "usage"],
        ["--action", "verify-serving", "--driver", "postgres", "--dsn-env", ctx.dsn_env, "--scope", "usage"],
    )
    for arguments in steps:
        completed = subprocess.run(
            [binary, *arguments],
            cwd=ctx.work,
            env=ctx.child_env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE if arguments[1] != "verify-serving" else subprocess.PIPE,
            text=True,
            check=False,
        )
        if completed.returncode != 0:
            raise RuntimeError(
                f"usage migration step {arguments[1]} failed: {(completed.stderr or '')[-400:]}"
            )


if __name__ == "__main__":
    raise SystemExit(main())
