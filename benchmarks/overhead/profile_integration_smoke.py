#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Synthetic localhost profile-phase integration; not a performance result."""
import hashlib
import json
import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import threading
import time
from http.server import ThreadingHTTPServer
from urllib.request import urlopen

from test_load import Upstream

ROOT = pathlib.Path(__file__).resolve().parents[2]
TOKEN = "synthetic-profile-smoke-token"


class SlowUpstream(Upstream):
    def do_POST(self):
        time.sleep(self.server.delay)
        super().do_POST()


class Server(ThreadingHTTPServer):
    request_queue_size = 512
    daemon_threads = True


def free_port():
    import socket
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    backend = Server(("127.0.0.1", 0), SlowUpstream)
    backend.lock = threading.Lock()
    backend.seen = []
    backend.bad_usage = False
    backend.mismatch_prompt = False
    worker = threading.Thread(target=backend.serve_forever, daemon=True)
    worker.start()
    try:
        with tempfile.TemporaryDirectory(prefix="profile-phase-smoke-") as temp:
            root = pathlib.Path(temp)
            binary = root / "profiled-router"
            env = os.environ.copy()
            env["PROFILE_SMOKE_TOKEN"] = TOKEN
            subprocess.run(["go", "build", "-tags", "dev_no_license", "-o", str(binary), "./benchmarks/overhead/profiled-router"], cwd=ROOT, env=env, check=True, timeout=300)
            for concurrency, streaming, valid, percent in ((32, True, True, 100), (128, False, True, 100),
                                                             (32, False, True, 90), (128, False, True, 90),
                                                             (32, False, False, 100)):
                backend.delay = 2.5 if valid else .001
                work = root / f"case-{concurrency}-{valid}-{percent}"
                work.mkdir()
                port = free_port()
                (work / "config.yaml").write_text(f'''server:
  listen: "127.0.0.1:{port}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  mock: {{base_url: "http://127.0.0.1:{backend.server_port}/v1", dialect: openai-chat}}
models:
  chat: {{strategy: static, targets: [{{provider: mock, model: chat}}]}}
callers:
  - id: profile-smoke
    token_sha256: "{hashlib.sha256(TOKEN.encode()).hexdigest()}"
    allow: [chat]
    rate: {{rpm: 2400, tpm: 100000000, concurrent: 160}}
    quota: {{day: {{requests: 100000, tokens: 400000000}}, month: {{tokens: 8000000000}}}}
    key: {{lifetime_tokens: 40000000000}}
''', encoding="utf-8")
                (work / "prompt.txt").write_text("synthetic prompt for profile smoke", encoding="utf-8")
                with (work / "router.log").open("w", encoding="utf-8") as log:
                    proc = subprocess.Popen([str(binary), "-config", str(work / "config.yaml"),
                                             "-control-dir", str(work / "control"), "-profile-dir", str(work / "profiles")],
                                            cwd=work, env=env, stdout=log, stderr=log)
                    try:
                        deadline = time.monotonic() + 60
                        while time.monotonic() < deadline:
                            if proc.poll() is not None:
                                raise RuntimeError("profiled router exited before readiness")
                            try:
                                if urlopen(f"http://127.0.0.1:{port}/readyz", timeout=1).status == 200:
                                    break
                            except OSError:
                                time.sleep(.05)
                        else:
                            raise RuntimeError("profiled router not ready")
                        cmd = [sys.executable, str(ROOT / "benchmarks/overhead/profile_phase.py"),
                               "--router-url", f"http://127.0.0.1:{port}/v1/chat/completions",
                               "--router-token-env", "PROFILE_SMOKE_TOKEN", "--control-socket", str(work / "control/control.sock"),
                               "--model", "chat", "--path-kind", "plain", "--prompt-file", str(work / "prompt.txt"),
                               "--output-tokens", "3", "--concurrency", str(concurrency), "--requests-per-worker", "1",
                               "--minimum-sampling-seconds", ".25", "--deadline-seconds", "30", "--socket-timeout", "15",
                               "--output-dir", str(work / "result"), "--minimum-active-percent", str(percent)]
                        if streaming:
                            cmd.append("--streaming")
                        result = subprocess.run(cmd, cwd=ROOT, env=env, capture_output=True, text=True, timeout=60)
                        if valid:
                            if result.returncode:
                                raise RuntimeError(f"guarded capture failed at {concurrency}: {result.stderr}")
                            metadata = json.loads((work / "result/profile-phase.json").read_text())
                            if (metadata["concurrency"] != concurrency or metadata["timed_requests"] != concurrency
                                    or metadata["minimum_active_percent"] != percent
                                    or metadata["guard_min_active_during_capture"] != (concurrency * percent + 99) // 100):
                                raise RuntimeError("profile metadata mismatch")
                            for name in ("cpu.pprof", "heap.pprof"):
                                subprocess.run(["go", "tool", "pprof", "-top", str(work / "profiles" / name)], cwd=ROOT,
                                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True, timeout=30)
                            print(f"PASS: synthetic guarded CPU/heap capture at {concurrency} clients with {percent}% floor, not a performance result")
                        else:
                            if result.returncode == 0 or (work / "result/profile-phase.json").exists():
                                raise RuntimeError("too-short load incorrectly accepted a profile")
                            print("PASS: insufficient load produces no success metadata")
                    finally:
                        if proc.poll() is None:
                            proc.send_signal(signal.SIGTERM)
                        try:
                            proc.wait(timeout=10)
                        except subprocess.TimeoutExpired:
                            proc.kill()
                            proc.wait(timeout=5)
    finally:
        backend.shutdown()
        backend.server_close()
        worker.join(timeout=5)


if __name__ == "__main__":
    main()
