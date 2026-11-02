#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Per-hop router hierarchy latency and TTFT (#587).

Measures streaming time to first token (TTFT) and total latency through zero,
one and two router hops in front of one OpenAI-Chat backend:

    direct   client -> backend
    1-hop    client -> edge router -> backend
    2-hop    client -> edge router -> datacenter router -> backend

The routers run the shipped `metrum-ai-router` binary with the same hop headers,
loop protection and usage recording a real layered deployment uses (see
`deploy/reference/hierarchy/`). Every lane hits the same backend with the same
prompt and output cap, so a per-hop delta is comparable within a run.

The backend is either a real self-hosted OpenAI-Chat service (`--backend-url`,
for example a vLLM server on a rented GPU node) or the built-in streaming mock
for a procedure dry run. A mock run validates the procedure and the arithmetic;
its absolute latency is fixture latency, not product evidence.

Run the load generator on a node separate from every router and the backend.
Report the CPU model and core count of each node in the run notes.

Output: `report.json` plus `RESULT.md` under `.out/hierarchy-latency/<utc>-<name>/`.
No hosted model calls; cost is the operator's rental, not model spend.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import platform
import random
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.parse
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCHEMA = "router-hierarchy-latency-v1"
LANES = ("direct", "1-hop", "2-hop")
PROMPT = "Summarize the release checklist in one short sentence."


# ---------------------------------------------------------------------------
# Small helpers


def now_iso() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def percentile(values: list[float], fraction: float) -> float | None:
    """Nearest-rank percentile, matching the governance harness convention."""
    if not values:
        return None
    ordered = sorted(values)
    rank = math.ceil(fraction * len(ordered))
    return ordered[min(len(ordered) - 1, max(0, rank - 1))]


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def cpu_model() -> str:
    try:
        for line in Path("/proc/cpuinfo").read_text(encoding="utf-8").splitlines():
            if line.startswith("model name"):
                return line.split(":", 1)[1].strip()
    except OSError:
        pass
    return platform.processor() or "unknown"


# ---------------------------------------------------------------------------
# Backend


