#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Paid Fireworks paired smoke; both tier aliases use the same hosted model."""
import argparse
from contextlib import ExitStack
from decimal import Decimal
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import secrets
import socket
import statistics
import sys
import threading
import time
from http.server import ThreadingHTTPServer
from urllib.parse import urlparse
from llm_router_lite import Router as LiteRouter

ROOT=Path(__file__).resolve().parents[2];HERE=Path(__file__).resolve().parent
MODEL='accounts/fireworks/models/deepseek-v4p1-flash-1017'
PRICES={'input':'0.30','cached':'0.006','output':'1.20'}


def load(name,path):
    s=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m

def port():
    with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]

def cost(usage):
    if not isinstance(usage,dict) or not all(isinstance(usage.get(k),int) for k in ('prompt_tokens','completion_tokens')):return None
    details=usage.get('prompt_tokens_details') or {};cached=details.get('cached_tokens')
    if not isinstance(cached,int) or cached<0 or cached>usage['prompt_tokens']:return None
    a=Decimal(usage['prompt_tokens']-cached)*Decimal(PRICES['input']);b=Decimal(cached)*Decimal(PRICES['cached']);c=Decimal(usage['completion_tokens'])*Decimal(PRICES['output'])
    return float((a+b+c)/Decimal(1000000))


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--router',required=True,type=Path);p.add_argument('--state-dir',required=True,type=Path)
    p.add_argument('--backend',choices=('fireworks','local'),default='fireworks');p.add_argument('--local-url');p.add_argument('--local-model');a=p.parse_args()
    if not a.state_dir.is_absolute():p.error('state-dir must be absolute')
    if a.backend=='local':
        u=urlparse(a.local_url or '')
        if u.scheme!='http' or u.hostname!='127.0.0.1' or u.username or u.password or u.path!='/v1' or u.query or u.fragment or not a.local_model:p.error('local mode needs a model and loopback /v1 URL')
    key=os.environ.get('FIREWORKS_API_KEY') if a.backend=='fireworks' else 'unused-local'
    if not key:p.error('FIREWORKS_API_KEY required')
    api='https://api.fireworks.ai/inference/v1' if a.backend=='fireworks' else a.local_url
    model=MODEL if a.backend=='fireworks' else a.local_model
    a.state_dir.mkdir(mode=0o700,parents=False,exist_ok=False);tmp=a.state_dir
    config=json.loads((HERE/'config.example.json').read_text());tasks=json.loads((HERE/'hosted-tasks.json').read_text())
    d=load('d3_e2e',ROOT/'scripts/run_d3_policy_e2e.py');pol=load('escalation',ROOT/'examples/escalation-continuum/policy.py')
    env={k:os.environ[k] for k in ('PATH','HOME','TMPDIR','SYSTEMROOT','LD_LIBRARY_PATH') if k in os.environ}
    if a.backend=='fireworks':env['FIREWORKS_API_KEY']=key
    def append(row):
        with (tmp/'receipts.jsonl').open('a') as f:f.write(json.dumps(row)+'\n')
    with ExitStack() as stack:
        class TwoTierPolicy(pol.Handler):
            cheap_max=200
            datacenter_max=200
        policy=ThreadingHTTPServer(('127.0.0.1',0),TwoTierPolicy);threading.Thread(target=policy.serve_forever,daemon=True).start();stack.callback(policy.server_close);stack.callback(policy.shutdown)
        router_port=port();token=secrets.token_urlsafe(32);db=tmp/'usage.sqlite'
        meta={'model':model};provider={'base_url':api,'dialect':'openai-chat','api_key':'unused-local'}
        if a.backend=='fireworks':
            meta.update(input_price_per_million_usd=float(PRICES['input']),cached_input_price_per_million_usd=float(PRICES['cached']),output_price_per_million_usd=float(PRICES['output']),pricing_source='https://fireworks.ai/models/deepseek-ai/deepseek-v4p1-flash-1017',pricing_updated_at='2026-10-28')
            provider.update(api_key='${FIREWORKS_API_KEY}',api_key_env='FIREWORKS_API_KEY')
        provider['models']={'target':meta};providers={name:provider for name in ('cheapco','strongco')}
        group={'strategy':'external','targets':[{'provider':'cheapco','model_ref':'target','tier':'local','weight':1},{'provider':'strongco','model_ref':'target','tier':'frontier','weight':1}],'external_policy':{'url':f'http://127.0.0.1:{policy.server_port}/route','allow_hosts':['127.0.0.1'],'timeout_ms':1000,'max_response_bytes':65536,'mode':'enforce','on_error':'fail_closed','include_request':False}}
        conf={'server':{'listen':f'127.0.0.1:{router_port}','default_model_group':'demo','logging':{'path':str(tmp/'requests.jsonl')},'cache':{'enabled':False},'decision_telemetry':{'enabled':True},'upstream':{'timeout_ms':30000},'identifiers':{'mode':'passthrough'},'usage_db':{'enabled':True,'driver':'sqlite','path':str(db),'migration_policy':'auto-safe'}},'state_path':str(tmp/'state.json'),'providers':providers,'models':{'demo':group},'callers':[{'id':'comparison-demo','user':'test','project':'test','environment':'test','status':'active','token_sha256':hashlib.sha256(token.encode()).hexdigest(),'token_id':'rtr_metrum_demo_test','allow':['demo'],'rate':{'rpm':10000,'tpm':1000000,'concurrent':8}}]}
        file=tmp/'router.json';file.write_text(json.dumps(conf));file.chmod(0o600)
        proc=d.start(stack,[str(a.router.resolve()),'-config',str(file)],tmp/'router.log',env);base=f'http://127.0.0.1:{router_port}';d.ready(base+'/readyz',proc,'router')
        lite=LiteRouter()
        for tier in ('cheap','strong'):
            opts=config['lite_model_assumptions'][tier];lite.add(tier,lambda prompt:prompt,cost=opts['cost_per_1000_tokens'],quality=opts['quality'])
        rows=[]
        for i,task in enumerate(tasks):
            for arm in (('metrum','lite') if i%2==0 else ('lite','metrum')):
                append({'task':task['id'],'arm':arm,'phase':'started'})
                start=time.perf_counter();messages=[{'role':'user','content':task['prompt']}]
                if arm=='metrum':
                    status,raw,rid=d.http(base+'/v1/chat/completions',{'model':'demo','messages':messages,'max_tokens':128},{'Authorization':'Bearer '+token},timeout=40)
                    tier=None
                else:
                    tier=lite.route(task['prompt']).model
                    status,raw,rid=d.http(api+'/chat/completions',{'model':model,'messages':messages,'max_tokens':128},{'Authorization':'Bearer '+key},timeout=40)
                elapsed=(time.perf_counter()-start)*1000
                try:response=json.loads(raw)
                except (ValueError,UnicodeDecodeError):response={}
                usage=response.get('usage');price=cost(usage) if a.backend=='fireworks' else None
                append({'task':task['id'],'arm':arm,'phase':'finished','status':status,'request_id':rid,'usage':usage,'configured_cost_usd':price})
                if status!=200:raise RuntimeError(f'{arm} request failed: {status}; preserve and reconcile private state')
                if arm=='metrum':
                    policy_row=d.policy_row(db,rid);idx=policy_row['selected_candidate_index'];tier=('cheap','strong')[idx]
                message=((response.get('choices') or [{}])[0].get('message') or {}).get('content')
                passed=isinstance(message,str) and message.strip().lower()==task['answer']
                if a.backend=='fireworks' and price is None:raise RuntimeError('missing token usage or cached split; reconcile private state')
                rows.append({'task':task['id'],'arm':arm,'tier':tier,'passed':passed,'elapsed_ms':round(elapsed,3),'usage':usage,'usage_cost_usd':price})
        usage_rows=d.rows(db,'select request_id,status,attempts,fallback_used,usage_source,input_tokens,cached_input_tokens,output_tokens,total_cost_usd from request_usage')
        if len(usage_rows)!=6 or any(r['status']!=200 or r['attempts']!=1 or r['fallback_used'] or r['usage_source']!='reported' for r in usage_rows):raise RuntimeError('unexpected router attempts; reconcile private state')
        summary={arm:{'successes':sum(r['passed'] for r in rows if r['arm']==arm),'trials':len(tasks),'median_client_ms':round(statistics.median(r['elapsed_ms'] for r in rows if r['arm']==arm),3),'usage_cost_usd':round(sum(r['usage_cost_usd'] for r in rows if r['arm']==arm),10) if a.backend=='fireworks' else None} for arm in ('metrum','lite')}
        print(json.dumps({'backend':a.backend,'model':model,'note':'both tiers use same model; no warmups, no quality ranking; local cost excludes compute','summary':summary,'results':rows},indent=2))

if __name__=='__main__':main()
