#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Credential-free router e2e for the d3 external routing policy (issue #278).

Starts a Metrum AI Router binary built from this tree, real `d3_policy.py`
adapter processes, the scripted fake d3 (`fake_d3.py`) and loopback mock
upstreams. Then it checks:

  D01 enforce: easy prompt -> cheap tier
  D02 enforce: hard prompt -> strong tier, d3 class label recorded
  D03 shadow: target 0 served, d3's strong pick recorded as shadow recommendation
  D04 d3 down + fail_closed -> 502 routing-policy-error, no upstream call
  D05 d3 down + fallback -> target 0 served, outcome fallback
  D06 d3 422 + fail_closed -> 502 routing-policy-error, no upstream call
  D07 include_request: false -> d3:no-request-content, d3 not called
  D08 streaming enforce: hard prompt -> strong tier over SSE
  D09 privacy: no prompt text or credential in the usage DB, router/adapter logs

Test credentials, configuration, logs and the SQLite database live in a
temporary directory that is deleted afterwards. The report is scalar only.

Copyright 2026 Metrum AI.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
import secrets
import socket
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from contextlib import ExitStack
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
EXAMPLES = ROOT / "examples" / "external-routing-policy"
EASY = "Say hello to the WOMBAT-4410 team in one short friendly sentence."
HARD = ("Prove that the lock-free queue keeps its invariant under concurrent dequeue "
        "for tenant ZEBRA-7731, and derive the worst-case complexity.")
MARKERS = ("WOMBAT-4410", "ZEBRA-7731", "lock-free queue", "friendly sentence")
CHEAP_MODEL, STRONG_MODEL = "mock-cheap-1", "mock-strong-1"
GROUPS = {
    "d3-enforce": {"adapter": "ok", "mode": "enforce", "on_error": "fail_closed", "include_request": True},
    "d3-shadow": {"adapter": "ok", "mode": "shadow", "on_error": "fail_closed", "include_request": True},
    "d3-down-closed": {"adapter": "down", "mode": "enforce", "on_error": "fail_closed", "include_request": True},
    "d3-down-fallback": {"adapter": "down", "mode": "enforce", "on_error": "fallback", "include_request": True},
    "d3-422-closed": {"adapter": "reject", "mode": "enforce", "on_error": "fail_closed", "include_request": True},
    "d3-no-content": {"adapter": "ok", "mode": "enforce", "on_error": "fail_closed", "include_request": False},
}
DECISION_TABLES = (
    "request_decision_shape_features", "request_target_candidates", "request_target_filter_reasons",
    "request_routing_decisions", "request_routing_signals", "request_policy_executions",
    "request_fallback_transitions",
)


class HarnessError(Exception):
    """Fixed scalar diagnostics only."""


def load_fake() -> Any:
    spec = importlib.util.spec_from_file_location("fake_d3", EXAMPLES / "fake_d3.py")
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def free_port() -> int:
    with socket.socket() as sock:
        for port in range(18100, 19000):
            try:
                sock.bind(("127.0.0.1", port))
                break
            except OSError:
                continue
        return sock.getsockname()[1]


def http(url: str, payload: Any = None, headers: dict[str, str] | None = None,
         timeout: float = 15) -> tuple[int, bytes, str]:
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(url, data=data, headers={"Content-Type": "application/json", **(headers or {})})
    try:
        resp = opener.open(req, timeout=timeout)
    except urllib.error.HTTPError as err:
        resp = err
    with resp:
        return resp.code, resp.read(4 * 1024 * 1024), resp.headers.get("X-Request-Id", "")


def stop(proc: subprocess.Popen[bytes]) -> None:
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)


def start(stack: ExitStack, cmd: list[str], log: Path, env: dict[str, str]) -> subprocess.Popen[bytes]:
    with log.open("wb") as out:
        proc = subprocess.Popen(cmd, cwd=log.parent, env=env, stdout=out, stderr=subprocess.STDOUT)
    stack.callback(stop, proc)
    return proc


