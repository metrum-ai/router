#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Exercise the load driver through a local router and scripted backend only."""
import hashlib
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
TOKEN = "synthetic-overhead-test-token"


def free_port():
    import socket
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    backend = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    backend.daemon_threads = True
    backend.lock = threading.Lock()
    backend.seen = []
    backend.bad_usage = False
    backend.mismatch_prompt = False
    worker = threading.Thread(target=backend.serve_forever, daemon=True)
    worker.start()
    try:
        with tempfile.TemporaryDirectory(prefix="overhead-load-smoke-") as temp:
            work = pathlib.Path(temp)
            binary = work / "router"
            env = os.environ.copy()
            env["OVERHEAD_SMOKE_TOKEN"] = TOKEN
            subprocess.run(["go", "build", "-tags", "dev_no_license", "-o", str(binary), "./cmd/metrum-ai-router"], cwd=ROOT, env=env, check=True, timeout=300)
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
  - id: overhead-smoke
    token_sha256: "{hashlib.sha256(TOKEN.encode()).hexdigest()}"
    allow: [chat]
    rate: {{rpm: 2400, tpm: 100000000, concurrent: 160}}
    quota: {{day: {{requests: 100000, tokens: 400000000}}, month: {{tokens: 8000000000}}}}
    key: {{lifetime_tokens: 40000000000}}
''', encoding="utf-8")
            (work / "prompt.txt").write_text("synthetic prompt for load driver", encoding="utf-8")
            with (work / "router.stderr").open("w", encoding="utf-8") as log:
                proc = subprocess.Popen([str(binary), "-config", str(work / "config.yaml")], cwd=work, env=env, stdout=log, stderr=log)
                try:
                    deadline = time.monotonic() + 60
                    while time.monotonic() < deadline:
                        if proc.poll() is not None:
                            raise RuntimeError("router exited before readiness")
                        try:
                            if urlopen(f"http://127.0.0.1:{port}/readyz", timeout=1).status == 200:
                                break
                        except OSError:
                            time.sleep(.05)
                    else:
                        raise RuntimeError("router did not become ready")
                    cmd = [sys.executable, str(ROOT / "benchmarks/overhead/load.py"),
                           "--direct-url", f"http://127.0.0.1:{backend.server_port}/v1/chat/completions",
                           "--router-url", f"http://127.0.0.1:{port}/v1/chat/completions",
                           "--router-token-env", "OVERHEAD_SMOKE_TOKEN", "--model", "chat",
                           "--prompt-file", str(work / "prompt.txt"), "--output-dir", str(work / "results"),
                           "--output-tokens", "3", "--concurrency", "2", "--repetitions", "3",
                           "--requests-per-worker", "2", "--timeout", "10"]
                    subprocess.run(cmd, cwd=ROOT, env=env, check=True, timeout=90)
                    print("PASS: paired load driver through localhost router and one synthetic upstream; not a performance result")
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