class MockBackend:
    """Streaming OpenAI-Chat fixture with a configurable TTFT and token pace.

    `ttft_ms` models prefill before the first event; `event_delay_ms` models
    decode pacing between events. Both are fixture knobs, not product behaviour.
    """

    def __init__(self, ttft_ms: float = 60.0, event_delay_ms: float = 8.0, events: int = 24) -> None:
        self.ttft_ms = ttft_ms
        self.event_delay_ms = event_delay_ms
        self.events = events
        self.lock = threading.Lock()
        self.calls = 0
        self.headers: list[dict[str, str]] = []
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), self._handler())
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def _handler(self):
        backend = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                return

            def do_POST(self):  # noqa: N802 - BaseHTTPRequestHandler API
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b"{}"
                try:
                    body = json.loads(raw)
                except json.JSONDecodeError:
                    body = {}
                with backend.lock:
                    backend.calls += 1
                    if len(backend.headers) < 500:
                        entries = {}
                        for key in self.headers.keys():
                            values = self.headers.get_all(key) or []
                            entries[key.lower()] = ", ".join(values)
                        backend.headers.append(entries)
                stream = bool(body.get("stream"))
                if stream:
                    self._stream(body)
                else:
                    self._unary(body)

            def _stream(self, body: dict) -> None:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.send_header("Connection", "close")
                self.end_headers()
                time.sleep(backend.ttft_ms / 1000.0)
                for index in range(backend.events):
                    payload = json.dumps(
                        {
                            "id": "chatcmpl_hop_fixture",
                            "object": "chat.completion.chunk",
                            "model": body.get("model", "fixture"),
                            "choices": [
                                {
                                    "index": 0,
                                    "delta": {"content": f"w{index} "} if index else {"role": "assistant", "content": "w0 "},
                                    "finish_reason": None,
                                }
                            ],
                        }
                    )
                    try:
                        self.wfile.write(f"data: {payload}\n\n".encode())
                        self.wfile.flush()
                    except (BrokenPipeError, ConnectionResetError):
                        return
                    time.sleep(backend.event_delay_ms / 1000.0)
                final = json.dumps(
                    {
                        "id": "chatcmpl_hop_fixture",
                        "object": "chat.completion.chunk",
                        "model": body.get("model", "fixture"),
                        "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                        "usage": {"prompt_tokens": 16, "completion_tokens": backend.events, "total_tokens": 16 + backend.events},
                    }
                )
                try:
                    self.wfile.write(f"data: {final}\n\ndata: [DONE]\n\n".encode())
                    self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    return
                self.close_connection = True

            def _unary(self, body: dict) -> None:
                time.sleep((backend.ttft_ms + backend.events * backend.event_delay_ms) / 1000.0)
                payload = json.dumps(
                    {
                        "id": "chatcmpl_hop_fixture",
                        "object": "chat.completion",
                        "model": body.get("model", "fixture"),
                        "choices": [
                            {
                                "index": 0,
                                "message": {"role": "assistant", "content": "fixture reply"},
                                "finish_reason": "stop",
                            }
                        ],
                        "usage": {"prompt_tokens": 16, "completion_tokens": backend.events,
                                  "total_tokens": 16 + backend.events},
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

    def start(self) -> "MockBackend":
        self.thread.start()
        return self

    def stop(self) -> None:
        self.server.shutdown()
        self.server.server_close()


# ---------------------------------------------------------------------------
# Router processes


@dataclass
class Ctx:
    work: Path
    bin_dir: Path
    commit: str
    strict: bool
    child_env: dict[str, str] = field(default_factory=dict)
    started: list = field(default_factory=list)


def build_binaries(ctx: Ctx) -> None:
    if (ctx.bin_dir / "metrum-ai-router").exists() and (ctx.bin_dir / "metrum-ai-routerctl").exists():
        return
    ctx.bin_dir.mkdir(parents=True, exist_ok=True)
    for target in ("metrum-ai-router", "metrum-ai-routerctl"):
        subprocess.run(
            ["go", "build", "-buildvcs=false", "-o", str(ctx.bin_dir / target), f"./cmd/{target}"],
            cwd=ROOT,
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )


def new_caller_token(ctx: Ctx, slug: str) -> str:
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
            "latency-owner",
            "--project",
            "latency-project",
            "--env",
            "hop",
            "--key",
            slug,
            "--allow",
            "hier",
            "--allow",
            "hier-direct",
            "--allow",
            "loop-demo",
            "--token-out",
            str(token_path),
        ],
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
    )
    os.chmod(token_path, 0o600)
    return token_path.read_text(encoding="utf-8").strip()


CALLER_BLOCK = """\
  - id: {caller_id}
    owner_user: latency-owner
    project: latency-project
    environment: hop
    status: active
    token_hash: "{hash_value}"
    token_id: {token_id}
    allow: [{allow}]
    rate:
      rpm: 0
      tpm: 0
      concurrent: 0
    quota:
      day:
        requests: 0
        tokens: 0
      month:
        tokens: 0
      soft_pct: 0
    key:
      lifetime_tokens: 0
      soft_pct: 0
"""


def caller_block(ctx: Ctx, slug: str, caller_id: str, allow: str) -> str:
    hash_value, token_id = token_hash(ctx, slug)
    return CALLER_BLOCK.format(caller_id=caller_id, hash_value=hash_value, token_id=token_id, allow=allow)


def token_hash(ctx: Ctx, slug: str) -> tuple[str, str]:
    token_path = ctx.work / "tokens" / f"{slug}.token"
    completed = subprocess.run(
        [str(ctx.bin_dir / "metrum-ai-routerctl"), "callers", "hash", "--token-file", str(token_path)],
        check=True,
        capture_output=True,
        text=True,
    )
    document = json.loads(completed.stdout)
    return document["token_hash"], document["token_id"]


ROUTER_CONFIG = """\
server:
  listen: 127.0.0.1:{port}
  default_model_group: {default_group}
  identifiers:
    mode: passthrough
  cache:
    enabled: false
  logging:
    path: {log_path}
    rotate_mb: 64
    keep: 2
  usage_db:
    enabled: true
    driver: sqlite
    path: {usage_path}
    migration_policy: auto-safe
    drain_timeout_ms: 3000
  upstream:
    max_router_hops: {max_hops}
    timeout_ms: 600000
state_path: {state_path}

providers:
{providers}

models:
{models}

users:
  - id: latency-owner
    name: Latency Owner
    type: service_account
    status: active

projects:
  - id: latency-project
    name: Latency Project
    status: active

project_memberships:
  - user_id: latency-owner
    project: latency-project
    role: developer
    status: active

callers:
{callers}
"""

