#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Compare repeated prefixes under pinned and deliberately spread routing."""
import argparse
from contextlib import ExitStack, nullcontext
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
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import Request, urlopen
import yaml

ROOT=Path(__file__).resolve().parents[2]
MODELS={'a':'accounts/fireworks/models/deepseek-v4p1-flash-1017', 'b':'accounts/fireworks/models/deepseek-v4p1-flash'}


def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path);obj=importlib.util.module_from_spec(spec);spec.loader.exec_module(obj);return obj


class FakeUpstream(BaseHTTPRequestHandler):
    cache=set()
    def log_message(self,*_): pass
    def do_POST(self):
        body=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        key=(body['model'],body['messages'][0]['content'][:32])
        cached=8000 if key in self.cache else 0; self.cache.add(key)
        response=json.dumps({'id':'mock','object':'chat.completion','created':0,'model':body['model'],
            'choices':[{'index':0,'message':{'role':'assistant','content':'ok'},'finish_reason':'stop'}],
            'usage':{'prompt_tokens':10000,'completion_tokens':20,'total_tokens':10020,'prompt_tokens_details':{'cached_tokens':cached}}}).encode()
        self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(response)));self.end_headers();self.wfile.write(response)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--router',type=Path,required=True);p.add_argument('--config',type=Path,default=Path(__file__).with_name('config.example.json'))
    p.add_argument('--backend',choices=('mock','fireworks','local'),default='mock');p.add_argument('--state-dir',type=Path)
    p.add_argument('--local-url');p.add_argument('--local-model-a');p.add_argument('--local-model-b')
    a=p.parse_args();s=json.loads(a.config.read_text())
    models=dict(MODELS)
    if a.backend=='local':
        u=urlparse(a.local_url or '')
        if u.scheme!='http' or u.hostname!='127.0.0.1' or u.username or u.password or u.path!='/v1' or u.query or u.fragment or not a.local_model_a or not a.local_model_b or a.local_model_a==a.local_model_b: p.error('local mode needs two distinct model IDs on a loopback /v1 server')
        models={'a':a.local_model_a,'b':a.local_model_b}
    if s['prompt_repetitions']!=1200: p.error('this bounded smoke requires 1200 synthetic repetitions')
    if a.backend=='fireworks' and not os.environ.get('FIREWORKS_API_KEY'): p.error('FIREWORKS_API_KEY required')
    if a.backend!='mock' and (not a.state_dir or not a.state_dir.is_absolute()): p.error('non-mock run requires private absolute state-dir')
    if a.state_dir: a.state_dir.mkdir(mode=0o700,parents=False,exist_ok=False)
    m=module('local_pi',ROOT/'scripts/local_pi_instance.py');demo=module('tokenomics',ROOT/'examples/tokenomics/run.py')
    FakeUpstream.cache=set();upstream=ThreadingHTTPServer(('127.0.0.1',0),FakeUpstream);thread=threading.Thread(target=upstream.serve_forever,daemon=True);thread.start();process=None
    try:
        with (nullcontext(a.state_dir) if a.state_dir else tempfile.TemporaryDirectory(prefix='router-kv-')) as temp:
            state=Path(temp);port=demo.port();base=f'http://127.0.0.1:{port}'
            token='rtr_metrum_eval_cache_dev_k424_'+secrets.token_urlsafe(24)
            cfg=m.build_config(out_dir=state,listen=f'127.0.0.1:{port}',id_key_hex=secrets.token_hex(64),token=token)
            cfg['server']['identifiers']={'mode':'passthrough'};cfg['server']['admin_auth']={'basic':{'enabled':False,'users':[]}};cfg['server']['admin_reports']={'enabled':False};cfg['server']['default_model_group']='pinned'
            metadata={}
            for key in ('a','b'):
                metadata[key]={'model':models[key],'tier':'coding','input_modalities':['text'],'output_modalities':['text'],'input_price_per_million_usd':s['input_price_per_million_usd'],
                  'cached_input_price_per_million_usd':s['cached_input_price_per_million_usd'],'output_price_per_million_usd':s['output_price_per_million_usd'],'pricing_source':s['price_source_'+key],'pricing_updated_at':s['price_date']}
            provider={'base_url':f'http://127.0.0.1:{upstream.server_port}/v1','dialect':'openai-chat','api_key':'mock-only','models':metadata}
            if a.backend=='fireworks':provider.update(base_url='https://api.fireworks.ai/inference/v1',api_key='${FIREWORKS_API_KEY}',api_key_env='FIREWORKS_API_KEY')
            if a.backend=='local':
                provider.update(base_url=a.local_url,api_key='unused-local')
                for model in metadata.values():
                    model.update(input_price_per_million_usd=0,cached_input_price_per_million_usd=0,output_price_per_million_usd=0,pricing_source='https://example.invalid/local-excludes-compute')
            cfg['providers']={'backend':provider};cfg['models']={}
            for group,key in [('pinned','a'),('spread-a','a'),('spread-b','b')]:
                cfg['models'][group]={'strategy':'static','targets':[{'provider':'backend','model_ref':key}]}
            cfg['callers'][0]['allow']=['pinned','spread-a','spread-b']
            config=state/'config.yaml';config.write_text(yaml.safe_dump(cfg,sort_keys=False));config.chmod(0o600)
            with (state/'router.log').open('wb') as log,ExitStack() as cleanup:
                process=subprocess.Popen([str(a.router.resolve()),'--config',str(config)],stdout=log,stderr=subprocess.STDOUT);cleanup.callback(demo.stop,process)
                for _ in range(150):
                    if process.poll() is not None:raise RuntimeError('router startup failed')
                    try:
                        with urlopen(base+'/readyz',timeout=1) as r:
                            if r.status==200:break
                    except (URLError,TimeoutError):pass
                    time.sleep(.1)
                else:raise RuntimeError('router readiness timeout')
                run_id=secrets.token_hex(8); requests=[]
                for arm,groups in [('pinned',['pinned']*4),('spread',['spread-a','spread-b','spread-a','spread-b'])]:
                    prefix=f'case-{arm}-{run_id}: ' + ('This synthetic document describes an imaginary product and contains no private data. '*s['prompt_repetitions'])
                    for group in groups:
                        body=json.dumps({'model':group,'messages':[{'role':'user','content':prefix}],'max_tokens':16}).encode()
                        if len(body)>120000:raise RuntimeError('prompt-size bound exceeded')
                        req=Request(base+'/v1/chat/completions',data=body,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
                        try:
                            with urlopen(req,timeout=120) as r:r.read();requests.append((arm,group,r.headers.get('X-Request-Id')))
                        except HTTPError as e:raise RuntimeError(f'request failed ({e.code}); reconcile before retry') from None
                rows=[]
                for _ in range(100):
                    with sqlite3.connect(state/'usage.sqlite') as db:
                        rows=[db.execute('select requested_model,target_model,status,attempts,fallback_used,input_tokens,cached_input_tokens,output_tokens,total_cost_usd,usage_source from request_usage where request_id=?',(rid,)).fetchone() for _,_,rid in requests]
                    if all(r is not None for r in rows):break
                    time.sleep(.1)
                if any(r is None for r in rows):raise RuntimeError('missing usage; reconcile before retry')
                output=[]
                for (arm,group,_),r in zip(requests,rows):
                    target=models['b'] if group=='spread-b' else models['a']
                    if r[0]!=group or r[1]!=target or r[2:5]!=(200,1,0) or r[9]!='reported':raise RuntimeError('unexpected target, retry or unreported usage')
                    output.append({'arm':arm,'group':group,'model':r[1],'input_tokens':r[5],'cached_input_tokens':r[6],'output_tokens':r[7],'usage_cost_usd':r[8]})
                if a.backend=='mock' and [r['cached_input_tokens'] for r in output]!=[0,8000,8000,8000,0,0,8000,8000]:raise RuntimeError('unexpected synthetic cache behavior')
                print(json.dumps({'mode':a.backend,'note':'Provider-reported cached tokens; configured-price usage is not an invoice. Mock cache is simulated; local costs exclude compute.', 'requests':output},indent=2))
    finally:
        if process:demo.stop(process)
        upstream.shutdown();upstream.server_close();thread.join()


if __name__=='__main__':main()
