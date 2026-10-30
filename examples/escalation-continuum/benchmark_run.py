#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Bounded synthetic escalation experiment; fake unless explicitly enabled."""
import argparse
from contextlib import ExitStack,closing,nullcontext
import importlib.util
import json
import os
from pathlib import Path
import secrets
import sqlite3
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from urllib.parse import urlparse
import yaml
import benchmark_policy as policy
import benchmark_relay

ROOT=Path(__file__).resolve().parents[2]


def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m


def loopback(url):
    u=urlparse(url or '')
    return u.scheme=='http' and u.hostname=='127.0.0.1' and not u.username and not u.password and not u.query and not u.fragment and u.path=='/v1'


def prompt(case):return f"SYNTHETIC427:{case['id']}\n{case['prompt']}\nReturn only the number."


def make_policy(cases):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*_):pass
        def do_POST(self):
            try:
                if self.path!='/route':raise ValueError('path')
                n=int(self.headers.get('Content-Length','0'))
                if not 0<n<=65536:raise ValueError('length')
                result=policy.choose(json.loads(self.rfile.read(n)),cases)
                if result is None:raise ValueError('unknown synthetic case')
                code=200
            except (TypeError,ValueError,json.JSONDecodeError):result={'error':'invalid synthetic case'};code=503
            raw=json.dumps(result).encode();self.send_response(code);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
    return Handler


def make_fake(cases):
    answers={prompt(c):c['answer'] for c in cases}
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*_):pass
        def do_POST(self):
            try:
                n=int(self.headers.get('Content-Length','0'))
                if self.path!='/v1/chat/completions' or not 0<n<=16384:raise ValueError()
                req=json.loads(self.rfile.read(n));text=req['messages'][-1]['content'];answer=answers[text]
                out={'id':'synthetic','object':'chat.completion','model':req['model'],'choices':[{'index':0,'message':{'role':'assistant','content':answer},'finish_reason':'stop'}],
                     'usage':{'prompt_tokens':16,'completion_tokens':1,'total_tokens':17}}
                raw=json.dumps(out).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
            except (KeyError,ValueError,TypeError,json.JSONDecodeError):self.send_error(400)
    return Handler


def server(stack,handler):
    s=ThreadingHTTPServer(('127.0.0.1',0),handler);threading.Thread(target=s.serve_forever,daemon=True).start()
    stack.callback(s.server_close);stack.callback(s.shutdown);return s


def usage_row(db,rid):
    deadline=time.monotonic()+15
    while time.monotonic()<deadline:
        with closing(sqlite3.connect(db)) as con:
            row=con.execute('SELECT target_provider,target_model,status,attempts,fallback_used,input_tokens,output_tokens,total_cost_usd,usage_source FROM request_usage WHERE request_id=?',(rid,)).fetchone()
        if row is not None:return row
        time.sleep(.05)
    raise RuntimeError('usage missing; reconcile potentially charged request')


