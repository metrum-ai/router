#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Local worktree-router tool continuation with two synthetic FT upstreams.

This proves plumbing, NOT trained-model quality. All processes are torn down.
"""
import argparse
import json
import subprocess
import tempfile
import threading
import time
import urllib.request
import urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import yaml

ROOT = Path(__file__).resolve().parents[3]


def wait_ready(process, url, timeout=60, opener=None):
    """Bounded startup gate; never send test traffic after readiness exhaustion."""
    opener = opener or urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        code = process.poll()
        if code is not None:
            raise RuntimeError(f'router exited before readiness (exit={code})')
        try:
            with opener.open(url, timeout=1) as response:
                if response.status == 200:
                    return
        except (urllib.error.URLError, TimeoutError, OSError):
            pass
        time.sleep(.1)
    raise RuntimeError(f'router readiness timed out after {timeout}s at {url}; no test traffic sent')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--router-bin', type=Path, required=True)
    a = p.parse_args()
    seen = {'fireworks_ft': 0, 'vllm_b200_ft': 0}
    seen_models = {'mock-adapter':0,'mock-base':0}
    servers = []
    for provider, port in [('fireworks_ft', 18531), ('vllm_b200_ft', 18532)]:
        def handler(name):
            class Handler(BaseHTTPRequestHandler):
                def log_message(self, *args):
                    pass
                def do_POST(self):
                    body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                    seen[name] += 1
                    seen_models[body['model']] += 1
                    messages = body['messages']
                    if messages[-1]['role'] == 'tool':
                        assert messages[-1]['tool_call_id'] == 'call_ft_0'
                        message = {'role': 'assistant', 'content': 'created; continuation complete'}
                        finish = 'stop'
                    else:
                        message = {'role': 'assistant', 'content': None, 'tool_calls': [{'id': 'call_ft_0', 'type': 'function',
                            'function': {'name': 'create_file', 'arguments': '{"name":"proof.txt","content":"synthetic"}'}}]}
                        finish = 'tool_calls'
                    result = {'id': 'chatcmpl-ft-mock', 'object': 'chat.completion', 'created': 1,
                        'model': body['model'], 'choices': [{'index': 0, 'message': message, 'finish_reason': finish}],
                        'usage': {'prompt_tokens': 20, 'completion_tokens': 10, 'total_tokens': 30}}
                    raw = json.dumps(result).encode()
                    self.send_response(200); self.send_header('Content-Type', 'application/json')
                    self.send_header('Content-Length', str(len(raw))); self.end_headers(); self.wfile.write(raw)
            return Handler
        server = ThreadingHTTPServer(('127.0.0.1', port), handler(provider))
        servers.append(server)
        threading.Thread(target=server.serve_forever, daemon=True).start()
    policy_calls = []
    class Policy(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass
        def do_POST(self):
            raw = self.rfile.read(int(self.headers['Content-Length']))
            assert b'FT_PRIVATE_PROMPT_MUST_NOT_BE_LOGGED_326' not in raw
            policy_calls.append(True)
            data = json.dumps({'targetIndex': 1, 'classLabel': 'ft-shadow-alternate'}).encode()
            self.send_response(200);self.send_header('Content-Type','application/json')
            self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
    policy = ThreadingHTTPServer(('127.0.0.1',18533), Policy)
    servers.append(policy)
    threading.Thread(target=policy.serve_forever, daemon=True).start()
    process = None
    try:
        with tempfile.TemporaryDirectory(prefix='ft-router-') as directory:
            run = Path(directory)
            subprocess.run(['python3', str(ROOT / 'scripts/local_dev_bootstrap.py'), '--out-dir', directory,
                '--force', '--allow', 'local'], cwd=ROOT, check=True, stdout=subprocess.DEVNULL)
            # Use JSON roundtrip through YAML BaseLoader to avoid changing RFC3339 timestamps.
            config_text = (run / 'config.yaml').read_text()
            # PyYAML SafeLoader coerces dates; preserve them as strings instead.
            class Loader(yaml.SafeLoader):
                pass
            Loader.yaml_implicit_resolvers = {k: [(tag, regex) for tag, regex in v if tag != 'tag:yaml.org,2002:timestamp']
                for k, v in Loader.yaml_implicit_resolvers.items()}
            x = yaml.load(config_text, Loader=Loader)
            x['server']['listen'] = '127.0.0.1:18530'
            x['server']['default_model_group'] = 'qwen3p8-27b-ft'
            x['server']['decision_telemetry'] = {'enabled':True}
            x['providers'] = {}
            for provider, port in [('fireworks_ft',18531),('vllm_b200_ft',18532)]:
                x['providers'][provider] = {'dialect': 'openai-chat', 'base_url': f'http://127.0.0.1:{port}/v1',
                    'models': {'adapter': {'model': 'mock-adapter', 'input_price_per_million_usd': 1,
                    'output_price_per_million_usd': 1, 'tool_support': {'openai_chat': ['tools','tool_choice']}}}}
            x['providers']['vllm_b200_ft']['models']['base'] = {
                **x['providers']['vllm_b200_ft']['models']['adapter'],'model':'mock-base'}
            x['models'] = {'qwen3p8-27b-ft': {'strategy': 'weighted', 'targets': [
                {'provider': n, 'model_ref': 'adapter', 'weight': 50} for n in seen]}}
            x['models']['qwen3p8-27b-ft-shadow'] = {'strategy':'external',
                'external_policy': {'url':'http://127.0.0.1:18533/route','mode':'shadow',
                    'allow_hosts':['127.0.0.1'],'include_request':False,'on_error':'fail_closed'},
                'targets':[{'provider':'vllm_b200_ft','model_ref':'base'},
                           {'provider':'vllm_b200_ft','model_ref':'adapter'}]}
            x['callers'][0]['allow'] = ['qwen3p8-27b-ft','qwen3p8-27b-ft-shadow']
            x['callers'][0]['rate'] = {'rpm':20000,'tpm':100000000,'concurrent':64}
            # INTENTIONAL LIMIT TEST: exactly45 successes then quota denial.
            # This is not a quality/load cohort; rates otherwise stay high.
            x['callers'][0]['quota']['day']['requests'] = 45
            (run / 'config.yaml').write_text(yaml.safe_dump(x))
            log = (run / 'process.log').open('w')
            process = subprocess.Popen([str(a.router_bin.resolve()), '--config', str(run/'config.yaml')], cwd=ROOT, stdout=log, stderr=log)
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            wait_ready(process, 'http://127.0.0.1:18530/readyz', opener=opener)
            token = (run / 'router.token').read_text().strip()
            marker = 'FT_PRIVATE_PROMPT_MUST_NOT_BE_LOGGED_326'
            tool = {'type':'function','function':{'name':'create_file','parameters':{'type':'object','properties':{'name':{'type':'string'},'content':{'type':'string'}},'required':['name','content']}}}
            def chat(messages, model='qwen3p8-27b-ft'):
                body = {'model':model,'messages':messages,'tools':[tool],'max_tokens':128}
                req = urllib.request.Request('http://127.0.0.1:18530/v1/chat/completions', data=json.dumps(body).encode(),
                    headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
                with opener.open(req, timeout=10) as response:return json.load(response)
            messages = [{'role':'user','content':marker}]
            result = chat(messages)
            assistant = result['choices'][0]['message'];call = assistant['tool_calls'][0]
            args = json.loads(call['function']['arguments'])
            (run / args['name']).write_text(args['content'])
            result = chat(messages + [assistant, {'role':'tool','tool_call_id':call['id'],'content':'file created'}])
            assert result['choices'][0]['message']['content'].endswith('continuation complete')
            assert (run/'proof.txt').read_text() == 'synthetic'
            for _ in range(42):chat(messages)
            before = dict(seen)
            chat(messages, 'qwen3p8-27b-ft-shadow')
            assert policy_calls == [True]
            assert seen['fireworks_ft'] == before['fireworks_ft']
            assert seen['vllm_b200_ft'] == before['vllm_b200_ft'] + 1
            assert seen_models == {'mock-adapter':44,'mock-base':1}
            try:
                chat(messages)
                raise AssertionError('request budget not enforced')
            except urllib.error.HTTPError as exc:
                assert exc.code in (402,403,429), exc.code
            assert all(v > 0 for v in seen.values()), seen
            process.terminate();process.wait(timeout=20);process=None;log.close()
            shadow_recorded = False
            for path in run.iterdir():
                if path.name.startswith(('usage.sqlite','requests.jsonl','process.log')):
                    content = path.read_bytes()
                    assert marker.encode() not in content, 'prompt leaked: '+path.name
                    shadow_recorded |= b'shadow_recommended_candidate' in content
            assert shadow_recorded, 'shadow recommendation not recorded'
            print(json.dumps({'tool_continuation':'pass','created_file':True,'weighted_targets':seen,
                'request_budget':'pass','intentional_limit_test':True,'prompt_content_absent':'pass','shadow_recommendation':'pass','synthetic_only':True}))
    finally:
        if process is not None:
            process.terminate();process.wait(timeout=20)
        for server in servers:server.shutdown();server.server_close()

if __name__ == '__main__':
    main()
