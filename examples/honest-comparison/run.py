#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Paired, synthetic routing smoke: Metrum proxy vs llm-router-lite library."""
import argparse
from contextlib import ExitStack
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import secrets
import socket
import statistics
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer

from llm_router_lite import Router as LiteRouter

ROOT=Path(__file__).resolve().parents[2];HERE=Path(__file__).resolve().parent


def load(name,path):
    s=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m

def port():
    with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--router',required=True,type=Path);p.add_argument('--config',type=Path,default=HERE/'config.example.json');args=p.parse_args()
    config=json.loads(args.config.read_text());tasks=config['tasks'];assume=config['lite_model_assumptions']
    if len(tasks)!=6 or len({t['id'] for t in tasks})!=6:raise ValueError('expected six unique toy cases')
    by_id={t['id']:t for t in tasks}
    class MockBackend(BaseHTTPRequestHandler):
        def log_message(self,*_):pass
        def do_POST(self):
            try:
                n=int(self.headers.get('Content-Length','0'))
                if n<1 or n>65536:raise ValueError()
                body=json.loads(self.rfile.read(n));text=body['messages'][0]['content'];model=body['model']
                task=next(t for t in tasks if text.startswith('Case '+t['id']+':'))
                tier={'cheap-model':'cheap','strong-model':'strong'}[model]
                answer='PASS' if task['required_tier']=='cheap' or tier=='strong' else 'FAIL'
                raw=json.dumps({'id':'mock','object':'chat.completion','model':model,'choices':[{'index':0,'message':{'role':'assistant','content':answer},'finish_reason':'stop'}],'usage':{'prompt_tokens':20,'completion_tokens':1,'total_tokens':21}}).encode()
                self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
            except (KeyError,ValueError,StopIteration):self.send_error(400)
    d=load('d3_e2e',ROOT/'scripts/run_d3_policy_e2e.py');pol=load('escalation',ROOT/'examples/escalation-continuum/policy.py')
    with tempfile.TemporaryDirectory(prefix='honest-comparison-') as directory,ExitStack() as stack:
        tmp=Path(directory);env={k:os.environ[k] for k in ('PATH','HOME','TMPDIR','SYSTEMROOT','LD_LIBRARY_PATH') if k in os.environ}
        upstream=ThreadingHTTPServer(('127.0.0.1',0),MockBackend);threading.Thread(target=upstream.serve_forever,daemon=True).start();stack.callback(upstream.server_close);stack.callback(upstream.shutdown)
        class TwoTierPolicy(pol.Handler):
            cheap_max=200
            datacenter_max=200
        policy=ThreadingHTTPServer(('127.0.0.1',0),TwoTierPolicy);threading.Thread(target=policy.serve_forever,daemon=True).start();stack.callback(policy.server_close);stack.callback(policy.shutdown)
        backend=f'http://127.0.0.1:{upstream.server_port}/v1/chat/completions';router_port=port();token=secrets.token_urlsafe(32)
        providers={name:{'base_url':f'http://127.0.0.1:{upstream.server_port}/v1','dialect':'openai-chat','api_key':'mock-only','models':{'target':{'model':model}}} for name,model in (('cheapco','cheap-model'),('strongco','strong-model'))}
        group={'strategy':'external','targets':[{'provider':'cheapco','model_ref':'target','tier':'local','weight':1},{'provider':'strongco','model_ref':'target','tier':'frontier','weight':1}], 'external_policy':{'url':f'http://127.0.0.1:{policy.server_port}/route','allow_hosts':['127.0.0.1'],'timeout_ms':1000,'max_response_bytes':65536,'mode':'enforce','on_error':'fail_closed','include_request':False}}
        conf={'server':{'listen':f'127.0.0.1:{router_port}','default_model_group':'demo','logging':{'path':str(tmp/'requests.jsonl')},'cache':{'enabled':False},'decision_telemetry':{'enabled':True},'upstream':{'timeout_ms':5000},'identifiers':{'mode':'passthrough'},'usage_db':{'enabled':True,'driver':'sqlite','path':str(tmp/'usage.sqlite'),'migration_policy':'auto-safe'}},'state_path':str(tmp/'state.json'),'providers':providers,'models':{'demo':group},'callers':[{'id':'comparison-demo','user':'test','project':'test','environment':'test','status':'active','token_sha256':hashlib.sha256(token.encode()).hexdigest(),'token_id':'rtr_metrum_demo_test','allow':['demo'],'rate':{'rpm':10000,'tpm':1000000,'concurrent':8}}]}
        file=tmp/'router.json';file.write_text(json.dumps(conf));file.chmod(0o600)
        proc=d.start(stack,[str(args.router.resolve()),'-config',str(file)],tmp/'router.log',env);base=f'http://127.0.0.1:{router_port}';d.ready(base+'/readyz',proc,'router')
        lite=LiteRouter()
        for tier in ('cheap','strong'):
            opts=assume[tier];lite.add(tier,lambda prompt:prompt,cost=opts['cost_per_1000_tokens'],quality=opts['quality'])
        def invoke(arm,task):
            start=time.perf_counter();messages=[{'role':'user','content':task['prompt']}]
            if arm=='metrum':status,raw,_=d.http(base+'/v1/chat/completions',{'model':'demo','messages':messages,'max_tokens':8},{'Authorization':'Bearer '+token})
            else:
                chosen=lite.route(task['prompt']).model
                status,raw,_=d.http(backend,{'model':chosen+'-model','messages':messages,'max_tokens':8})
            duration=(time.perf_counter()-start)*1000
            if status!=200:raise RuntimeError(f'{arm} request failed: {status}')
            response=json.loads(raw);model=response['model'];answer=response['choices'][0]['message']['content']
            if model not in ('cheap-model','strong-model') or answer not in ('PASS','FAIL'):raise RuntimeError('unexpected mock response')
            return {'task':task['id'],'arm':arm,'tier':model.removesuffix('-model'),'passed':answer=='PASS','elapsed_ms':round(duration,3)}
        # Warm both paths once, exclude from results; alternate order on measured pairs.
        for arm in ('metrum','lite'):invoke(arm,tasks[0])
        rows=[]
        for i,task in enumerate(tasks):
            for arm in (('metrum','lite') if i%2==0 else ('lite','metrum')):rows.append(invoke(arm,task))
        summary={arm:{'successes':sum(r['passed'] for r in rows if r['arm']==arm),'trials':len(tasks),'median_client_ms':round(statistics.median(r['elapsed_ms'] for r in rows if r['arm']==arm),3)} for arm in ('metrum','lite')}
        print(json.dumps({'backend':'synthetic_mock','competitor':'llm-router-lite 0.1.0','summary':summary,'results':rows},indent=2))

if __name__=='__main__':main()