def policy_row(db,rid):
    deadline=time.monotonic()+15
    while time.monotonic()<deadline:
        with closing(sqlite3.connect(db)) as con:
            con.row_factory=sqlite3.Row
            row=con.execute('SELECT * FROM request_policy_executions WHERE request_id=? ORDER BY seq DESC LIMIT 1',(rid,)).fetchone()
        if row is not None:return dict(row)
        time.sleep(.05)
    raise RuntimeError('policy telemetry missing; reconcile potentially charged request')


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--router',type=Path,required=True);p.add_argument('--backend',choices=('fake','mixed'),default='fake')
    p.add_argument('--local-url');p.add_argument('--local-model');p.add_argument('--datacenter-url');p.add_argument('--datacenter-model')
    p.add_argument('--state-dir',type=Path);p.add_argument('--allow-paid',action='store_true');a=p.parse_args()
    if a.backend=='mixed':
        if not a.allow_paid or not os.environ.get('OPENAI_API_KEY'):p.error('mixed mode requires explicit --allow-paid and OPENAI_API_KEY')
        if not loopback(a.local_url) or not loopback(a.datacenter_url) or not a.local_model or not a.datacenter_model:p.error('mixed mode requires named loopback local/datacenter endpoints')
        if not a.state_dir or not a.state_dir.is_absolute():p.error('mixed mode requires a new private absolute state directory')
    if a.state_dir:a.state_dir.mkdir(mode=0o700,parents=False,exist_ok=False)
    cases=policy.load_cases();m=module('local_pi',ROOT/'scripts/local_pi_instance.py');secure=module('catalog_hash',ROOT/'examples/catalog/token_hash.py')
    demo=module('tokenomics',ROOT/'examples/tokenomics/run.py');d=module('d3_e2e',ROOT/'scripts/run_d3_policy_e2e.py')
    with (nullcontext(a.state_dir) if a.state_dir else tempfile.TemporaryDirectory(prefix='escalation-427-')) as directory,ExitStack() as stack:
        state=Path(directory);fake=server(stack,make_fake(cases));chooser=server(stack,make_policy(cases));relay=None
        local_token='relay427_'+secrets.token_urlsafe(24)
        if a.backend=='mixed':
            relay=benchmark_relay.Relay(os.environ['OPENAI_API_KEY'],local_token,[prompt(c) for c in cases],limit=15)
            threading.Thread(target=relay.serve_forever,daemon=True).start();stack.callback(relay.server_close);stack.callback(relay.shutdown)
        listen=demo.port();base=f'http://127.0.0.1:{listen}';caller='rtr_metrum_eval_escalation_dev_k424_'+secrets.token_urlsafe(24)
        cfg=m.build_config(out_dir=state,listen=f'127.0.0.1:{listen}',id_key_hex=secrets.token_hex(64),token=caller)
        secure.apply(cfg['callers'][0],secure.prepare(ROOT),caller,state)
        cfg['server']['identifiers']={'mode':'passthrough'};cfg['server']['admin_auth']={'basic':{'enabled':False,'users':[]}}
        cfg['server']['admin_reports']={'enabled':False};cfg['server']['default_model_group']='escalation';cfg['server']['decision_telemetry']={'enabled':True}
        names={'local':'fake-local','datacenter':'fake-datacenter','frontier':'fake-frontier'}
        if a.backend=='mixed':names={'local':a.local_model,'datacenter':a.datacenter_model,'frontier':'gpt-6.1-sol'}
        providers={};targets=[]
        for tier in ('local','datacenter','frontier'):
            url=f'http://127.0.0.1:{fake.server_port}/v1';key='synthetic';dialect='openai-chat'
            meta={'model':names[tier],'tier':tier,'input_modalities':['text'],'output_modalities':['text'],'input_price_per_million_usd':0,'output_price_per_million_usd':0}
            target={'provider':tier,'model_ref':'target','tier':tier,'weight':1}
            if a.backend=='mixed':
                if tier=='local':url=a.local_url
                elif tier=='datacenter':url=a.datacenter_url
                else:
                    url=f'http://127.0.0.1:{relay.server_port}/v1';key=local_token;dialect='openai-responses'
                    meta.update(input_price_per_million_usd=2,output_price_per_million_usd=10,cached_input_price_per_million_usd=.1,pricing_source='https://developers.openai.com/api/docs/models/gpt-6.1-sol',pricing_updated_at='2026-10-29')
                    target['bridges']={'chat_to_responses':{'enabled':True,'text':True}}
            providers[tier]={'base_url':url,'dialect':dialect,'api_key':key,'models':{'target':meta}};targets.append(target)
        cfg['providers']=providers;cfg['models']={
            'escalation':{'strategy':'external','external_policy':{'url':f'http://127.0.0.1:{chooser.server_port}/route','allow_hosts':['127.0.0.1'],'mode':'enforce','timeout_ms':500,'max_response_bytes':65536,'on_error':'fail_closed','include_request':True},'targets':targets},
            'frontier-only':{'strategy':'static','targets':[targets[2]]}}
        cfg['callers'][0]['allow']=['escalation','frontier-only'];file=state/'config.yaml';file.write_text(yaml.safe_dump(cfg,sort_keys=False));file.chmod(0o600)
        env={k:os.environ[k] for k in ('PATH','HOME','TMPDIR','SYSTEMROOT','LD_LIBRARY_PATH') if k in os.environ}
        proc=d.start(stack,[str(a.router.resolve()),'-config',str(file)],state/'router.log',env);d.ready(base+'/readyz',proc,'router')
        db=state/'usage.sqlite';rows=[]
        with (state/'partial.jsonl').open('x') as partial:
            for case in cases:
                for path in ('frontier-only','escalation'):
                    partial.write(json.dumps({'phase':'started','case':case['id'],'path':path})+'\n');partial.flush()
                    started=time.monotonic_ns();status,body,rid=d.http(base+'/v1/chat/completions',{'model':path,'messages':[{'role':'user','content':prompt(case)}],'max_tokens':32},{'Authorization':'Bearer '+caller},timeout=120)
                    elapsed=(time.monotonic_ns()-started)/1e6
                    partial.write(json.dumps({'phase':'received','case':case['id'],'path':path,'status':status,'request_id':rid})+'\n');partial.flush()
                    record={'case':case['id'],'path':path,'status':status,'request_ms':elapsed}
                    if status!=200 or not rid:
                        partial.write(json.dumps(record)+'\n');partial.flush();raise RuntimeError('request failed or request ID missing; stop and reconcile')
                    response=json.loads(body);text=response['choices'][0]['message']['content'];record['answer']=text.strip();record['correct']=text.strip()==case['answer']
                    row=usage_row(db,rid);provider,model,code,attempts,fallback,inp,out,cost,source=row
                    record.update(tier=provider,model=model,input_tokens=inp,output_tokens=out,usage_cost_usd=cost,usage_source=source,attempts=attempts)
                    if code!=200 or attempts!=1 or fallback or source!='reported':
                        partial.write(json.dumps(record)+'\n');partial.flush();raise RuntimeError('unexpected retry/usage; stop and reconcile')
                    if path=='frontier-only' and provider!='frontier':raise RuntimeError('unexpected baseline target')
                    if path=='escalation':
                        execution=policy_row(db,rid);record['reason']=execution['class_label'];expected=policy.choose({'request':{'messages':[{'content':prompt(case)}]},'targets':[dict(t,keyConfigured=True) for t in targets]},cases)
                        if execution['outcome']!='selected' or execution['selected_candidate_index']!=expected['targetIndex'] or provider!=('local','datacenter','frontier')[expected['targetIndex']] or record['reason']!=expected['classLabel']:raise RuntimeError('routing outcome mismatch')
                    partial.write(json.dumps({'phase':'validated',**record})+'\n');partial.flush();rows.append(record)
        result={'backend':a.backend,'note':'Synthetic labels and exact answers only; local/DC hardware costs excluded; provider rates are estimates, not invoices.',
                'hosted_attempts':relay.attempts if relay else 0,'results':rows}
        print(json.dumps(result,indent=2))

if __name__=='__main__':main()
