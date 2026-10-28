#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Loopback governance demo using synthetic keys, PII and a mock upstream."""
import argparse
import base64
from contextlib import ExitStack, nullcontext
import importlib.util
import json
import os
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
import secrets
import sqlite3
import subprocess
import tempfile
import threading
import time
from urllib.error import HTTPError,URLError
from urllib.parse import urlparse
from urllib.request import Request,urlopen
import yaml

ROOT=Path(__file__).resolve().parents[2]
SYNTHETIC_EMAIL='synthetic-person@example.invalid'


def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path);obj=importlib.util.module_from_spec(spec);spec.loader.exec_module(obj);return obj


class Upstream(BaseHTTPRequestHandler):
    seen=[]
    def log_message(self,*_):pass
    def do_POST(self):
        body=self.rfile.read(int(self.headers['Content-Length'])).decode()
        self.seen.append({'no_raw_pii':SYNTHETIC_EMAIL not in body,'redacted':'EMAIL' in body})
        data=json.dumps({'id':'mock','object':'chat.completion','created':0,'model':'mock-model','choices':[{'index':0,'message':{'role':'assistant','content':'ok'},'finish_reason':'stop'}], 'usage':{'prompt_tokens':100,'completion_tokens':20,'total_tokens':120}}).encode()
        self.send_response(200);self.send_header('Content-Length',str(len(data)));self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(data)


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--router',type=Path,required=True);p.add_argument('--config',type=Path,default=Path(__file__).with_name('config.example.json'))
    p.add_argument('--backend',choices=('mock','local','fireworks'),default='mock');p.add_argument('--state-dir',type=Path);p.add_argument('--local-url');p.add_argument('--local-model')
    a=p.parse_args();s=json.loads(a.config.read_text())
    if a.backend!='mock' and (not a.state_dir or not a.state_dir.is_absolute()):p.error('non-mock run requires private absolute state-dir')
    if a.backend=='fireworks' and not os.environ.get('FIREWORKS_API_KEY'):p.error('FIREWORKS_API_KEY required')
    if a.backend=='local':
        u=urlparse(a.local_url or '')
        if u.scheme!='http' or u.hostname!='127.0.0.1' or u.username or u.password or u.path!='/v1' or u.query or u.fragment or not a.local_model:p.error('local mode needs a model and loopback /v1 server')
    if a.state_dir:a.state_dir.mkdir(mode=0o700,parents=False,exist_ok=False)
    if s['daily_requests']!=1 or s['daily_tokens']!=10000 or s['rate_rpm']!=60:p.error('use the checked-in bounded demo limits')
    m=module('local_pi',ROOT/'scripts/local_pi_instance.py');demo=module('tokenomics',ROOT/'examples/tokenomics/run.py');policy_module=module('policy',ROOT/'examples/escalation-continuum/policy.py')
    Upstream.seen=[];upstream=ThreadingHTTPServer(('127.0.0.1',0),Upstream);policy=ThreadingHTTPServer(('127.0.0.1',0),policy_module.Handler)
    threads=[threading.Thread(target=x.serve_forever,daemon=True) for x in (upstream,policy)]
    for t in threads:t.start()
    process=None
    try:
        with (nullcontext(a.state_dir) if a.state_dir else tempfile.TemporaryDirectory(prefix='router-governance-')) as temp:
            state=Path(temp);port=demo.port();base=f'http://127.0.0.1:{port}'
            tokens={u:f'rtr_metrum_{u}_example-project_dev_k424_'+secrets.token_urlsafe(24) for u in ('scoped','rate','tokenlimited')}
            cfg=m.build_config(out_dir=state,listen=f'127.0.0.1:{port}',id_key_hex=secrets.token_hex(64),token=tokens['scoped'])
            cfg['server']['identifiers']={'mode':'passthrough'};cfg['server']['default_model_group']=s['allowed_group'];cfg['server']['decision_telemetry']={'enabled':True}
            cfg['server']['admin_auth']['basic']['users'][0]['domain']='example-project/dev'
            cfg['server']['admin_auth']['authorization']['policy']=['p, basic:admin, example-project/dev, admin:reports, read|export|drilldown','p, basic:admin, example-project/dev, admin:security_reports, read|export']
            cfg['server']['admin_reports']['security']={'enabled':True,'retention_days':7}
            meta={'model':'mock-model','tier':'local','input_modalities':['text'],'output_modalities':['text']}
            provider={'base_url':f'http://127.0.0.1:{upstream.server_port}/v1','dialect':'openai-chat','api_key':'mock-only'}
            if a.backend=='local':meta['model']=a.local_model;provider.update(base_url=a.local_url,api_key='unused-local')
            if a.backend=='fireworks':
                meta.update(model='accounts/fireworks/models/deepseek-v4p1-flash-1017',input_price_per_million_usd=s['fireworks_input_per_million_usd'],cached_input_price_per_million_usd=s['fireworks_cached_input_per_million_usd'],output_price_per_million_usd=s['fireworks_output_per_million_usd'],pricing_source=s['fireworks_pricing_source'],pricing_updated_at=s['fireworks_pricing_date'])
                provider.update(base_url='https://api.fireworks.ai/inference/v1',api_key='${FIREWORKS_API_KEY}',api_key_env='FIREWORKS_API_KEY')
            provider['models']={'target':meta};cfg['providers']={'mock':provider}
            group={'strategy':'external','external_policy':{'url':f'http://127.0.0.1:{policy.server_port}/route','allow_hosts':['127.0.0.1'],'mode':'enforce','timeout_ms':500,'max_response_bytes':65536,'on_error':'fail_closed','include_request':False},
                   'pii_filter':{'enabled':True,'mode':'redact_only','restore_response':False,'rules':[{'name':s['pii_rule_name'],'expression':s['pii_expression'],'placeholder_prefix':s['pii_placeholder_prefix']}]},
                   'targets':[{'provider':'mock','model_ref':'target','tier':'local','weight':1}]}
            cfg['models']={s['allowed_group']:group,s['restricted_group']:group}
            prototype=cfg['callers'][0];cfg['callers']=[]
            for user in tokens:
                caller=dict(prototype);caller.update(id=user,allow=[s['allowed_group']]);caller.update(m.caller_token_fields(tokens[user]))
                caller['rate']={'rpm':1 if user=='rate' else s['rate_rpm'],'tpm':100000,'concurrent':10}
                caller['quota']={'day':{'requests':s['daily_requests'] if user=='scoped' else 100,'tokens':10 if user=='tokenlimited' else s['daily_tokens']},'month':{'requests':0,'tokens':100000},'soft_pct':80}
                cfg['callers'].append(caller)
            path=state/'config.yaml';path.write_text(yaml.safe_dump(cfg,sort_keys=False));path.chmod(0o600)
            env=dict(os.environ);env['LOCAL_PI_ADMIN_PASSWORD_HASH']=m.DEMO_ADMIN_PASSWORD_HASH
            with (state/'router.log').open('wb') as log,ExitStack() as cleanup:
                process=subprocess.Popen([str(a.router.resolve()),'--config',str(path)],stdout=log,stderr=subprocess.STDOUT,env=env);cleanup.callback(demo.stop,process)
                for _ in range(150):
                    if process.poll() is not None:raise RuntimeError('router startup failed')
                    try:
                        with urlopen(base+'/readyz',timeout=1) as r:
                            if r.status==200:break
                    except (URLError,TimeoutError):pass
                    time.sleep(.1)
                else:raise RuntimeError('router readiness timeout')
                def request(user,group,text='synthetic test'):
                    data=json.dumps({'model':group,'messages':[{'role':'user','content':text}],'max_tokens':20}).encode()
                    req=Request(base+'/v1/chat/completions',data=data,headers={'Authorization':'Bearer '+tokens[user],'Content-Type':'application/json'})
                    try:
                        with urlopen(req,timeout=15) as r:r.read();return r.status
                    except HTTPError as e:return e.code
                statuses=[request('scoped',s['allowed_group'],SYNTHETIC_EMAIL),request('scoped',s['restricted_group']),request('scoped',s['allowed_group']),request('tokenlimited',s['allowed_group']),request('rate',s['allowed_group']),request('rate',s['allowed_group'])]
                if statuses!=[200,403,429,429,200,429]:raise RuntimeError(f'unexpected control statuses {statuses}')
                if a.backend=='mock' and (len(Upstream.seen)!=2 or not Upstream.seen[0]['no_raw_pii'] or not Upstream.seen[0]['redacted']):raise RuntimeError('PII redaction control failed')
                reasons=[];events=0;usage=[]
                for _ in range(80):
                    with sqlite3.connect(state/'usage.sqlite') as db:
                        reasons=db.execute('select class_label from request_routing_decisions').fetchall()
                        events=db.execute('select count(*) from security_access_events').fetchone()[0]
                        usage=db.execute('select caller_id,pii_filter_applied,input_tokens,cached_input_tokens,output_tokens,total_cost_usd,attempts,fallback_used,usage_source from request_usage where status=200 order by caller_id').fetchall()
                    if len(reasons)>=2 and events>=2 and len(usage)==2:break
                    time.sleep(.1)
                if len(reasons)<2 or events<2 or not all(x[0]=='length:local' for x in reasons):raise RuntimeError('route decision or security telemetry missing')
                if len(usage)!=2 or not all(row[6]==1 and not row[7] and row[8]=='reported' for row in usage) or not any(row[0]=='scoped' and row[1] for row in usage):raise RuntimeError('missing PII or usage telemetry')
                ordinary=Request(base+'/admin/reports/security/export.csv?since=24h',headers={'Authorization':'Bearer '+tokens['scoped']})
                try:
                    with urlopen(ordinary,timeout=15) as r: ordinary_status=r.status
                except HTTPError as e: ordinary_status=e.code
                if ordinary_status != 403: raise RuntimeError('ordinary caller unexpectedly accessed audit export')
                auth='Basic '+base64.b64encode(b'admin:admin').decode()
                req=Request(base+'/admin/reports/security/export.csv?since=24h',headers={'Authorization':auth})
                with urlopen(req,timeout=15) as r:csv=r.read().decode();status=r.status
                if status!=200 or len(csv.splitlines())<2 or SYNTHETIC_EMAIL in csv or any(v in csv for v in tokens.values()):raise RuntimeError('unsafe or empty audit export')
                print(json.dumps({'mode':a.backend,'statuses':statuses,'pii_applied_recorded':True,'mock_upstream_redaction_verified':a.backend=='mock','routing_reason':'length:local','audit_export':True,'ordinary_export_status':ordinary_status,'security_event_count':events,'usage':[{'caller':r[0],'input_tokens':r[2],'cached_input_tokens':r[3],'output_tokens':r[4],'usage_cost_usd':r[5]} for r in usage]},indent=2))
    finally:
        if process:demo.stop(process)
        for server in (policy,upstream):server.shutdown();server.server_close()
        for t in threads:t.join()


if __name__=='__main__':main()
