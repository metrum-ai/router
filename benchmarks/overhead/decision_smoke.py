#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Loopback-only paired load-driver smoke with the real d3 adapter and fake d3."""
import hashlib
import importlib.util
import json
import os
import pathlib
import signal
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from http.server import ThreadingHTTPServer
from urllib.request import urlopen

from test_load import Upstream

ROOT = pathlib.Path(__file__).resolve().parents[2]
TOKEN = "synthetic-decision-smoke-token"
D3_KEY = "synthetic-decision-model-token"


def free_port():
    import socket
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def stop(proc):
    if proc.poll() is None:
        proc.send_signal(signal.SIGTERM)
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=5)


def wait_ready(url, proc):
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            raise RuntimeError("process exited before readiness")
        try:
            if urlopen(url, timeout=1).status == 200:
                return
        except OSError:
            time.sleep(.05)
    raise RuntimeError("process not ready")


def main():
    spec = importlib.util.spec_from_file_location("overhead_fake_d3", ROOT / "examples/external-routing-policy/fake_d3.py")
    fake = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(fake)
    d3 = fake.start_in_thread(api_key=D3_KEY)
    backend = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    backend.daemon_threads = True
    backend.lock = threading.Lock()
    backend.seen = []
    backend.bad_usage = False
    backend.mismatch_prompt = False
    thread = threading.Thread(target=backend.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="overhead-decision-smoke-") as temp:
            root = pathlib.Path(temp)
            binary = root / "router"
            env = os.environ.copy()
            env.update(OVERHEAD_SMOKE_TOKEN=TOKEN, OVERHEAD_D3_KEY=D3_KEY)
            subprocess.run(["go", "build", "-tags", "dev_no_license", "-o", str(binary), "./cmd/metrum-ai-router"], cwd=ROOT, env=env, check=True, timeout=300)
            adapter_port = free_port()
            with (root / "adapter.log").open("w", encoding="utf-8") as adapter_log:
                adapter = subprocess.Popen([sys.executable, str(ROOT / "examples/external-routing-policy/d3_policy.py"),
                                            "--port", str(adapter_port), "--url", f"http://127.0.0.1:{d3.server_port}",
                                            "--timeout-ms", "3000", "--api-key-env", "OVERHEAD_D3_KEY"],
                                           cwd=root, env=env, stdout=adapter_log, stderr=adapter_log)
                try:
                    wait_ready(f"http://127.0.0.1:{adapter_port}/health", adapter)
                    for mode in ("plain", "shadow", "enforce"):
                        work = root / mode
                        work.mkdir()
                        port = free_port()
                        target = {"provider": "mock", "model": "chat", "tier": "cheap"}
                        group = {"strategy": "static", "targets": [target]}
                        if mode != "plain":
                            group = {"strategy": "external", "targets": [target], "external_policy": {
                                "url": f"http://127.0.0.1:{adapter_port}/route", "allow_hosts": ["127.0.0.1"],
                                "timeout_ms": 5000, "max_response_bytes": 65536,
                                "include_request": True, "mode": mode, "on_error": "fail_closed"}}
                        config = {"server": {"listen": f"127.0.0.1:{port}", "default_model_group": "chat",
                                             "cache": {"enabled": False}, "logging": {"path": str(work / "requests.jsonl")},
                                             "identifiers": {"mode": "passthrough"}, "decision_telemetry": {"enabled": True},
                                             "usage_db": {"enabled": True, "driver": "sqlite", "path": str(work / "usage.sqlite"), "migration_policy": "auto-safe"}},
                                  "state_path": str(work / "state.json"),
                                  "providers": {"mock": {"base_url": f"http://127.0.0.1:{backend.server_port}/v1", "dialect": "openai-chat"}},
                                  "models": {"chat": group},
                                  "callers": [{"id": "decision-smoke", "token_sha256": hashlib.sha256(TOKEN.encode()).hexdigest(),
                                               "allow": ["chat"], "rate": {"rpm": 2400, "tpm": 100000000, "concurrent": 160}}]}
                        (work / "config.json").write_text(json.dumps(config), encoding="utf-8")
                        (work / "config.json").chmod(0o600)
                        (work / "prompt.txt").write_text("synthetic decision smoke prompt", encoding="utf-8")
                        with (work / "router.log").open("w", encoding="utf-8") as router_log:
                            router = subprocess.Popen([str(binary), "-config", str(work / "config.json")], cwd=work, env=env,
                                                      stdout=router_log, stderr=router_log)
                            try:
                                wait_ready(f"http://127.0.0.1:{port}/readyz", router)
                                with d3.lock:
                                    before = d3.calls
                                with backend.lock:
                                    upstream_before = len(backend.seen)
                                cmd = [sys.executable, str(ROOT / "benchmarks/overhead/load.py"),
                                       "--direct-url", f"http://127.0.0.1:{backend.server_port}/v1/chat/completions",
                                       "--router-url", f"http://127.0.0.1:{port}/v1/chat/completions",
                                       "--router-token-env", "OVERHEAD_SMOKE_TOKEN", "--model", "chat",
                                       "--prompt-file", str(work / "prompt.txt"), "--output-dir", str(work / "results"),
                                       "--output-tokens", "3", "--concurrency", "2", "--requests-per-worker", "2",
                                       "--repetitions", "3", "--timeout", "10"]
                                subprocess.run(cmd, cwd=ROOT, env=env, check=True, timeout=90)
                                expected = 0 if mode == "plain" else 36
                                with backend.lock:
                                    upstream = backend.seen[upstream_before:]
                                if len(upstream) != 72:
                                    raise RuntimeError(f"{mode} upstream calls={len(upstream)}, expected 72")
                                for _, _, raw in upstream:
                                    payload = json.loads(raw)
                                    messages = payload.get("messages", [])
                                    if (payload.get("model") != "chat" or payload.get("max_tokens") != 3 or
                                            messages != [{"role": "user", "content": "synthetic decision smoke prompt"}]):
                                        raise RuntimeError(f"{mode} upstream workload mismatch")
                                # SQLite usage writes are asynchronous; wait for all policy rows.
                                deadline = time.monotonic() + 10
                                outcomes = []
                                while mode != "plain" and time.monotonic() < deadline:
                                    with sqlite3.connect(work / "usage.sqlite") as conn:
                                        outcomes = [row[0] for row in conn.execute("SELECT outcome FROM request_policy_executions")]
                                    if len(outcomes) >= expected:
                                        break
                                    time.sleep(.05)
                                with d3.lock:
                                    calls = d3.calls - before
                                if calls != expected:
                                    raise RuntimeError(f"{mode} fake-d3 call count {calls}, expected {expected}")
                                if mode != "plain":
                                    wanted = "shadow_recommended" if mode == "shadow" else "selected"
                                    if len(outcomes) != expected or any(value != wanted for value in outcomes):
                                        raise RuntimeError(f"{mode} policy telemetry mismatch")
                                print(f"PASS: {mode} routed driver and fake d3 calls={calls}; not a performance result")
                            finally:
                                stop(router)
                finally:
                    stop(adapter)
    finally:
        backend.shutdown()
        backend.server_close()
        thread.join(timeout=5)
        d3.shutdown()
        d3.server_close()


if __name__ == "__main__":
    main()