def ready(url: str, proc: subprocess.Popen[bytes], stage: str) -> None:
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            raise HarnessError(stage + "_exited_before_ready")
        try:
            if http(url, timeout=.5)[0] == 200:
                return
        except OSError:
            pass
        time.sleep(.1)
    raise HarnessError(stage + "_readiness_timeout")


class UpstreamHandler(BaseHTTPRequestHandler):
    def log_message(self, *_args: object) -> None:
        pass

    def do_POST(self) -> None:  # noqa: N802
        server: Any = self.server
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        model = str(body.get("model", ""))
        with server.lock:
            server.calls.append(model)  # model IDs only; content is discarded
        if body.get("stream"):
            chunks = [
                {"id": "mock", "object": "chat.completion.chunk", "model": model,
                 "choices": [{"index": 0, "delta": {"role": "assistant", "content": "ok"}, "finish_reason": None}]},
                {"id": "mock", "object": "chat.completion.chunk", "model": model,
                 "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                 "usage": {"prompt_tokens": 10, "completion_tokens": 1, "total_tokens": 11}},
            ]
            raw = b"".join(b"data: " + json.dumps(c).encode() + b"\n\n" for c in chunks) + b"data: [DONE]\n\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return
        if body.get("tools") and not any(m.get("role") == "tool" for m in body.get("messages", [])):
            raw = json.dumps({"id": "mock-tool", "object": "chat.completion", "model": model,
                              "choices": [{"index": 0, "finish_reason": "tool_calls", "message": {
                                  "role": "assistant", "content": None, "tool_calls": [{
                                      "id": "call_smoke", "type": "function", "function": {
                                          "name": "create_file", "arguments": '{"content":"smoke"}'}}]}}]}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return
        raw = json.dumps({"id": "mock", "object": "chat.completion", "model": model,
                          "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"},
                                       "finish_reason": "stop"}],
                          "usage": {"prompt_tokens": 10, "completion_tokens": 1, "total_tokens": 11}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def upstream_server(stack: ExitStack) -> Any:
    server: Any = ThreadingHTTPServer(("127.0.0.1", 0), UpstreamHandler)
    server.daemon_threads = True
    server.calls = []
    server.lock = threading.Lock()
    threading.Thread(target=server.serve_forever, daemon=True).start()
    stack.callback(server.server_close)
    stack.callback(server.shutdown)
    return server


def rows(db: Path, query: str, params: tuple[Any, ...] = ()) -> list[dict[str, Any]]:
    with sqlite3.connect(f"{db.as_uri()}?mode=ro", uri=True, timeout=5) as conn:
        conn.row_factory = sqlite3.Row
        return [dict(r) for r in conn.execute(query, params)]


def policy_row(db: Path, rid: str) -> dict[str, Any]:
    if not rid:
        raise HarnessError("router_request_id_missing")
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        found = rows(db, "SELECT * FROM request_policy_executions WHERE request_id = ? ORDER BY seq", (rid,))
        if found:
            return found[-1]
        time.sleep(.02)
    raise HarnessError("policy_telemetry_missing")


def served_model(db: Path, rid: str) -> str:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        found = rows(db, "SELECT model FROM request_attempts WHERE request_id = ? AND selected = 1", (rid,))
        if len(found) == 1:
            return str(found[0]["model"])
        time.sleep(.02)
    return ""


def privacy_ok(db: Path, forbidden: list[str], logs: list[Path]) -> bool:
    for table in DECISION_TABLES + ("request_usage", "request_attempts"):
        if not rows(db, f'PRAGMA table_info("{table}")'):
            return False
        for row in rows(db, f'SELECT * FROM "{table}"'):
            for value in row.values():
                if isinstance(value, str) and any(s in value for s in forbidden):
                    return False
    # Every log must exist and hold data, so the scan below cannot pass vacuously.
    if not all(p.exists() and p.stat().st_size > 0 for p in logs):
        return False
    return all(not any(s in p.read_text(errors="replace") for s in forbidden) for p in logs)


def execute(args: argparse.Namespace, checks: list[dict[str, Any]]) -> dict[str, Any]:
    fake = load_fake()
    evidence: dict[str, Any] = {}
    with tempfile.TemporaryDirectory(prefix="metrum-d3-e2e-") as temp_name, ExitStack() as stack:
        temp = Path(temp_name)
        binary = args.router_binary.resolve() if args.router_binary else temp / "metrum-ai-router"
        if args.router_binary is None:
            result = subprocess.run(["go", "build", "-buildvcs=false", "-o", str(binary), "./cmd/metrum-ai-router"],
                                    cwd=ROOT, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=600)
            if result.returncode:
                raise HarnessError("router_build_failed")
        token, d3_key, provider_key = (secrets.token_urlsafe(32) for _ in range(3))
        token_digest = hashlib.sha256(token.encode()).hexdigest()
        upstream = upstream_server(stack)
        d3_ok = fake.start_in_thread(api_key=d3_key)
        d3_reject = fake.start_in_thread(api_key=d3_key, mode="http422")
        for srv in (d3_ok, d3_reject):
            stack.callback(srv.server_close)
            stack.callback(srv.shutdown)
        down_port = free_port()  # nothing listens: d3 unreachable
        env = {k: os.environ[k] for k in ("PATH", "HOME", "TMPDIR", "SYSTEMROOT", "LD_LIBRARY_PATH") if k in os.environ}
        env.update({"PYTHONUNBUFFERED": "1", "D3_E2E_KEY": d3_key})
        adapters = {"ok": d3_ok.server_port, "down": down_port, "reject": d3_reject.server_port}
        adapter_ports: dict[str, int] = {}
        logs = []
        for name, d3_port in adapters.items():
            port = free_port()
            log = temp / f"adapter-{name}.log"
            logs.append(log)
            proc = start(stack, [sys.executable, str(EXAMPLES / "d3_policy.py"), "--port", str(port),
                                 "--url", f"http://127.0.0.1:{d3_port}", "--timeout-ms", "250",
                                 "--api-key-env", "D3_E2E_KEY"], log, env)
            ready(f"http://127.0.0.1:{port}/health", proc, "adapter_" + name)
            adapter_ports[name] = port
        db = temp / "usage.sqlite"
        router_port = free_port()
        providers = {
            "cheapco": {"base_url": f"http://127.0.0.1:{upstream.server_port}/v1", "dialect": "openai-chat",
                        "api_key": provider_key},
            "strongco": {"base_url": f"http://127.0.0.1:{upstream.server_port}/v1", "dialect": "openai-chat",
                         "api_key": provider_key},
        }
        targets = [
            {"provider": "cheapco", "model": CHEAP_MODEL, "tier": "cheap",
             "input_price_per_million_usd": .1, "output_price_per_million_usd": .4},
            {"provider": "strongco", "model": STRONG_MODEL, "tier": "strong",
             "input_price_per_million_usd": 2.0, "output_price_per_million_usd": 8.0},
        ]
        for target in targets:
            provider = providers[target["provider"]]
            provider["models"] = {"catalog": {"model": target.pop("model"), "tool_support": {"openai_chat": ["tools", "tool_choice"]}}}
            target["model_ref"] = "catalog"
        models = {}
        for group, spec in GROUPS.items():
            models[group] = {"strategy": "external", "targets": targets, "external_policy": {
                "url": f"http://127.0.0.1:{adapter_ports[spec['adapter']]}/route", "allow_hosts": ["127.0.0.1"],
                "timeout_ms": 300, "max_response_bytes": 65536, "include_request": spec["include_request"],
                "mode": spec["mode"], "on_error": spec["on_error"]}}
        config = {"server": {
            "listen": f"127.0.0.1:{router_port}", "default_model_group": "d3-enforce",
            "logging": {"path": str(temp / "requests.jsonl")}, "cache": {"enabled": False},
            "decision_telemetry": {"enabled": True}, "diagnostics": {"enabled": True},
            "upstream": {"timeout_ms": 5000}, "identifiers": {"mode": "passthrough"},
            "usage_db": {"enabled": True, "driver": "sqlite", "path": str(db), "migration_policy": "auto-safe"}},
            "state_path": str(temp / "state.json"), "providers": providers, "models": models,
            "callers": [{"id": "d3-e2e-caller", "user": "test-user", "project": "test-project",
                         "environment": "test", "status": "active", "token_sha256": token_digest,
                         "token_id": "rtr_metrum_d3_e2e_test", "allow": list(GROUPS),
                         "rate": {"rpm": 100000, "tpm": 100000000, "concurrent": 8}}]}
        (temp / "router.json").write_text(json.dumps(config), encoding="utf-8")
        (temp / "router.json").chmod(0o600)
        router_log = temp / "router.log"
        logs += [router_log, temp / "requests.jsonl"]
        router = start(stack, [str(binary), "-config", str(temp / "router.json")], router_log, env)
        base = f"http://127.0.0.1:{router_port}"
        ready(base + "/readyz", router, "router")

        def chat(group: str, text: str, stream: bool = False) -> tuple[int, bytes, str]:
            payload: dict[str, Any] = {"model": group, "messages": [{"role": "user", "content": text}],
                                       "max_tokens": 64}
            if stream:
                payload["stream"] = True
            return http(base + "/v1/chat/completions", payload, {"Authorization": "Bearer " + token})

        def record(case: str, passed: bool, **scalars: Any) -> None:
            checks.append({"case": case, "passed": bool(passed), **scalars})
            print(json.dumps(checks[-1]), flush=True)

        status, _, rid = chat("d3-enforce", EASY)
        row = policy_row(db, rid)
        record("D01-enforce-easy-cheap", status == 200 and served_model(db, rid) == CHEAP_MODEL
               and row["outcome"] == "selected" and str(row["class_label"]).startswith("d3:trivial:"),
               http_status=status, class_label=row["class_label"], policy_outcome=row["outcome"])

        status, _, rid = chat("d3-enforce", HARD)
        row = policy_row(db, rid)
        record("D02-enforce-hard-strong", status == 200 and served_model(db, rid) == STRONG_MODEL
               and row["outcome"] == "selected" and str(row["class_label"]).startswith("d3:hard:"),
               http_status=status, class_label=row["class_label"], duration_ms=row["duration_ms"])

        status, _, rid = chat("d3-shadow", HARD)
        row = policy_row(db, rid)
        rec = rows(db, "SELECT c.model FROM request_routing_signals s JOIN request_target_candidates c "
                       "ON c.request_id = s.request_id AND c.candidate_index = s.candidate_index "
                       "WHERE s.request_id = ? AND s.signal_name = 'shadow_recommended_candidate'", (rid,))
        record("D03-shadow-serves-target0-records-d3", status == 200 and served_model(db, rid) == CHEAP_MODEL
               and row["outcome"] == "shadow_recommended" and row["selected_candidate_index"] == 1
               and len(rec) == 1 and rec[0]["model"] == STRONG_MODEL,
               http_status=status, policy_outcome=row["outcome"], class_label=row["class_label"])

        status, _, rid = chat("d3-shadow", EASY)
        row = policy_row(db, rid)
        signals = rows(db, "SELECT candidate_index FROM request_routing_signals WHERE request_id = ? "
                       "AND signal_name = 'shadow_recommended_candidate'", (rid,))
        record("D03b-shadow-model-ref-target0", status == 200 and signals[0]["candidate_index"] == 0
               and row["selected_candidate_index"] == 0, signals=signals, selected=row["selected_candidate_index"], label=row["class_label"])

        for case, group in (("D04-d3-down-fail-closed", "d3-down-closed"), ("D06-d3-422-fail-closed", "d3-422-closed")):
            before = len(upstream.calls)
            status, body, rid = chat(group, HARD)
            row = policy_row(db, rid)
            record(case, status == 502 and b"routing-policy-error" in body and len(upstream.calls) == before
                   and row["outcome"] != "selected", http_status=status, policy_outcome=row["outcome"])

        status, _, rid = chat("d3-down-fallback", HARD)
        row = policy_row(db, rid)
        record("D05-d3-down-fallback-target0", status == 200 and served_model(db, rid) == CHEAP_MODEL
               and row["outcome"] == "fallback", http_status=status, policy_outcome=row["outcome"])

        before_calls = d3_ok.calls
        status, _, rid = chat("d3-no-content", HARD)
        row = policy_row(db, rid)
        record("D07-no-request-content", status == 200 and row["class_label"] == "d3:no-request-content"
               and d3_ok.calls == before_calls and served_model(db, rid) == CHEAP_MODEL,
               http_status=status, class_label=row["class_label"])

        status, body, rid = chat("d3-enforce", HARD, stream=True)
        row = policy_row(db, rid)
        record("D08-stream-hard-strong", status == 200 and b"[DONE]" in body
               and served_model(db, rid) == STRONG_MODEL and str(row["class_label"]).startswith("d3:hard:"),
               http_status=status, class_label=row["class_label"])

        messages = [{"role": "user", "content": EASY}]
        tool_payload = {"model": "d3-shadow", "messages": messages, "tools": [{"type": "function", "function": {
            "name": "create_file", "parameters": {"type": "object", "properties": {"content": {"type": "string"}}}}}]}
        status, body, _ = http(base + "/v1/chat/completions", tool_payload, {"Authorization": "Bearer " + token})
        if status != 200:
            raise HarnessError("tool_request_status_" + str(status) + "_" + str(json.loads(body).get("error", {})))
        message = json.loads(body)["choices"][0]["message"]
        call = message["tool_calls"][0]
        smoke = temp / "tool-created.txt"
        smoke.write_text(json.loads(call["function"]["arguments"])["content"])
        messages.extend([message, {"role": "tool", "tool_call_id": call["id"], "content": "created"}])
        status2, body2, _ = http(base + "/v1/chat/completions", tool_payload, {"Authorization": "Bearer " + token})
        record("D10-tool-client-continuation", status == 200 and status2 == 200 and smoke.read_text() == "smoke"
               and json.loads(body2)["choices"][0]["message"]["content"] == "ok")

        durations = [float(r["duration_ms"]) for r in rows(
            db, "SELECT duration_ms FROM request_policy_executions WHERE outcome IN ('selected','shadow_recommended')")]
        evidence["policy_duration_ms_max"] = max(durations) if durations else None
        evidence["fake_d3_calls"] = d3_ok.calls + d3_reject.calls
        # Stop the router to flush its logs and close SQLite. Adapter logs are unbuffered.
        stop(router)
        forbidden = [token, d3_key, provider_key, token_digest, *MARKERS]
        ok = privacy_ok(db, forbidden, logs)
        ok = ok and not any(s in json.dumps(checks) for s in forbidden)
        record("D09-privacy-no-content-or-credentials", ok)
    return evidence


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--router-binary", type=Path, help="router binary; built from this tree if omitted")
    ap.add_argument("--output", type=Path, help="optional scalar JSON report")
    args = ap.parse_args()
    checks: list[dict[str, Any]] = []
    report: dict[str, Any] = {"schema": "metrum.d3-policy.e2e.v1", "evidence_scope": "fake-d3-local-binary-http",
                              "checks": checks}
    try:
        report.update(execute(args, checks))
    except HarnessError as exc:
        report["error_class"] = str(exc)
    except Exception as exc:  # noqa: BLE001 - emit a fixed class, never protected input
        report["error_class"] = "harness_internal_error:" + type(exc).__name__
    passed = len(checks) == 11 and all(c["passed"] for c in checks) and "error_class" not in report
    report["result"] = "passed" if passed else "failed"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, sort_keys=True), flush=True)
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
