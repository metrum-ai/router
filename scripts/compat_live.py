#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Run the pinned core live compatibility subset with a guarded local config.

This deliberately does not exercise the full catalog: provider-hosted tools and
remote-object creation need separate authorization. Results are evidence, not
a claim that omitted suites pass. Provider keys and the router caller token
are read only from the environment and never printed.
"""
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.request

from compat_mock import ROOT, TESTERS, build_tester


def fail(message):
    raise RuntimeError(message)


def sha256(path):
    h = hashlib.sha256()
    with open(path, 'rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def meter(path):
    with sqlite3.connect(path, timeout=10) as db:
        return db.execute('select coalesce(sum(total_cost_usd),0) from request_usage').fetchone()[0]


def suites(kind):
    selected = TESTERS[kind][1].split(',')
    # This provider-hosted tool may allocate a sandbox and incur non-token
    # charges. Omission is explicit; it is not an unsupported-endpoint pass.
    return [s for s in selected if s != 'messages_code_execution']


def local_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def wait_ready(proc, port):
    for _ in range(200):
        if proc.poll() is not None:
            fail('router exited before readiness; inspect the private server log')
        try:
            with urllib.request.urlopen(f'http://127.0.0.1:{port}/readyz', timeout=1) as response:
                if response.status == 200:
                    return
        except (OSError, TimeoutError):
            pass
        time.sleep(0.1)
    fail('router readiness timed out')


def stop(proc):
    if proc is None or proc.poll() is not None:
        return
    proc.send_signal(signal.SIGTERM)
    try:
        proc.wait(timeout=30)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()


def main():
    config = Path(os.environ.get('COMPAT_LIVE_CONFIG', '')).expanduser()
    output_name = os.environ.get('COMPAT_LIVE_OUT', '')
    if not config.is_file() or not output_name:
        fail('set COMPAT_LIVE_CONFIG to a config file and COMPAT_LIVE_OUT to a new output directory')
    for key in ('OPENAI_API_KEY', 'ANTHROPIC_API_KEY', 'ROUTER_CALLER_TOKEN'):
        if not os.environ.get(key):
            fail(f'{key} must be set in the environment')
    config = config.resolve()
    output = Path(output_name).expanduser().resolve()
    if output == ROOT or ROOT in output.parents:
        fail('output directory must be outside the repository')
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    config_hash = sha256(config)
    guard = subprocess.run(['go', 'run', './scripts/compat-live-guard', str(config)], cwd=ROOT, check=True, capture_output=True, text=True)
    db = json.loads(guard.stdout)['usage_db']
    testers = {kind: build_tester(kind, TESTERS[kind][0])[0] for kind in TESTERS}
    hashes = {kind: sha256(path) for kind, path in testers.items()}
    git_head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    dirty = bool(subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=all'], cwd=ROOT, text=True).strip())
    metadata = {'router_commit': git_head, 'git_dirty': dirty, 'config_sha256': config_hash, 'tester_commits': {k: v[0] for k, v in TESTERS.items()}, 'tester_sha256': hashes, 'suites': {k: suites(k) for k in TESTERS}, 'scope': 'core subset; not the complete tester catalog'}
    (output / 'metadata.json').write_text(json.dumps(metadata, indent=2) + '\n')
    with tempfile.TemporaryDirectory(prefix='compat-live-') as temp:
        binary = Path(temp) / 'router'
        subprocess.run(['go', 'build', '-buildvcs=false', '-o', str(binary), './cmd/metrum-ai-router'], cwd=ROOT, check=True)
        if sha256(config) != config_hash:
            fail('config changed during preparation; refusing to run')
        port = local_port()
        proc = None
        failed = False
        try:
            with (output / 'server.log').open('w') as log:
                proc = subprocess.Popen([str(binary), '--config', str(config), '--listen', f'127.0.0.1:{port}'], cwd=ROOT, stdout=log, stderr=subprocess.STDOUT)
                wait_ready(proc, port)
                for kind, group in (('openai', 'gpt'), ('openai', 'claude'), ('anthropic', 'gpt'), ('anthropic', 'claude')):
                    env = os.environ.copy()
                    env.pop('OPENAI_ADMIN_API_KEY', None)
                    token = env.pop('ROUTER_CALLER_TOKEN')
                    env.update({'TEST_SUITES': ','.join(suites(kind)), 'REQUEST_TIMEOUT': '90s'})
                    if kind == 'openai':
                        env.update({'OPENAI_BASE_URL': f'http://127.0.0.1:{port}/v1', 'OPENAI_API_KEY': token, 'OPENAI_MODEL': group, 'OPENAI_RESPONSES_MODEL': group, 'OPENAI_VISION_MODEL': group, 'OPENAI_REASONING_MODEL': group, 'OPENAI_EMBEDDING_MODEL': group, 'OPENAI_COMPLETION_MODEL': group})
                    else:
                        env.update({'ANTHROPIC_BASE_URL': f'http://127.0.0.1:{port}', 'ANTHROPIC_API_KEY': token, 'ANTHROPIC_MODEL': group})
                    label = f'{kind}-{group}'
                    with (output / f'{label}.txt').open('w') as out:
                        code = subprocess.run([str(testers[kind])], env=env, stdout=out, stderr=subprocess.STDOUT).returncode
                    print(f'{label}: tester exit {code}', flush=True)
                    if code not in (0, 1):
                        fail('tester runner error; inspect its private log')
                    failed = failed or code == 1
                    measured = meter(db)
                    print(f'cumulative router meter: ${measured:.8f} (list price, not invoice)', flush=True)
                    if measured > 1:
                        fail('metered run exceeded $1; stop and report NEEDS MAC')
        finally:
            stop(proc)
        measured = meter(db)
        print(f'after shutdown and audit drain: ${measured:.8f} (list price, not invoice)', flush=True)
        if measured > 1:
            fail('metered run exceeded $1; stop and report NEEDS MAC')
        if failed:
            print('one or more selected suites failed; inspect the private logs', file=sys.stderr)
            return 1
        return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except RuntimeError as exc:
        # RuntimeError messages above are static, never provider or config data.
        print(f'compat-live stopped: {exc}', file=sys.stderr)
        sys.exit(2)
    except (OSError, subprocess.CalledProcessError, sqlite3.Error, ValueError) as exc:
        # Do not print subprocess output, config contents, or environment values.
        print(f'compat-live stopped: {type(exc).__name__}', file=sys.stderr)
        sys.exit(2)
