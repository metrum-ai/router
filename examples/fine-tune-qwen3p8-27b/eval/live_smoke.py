#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Live chat/stream/tool/schema router smoke; only scalar outcomes are saved."""
import argparse
import json
import tempfile
import urllib.request
from pathlib import Path
import sys
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from preflight import preflight


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--api-base', default='http://127.0.0.1:18525/v1')
    p.add_argument('--token-file', type=Path, required=True)
    p.add_argument('--model', required=True)
    p.add_argument('--preflight-targets', nargs='*', default=[], help='static target aliases for custom weighted routes')
    p.add_argument('--out', type=Path, required=True)
    a = p.parse_args()
    token = a.token_file.read_text().strip() if a.token_file else None
    preflight(a.api_base,token,[a.model,*a.preflight_targets])
    ids = []
    def call(body):
        headers = {'Content-Type':'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        payload = {'model':a.model,'max_tokens':2048,'temperature':0,
            'chat_template_kwargs':{'enable_thinking':False},**body}
        req = urllib.request.Request(a.api_base+'/chat/completions',data=json.dumps(payload).encode(),headers=headers)
        response = urllib.request.urlopen(req,timeout=180)
        ids.append(response.headers.get('X-Request-Id'))
        return response
    with call({'messages':[{'role':'user','content':'Reply with one short greeting.'}]}) as response:
        chat = json.load(response)
    greeting = chat['choices'][0]['message']
    assert greeting.get('content') and not greeting.get('reasoning_content')
    assert '<think>' not in greeting['content']
    chunks = 0;done = False
    with call({'messages':[{'role':'user','content':'Say hello.'}],'stream':True}) as response:
        for line in response:
            if line.startswith(b'data:'):
                if line[5:].strip()==b'[DONE]':
                    done=True;break
                json.loads(line[5:]);chunks+=1
    assert done and chunks
    schema = {'type':'object','properties':{'name':{'type':'string','const':'proof.txt'},
        'content':{'type':'string','const':'metrum-ft-proof'}},'required':['name','content'],'additionalProperties':False}
    tool = {'type':'function','function':{'name':'create_file','description':'Create the requested local proof file.',
        'parameters':schema}}
    messages = [{'role':'user','content':'Use create_file to create proof.txt with content metrum-ft-proof. After receiving its result, acknowledge success briefly.'}]
    with call({'messages':messages,'tools':[tool],'tool_choice':{'type':'function','function':{'name':'create_file'}}}) as response:
        result = json.load(response)
    assistant = result['choices'][0]['message'];tc = assistant['tool_calls'][0]
    assert tc['function']['name']=='create_file'
    args = json.loads(tc['function']['arguments'])
    assert args == {'name':'proof.txt','content':'metrum-ft-proof'}
    with tempfile.TemporaryDirectory(prefix='ft-live-tool-') as directory:
        target = Path(directory)/'proof.txt';target.write_text(args['content'])
        with call({'messages':messages+[assistant,{'role':'tool','tool_call_id':tc['id'],'content':'File created successfully.'}],
            'tools':[tool],'tool_choice':'none'}) as response:
            continuation = json.load(response)
        assert continuation['choices'][0]['message'].get('content')
        assert target.read_text()=='metrum-ft-proof'
    with call({'messages':[{'role':'user','content':'Return a JSON object with ok equal to true.'}],
        'response_format':{'type':'json_schema','json_schema':{'name':'smoke','strict':True,
            'schema':{'type':'object','properties':{'ok':{'type':'boolean','const':True}},'required':['ok'],'additionalProperties':False}}}}) as response:
        result = json.load(response)
    assert json.loads(result['choices'][0]['message']['content'])=={'ok':True}
    a.out.write_text(json.dumps({'copyright':'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0',
        'model':a.model,'api_base':a.api_base,'reasoning_mode':'explicitly disabled; no greeting reasoning trace','chat':'pass','stream':'pass','stream_frames':chunks,
        'tool_file_creation':'pass','tool_result_continuation':'pass','json_schema':'pass','router_request_ids':ids},indent=2))
    print('chat, streaming, tool continuation, JSON-schema: PASS')

if __name__ == '__main__':
    main()
