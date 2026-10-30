#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Build pinned testers, launch loopback mocks and compare mock-tier results."""
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import tempfile
import time
from urllib.request import urlopen

from compat_results import compare

ROOT = Path(__file__).resolve().parents[1]
CACHE = Path(os.environ.get('COMPAT_CACHE', Path.home() / '.cache/router-compat'))
TESTERS = {
    'openai': ('5935805af52c01059e5a166b01cf1c28077103f5', 'models,models_get,chat_completions,chat_completions_stream,chat_completions_stream_usage,chat_completions_logprobs,chat_completions_json,chat_completions_vision,chat_completions_reasoning,chat_completions_tools,chat_completions_tools_stream,chat_completions_multi_turn,completions,completions_stream,embeddings,embeddings_batch,responses,responses_stream,responses_tools,responses_tools_stream,responses_json,responses_get,responses_delete,responses_input_items,responses_input_tokens,responses_compact,responses_vision,responses_reasoning,responses_multi_turn,moderations,error_responses'),
    'anthropic': ('799b0bca29ee8a95913b096ecbb5c0e0960068d2', 'models,models_get,messages,messages_stream,messages_tools,messages_tools_stream,messages_json,messages_multi_turn,messages_count_tokens,messages_vision,completions,completions_stream,message_batches_create,message_batches_get,message_batches_cancel,message_batches_list,message_batches_results,message_batches_delete,messages_thinking,messages_prompt_cache,messages_document,beta_files,error_responses,beta_models,beta_models_get,beta_messages,beta_messages_stream,messages_code_execution'),
}


def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def build_tester(kind, commit):
    target = CACHE / f'{kind}-{commit}'
    tester, mock = target / 'tester', target / 'mock'
    if tester.is_file() and mock.is_file():
        return tester, mock
    CACHE.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=CACHE) as temp:
        temp = Path(temp)
        repo = temp / 'src'
        subprocess.run(['git', 'clone', '--quiet', '--no-checkout', f'https://github.com/beranekio/{kind}-compatibility-tester.git', str(repo)], check=True)
        subprocess.run(['git', '-C', str(repo), 'checkout', '--quiet', '--detach', commit], check=True)
        actual = subprocess.check_output(['git', '-C', str(repo), 'rev-parse', 'HEAD'], text=True).strip()
        if actual != commit:
            raise RuntimeError('tester checkout mismatch')
        out = temp / 'bin'
        out.mkdir()
        for name, path in [('tester', f'./cmd/{kind}-compatibility-tester'), ('mock', './cmd/mockserver')]:
            subprocess.run(['go', 'build', '-o', str(out / name), path], cwd=repo, check=True)
        try:
            out.rename(target)
        except OSError:
            if not (tester.is_file() and mock.is_file()):
                raise
    return tester, mock


def wait_listening(port_number, process):
    for _ in range(200):
        if process.poll() is not None:
            raise RuntimeError(f'mock exited ({process.returncode}) before readiness')
        try:
            with socket.create_connection(('127.0.0.1', port_number), timeout=1):
                return
        except OSError:
            time.sleep(.1)
    raise RuntimeError(f'timeout waiting for mock on port {port_number}')


def wait_ready(url, process):
    for _ in range(200):
        if process.poll() is not None:
            raise RuntimeError(f'process exited ({process.returncode}) before readiness')
        try:
            with urlopen(url, timeout=1) as response:
                if response.status == 200:
                    return
        except OSError:
            time.sleep(.1)
    raise RuntimeError(f'timeout waiting for {url}')


def report_differences(kind, differences):
    if differences:
        print(f'{kind} compatibility differences:', flush=True)
        for difference in differences:
            print(f'  {difference}', flush=True)
    else:
        print(f'{kind} compatibility: all suites match expected failures', flush=True)


