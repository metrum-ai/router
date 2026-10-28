#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Local mock-only tool continuation and pprof wrapper smoke; not a benchmark."""
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import time
from http.server import ThreadingHTTPServer
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'tests/agent_compat'))
from run_chat_smoke import DIGEST, TOKEN, Mock, free_port, runtime_environment  # noqa: E402


def main():
    client_python = ROOT / 'tests/agent_compat/.venv/bin/python'
    if not client_python.is_file():
        raise SystemExit('Run uv sync --locked --directory tests/agent_compat first')
    Mock.calls.clear()
    Mock.errors.clear()
    Mock.parallel = False
    upstream = ThreadingHTTPServer(('127.0.0.1', 0), Mock)
    thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix='router-overhead-smoke-') as temp:
            work = Path(temp)
            env = runtime_environment()
            binary = work / 'profiled-router'
            subprocess.run(['go', 'build', '-tags', 'dev_no_license', '-o', str(binary), './benchmarks/overhead/profiled-router'], cwd=ROOT, env=env, check=True)
            port = free_port()
            config = f'''server:
  listen: "127.0.0.1:{port}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work / 'router.jsonl'}"}}
state_path: "{work / 'state.json'}"
providers:
  mock: {{base_url: "http://127.0.0.1:{upstream.server_port}/v1", dialect: openai-chat}}
models:
  chat: {{strategy: static, targets: [{{provider: mock, model: synthetic-chat, tool_support: {{openai_chat: [tools, tool_choice]}}}}]}}
callers:
  - id: agent-compat
    token_sha256: "{DIGEST}"
    allow: [chat]
    rate: {{rpm: 2400, tpm: 100000000, concurrent: 160}}
    quota: {{day: {{requests: 100000, tokens: 400000000}}, month: {{tokens: 8000000000}}}}
    key: {{lifetime_tokens: 40000000000}}
'''
            (work / 'config.yaml').write_text(config, encoding='utf-8')
            stderr = (work / 'router.stderr').open('w', encoding='utf-8')
            process = subprocess.Popen([str(binary), '-config', str(work / 'config.yaml'), '-control-dir', str(work / 'control'), '-profile-dir', str(work / 'profiles')], cwd=work, env=env, stdout=stderr, stderr=stderr)
            try:
                deadline = time.monotonic() + 60
                while time.monotonic() < deadline:
                    if process.poll() is not None:
                        raise RuntimeError('profiled router exited before ready')
                    try:
                        if urlopen(f'http://127.0.0.1:{port}/readyz', timeout=1).status == 200:
                            break
                    except OSError:
                        time.sleep(0.05)
                else:
                    raise RuntimeError('profiled router not ready')
                control = [sys.executable, str(ROOT / 'benchmarks/overhead/profiled-router/control.py'), '--socket', str(work / 'control/control.sock')]
                subprocess.run(control + ['start'], env=env, check=True, timeout=30)
                child_env = env | {'HOME': str(work), 'ROUTER_BASE_URL': f'http://127.0.0.1:{port}/v1', 'ROUTER_CALLER_TOKEN': TOKEN}
                child = subprocess.run([str(client_python), str(ROOT / 'tests/agent_compat/sdk_client.py'), 'openai-agents'], cwd=work, env=child_env, capture_output=True, text=True, timeout=90)
                if child.returncode or 'agent tool loop complete' not in child.stdout:
                    raise RuntimeError(f'local mock agent failed: exit={child.returncode}; stderr={child.stderr[-1500:]}')
                if (work / 'result.txt').read_text(encoding='utf-8') != 'tool-result-42\n' or Mock.errors or len(Mock.calls) != 2:
                    raise RuntimeError(f'local continuation mismatch: errors={Mock.errors}, calls={len(Mock.calls)}')
                subprocess.run(control + ['stop'], env=env, check=True, timeout=30)
                for name in ('cpu.pprof', 'heap.pprof'):
                    subprocess.run(['go', 'tool', 'pprof', '-top', str(work / 'profiles' / name)], cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
                print('PASS: localhost profiled router, synthetic SDK streamed tool continuation, parseable CPU and heap profiles; no benchmark or hosted call')
            finally:
                if process.poll() is None:
                    process.send_signal(signal.SIGTERM)
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
                stderr.close()
    finally:
        upstream.shutdown()
        upstream.server_close()
        thread.join(timeout=5)


if __name__ == '__main__':
    main()
