# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Opt-in Mistral Responses/Anthropic tool and streaming continuation smoke."""
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
headers = {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + pathlib.Path(a.token_file).read_text().strip(), 'anthropic-version': '2023-06-01'}

def call(path, body):
    try:
        with urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:18430' + path, data=json.dumps(body).encode(), headers=headers), timeout=90) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()

schema = {'type': 'object', 'properties': {'content': {'type': 'string'}}, 'required': ['content']}
for dialect, path in [('responses', '/v1/responses'), ('anthropic', '/v1/messages')]:
    for stream in [False, True]:
        b = {'model': 'mistral', 'max_tokens': 128, 'stream': stream}
        if dialect == 'responses':
            b.update(instructions='Use tools when requested.', input=[{'role': 'user', 'content': 'Call write_file with content OK now.'}], tools=[{'type': 'function', 'name': 'write_file', 'description': 'Write a file', 'parameters': schema}])
        else:
            b.update(system='Use tools when requested.', messages=[{'role': 'user', 'content': 'Call write_file with content OK now.'}], tools=[{'name': 'write_file', 'description': 'Write a file', 'input_schema': schema}])
        status, response = call(path, b)
        rec = {'status': status, 'response': response}
        assert status == 200, response
        if stream:
            assert 'write_file' in response, response
        else:
            r = json.loads(response)
            if dialect == 'responses':
                calls = [x for x in r['output'] if x['type'] == 'function_call']
                b['input'] += calls
                for c in calls:
                    b['input'].append({'type': 'function_call_output', 'call_id': c['call_id'], 'output': 'File written successfully.'})
            else:
                calls = [x for x in r['content'] if x['type'] == 'tool_use']
                b['messages'].append({'role': 'assistant', 'content': r['content']})
                b['messages'].append({'role': 'user', 'content': [{'type': 'tool_result', 'tool_use_id': c['id'], 'content': 'File written successfully.'} for c in calls]})
            assert calls, response
            (out / 'dialect-proof.txt').write_text('OK')
            s, v = call(path, b)
            rec['continuation'] = {'status': s, 'response': v}
            assert s == 200, v
        (out / (dialect + '-tools' + ('-stream' if stream else '') + '.json')).write_text(json.dumps(rec, indent=2) + '\n')
        print(dialect, 'stream' if stream else 'tools+continuation', 'pass', flush=True)