def main():
    record = sys.argv[1:] == ['--record-main']
    if sys.argv[1:] and not record:
        raise ValueError('usage: compat_mock.py [--record-main]')
    processes = []
    with tempfile.TemporaryDirectory(prefix='router-compat-') as work:
        work = Path(work)
        try:
            binaries = {k: build_tester(k, v[0]) for k, v in TESTERS.items()}
            router = work / 'router'
            subprocess.run(['go', 'build', '-tags', 'dev_no_license', '-o', str(router), './cmd/metrum-ai-router'], cwd=ROOT, check=True)
            ports = {k: port() for k in ('openai', 'anthropic', 'router')}
            if len(set(ports.values())) != 3:
                raise RuntimeError('could not reserve unique ports')
            for kind in TESTERS:
                processes.append(subprocess.Popen([str(binaries[kind][1]), '-addr', f'127.0.0.1:{ports[kind]}'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
                wait_listening(ports[kind], processes[-1])
            token = 'local-compat-token'
            digest = hashlib.sha256(token.encode()).hexdigest()
            config = f'''server:
  listen: "127.0.0.1:{ports['router']}"
  identifiers: {{mode: passthrough}}
  default_model_group: chat
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work}/requests.jsonl"}}
state_path: "{work}/state.json"
providers:
  chat: {{base_url: "http://127.0.0.1:{ports['openai']}/v1", dialect: openai-chat}}
  responses: {{base_url: "http://127.0.0.1:{ports['openai']}/v1", dialect: openai-responses}}
  messages: {{base_url: "http://127.0.0.1:{ports['anthropic']}", dialect: anthropic}}
models:
  chat: {{strategy: static, targets: [{{provider: chat, model: synthetic-chat, structured_output: true, input_modalities: [text, image], tool_support: {{openai_chat: [tools, tool_choice]}}}}]}}
  responses: {{strategy: static, targets: [{{provider: responses, model: synthetic-responses, structured_output: true, input_modalities: [text, image], tool_support: {{openai_responses: [function, tool_choice]}}}}]}}
  full: {{strategy: static, targets: [{{provider: chat, model: synthetic-chat, structured_output: true, input_modalities: [text, image], reasoning: {{supported: true, mode: opt_in, control: effort_enum, supports_summaries: true}}, tool_support: {{openai_chat: [tools, tool_choice]}}}}, {{provider: responses, model: synthetic-responses, structured_output: true, input_modalities: [text, image], reasoning: {{supported: true, mode: opt_in, control: effort_enum, supports_summaries: true}}, tool_support: {{openai_responses: [function, tool_choice]}}}}]}}
  messages: {{strategy: static, targets: [{{provider: messages, model: synthetic-messages, structured_output: true, input_modalities: [text, image], reasoning: {{supported: true, mode: opt_in, control: token_budget, min_budget_tokens: 1024, max_budget_tokens: 4096, budget_must_be_less_than_max_tokens: true}}, tool_support: {{anthropic_messages: [client_tools]}}, request_shape_support: {{supported_inbound_dialects: [anthropic], validation_status: passed}}}}]}}
callers:
  - id: synthetic
    token_sha256: {digest}
    allow: [chat, responses, full, messages]
    rate: {{rpm: 2400, tpm: 100000000, concurrent: 160}}
    quota: {{day: {{requests: 100000, tokens: 400000000}}, month: {{tokens: 8000000000}}}}
    key: {{lifetime_tokens: 40000000000}}
'''
            (work / 'config.yaml').write_text(config)
            log = open(work / 'router.log', 'w+', encoding='utf-8')
            processes.append(subprocess.Popen([str(router), '-config', str(work / 'config.yaml')], cwd=work, stdout=log, stderr=log))
            try:
                wait_ready(f"http://127.0.0.1:{ports['router']}/readyz", processes[-1])
            except Exception:
                log.flush(); log.seek(0); print(log.read(), file=sys.stderr)
                raise
            errors = []
            for kind, (_, suite_string) in TESTERS.items():
                base = f"http://127.0.0.1:{ports['router']}"
                env = os.environ | {'TEST_SUITES': suite_string, 'REQUEST_TIMEOUT': '10s', 'OPENAI_BASE_URL': base + '/v1', 'OPENAI_API_KEY': token, 'OPENAI_MODEL': 'chat', 'OPENAI_RESPONSES_MODEL': 'responses', 'OPENAI_COMPLETION_MODEL': 'chat', 'OPENAI_EMBEDDING_MODEL': 'chat', 'OPENAI_VISION_MODEL': 'full', 'OPENAI_REASONING_MODEL': 'full', 'ANTHROPIC_COMPLETION_MODEL': 'messages', 'ANTHROPIC_VISION_MODEL': 'messages', 'ANTHROPIC_BASE_URL': base, 'ANTHROPIC_API_KEY': token, 'ANTHROPIC_MODEL': 'messages'}
                result = subprocess.run([str(binaries[kind][0])], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=300)
                print(f'## {kind}\n{result.stdout}', flush=True)
                path = ROOT / 'tests' / 'compat_mock' / f'{kind}-expected.json'
                if record:
                    # Record is a manual baseline-generation operation, never a CI mode.
                    failures = re.findall(r'^  \[FAIL\] ([a-z][a-z0-9_]*)', result.stdout, re.M)
                    expected = {name: {'reason': 'Observed failure on main; investigate compatibility gap', 'issue': 'https://github.com/metrum-ai/router/issues/535'} for name in failures}
                    compare(result.stdout, result.returncode, suite_string.split(','), expected)
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text(json.dumps(expected, indent=2) + '\n')
                else:
                    expected = json.loads(path.read_text())
                    try:
                        differences = compare(result.stdout, result.returncode, suite_string.split(','), expected)
                    except ValueError as exc:
                        raise ValueError(f'{kind} result validation: {exc}') from exc
                    report_differences(kind, differences)
                    errors.extend(f'{kind}: {x}' for x in differences)
                    summary_path = os.environ.get('GITHUB_STEP_SUMMARY')
                    if summary_path:
                        rows = re.findall(r'^  \[(PASS|FAIL)\] ([a-z][a-z0-9_]*)(?:: .*)?$', result.stdout, re.M)
                        with open(summary_path, 'a', encoding='utf-8') as summary:
                            summary.write(f'\n### {kind} mock compatibility\n\n| Suite | Result | Reason |\n| --- | --- | --- |\n')
                            for status, name in rows:
                                entry = expected.get(name)
                                label = ('expected failure' if entry else 'new failure') if status == 'FAIL' else ('unexpected pass' if entry else 'pass')
                                reason = '' if not entry else entry['reason'].replace('|', '&#124;').replace('\n', ' ') + ' (' + entry['issue'] + ')'
                                summary.write(f'| `{name}` | {label} | {reason} |\n')
            if errors:
                raise RuntimeError('compatibility differences:\n' + '\n'.join(errors))
            if not record:
                print('compat-mock: both tester baselines matched', flush=True)
        finally:
            for proc in reversed(processes):
                proc.terminate()
            for proc in reversed(processes):
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill(); proc.wait()
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as exc:
        sys.exit(f'compat mock failed: {exc}')
