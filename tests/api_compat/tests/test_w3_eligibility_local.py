# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""W3 localhost proof. Run directly with router and routerctl binary paths."""
import hashlib
import json
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.request import Request, urlopen
from urllib.error import HTTPError


def run(router, ctl):
    calls = []

    class Upstream(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            calls.append((dict(self.headers), body))
            content = [{'type': 'text', 'text': 'continued'}]
            if body.get('tools') and len(body['messages']) == 1:
                content = [{'type': 'tool_use', 'id': 'toolu_local', 'name': 'write_file',
                            'input': {'path': 'proof.txt', 'content': 'W3 tool continuation'}}]
            raw = json.dumps({'id': 'msg_local', 'type': 'message', 'role': 'assistant',
                              'model': body['model'], 'content': content,
                              'stop_reason': 'tool_use' if content[0]['type'] == 'tool_use' else 'end_turn',
                              'usage': {'input_tokens': 5, 'output_tokens': 5}}).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

    server = ThreadingHTTPServer(('127.0.0.1', 18891), Upstream)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    token = 'synthetic-w3-local'
    with tempfile.TemporaryDirectory(prefix='w3-e2e-') as d:
        work = Path(d)
        config = f'''server:
  listen: "127.0.0.1:18892"
  identifiers: {{mode: passthrough}}
  cache: {{enabled: false}}
  usage_db: {{enabled: false}}
  logging: {{path: "{work}/requests.jsonl"}}
state_path: "{work}/state.json"
providers:
  anth: {{dialect: anthropic, base_url: "http://127.0.0.1:18891/anthropic"}}
models:
  plain:
    strategy: static
    targets:
      - {{provider: anth, model: plain, tool_support: {{anthropic_messages: [client_tools]}}, allowed_betas: [context-management-2025-06-27]}}
  think:
    strategy: static
    targets:
      - {{provider: anth, model: unsupported}}
      - {{provider: anth, model: reasoning, reasoning: {{supported: true, mode: opt_in, control: token_budget}}}}
callers:
  - id: local
    token_sha256: {hashlib.sha256(token.encode()).hexdigest()}
    allow: [plain, think]
'''
        path = work / 'config.yaml'
        path.write_text(config)

        def post(payload, beta='', chat=False):
            req = Request('http://127.0.0.1:18892' + ('/v1/chat/completions' if chat else '/anthropic/v1/messages'),
                          data=json.dumps(payload).encode(), headers={'Authorization': 'Bearer '+token,
                          'Content-Type': 'application/json', 'anthropic-beta': beta})
            try:
                with urlopen(req, timeout=10) as r:
                    return r.status, json.loads(r.read())
            except HTTPError as e:
                return e.code, json.loads(e.read())

        def start():
            log = open(work / 'stderr', 'w+')
            process = subprocess.Popen([router, '-config', str(path)], stdout=log, stderr=log, cwd=work)
            for _ in range(100):
                try:
                    urlopen('http://127.0.0.1:18892/readyz', timeout=.2).close()
                    return process, log
                except Exception:
                    time.sleep(.1)
            process.terminate()
            process.wait()
            log.seek(0)
            raise AssertionError(log.read())

        def stop(process, log):
            process.terminate()
            process.wait(timeout=10)
            log.seek(0)
            text = log.read()
            log.close()
            return text

        process, log = start()
        try:
            payload = {'model': 'plain', 'max_tokens': 64, 'messages': [{'role': 'user', 'content': 'hi'}]}
            assert post(payload, chat=True)[0] == 200
            assert post(payload, 'blocked')[0] == 200
            assert not any(k.lower() == 'anthropic-beta' for k in calls[-1][0])
            gated = payload | {'context_management': {'edits': []}}
            before = len(calls)
            status, body = post(gated)
            assert status == 400 and 'context_management' in json.dumps(body), body
            assert len(calls) == before
            assert post(gated, 'blocked,context-management-2025-06-27')[0] == 200
            headers = {k.lower(): v for k, v in calls[-1][0].items()}
            assert headers['anthropic-beta'] == 'context-management-2025-06-27'
            status, body = post(payload | {'model': 'think', 'thinking': {'type': 'adaptive'}})
            assert status == 200, body
            assert calls[-1][1]['model'] == 'reasoning'
            # A tool-using client creates a file, then continues with a tool result.
            tool = {'name': 'write_file', 'description': 'write a file', 'input_schema': {'type': 'object'}}
            status, body = post(payload | {'tools': [tool]})
            assert status == 200, body
            use = body['content'][0]
            (work / use['input']['path']).write_text(use['input']['content'])
            continuation = payload | {'tools': [tool], 'messages': payload['messages'] + [
                {'role': 'assistant', 'content': body['content']},
                {'role': 'user', 'content': [{'type': 'tool_result', 'tool_use_id': use['id'], 'content': 'written'}]}]}
            status, body = post(continuation)
            assert status == 200 and body['content'][0]['text'] == 'continued', body
            assert (work / 'proof.txt').read_text() == 'W3 tool continuation'
        finally:
            warnings = stop(process, log)
        assert warnings.count('relies on eligibility.implicit_openai_to_anthropic') == 3, warnings
        print('PASS default flag on; three per-target warnings; beta capture/strip; gated field 400; adaptive selects reasoning; file tool continuation')
        path.write_text(config + '\neligibility: {implicit_openai_to_anthropic: false}\n')
        process, log = start()
        try:
            assert post(payload, chat=True)[0] == 502
            assert post(payload)[0] == 200
        finally:
            assert 'relies on eligibility' not in stop(process, log)
        print('PASS flag off rejects implicit OpenAI translation; native still works; no warning')
        dry = subprocess.check_output([ctl, 'config', 'patch-eligibility', '--config', str(path), '--dry-run', '--diff'], text=True)
        assert 'supported_inbound_dialects' in dry and '+++ patched' in dry
        patched = work / 'patched.yaml'
        subprocess.run([ctl, 'config', 'patch-eligibility', '--config', str(path), '--output', str(patched)], check=True)
        subprocess.run([ctl, 'config', 'validate', '--config', str(patched)], check=True, stdout=subprocess.DEVNULL)
        path.write_text(patched.read_text())
        process, log = start()
        try:
            assert post(payload, chat=True)[0] == 200
        finally:
            assert 'relies on eligibility' not in stop(process, log)
        print('PASS patch dry-run diff; applied config loads and restores OpenAI translation with flag off')
    server.shutdown()
    server.server_close()
    thread.join()


if __name__ == '__main__':
    run(*sys.argv[1:])
