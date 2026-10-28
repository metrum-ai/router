#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Exercise length-based escalation with mock or explicit mixed backends."""
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
from http.server import ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import Request, urlopen
import yaml

from policy import Handler
ROOT=Path(__file__).resolve().parents[2]


def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path); obj=importlib.util.module_from_spec(spec); spec.loader.exec_module(obj); return obj


def loopback(url):
    u=urlparse(url or '')
    return u.scheme=='http' and u.hostname=='127.0.0.1' and not u.username and not u.password and not u.query and not u.fragment and u.path=='/v1'


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--router',type=Path,required=True)
    p.add_argument('--config',type=Path,default=Path(__file__).with_name('config.example.json'))
    p.add_argument('--backend',choices=('mock','mixed'),default='mock')
    p.add_argument('--local-url'); p.add_argument('--local-model'); p.add_argument('--datacenter-url'); p.add_argument('--datacenter-model')
    p.add_argument('--state-dir',type=Path)
    a=p.parse_args(); settings=json.loads(a.config.read_text())
    if settings['cheap_max_chars']!=200 or settings['datacenter_max_chars']!=1000: p.error('this fixed smoke requires the documented thresholds')
    if a.backend=='mixed':
        if not loopback(a.local_url) or not loopback(a.datacenter_url) or not a.local_model or not a.datacenter_model: p.error('mixed mode requires two named loopback /v1 backends')
        if not os.environ.get('OPENAI_API_KEY'): p.error('OPENAI_API_KEY required')
        if not a.state_dir or not a.state_dir.is_absolute(): p.error('mixed mode requires a private absolute state-dir for reconciliation')
    if a.state_dir: a.state_dir.mkdir(mode=0o700,parents=False,exist_ok=False)
    m=module('local_pi',ROOT/'scripts/local_pi_instance.py')
    demo=module('tokenomics',ROOT/'examples/tokenomics/run.py')
    upstream=ThreadingHTTPServer(('127.0.0.1',0),demo.Upstream)
    policy=ThreadingHTTPServer(('127.0.0.1',0),Handler)
    threads=[threading.Thread(target=s.serve_forever,daemon=True) for s in (upstream,policy)]
    for t in threads: t.start()
    process=None
    try:
        with (nullcontext(a.state_dir) if a.state_dir else tempfile.TemporaryDirectory(prefix='router-escalation-')) as temp:
            state=Path(temp); port=demo.port(); base=f'http://127.0.0.1:{port}'
            token='rtr_metrum_eval_escalation_dev_k424_'+secrets.token_urlsafe(24)
            cfg=m.build_config(out_dir=state,listen=f'127.0.0.1:{port}',id_key_hex=secrets.token_hex(64),token=token)
            cfg['server']['identifiers']={'mode':'passthrough'}; cfg['server']['admin_auth']={'basic':{'enabled':False,'users':[]}}
            cfg['server']['admin_reports']={'enabled':False}; cfg['server']['default_model_group']='escalation'
            names={tier:settings[tier+'_model'] for tier in ('local','datacenter','frontier')}
            if a.backend=='mixed': names={'local':a.local_model,'datacenter':a.datacenter_model,'frontier':'gpt-6.1-sol'}
            providers={}; targets=[]
            for tier in ('local','datacenter','frontier'):
                url=f'http://127.0.0.1:{upstream.server_port}/v1'; api_key='mock-only'; model={'model':names[tier],'tier':tier,'input_modalities':['text'],'output_modalities':['text'],'input_price_per_million_usd':0,'output_price_per_million_usd':0,'pricing_source':'https://example.invalid/excludes-compute','pricing_updated_at':'2026-10-28'}
                extra={}
                if a.backend=='mixed':
                    if tier=='local': url=a.local_url
                    elif tier=='datacenter': url=a.datacenter_url
                    else:
                        url='https://api.openai.com/v1'; api_key='${OPENAI_API_KEY}'; extra={'api_key_env':'OPENAI_API_KEY'}
                        model.update(input_price_per_million_usd=2.0,cached_input_price_per_million_usd=.1,output_price_per_million_usd=10.0,pricing_source='https://developers.openai.com/api/docs/models/gpt-6.1-sol')
                dialect='openai-responses' if a.backend=='mixed' and tier=='frontier' else 'openai-chat'
                providers[tier]={'base_url':url,'dialect':dialect,'api_key':api_key,'models':{'target':model},**extra}
                target={'provider':tier,'model_ref':'target','tier':tier,'weight':1}
                if a.backend=='mixed' and tier=='frontier':
                    target['bridges']={'chat_to_responses':{'enabled':True,'text':True,'tools':True,'tool_choice':True}}
                targets.append(target)
            cfg['providers']=providers
            cfg['models']={'escalation':{'strategy':'external','external_policy':{'url':f'http://127.0.0.1:{policy.server_port}/route','allow_hosts':['127.0.0.1'],'mode':'enforce','timeout_ms':500,'max_response_bytes':65536,'on_error':'fail_closed','include_request':False},'targets':targets}}
            cfg['callers'][0]['allow']=['escalation']
            config=state/'config.yaml'; config.write_text(yaml.safe_dump(cfg,sort_keys=False)); config.chmod(0o600)
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
                for length in (50,500,1500):
                    body=json.dumps({'model':'escalation','messages':[{'role':'user','content':'x'*length}],'max_tokens':20}).encode()
                    req=Request(base+'/v1/chat/completions',data=body,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
                    try:
                        with urlopen(req,timeout=60) as r:
                            r.read(); requests.append(r.headers.get('X-Request-Id'))
                    except HTTPError as e: raise RuntimeError(f'request failed ({e.code}); reconcile usage before retry') from None
                rows=[]
                for _ in range(100):
                    with sqlite3.connect(state/'usage.sqlite') as db:
                        rows=[db.execute('select target_provider,target_model,status,attempts,fallback_used,input_tokens,output_tokens,total_cost_usd,usage_source from request_usage where request_id=?',(rid,)).fetchone() for rid in requests]
                    if all(r is not None for r in rows): break
                    time.sleep(.1)
                if any(r is None for r in rows): raise RuntimeError('usage rows missing; reconcile before retry')
                for row,tier in zip(rows,('local','datacenter','frontier')):
                    if row[0]!=tier or row[1]!=names[tier] or row[2:5]!=(200,1,0) or row[8]!='reported': raise RuntimeError('unexpected route, retry or incomplete usage')
                print(json.dumps({'mode':a.backend,'note':'length heuristic only; local token costs exclude compute, not provider invoices',
                                  'routes':[{'prompt_chars':length,'tier':r[0],'target_model':r[1],'input_tokens':r[5],'output_tokens':r[6],'usage_cost_usd':r[7]} for length,r in zip((50,500,1500),rows)]},indent=2))
    finally:
        if process: demo.stop(process)
        for s in (policy,upstream): s.shutdown(); s.server_close()
        for t in threads: t.join()


if __name__=='__main__': main()
