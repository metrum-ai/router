# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Run fixed, matched closed-loop traffic through a loopback router group."""
import argparse
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import random
import time
import urllib.error
import urllib.parse
import urllib.request

import scoring

SUITE_SHA = '894fdd2eb4989f75ad428a40e519200de8f066e6b0bc74db69501a060f52ee77'
PERSONAS = ('biomedical', 'finance', 'math', 'coding')


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl): return None


def read_stream(response, started):
    text = []; calls = {}; ttft = None; usage = None; done = False; finish = None
    for raw in response:
        line = raw.decode('utf-8').strip()
        if not line.startswith('data:'): continue
        item = line[5:].strip()
        if item == '[DONE]': done = True; break
        event = json.loads(item)
        if event.get('usage') is not None: usage = event['usage']
        for choice in event.get('choices') or []:
            if choice.get('finish_reason') is not None: finish = choice['finish_reason']
            delta = choice.get('delta') or {}
            content = delta.get('content') or ''; tool_deltas = delta.get('tool_calls') or []
            if ttft is None and (content or tool_deltas or delta.get('reasoning_content')): ttft = (time.monotonic()-started)*1000
            if content: text.append(content)
            for call in tool_deltas:
                index = call.get('index', 0); acc = calls.setdefault(index, {'function': {'name':'', 'arguments':''}})
                function = call.get('function') or {}
                for key in ('name','arguments'): acc['function'][key] += function.get(key) or ''
    if not done or finish not in ('stop','tool_calls'): raise ValueError('incomplete stream')
    return {'content':''.join(text), 'tool_calls':[calls[k] for k in sorted(calls)]}, ttft, usage


def one(base, group, key, case):
    start = time.monotonic(); request_id = None; status = None
    payload = {'model':group,'messages':[{'role':'user','content':case['prompt']}],
               'temperature':0,'max_tokens':512,'stream':True,'stream_options':{'include_usage':True},
               'chat_template_kwargs':{'enable_thinking':False}}
    if case['tools']: payload.update(tools=case['tools'], tool_choice='auto')
    req = urllib.request.Request(base+'/v1/chat/completions', data=json.dumps(payload).encode(),
        headers={'Content-Type':'application/json','Authorization':'Bearer '+key})
    try:
        with urllib.request.build_opener(NoRedirect).open(req, timeout=180) as response:
            status = response.status; request_id = response.headers.get('X-Request-Id')
            message, ttft, usage = read_stream(response, start)
        return {'case_id':case['id'],'domain':case['domain'],'status':status,'request_id':request_id,
                'latency_ms':(time.monotonic()-start)*1000,'ttft_ms':ttft,'usage':usage,
                'passed':status == 200 and scoring.score(case,message),'error':None}
    except (OSError, ValueError, TypeError, KeyError, urllib.error.HTTPError) as error:
        if isinstance(error, urllib.error.HTTPError): status = error.code
        return {'case_id':case['id'],'domain':case['domain'],'status':status,'request_id':request_id,
                'latency_ms':(time.monotonic()-start)*1000,'ttft_ms':None,'usage':None,
                'passed':False,'error':type(error).__name__}


def run(base, group, keys, cases):
    shuffled = list(cases); random.Random(426).shuffle(shuffled)
    start = time.monotonic(); records = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        futures = []
        for index, case in enumerate(shuffled):
            persona = case['domain'] if case['domain'] in PERSONAS else PERSONAS[index % len(PERSONAS)]
            futures.append(pool.submit(one, base, group, keys[persona], case))
        for future in futures: records.append(future.result())
    return records, time.monotonic()-start


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--router', required=True, help='loopback HTTP router base URL')
    parser.add_argument('--group', default='expert-study'); parser.add_argument('--tokens', required=True, type=Path)
    parser.add_argument('--suite', required=True, type=Path); parser.add_argument('--out', required=True, type=Path)
    args = parser.parse_args(); parsed = urllib.parse.urlsplit(args.router)
    if parsed.scheme != 'http' or parsed.hostname != '127.0.0.1' or not parsed.port or parsed.username or parsed.password or parsed.path not in ('','/') or parsed.query or parsed.fragment:
        parser.error('router must be a loopback HTTP origin with explicit port')
    if hashlib.sha256(args.suite.read_bytes()).hexdigest() != SUITE_SHA: parser.error('suite hash mismatch')
    if not args.tokens.resolve().is_relative_to(Path('/dev/shm').resolve()): parser.error('token directory must be in private tmpfs')
    for name in PERSONAS:
        path = args.tokens/name
        if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o077: parser.error('caller token files must be private regular files')
    keys = {name:(args.tokens/name).read_text().strip() for name in PERSONAS}
    if any(not token for token in keys.values()) or len(set(keys.values())) != len(keys): parser.error('four distinct caller tokens required')
    cases = json.loads(args.suite.read_text()); records, elapsed = run(args.router.rstrip('/'),args.group,keys,cases)
    output = {'records':records,'elapsed_seconds':elapsed,'summary':scoring.summarize(records,elapsed),
              'notes':'closed-loop concurrency 4, no retries; raw prompts and model responses omitted; GPU cost awaits invoice'}
    fd = os.open(args.out, os.O_WRONLY|os.O_CREAT|os.O_EXCL, 0o600)
    with os.fdopen(fd,'w') as file: json.dump(output,file,indent=2)
    print(json.dumps(output['summary']))


if __name__ == '__main__': main()
