#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Compare three deterministic fakes through router shadow/enforce wiring."""
import argparse
from contextlib import ExitStack, nullcontext
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import secrets
import socket
import sys
import tempfile
import threading
from http.server import ThreadingHTTPServer
from urllib.parse import urlparse

ROOT=Path(__file__).resolve().parents[2];EXTERNAL=ROOT/'examples/external-routing-policy';HERE=Path(__file__).resolve().parent


def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path);obj=importlib.util.module_from_spec(spec);spec.loader.exec_module(obj);return obj


def free_port():
    # Random OS-assigned ports avoid collisions between concurrent demo runs.
    with socket.socket() as s:
        s.bind(('127.0.0.1',0));return s.getsockname()[1]


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--router',required=True,type=Path);p.add_argument('--config',type=Path,default=HERE/'config.example.json')
    p.add_argument('--backend',choices=('mock','local','fireworks'),default='mock');p.add_argument('--state-dir',type=Path);p.add_argument('--local-url');p.add_argument('--local-model')
    p.add_argument('--decisions',choices=('fake','external'),default='fake');p.add_argument('--d3-url');p.add_argument('--openjev-url');p.add_argument('--clef-url');args=p.parse_args()
    def loopback(url):
        u=urlparse(url or '');return u.scheme=='http' and u.hostname=='127.0.0.1' and not u.username and not u.password and not u.query and not u.fragment
    if (args.backend!='mock' or args.decisions!='fake') and not args.state_dir:p.error('use an absolute new private state directory')
    if args.state_dir:
        if not args.state_dir.is_absolute():p.error('state directory must be absolute')
        args.state_dir.mkdir(mode=0o700,parents=False,exist_ok=False)
    if args.backend=='local' and (not loopback(args.local_url) or urlparse(args.local_url).path!='/v1' or not args.local_model):p.error('local backend requires loopback /v1 URL and model')
    if args.backend=='fireworks' and not os.environ.get('FIREWORKS_API_KEY'):p.error('FIREWORKS_API_KEY required')
    if args.decisions=='external' and (not all(loopback(u) for u in (args.d3_url,args.openjev_url,args.clef_url)) or urlparse(args.d3_url).path not in ('','/') or urlparse(args.openjev_url).path not in ('','/')):p.error('all decision endpoints must be loopback HTTP; d3/OpenJev expect base URLs')
    cfg=json.loads(args.config.read_text());models=cfg['target_models'];prompts=cfg['prompts']
    if len(models)!=2 or set(prompts)!= {'easy','hard'}:raise ValueError('expected two targets and easy/hard prompts')
    d=load('d3_e2e',ROOT/'scripts/run_d3_policy_e2e.py');d3fake=d.load_fake();openjev=load('fake_openjev',EXTERNAL/'fake_openjev.py');clef=load('fake_clef',HERE/'fake_clef.py')
    with (nullcontext(args.state_dir) if args.state_dir else tempfile.TemporaryDirectory(prefix='decision-examples-')) as directory,ExitStack() as stack:
        tmp=Path(directory);env={k:os.environ[k] for k in ('PATH','HOME','TMPDIR','SYSTEMROOT','LD_LIBRARY_PATH') if k in os.environ}
        if args.backend=='fireworks':env['FIREWORKS_API_KEY']=os.environ['FIREWORKS_API_KEY']
        upstream=d.upstream_server(stack)
        if args.decisions=='fake':
            d3=d3fake.start_in_thread(api_key='');stack.callback(d3.server_close);stack.callback(d3.shutdown)
            jev=ThreadingHTTPServer(('127.0.0.1',0),openjev.Handler);threading.Thread(target=jev.serve_forever,daemon=True).start();stack.callback(jev.server_close);stack.callback(jev.shutdown)
            cf,thread=clef.start();stack.callback(thread.join);stack.callback(cf.server_close);stack.callback(cf.shutdown)
            urls=[f'http://127.0.0.1:{d3.server_port}',f'http://127.0.0.1:{jev.server_port}',f'http://127.0.0.1:{cf.server_port}/v1/systemone']
        else:urls=[args.d3_url,args.openjev_url,args.clef_url]
        ports={}
        for name,script,flag,url in [
            ('d3',EXTERNAL/'d3_policy.py','--url',urls[0]),
            ('openjev',EXTERNAL/'openjev_policy.py','--openjev-url',urls[1]),
            ('clef',HERE/'clef_policy.py','--clef-url',urls[2])]:
            port=free_port();proc=d.start(stack,[sys.executable,str(script),'--port',str(port),flag,url],tmp/(name+'.log'),env)
            d.ready(f'http://127.0.0.1:{port}/health',proc,name);ports[name]=port
        targets=[{'provider':'cheapco','model_ref':'target','tier':'cheap','weight':1},{'provider':'strongco','model_ref':'target','tier':'strong','weight':1}]
        groups={f'{name}-{mode}':{'strategy':'external','targets':targets,'external_policy':{'url':f'http://127.0.0.1:{port}/route','allow_hosts':['127.0.0.1'],'timeout_ms':4000,'max_response_bytes':65536,'include_request':True,'mode':mode,'on_error':'fail_closed'}} for name,port in ports.items() for mode in ('shadow','enforce')}
        token=secrets.token_urlsafe(32);port=free_port();db=tmp/'usage.sqlite'
        if args.backend=='local':models=[args.local_model]*2
        if args.backend=='fireworks':models=[cfg['fireworks_model']]*2
        providers={}
        for name,model in zip(('cheapco','strongco'),models):
            meta={'model':model};provider={'base_url':f'http://127.0.0.1:{upstream.server_port}/v1','dialect':'openai-chat','api_key':'mock-only'}
            if args.backend=='local':provider.update(base_url=args.local_url,api_key='unused-local')
            if args.backend=='fireworks':
                meta.update(input_price_per_million_usd=cfg['fireworks_input_per_million_usd'],cached_input_price_per_million_usd=cfg['fireworks_cached_input_per_million_usd'],output_price_per_million_usd=cfg['fireworks_output_per_million_usd'],pricing_source=cfg['fireworks_pricing_source'],pricing_updated_at=cfg['fireworks_pricing_date'])
                provider.update(base_url='https://api.fireworks.ai/inference/v1',api_key='${FIREWORKS_API_KEY}',api_key_env='FIREWORKS_API_KEY')
            provider['models']={'target':meta};providers[name]=provider
        router={'server':{'listen':f'127.0.0.1:{port}','default_model_group':'d3-enforce','logging':{'path':str(tmp/'requests.jsonl')},'cache':{'enabled':False},'decision_telemetry':{'enabled':True},'upstream':{'timeout_ms':5000},'identifiers':{'mode':'passthrough'},'usage_db':{'enabled':True,'driver':'sqlite','path':str(db),'migration_policy':'auto-safe'}},
            'state_path':str(tmp/'state.json'),'providers':providers,
            'models':groups,'callers':[{'id':'decision-demo','user':'test','project':'test','environment':'test','status':'active','token_sha256':hashlib.sha256(token.encode()).hexdigest(),'token_id':'rtr_metrum_demo_test','allow':list(groups),'rate':{'rpm':10000,'tpm':1000000,'concurrent':8}}]}
        file=tmp/'router.json';file.write_text(json.dumps(router));file.chmod(0o600)
        proc=d.start(stack,[str(args.router.resolve()),'-config',str(file)],tmp/'router.log',env);base=f'http://127.0.0.1:{port}';d.ready(base+'/readyz',proc,'router')
        results=[]
        for name in ports:
            for mode in ('shadow','enforce'):
                for difficulty in ('easy','hard'):
                    status,_,rid=d.http(base+'/v1/chat/completions',{'model':f'{name}-{mode}','messages':[{'role':'user','content':prompts[difficulty]}],'max_tokens':32},{'Authorization':'Bearer '+token})
                    row=d.policy_row(db,rid);served=d.served_model(db,rid)
                    index=row['selected_candidate_index'];served_index=0 if mode=='shadow' else index
                    expected_outcome='shadow_recommended' if mode=='shadow' else 'selected'
                    attempts=d.rows(db,'select provider from request_attempts where request_id = ? and selected = 1',(rid,))
                    if status!=200 or index not in (0,1) or row['outcome']!=expected_outcome or served!=models[served_index] or len(attempts)!=1 or attempts[0]['provider']!=('cheapco','strongco')[served_index]:raise RuntimeError(f'unexpected routing for {name}/{mode}/{difficulty}')
                    if args.decisions=='fake' and index!=(0 if difficulty=='easy' else 1):raise RuntimeError('fake recommendation mismatch')
                    results.append({'decision':name,'mode':mode,'input':difficulty,'recommended_index':index,'served_tier':('cheap','strong')[served_index],'outcome':row['outcome']})
        usage=d.rows(db,'select input_tokens,cached_input_tokens,output_tokens,total_cost_usd,attempts,usage_source from request_usage where status = 200')
        if len(usage)!=12 or any(r['attempts']!=1 or r['usage_source']!='reported' for r in usage):raise RuntimeError('missing usage or unexpected attempts; reconcile private state')
        print(json.dumps({'backend':args.backend,'decisions':args.decisions,'usage':usage,'results':results},indent=2))

if __name__=='__main__':main()