BACKEND_PROVIDER = """\
  backend:
    base_url: {base_url}
    dialect: openai-chat
    api_key: {api_key}
    key_id: latency-backend
    models:
      {model}:
        model: {model}
        input_modalities: [text]
        output_modalities: [text]
"""


class Router:
    def __init__(self, ctx: Ctx, name: str, config_path: Path, port: int, log_path: Path) -> None:
        self.ctx = ctx
        self.name = name
        self.config_path = config_path
        self.port = port
        self.log_path = log_path
        self.proc: subprocess.Popen | None = None

    def start(self) -> "Router":
        self.log_path.parent.mkdir(parents=True, exist_ok=True)
        sink = open(self.log_path, "ab")
        self.proc = subprocess.Popen(
            [str(self.ctx.bin_dir / "metrum-ai-router"), "-config", str(self.config_path)],
            cwd=self.ctx.work,
            env=self.ctx.child_env,
            stdout=sink,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        sink.close()
        self.ctx.started.append(self)
        return self

    @property
    def addr(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def wait_ready(self, timeout: float = 60.0) -> None:
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                with socket.create_connection(("127.0.0.1", self.port), timeout=0.5) as sock:
                    sock.sendall(b"GET /readyz HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
                    if b"200" in sock.recv(64).split(b"\r\n", 1)[0]:
                        return
            except OSError:
                pass
            if self.proc is not None and self.proc.poll() is not None:
                raise RuntimeError(f"{self.name} exited during startup ({self.proc.returncode})")
            time.sleep(0.1)
        raise RuntimeError(f"{self.name} did not become ready")

    def stop(self) -> None:
        if self.proc is None or self.proc.poll() is not None:
            return
        self.proc.send_signal(signal.SIGTERM)
        try:
            self.proc.wait(timeout=20)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait(timeout=10)


def render_router_config(
    *,
    port: int,
    default_group: str,
    log_path: Path,
    usage_path: Path,
    state_path: Path,
    max_hops: int,
    providers: str,
    models: str,
    callers: str,
) -> str:
    return ROUTER_CONFIG.format(
        port=port,
        default_group=default_group,
        log_path=log_path,
        usage_path=usage_path,
        state_path=state_path,
        max_hops=max_hops,
        providers=providers.rstrip(),
        models=models.rstrip(),
        callers=callers.rstrip(),
    )


# ---------------------------------------------------------------------------
# Load


def stream_request(addr: str, token: str, model: str, max_tokens: int) -> dict:
    """Send one streaming chat request and time TTFT and completion."""
    payload = json.dumps(
        {
            "model": model,
            "max_tokens": max_tokens,
            "stream": True,
            "messages": [{"role": "user", "content": PROMPT}],
        }
    ).encode()
    host, port = urllib.parse.urlsplit(addr).hostname, urllib.parse.urlsplit(addr).port
    started = time.perf_counter()
    ttft = None
    first_byte = None
    status = 0
    body_bytes = 0
    try:
        connection = socket.create_connection((host, port), timeout=120)
        connection.sendall(
            (
                f"POST /v1/chat/completions HTTP/1.1\r\nHost: {host}:{port}\r\n"
                f"Authorization: Bearer {token}\r\nContent-Type: application/json\r\n"
                f"Content-Length: {len(payload)}\r\nConnection: close\r\n\r\n"
            ).encode()
            + payload
        )
        buffer = b""
        headers_done = False
        while True:
            chunk = connection.recv(65536)
            if not chunk:
                break
            body_bytes += len(chunk)
            if first_byte is None:
                first_byte = time.perf_counter() - started
            buffer += chunk
            if not headers_done and b"\r\n\r\n" in buffer:
                headers_done = True
                head = buffer.split(b"\r\n\r\n", 1)[0]
                line = head.split(b"\r\n", 1)[0].split()
                status = int(line[1]) if len(line) > 1 else 0
                buffer = buffer.split(b"\r\n\r\n", 1)[1]
            if ttft is None and b"data: " in buffer:
                ttft = time.perf_counter() - started
        connection.close()
    except OSError as exc:
        return {"status": 0, "error": str(exc), "ttft_ms": None, "total_ms": None}
    total = time.perf_counter() - started
    return {
        "status": status,
        "ttft_ms": (ttft if ttft is not None else (first_byte if first_byte is not None else total)) * 1000,
        "first_byte_ms": (first_byte or total) * 1000,
        "total_ms": total * 1000,
        "body_bytes": body_bytes,
    }


def unary_request(addr: str, token: str, model: str, max_tokens: int) -> dict:
    payload = json.dumps(
        {
            "model": model,
            "max_tokens": max_tokens,
            "messages": [{"role": "user", "content": PROMPT}],
        }
    ).encode()
    host, port = urllib.parse.urlsplit(addr).hostname, urllib.parse.urlsplit(addr).port
    started = time.perf_counter()
    try:
        connection = socket.create_connection((host, port), timeout=120)
        connection.sendall(
            (
                f"POST /v1/chat/completions HTTP/1.1\r\nHost: {host}:{port}\r\n"
                f"Authorization: Bearer {token}\r\nContent-Type: application/json\r\n"
                f"Content-Length: {len(payload)}\r\nConnection: close\r\n\r\n"
            ).encode()
            + payload
        )
        data = b""
        while True:
            chunk = connection.recv(65536)
            if not chunk:
                break
            data += chunk
        connection.close()
    except OSError as exc:
        return {"status": 0, "error": str(exc), "total_ms": None}
    total = (time.perf_counter() - started) * 1000
    status = 0
    head = data.split(b"\r\n\r\n", 1)[0].split(b"\r\n", 1)[0].split()
    if len(head) > 1:
        status = int(head[1])
    return {"status": status, "total_ms": total}


def describe(values: list[float]) -> dict:
    if not values:
        return {"count": 0}
    return {
        "count": len(values),
        "p50_ms": round(percentile(values, 0.50), 3),
        "p90_ms": round(percentile(values, 0.90), 3),
        "p99_ms": round(percentile(values, 0.99), 3),
        "min_ms": round(min(values), 3),
        "max_ms": round(max(values), 3),
        "mean_ms": round(sum(values) / len(values), 3),
    }


def run_lane(addr: str, token: str, model: str, requests: int, concurrency: int, max_tokens: int) -> dict:
    streaming: list[dict] = []
    unary: list[dict] = []
    lock = threading.Lock()

    def stream_worker(count: int) -> None:
        for _ in range(count):
            result = stream_request(addr, token, model, max_tokens)
            with lock:
                streaming.append(result)

    def unary_worker(count: int) -> None:
        for _ in range(count):
            result = unary_request(addr, token, model, max_tokens)
            with lock:
                unary.append(result)

    half = max(1, concurrency // 2)
    per_worker_stream = max(1, (requests // 2) // half)
    per_worker_unary = max(1, (requests // 2) // half)
    threads = [threading.Thread(target=stream_worker, args=(per_worker_stream,)) for _ in range(half)]
    threads += [threading.Thread(target=unary_worker, args=(per_worker_unary,)) for _ in range(half)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    ok_stream = [item for item in streaming if item["status"] == 200]
    ok_unary = [item for item in unary if item["status"] == 200]
    return {
        "streaming": {
            "requests": len(streaming),
            "ok": len(ok_stream),
            "statuses": sorted({item["status"] for item in streaming}),
            "ttft": describe([item["ttft_ms"] for item in ok_stream if item["ttft_ms"] is not None]),
            "total": describe([item["total_ms"] for item in ok_stream if item["total_ms"] is not None]),
        },
        "unary": {
            "requests": len(unary),
            "ok": len(ok_unary),
            "statuses": sorted({item["status"] for item in unary}),
            "total": describe([item["total_ms"] for item in ok_unary if item["total_ms"] is not None]),
        },
    }


# ---------------------------------------------------------------------------
# Per-hop usage rows


def sqlite_rows(path: Path, sql: str) -> list[dict]:
    import sqlite3

    connection = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=10)
    connection.row_factory = sqlite3.Row
    try:
        return [dict(row) for row in connection.execute(sql).fetchall()]
    finally:
        connection.close()


def hop_usage(path: Path, caller_id: str, group: str | None = None) -> dict:
    if not path.exists():
        return {"available": False}
    where = f"caller_id = '{caller_id}'"
    if group:
        where += f" AND resolved_group = '{group}'"
    rows = sqlite_rows(
        path,
        "SELECT status, stream, latency_ms, ttfb_ms, upstream_duration_ms, total_tokens, target_provider "
        f"FROM request_usage WHERE {where}",
    )
    ok = [row for row in rows if int(row["status"]) == 200]
    return {
        "available": True,
        "requests": len(rows),
        "admitted": len(ok),
        "statuses": sorted({int(row["status"]) for row in rows}),
        "latency": describe([float(row["latency_ms"]) for row in ok]),
        "ttfb": describe([float(row["ttfb_ms"]) for row in ok if row["ttfb_ms"] is not None]),
        "upstream_duration": describe([float(row["upstream_duration_ms"]) for row in ok
                                       if row["upstream_duration_ms"] is not None]),
        "settled_tokens": sum(int(row["total_tokens"]) for row in ok),
        "providers": sorted({str(row["target_provider"]) for row in ok}),
    }


# ---------------------------------------------------------------------------
# Report


def render_markdown(report: dict) -> str:
    env = report["environment"]
    lines = [
        "# Per-hop router latency and TTFT (#587)",
        "",
        f"- Generated: {report['generated_at']}",
        f"- Router commit: `{env['router_commit']}`",
        f"- Backend: {env['backend']}",
        f"- Load generator host: {env['cpu_model']} ({env['visible_cpus']} visible CPUs), {env['kernel']}",
        f"- Requests per lane: {report['config']['requests']}, concurrency {report['config']['concurrency']}, "
        f"max_tokens {report['config']['max_tokens']}",
        "",
        "| Lane | Streams OK | TTFT p50 | TTFT p99 | Total p50 | Total p99 | Unary p50 | Unary p99 |",
        "|---|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for lane in LANES:
        data = report["lanes"][lane]["streaming"]
        unary = report["lanes"][lane]["unary"]
        def value(block: dict, field: str) -> str:
            return f"{block[field]:.1f}" if block.get(field) is not None else "-"
        lines.append(
            f"| {lane} | {data['ok']}/{data['requests']} | {value(data['ttft'], 'p50_ms')} ms | "
            f"{value(data['ttft'], 'p99_ms')} ms | {value(data['total'], 'p50_ms')} ms | "
            f"{value(data['total'], 'p99_ms')} ms | {value(unary['total'], 'p50_ms')} ms | "
            f"{value(unary['total'], 'p99_ms')} ms |"
        )
    lines.extend(["", "## Per-hop deltas", "", "| Compare | TTFT p50 | TTFT p99 | Total p50 | Total p99 |", "|---|---:|---:|---:|---:|"])
    for name, delta in report["deltas"].items():
        lines.append(
            f"| {name} | {delta['ttft_p50_ms']:+.1f} ms | {delta['ttft_p99_ms']:+.1f} ms | "
            f"{delta['total_p50_ms']:+.1f} ms | {delta['total_p99_ms']:+.1f} ms |"
        )
    lines.extend(["", "## Per-hop usage rows", ""])
    for hop, usage in report["usage"].items():
        if not usage.get("available"):
            lines.append(f"- {hop}: usage store not found")
            continue
        lines.append(
            f"- {hop}: {usage['admitted']}/{usage['requests']} admitted, latency p50 "
            f"{usage['latency'].get('p50_ms', '-')} ms / p99 {usage['latency'].get('p99_ms', '-')} ms, "
            f"TTFB p50 {usage['ttfb'].get('p50_ms', '-')} ms, upstream p50 "
            f"{usage['upstream_duration'].get('p50_ms', '-')} ms, {usage['settled_tokens']} tokens, "
            f"providers {', '.join(usage['providers']) or '-'}"
        )
    if report["backend_headers"]:
        lines.extend(["", "## Hop headers observed at the backend", ""])
        vias = report["backend_headers"].get("via", [])
        for value in vias[:5]:
            lines.append(f"- `Via: {value}`")
    lines.extend(["", "## Limits", ""])
    lines.extend(f"- {limit}" for limit in report["limits"])
    lines.append("")
    return "\n".join(lines)


def write_report(work: Path, ctx: Ctx, report: dict) -> Path:
    path = work / "report.json"
    path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    (work / "RESULT.md").write_text(render_markdown(report), encoding="utf-8")
    return path


# ---------------------------------------------------------------------------
# Main


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--backend-url", default="", help="self-hosted OpenAI-Chat base URL, e.g. http://gpu:8000/v1")
    parser.add_argument("--backend-key-env", default="", help="environment variable holding the backend key")
    parser.add_argument("--model", default="", help="served model name at the backend and on the routers")
    parser.add_argument("--requests", type=int, default=200, help="streaming requests per lane (half unary too)")
    parser.add_argument("--concurrency", type=int, default=8)
    parser.add_argument("--max-tokens", type=int, default=32)
    parser.add_argument("--warmup", type=int, default=8, help="requests per lane discarded before measuring")
    parser.add_argument("--mock-ttft-ms", type=float, default=60.0, help="mock backend TTFT for a dry run")
    parser.add_argument("--mock-event-delay-ms", type=float, default=8.0)
    parser.add_argument("--name", default="run")
    parser.add_argument("--keep", action="store_true")
    args = parser.parse_args()

    if args.backend_url:
        if not args.model:
            print("--backend-url needs --model", file=sys.stderr)
            return 2
        backend_token = os.environ.get(args.backend_key_env, "") if args.backend_key_env else ""
        if args.backend_key_env and not backend_token:
            print(f"--backend-key-env {args.backend_key_env} is not set", file=sys.stderr)
            return 2
        backend_kind = "self-hosted"
        backend_url = args.backend_url
    else:
        backend_kind = "loopback-mock"
        backend_token = ""
        backend_url = ""

    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    work = ROOT / ".out" / "hierarchy-latency" / f"{stamp}-{args.name}"
    work.mkdir(parents=True, exist_ok=False)
    commit = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True).stdout.strip()
    child_env = dict(os.environ)
    for secret in ("OPENAI_API_KEY", "ANTHROPIC_API_KEY", "FIREWORKS_API_KEY"):
        child_env.pop(secret, None)
    ctx = Ctx(work=work, bin_dir=work / "bin", commit=commit, strict=False, child_env=child_env)
    build_binaries(ctx)

    app_token = new_caller_token(ctx, "hop-app")
    router_token = new_caller_token(ctx, "hop-router")
    loop_token = new_caller_token(ctx, "hop-loop")
    edge_callers = caller_block(ctx, "hop-app", "hop-app", "hier, hier-direct, loop-demo")
    edge_callers += caller_block(ctx, "hop-loop", "hop-loop", "loop-demo")
    dc_callers = caller_block(ctx, "hop-router", "hop-router", "hier-dc, loop-demo")

    backend = None
    if backend_kind == "loopback-mock":
        backend = MockBackend(ttft_ms=args.mock_ttft_ms, event_delay_ms=args.mock_event_delay_ms).start()
        backend_url = backend.base_url
        model = args.model or "hop-fixture"
    else:
        model = args.model

    backend_provider = BACKEND_PROVIDER.format(
        base_url=backend_url,
        api_key=backend_token or "self-hosted-no-key",
        model=model,
    )

    ports = {"direct": free_port(), "edge": free_port(), "dc": free_port()}
    # Direct lane: a router is not involved at all, so it talks to the backend
    # API exactly as a client would.
    direct_addr = backend_url.removesuffix("/v1")
    direct_addr = urllib.parse.urlsplit(backend_url)._replace(path="").geturl()

    # Edge router: `hier-direct` goes straight to the backend (1-hop);
    # `hier` hands off to the datacenter router (2-hop); `loop-demo` points
    # back at itself through the datacenter router to prove loop protection.
    edge_usage = work / "edge-usage.sqlite"
    dc_usage = work / "dc-usage.sqlite"
    edge_config = work / "edge.yaml"
    dc_config = work / "dc.yaml"

    dc_config.write_text(
        render_router_config(
            port=ports["dc"],
            default_group="hier-dc",
            log_path=work / "logs" / "dc.jsonl",
            usage_path=dc_usage,
            state_path=work / "dc-state.json",
            max_hops=4,
            providers=backend_provider + (
                "  edge_loop:\n"
                f"    base_url: http://127.0.0.1:{ports['edge']}/v1\n"
                "    dialect: openai-chat\n"
                f"    api_key: {loop_token}\n"
                "    key_id: latency-edge-loop\n"
                "    upstream_kind: router\n"
                "    models:\n"
                "      loop-demo:\n"
                "        model: loop-demo\n"
            ),
            models=(
                "  hier-dc:\n"
                "    strategy: static\n"
                "    targets:\n"
                f"      - {{provider: backend, model_ref: {model}}}\n"
                "  loop-demo:\n"
                "    strategy: static\n"
                "    targets:\n"
                "      - {provider: edge_loop, model_ref: loop-demo}\n"
            ),
            callers=dc_callers,
        ),
        encoding="utf-8",
    )
    os.chmod(dc_config, 0o600)

    edge_config.write_text(
        render_router_config(
            port=ports["edge"],
            default_group="hier",
            log_path=work / "logs" / "edge.jsonl",
            usage_path=edge_usage,
            state_path=work / "edge-state.json",
            max_hops=4,
            providers=(
                "  backend:\n"
                f"    base_url: {backend_url}\n"
                "    dialect: openai-chat\n"
                f"    api_key: {backend_token or 'self-hosted-no-key'}\n"
                "    key_id: latency-backend\n"
                "    models:\n"
                f"      {model}:\n"
                f"        model: {model}\n"
                "  dc:\n"
                f"    base_url: http://127.0.0.1:{ports['dc']}/v1\n"
                "    dialect: openai-chat\n"
                f"    api_key: {router_token}\n"
                "    key_id: latency-dc\n"
                "    upstream_kind: router\n"
                "    models:\n"
                "      hier-dc:\n"
                "        model: hier-dc\n"
                "      loop-demo:\n"
                "        model: loop-demo\n"
            ),
            models=(
                "  hier:\n"
                "    strategy: static\n"
                "    targets:\n"
                "      - {provider: dc, model_ref: hier-dc}\n"
                "  hier-direct:\n"
                "    strategy: static\n"
                "    targets:\n"
                f"      - {{provider: backend, model_ref: {model}}}\n"
                "  loop-demo:\n"
                "    strategy: static\n"
                "    targets:\n"
                "      - {provider: dc, model_ref: loop-demo}\n"
            ),
            callers=edge_callers,
        ),
        encoding="utf-8",
    )
    os.chmod(edge_config, 0o600)

    dc = Router(ctx, "dc", dc_config, ports["dc"], work / "logs" / "dc.out").start()
    dc.wait_ready()
    edge = Router(ctx, "edge", edge_config, ports["edge"], work / "logs" / "edge.out").start()
    edge.wait_ready()

    lanes = {
        "direct": {"addr": direct_addr, "model": model, "token": backend_token or "self-hosted-no-key"},
        "1-hop": {"addr": edge.addr, "model": "hier-direct", "token": app_token},
        "2-hop": {"addr": edge.addr, "model": "hier", "token": app_token},
    }
    # Loop protection: a request that bounces between the two routers must end
    # as a bounded 508, never an unbounded ping-pong.
    loop_status, loop_body = post_chat(edge.addr, app_token, "loop-demo", args.max_tokens)

    results: dict[str, dict] = {}
    try:
        for lane, spec in lanes.items():
            print(f"warming {lane}", flush=True)
            for _ in range(args.warmup):
                stream_request(spec["addr"], spec["token"], spec["model"], args.max_tokens)
            print(f"measuring {lane}", flush=True)
            results[lane] = run_lane(
                spec["addr"], spec["token"], spec["model"], args.requests, args.concurrency, args.max_tokens
            )
    finally:
        edge.stop()
        dc.stop()
        if backend is not None:
            backend.stop()

    def delta(lane: str) -> dict:
        base = results["direct"]["streaming"]
        other = results[lane]["streaming"]
        return {
            "ttft_p50_ms": (other["ttft"].get("p50_ms") or 0) - (base["ttft"].get("p50_ms") or 0),
            "ttft_p99_ms": (other["ttft"].get("p99_ms") or 0) - (base["ttft"].get("p99_ms") or 0),
            "total_p50_ms": (other["total"].get("p50_ms") or 0) - (base["total"].get("p50_ms") or 0),
            "total_p99_ms": (other["total"].get("p99_ms") or 0) - (base["total"].get("p99_ms") or 0),
        }

    headers: dict[str, str] = {}
    if backend is not None and backend.headers:
        headers = {"via": sorted({entry.get("via", "") for entry in backend.headers if entry.get("via")})}

    report = {
        "schema": SCHEMA,
        "generated_at": now_iso(),
        "environment": {
            "router_commit": commit,
            "backend": backend_kind,
            "cpu_model": cpu_model(),
            "visible_cpus": os.cpu_count() or 0,
            "kernel": platform.release(),
            "machine": platform.machine(),
            "hosted_model_calls": 0,
            "loop_protection_status": loop_status,
            "loop_protection_body": loop_body[:200],
        },
        "config": {
            "requests": args.requests,
            "concurrency": args.concurrency,
            "max_tokens": args.max_tokens,
            "warmup": args.warmup,
            "model": model,
            "mock_ttft_ms": args.mock_ttft_ms if backend_kind == "loopback-mock" else None,
        },
        "lanes": results,
        "deltas": {lane: delta(lane) for lane in ("1-hop", "2-hop")},
        "usage": {
            "edge/1-hop": hop_usage(edge_usage, "hop-app", "hier-direct"),
            "edge/2-hop": hop_usage(edge_usage, "hop-app", "hier"),
            "dc/2-hop": hop_usage(dc_usage, "hop-router", "hier-dc"),
        },
        "backend_headers": headers,
        "limits": [
            "Client-side wall clock on this load-generator host; router and backend share the host in a mock run.",
            "TTFT is the first SSE data event, so a provider that emits an initial role event shifts it.",
            "A mock backend run validates the procedure and arithmetic only; it is not product latency evidence.",
            "Only self-hosted backends are valid for latency claims; hosted APIs are not used here.",
        ],
    }
    path = write_report(work, ctx, report)
    print(f"report: {path}")
    for lane in LANES:
        streaming = results[lane]["streaming"]
        print(
            f"  {lane}: ok={streaming['ok']}/{streaming['requests']} "
            f"ttft p50={streaming['ttft'].get('p50_ms')} p99={streaming['ttft'].get('p99_ms')} "
            f"total p50={streaming['total'].get('p50_ms')} statuses={streaming['statuses']}"
        )
    print(f"loop protection status: {loop_status}")
    if not args.keep:
        shutil.rmtree(work / "bin", ignore_errors=True)
    return 0 if all(results[lane]["streaming"]["ok"] for lane in LANES) and loop_status == 508 else 1


def post_chat(addr: str, token: str, model: str, max_tokens: int) -> tuple[int, str]:
    """One unary call, returning the status and a bounded body excerpt."""
    payload = json.dumps(
        {"model": model, "max_tokens": max_tokens, "messages": [{"role": "user", "content": PROMPT}]}
    ).encode()
    host, port = urllib.parse.urlsplit(addr).hostname, urllib.parse.urlsplit(addr).port
    try:
        connection = socket.create_connection((host, port), timeout=120)
        connection.sendall(
            (
                f"POST /v1/chat/completions HTTP/1.1\r\nHost: {host}:{port}\r\n"
                f"Authorization: Bearer {token}\r\nContent-Type: application/json\r\n"
                f"Content-Length: {len(payload)}\r\nConnection: close\r\n\r\n"
            ).encode()
            + payload
        )
        data = b""
        while True:
            chunk = connection.recv(65536)
            if not chunk:
                break
            data += chunk
        connection.close()
    except OSError as exc:
        return 0, str(exc)[:200]
    head, _, body = data.partition(b"\r\n\r\n")
    line = head.split(b"\r\n", 1)[0].split()
    status = int(line[1]) if len(line) > 1 else 0
    return status, body.decode("utf-8", "replace")[:300]


if __name__ == "__main__":
    raise SystemExit(main())
