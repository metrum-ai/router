#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Run a credential-free, local mock-upstream tokenomics demonstration."""
from __future__ import annotations
import argparse
from contextlib import ExitStack, nullcontext
import importlib.util
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import secrets
import socket
import sqlite3
import subprocess
import tempfile
import threading
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import Request, urlopen

import yaml

ROOT = Path(__file__).resolve().parents[2]


class Upstream(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        response = json.dumps({'id':'mock-response', 'object':'chat.completion', 'created':0,
                'model':request['model'], 'choices':[{'index':0,'message':{'role':'assistant','content':'ok'},'finish_reason':'stop'}],
                'usage':{'prompt_tokens':100,'completion_tokens':20,'total_tokens':120}}).encode()
        self.send_response(200); self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(response))); self.end_headers(); self.wfile.write(response)


def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0)); return s.getsockname()[1]


def stop(process):
    if process.poll() is None:
        process.terminate()
        try: process.wait(timeout=10)
        except subprocess.TimeoutExpired: process.kill(); process.wait()


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--router', type=Path, required=True, help='path to built router binary')
    p.add_argument('--config', type=Path, default=Path(__file__).with_name('config.example.json'))
    p.add_argument('--backend', choices=('mock','fireworks','local'), default='mock')
    p.add_argument('--local-url', help='self-hosted OpenAI-compatible server root on loopback, including /v1')
    p.add_argument('--local-model', default='local-model')
    p.add_argument('--state-dir', type=Path, help='new private state directory, required outside mock mode')
    a=p.parse_args(); settings=json.loads(a.config.read_text())
    if a.backend != 'mock' and not a.state_dir: p.error('non-mock runs require --state-dir for error reconciliation')
    if a.state_dir and not a.state_dir.is_absolute(): p.error('state-dir must be absolute')
    if a.state_dir: a.state_dir.mkdir(mode=0o700, parents=False, exist_ok=False)
    if a.backend == 'fireworks' and not os.environ.get('FIREWORKS_API_KEY'): p.error('FIREWORKS_API_KEY required for the hosted backend')
    if a.backend == 'local':
        u = urlparse(a.local_url or '')
        if u.scheme != 'http' or u.hostname != '127.0.0.1' or u.username or u.password or u.query or u.fragment or u.path != '/v1':
            p.error('local backend requires an http://127.0.0.1:PORT/v1 URL')
    if settings['alice_daily_request_limit'] != 1 or settings['bob_daily_request_limit'] != 10:
        p.error('this demonstration requires its checked-in quota settings')
    spec=importlib.util.spec_from_file_location('local_pi_instance', ROOT/'scripts/local_pi_instance.py')
    m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
    upstream=ThreadingHTTPServer(('127.0.0.1',0),Upstream)
    thread=threading.Thread(target=upstream.serve_forever,daemon=True); thread.start()
    process=None
    try:
        with (nullcontext(a.state_dir) if a.state_dir else tempfile.TemporaryDirectory(prefix='router-tokenomics-')) as tmp:
            state=Path(tmp); router_port=port(); base=f'http://127.0.0.1:{router_port}'
            tokens={user:f'rtr_metrum_{user}_project_dev_k424_'+secrets.token_urlsafe(24) for user in ('alice','bob')}
            cfg=m.build_config(out_dir=state,listen=f'127.0.0.1:{router_port}',id_key_hex=secrets.token_hex(64),token=tokens['alice'])
            cfg['server']['identifiers']={'mode':'passthrough'}
            cfg['server']['admin_auth']={'basic':{'enabled':False,'users':[]}}
            cfg['server']['admin_reports']={'enabled':False}
            cfg['server']['default_model_group']='demo'
            cfg['users']=[{'id':u,'name':u,'type':'service_account','status':'active'} for u in tokens]
            cfg['projects']=[{'id':'project','name':'project','status':'active'}]
            cfg['project_memberships']=[{'user_id':u,'project':'project','role':'developer','status':'active'} for u in tokens]
            model={'model':'mock-model','tier':'mock','input_modalities':['text'],'output_modalities':['text'],
                   'input_price_per_million_usd':settings['mock_input_price_per_million_usd'],
                   'output_price_per_million_usd':settings['mock_output_price_per_million_usd'],
                   'pricing_source':settings['mock_pricing_source'],'pricing_updated_at':settings['mock_pricing_date']}
            provider={'base_url':f'http://127.0.0.1:{upstream.server_port}/v1','dialect':'openai-chat','api_key':'mock-only'}
            if a.backend == 'fireworks':
                provider.update(base_url='https://api.fireworks.ai/inference/v1',api_key='${FIREWORKS_API_KEY}',api_key_env='FIREWORKS_API_KEY')
                model.update(model='accounts/fireworks/models/deepseek-v4p1-flash-1017',tier='coding',
                             input_price_per_million_usd=settings['fireworks_input_price_per_million_usd'],
                             cached_input_price_per_million_usd=settings['fireworks_cached_input_price_per_million_usd'],
                             output_price_per_million_usd=settings['fireworks_output_price_per_million_usd'],
                             pricing_source=settings['fireworks_pricing_source'],pricing_updated_at=settings['fireworks_pricing_date'])
            elif a.backend == 'local':
                provider.update(base_url=a.local_url,api_key='unused-local')
                model.update(model=a.local_model,tier='local',input_price_per_million_usd=0,output_price_per_million_usd=0,
                             pricing_source='https://example.invalid/local-excludes-compute',pricing_updated_at=settings['mock_pricing_date'])
            provider['models']={'target':model}; cfg['providers']={'backend':provider}
            cfg['models']={'demo':{'strategy':'static','targets':[{'provider':'backend','model_ref':'target'}]}}
            prototype=cfg['callers'][0]; cfg['callers']=[]
            for user in tokens:
                caller=dict(prototype); caller.update(id=user,owner_user=user,project='project',environment='dev',allow=['demo'])
                caller.update(m.caller_token_fields(tokens[user])); caller['quota']={'day':{'requests':settings[user+'_daily_request_limit'],'tokens':100000},'month':{'requests':0,'tokens':100000},'soft_pct':80}
                cfg['callers'].append(caller)
            config=state/'config.yaml'; config.write_text(yaml.safe_dump(cfg,sort_keys=False)); config.chmod(0o600)
            with (state/'router.log').open('wb') as log, ExitStack() as cleanup:
                process=subprocess.Popen([str(a.router.resolve()),'--config',str(config)],stdout=log,stderr=subprocess.STDOUT)
                cleanup.callback(stop, process)
                for _ in range(150):
                    if process.poll() is not None: raise RuntimeError('router failed to start; check config and binary')
                    try:
                        with urlopen(base+'/readyz',timeout=1) as r:
                            if r.status==200: break
                    except (URLError,TimeoutError): pass
                    time.sleep(.1)
                else: raise RuntimeError('router readiness timeout')
                def call(user):
                    body=json.dumps({'model':'demo','messages':[{'role':'user','content':'synthetic test'}],'max_tokens':20}).encode()
                    request=Request(base+'/v1/chat/completions',data=body,headers={'Authorization':'Bearer '+tokens[user],'Content-Type':'application/json'})
                    try:
                        with urlopen(request,timeout=15) as response: return response.status
                    except HTTPError as e: return e.code
                statuses=[call('alice'),call('bob'),call('alice')]
                if statuses != [200,200,429]: raise RuntimeError(f'unexpected demo statuses {statuses}')
                for _ in range(50):
                    with sqlite3.connect(state/'usage.sqlite') as db:
                        found=db.execute('select caller_user,caller_project,token_id,resolved_group,input_tokens,output_tokens,total_cost_usd,usage_source,attempts,fallback_used,cached_input_tokens from request_usage where status=200 order by caller_user').fetchall()
                        rejected=db.execute('select caller_user,attempts from request_usage where status=429').fetchall()
                    if len(found)==2 and len(rejected)==1: break
                    time.sleep(.1)
                if len(found)!=2 or rejected != [('alice', 0)]: raise RuntimeError('usage rows missing or quota rejection reached the upstream')
                for row, user in zip(found, ('alice','bob')):
                    if row[0]!=user or row[1]!='project' or row[3]!='demo' or row[4]<0 or row[5]<0 or row[6]<0 or row[7]!='reported' or row[8]!=1 or row[9]: raise RuntimeError('unexpected usage attribution, incomplete usage, retry or fallback')
                    if a.backend=='mock' and (row[4:6]!=(100,20) or abs(row[6]-.00016)>1e-10): raise RuntimeError('unexpected mock tokens or cost')
                labels={'mock':'synthetic-mock-not-provider-billing','fireworks':'hosted-usage-derived-not-invoice','local':'self-hosted-excludes-compute'}
                print(json.dumps({'mode':labels[a.backend],'statuses':statuses,'rejected_upstream_attempts':rejected[0][1],'attribution':[{'owner':r[0],'project':r[1],'key_id':r[2],'model_group':r[3],'input_tokens':r[4],'cached_input_tokens':r[10],'output_tokens':r[5],'usage_cost_usd':r[6]} for r in found]},indent=2))
    finally:
        if process: stop(process)
        upstream.shutdown(); upstream.server_close(); thread.join()


if __name__=='__main__': main()
