#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Exercise the generated local starter against the pinned OpenAI mock SDK."""

import os
from pathlib import Path
import subprocess
import sys
import tempfile

from compat_mock import ROOT, TESTERS, build_tester, port, wait_listening, wait_ready
from compat_results import compare

# models_get and chat_completions_stream are covered by the full compatibility
# gate; they are added by independent PRs #529 and #528 respectively.
SUITES = [
    'models', 'chat_completions', 'chat_completions_stream_usage',
    'chat_completions_json', 'chat_completions_vision', 'chat_completions_tools',
    'chat_completions_tools_stream', 'responses', 'responses_stream',
    'responses_tools', 'responses_tools_stream', 'responses_json',
]


def main():
    processes = []
    with tempfile.TemporaryDirectory(prefix='router-starter-compat-') as directory:
        work = Path(directory)
        try:
            tester, mock = build_tester('openai', TESTERS['openai'][0])
            router, routerctl = work / 'router', work / 'routerctl'
            for binary, package in [(router, './cmd/metrum-ai-router'), (routerctl, './cmd/metrum-ai-routerctl')]:
                command = ['go', 'build']
                if binary == router:
                    command += ['-tags', 'dev_no_license']
                subprocess.run(command + ['-o', str(binary), package], cwd=ROOT, check=True)
            mock_port, router_port = port(), port()
            if mock_port == router_port:
                raise RuntimeError('could not reserve distinct mock and router ports')
            processes.append(subprocess.Popen([str(mock), '-addr', f'127.0.0.1:{mock_port}'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
            wait_listening(mock_port, processes[-1])
            starter = work / 'starter'
            subprocess.run([
                sys.executable, str(ROOT / 'scripts/local_dev_bootstrap.py'),
                '--out-dir', str(starter), '--repo-root', str(ROOT),
                '--smartrouterctl', str(routerctl), '--upstream-base-url',
                f'http://127.0.0.1:{mock_port}/v1',
            ], cwd=ROOT, check=True, stdout=subprocess.DEVNULL)
            env = os.environ.copy()
            env['OPENAI_API_KEY'] = 'starter-loopback-mock'
            processes.append(subprocess.Popen([
                str(router), '--config', str(starter / 'config.yaml'),
                '--listen', f'127.0.0.1:{router_port}',
            ], cwd=work, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
            wait_ready(f'http://127.0.0.1:{router_port}/readyz', processes[-1])
            env.update({
                'OPENAI_BASE_URL': f'http://127.0.0.1:{router_port}/v1',
                'OPENAI_API_KEY': (starter / 'router.token').read_text().strip(),
                'OPENAI_MODEL': 'local', 'OPENAI_RESPONSES_MODEL': 'local',
                'OPENAI_VISION_MODEL': 'local', 'OPENAI_REASONING_MODEL': 'local',
                'OPENAI_COMPLETION_MODEL': 'local', 'OPENAI_EMBEDDING_MODEL': 'local',
                'TEST_SUITES': ','.join(SUITES), 'REQUEST_TIMEOUT': '10s',
                'ALLOW_INSECURE_HTTP': 'true',
            })
            result = subprocess.run([str(tester)], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=300)
            differences = compare(result.stdout, result.returncode, SUITES, {})
            if differences:
                print(result.stdout, file=sys.stderr)
                raise RuntimeError('starter compatibility differences: ' + ', '.join(differences))
            print(f'starter compatibility: {len(SUITES)}/{len(SUITES)} core suites passed')
        finally:
            for process in reversed(processes):
                if process.poll() is None:
                    process.terminate()
            for process in reversed(processes):
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == '__main__':
    try:
        main()
    except (OSError, RuntimeError, ValueError, subprocess.SubprocessError) as exc:
        print(f'starter compatibility failed: {exc}', file=sys.stderr)
        sys.exit(1)
