#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Route tagged synthetic requests to four experts and a general fallback."""
import argparse
import datetime
from contextlib import ExitStack, nullcontext
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import sqlite3
import subprocess
import tempfile
import threading
import time
from http.server import ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import Request, urlopen
import yaml
from policy import DOMAINS, Handler

ROOT=Path(__file__).resolve().parents[2]
TIERS=(*DOMAINS,'general')


def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path); obj=importlib.util.module_from_spec(spec); spec.loader.exec_module(obj); return obj


def loopback(url):
    u=urlparse(url or '')
    return u.scheme=='http' and u.hostname=='127.0.0.1' and not u.username and not u.password and not u.query and not u.fragment and u.path=='/v1'


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--router',type=Path,required=True); p.add_argument('--config',type=Path,default=Path(__file__).with_name('config.example.json'))
    p.add_argument('--backend',choices=('mock','local','fireworks'),default='mock'); p.add_argument('--state-dir',type=Path)
    a=p.parse_args(); settings=json.loads(a.config.read_text()); models=settings['models']
    if set(models)!=set(TIERS) or any(not isinstance(models[t],str) or not models[t] for t in TIERS): p.error('five model identifiers required')
    if a.backend!='mock' and (not a.state_dir or not a.state_dir.is_absolute()): p.error('non-mock backend requires private absolute state-dir')
    if a.backend=='local' and (set(settings.get('local_urls',{}))!=set(TIERS) or not all(loopback(settings['local_urls'][t]) for t in TIERS)): p.error('local URLs must all be loopback /v1 endpoints')
    if a.backend=='fireworks':
        if not os.environ.get('FIREWORKS_API_KEY'): p.error('FIREWORKS_API_KEY required')
        if any(not re.fullmatch(r'accounts/[A-Za-z0-9_.-]+/models/[A-Za-z0-9_.-]+',models[t]) for t in TIERS) or len(set(models.values()))!=5: p.error('configure five distinct Fireworks model IDs')
        prices=settings.get('pricing',{})
        if set(prices)!=set(TIERS): p.error('configure dated prices for all models')
        reserve=0.0
        for t in TIERS:
            v=prices[t]
            if not all(type(v.get(k)) in (int,float) and 0<v[k]<=100 for k in ('input_per_million_usd','output_per_million_usd')) or not isinstance(v.get('source'),str) or not v['source'].startswith('https://') or type(v.get('cached_input_per_million_usd')) not in (int,float) or not 0<=v['cached_input_per_million_usd']<=v['input_per_million_usd']: p.error('invalid dated price')
            try: datetime.date.fromisoformat(v.get('date',''))
            except (TypeError,ValueError): p.error('invalid price date')
            reserve+=(4000*v['input_per_million_usd']+20*v['output_per_million_usd'])/1_000_000
        if reserve>1: p.error('conservative token-price reservation exceeds $1')
    if a.state_dir: a.state_dir.mkdir(mode=0o700,parents=False,exist_ok=False)
    m=module('local_pi',ROOT/'scripts/local_pi_instance.py'); demo=module('tokenomics',ROOT/'examples/tokenomics/run.py'); secure=module('catalog_hash',ROOT/'examples/catalog/token_hash.py')
    upstream=ThreadingHTTPServer(('127.0.0.1',0),demo.Upstream); policy=ThreadingHTTPServer(('127.0.0.1',0),Handler)
    threads=[threading.Thread(target=s.serve_forever,daemon=True) for s in (upstream,policy)]
    for t in threads: t.start()
    process=None
    try:
        with (nullcontext(a.state_dir) if a.state_dir else tempfile.TemporaryDirectory(prefix='router-domain-')) as temp:
            state=Path(temp); port=demo.port(); base=f'http://127.0.0.1:{port}'
            token='rtr_metrum_eval_domains_dev_k424_'+secrets.token_urlsafe(24)
            cfg=m.build_config(out_dir=state,listen=f'127.0.0.1:{port}',id_key_hex=secrets.token_hex(64),token=token)
            secure.apply(cfg['callers'][0],secure.prepare(ROOT),token,state)
            cfg['server']['identifiers']={'mode':'passthrough'}; cfg['server']['admin_auth']={'basic':{'enabled':False,'users':[]}}; cfg['server']['admin_reports']={'enabled':False}; cfg['server']['default_model_group']='domains'
            providers={};targets=[]
            for tier in TIERS:
                url=f'http://127.0.0.1:{upstream.server_port}/v1'; key='mock-only'; extra={}
                price={'input_per_million_usd':0,'output_per_million_usd':0,'source':'https://example.invalid/unpriced','date':'2026-10-28'}
                if a.backend=='local': url=settings['local_urls'][tier]
                if a.backend=='fireworks': url='https://api.fireworks.ai/inference/v1'; key='${FIREWORKS_API_KEY}'; extra={'api_key_env':'FIREWORKS_API_KEY'}; price=settings['pricing'][tier]
                model={'model':models[tier],'tier':tier,'input_modalities':['text'],'output_modalities':['text'],'input_price_per_million_usd':price['input_per_million_usd'],'cached_input_price_per_million_usd':price.get('cached_input_per_million_usd',0),'output_price_per_million_usd':price['output_per_million_usd'],'pricing_source':price['source'],'pricing_updated_at':price['date']}
                providers[tier]={'base_url':url,'dialect':'openai-chat','api_key':key,'models':{'target':model},**extra}
                targets.append({'provider':tier,'model_ref':'target','tier':tier,'weight':1})
            cfg['providers']=providers
            cfg['models']={'domains':{'strategy':'external','external_policy':{'url':f'http://127.0.0.1:{policy.server_port}/route','allow_hosts':['127.0.0.1'],'mode':'enforce','timeout_ms':500,'max_response_bytes':65536,'on_error':'fail_closed','include_request':True},'targets':targets}}
            cfg['callers'][0]['allow']=['domains']
            config=state/'config.yaml'; config.write_text(yaml.safe_dump(cfg,sort_keys=False));config.chmod(0o600)
            with (state/'router.log').open('wb') as log, ExitStack() as cleanup:
                process=subprocess.Popen([str(a.router.resolve()),'--config',str(config)],stdout=log,stderr=subprocess.STDOUT); cleanup.callback(demo.stop,process)
                for _ in range(150):
                    if process.poll() is not None: raise RuntimeError('router startup failed')
                    try:
                        with urlopen(base+'/readyz',timeout=1) as r:
                            if r.status==200: break
                    except (URLError,TimeoutError): pass
                    time.sleep(.1)
                else: raise RuntimeError('router readiness timeout')
                requests=[]
                for tier in TIERS:
                    text='[domain:'+tier+'] Synthetic task.' if tier!='general' else 'Unknown domain synthetic task.'
                    body=json.dumps({'model':'domains','messages':[{'role':'user','content':text}],'max_tokens':20}).encode()
                    req=Request(base+'/v1/chat/completions',data=body,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
                    try:
                        with urlopen(req,timeout=60) as r: r.read(); requests.append(r.headers.get('X-Request-Id'))
                    except HTTPError as e: raise RuntimeError(f'request failed ({e.code}); reconcile before retry') from None
                rows=[]
                for _ in range(100):
                    with sqlite3.connect(state/'usage.sqlite') as db:
                        rows=[db.execute('select target_provider,target_model,status,attempts,fallback_used,input_tokens,output_tokens,total_cost_usd,usage_source from request_usage where request_id=?',(rid,)).fetchone() for rid in requests]
                    if all(r is not None for r in rows): break
                    time.sleep(.1)
                if any(r is None for r in rows): raise RuntimeError('usage rows missing; reconcile before retry')
                for row,tier in zip(rows,TIERS):
                    if row[0]!=tier or row[1]!=models[tier] or row[2:5]!=(200,1,0) or row[8]!='reported': raise RuntimeError('unexpected route, retry or incomplete usage')
                print(json.dumps({'mode':a.backend,'note':'Explicit tags, not classification or quality evidence. Local and mock costs exclude compute.',
                    'routes':[{'declared_domain':tier,'target_model':r[1],'input_tokens':r[5],'output_tokens':r[6],'usage_cost_usd':r[7]} for tier,r in zip(TIERS,rows)]},indent=2))
    finally:
        if process: demo.stop(process)
        for s in (policy,upstream): s.shutdown();s.server_close()
        for t in threads:t.join()


if __name__=='__main__': main()
