# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Opt-in localhost Bedrock matrix. Requires an already running router and token file.
Never sends credentials anywhere except the supplied localhost router.
"""
import argparse
import json
import pathlib
import urllib.error
import urllib.request

p = argparse.ArgumentParser()
p.add_argument('--token-file', required=True)
p.add_argument('--out', required=True)
a = p.parse_args()
out = pathlib.Path(a.out)
out.mkdir(parents=True, exist_ok=True)
token = pathlib.Path(a.token_file).read_text().strip()
models = {
    'llama': 'us.meta.llama3-3-70b-instruct-v1:0',
    'mistral': 'mistral.mistral-large-3-675b-instruct',
    'qwen': 'qwen.qwen3-32b-v1:0',
    'deepseek': 'deepseek.v3.2',
    'gptoss': 'openai.gpt-oss-20b-1:0',
}

def call(body, path='/v1/chat/completions'):
    req = urllib.request.Request('http://127.0.0.1:18430' + path,
        data=json.dumps(body).encode(), headers={'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + token})
    try:
        with urllib.request.urlopen(req, timeout=90) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()

results = []
for group, model in models.items():
    for mode in ['chat', 'stream', 'tools']:
        body = {'model': group, 'max_tokens': 256,
                'messages': [{'role': 'user', 'content': 'Say OK'}]}
        if mode == 'stream':
            body['stream'] = True
        if mode == 'tools':
            body['messages'][0]['content'] = 'Call write_file with path bedrock-proof.txt and content OK. Do not just describe it.'
            body['tools'] = [{'type': 'function', 'function': {'name': 'write_file',
                'description': 'Write a file', 'parameters': {'type': 'object',
                'properties': {'path': {'type': 'string'}, 'content': {'type': 'string'}},
                'required': ['path', 'content']}}}]
        status, response = call(body)
        passed = status == 200
        if mode == 'stream':
            passed = passed and '[DONE]' in response and 'prompt_tokens' in response
        elif mode == 'tools':
            passed = passed and bool(json.loads(response).get('choices', [{}])[0].get('message', {}).get('tool_calls'))
        record = {'model': model, 'region': 'us-east-1', 'mode': mode,
                  'status': 'pass' if passed else 'fail', 'http_status': status,
                  'request': body, 'response': response}
        if mode == 'tools' and passed:
            msg = json.loads(response)['choices'][0]['message']
            body['messages'].append(msg)
            for tc in msg['tool_calls']:
                args = json.loads(tc['function']['arguments'])
                proof = out / 'bedrock-proof.txt'
                proof.write_text(args.get('content', 'OK'))
                body['messages'].append({'role': 'tool', 'tool_call_id': tc['id'], 'content': 'File created successfully.'})
            s, r = call(body)
            record['continuation'] = {'http_status': s, 'response': r, 'file_created': proof.exists()}
            if s != 200:
                record['status'] = 'fail'
        (out / f'{group}-{mode}.json').write_text(json.dumps(record, indent=2) + '\n')
        results.append({k: v for k, v in record.items() if k not in ['request', 'response', 'continuation']})
        print(group, mode, record['status'], status, flush=True)
(out / 'matrix.json').write_text(json.dumps(results, indent=2) + '\n')
raise SystemExit(0 if all(r['status'] == 'pass' for r in results) else 1)
