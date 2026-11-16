#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Compare the same model group with prefix affinity off and on."""
import argparse
import sys
from contextlib import ExitStack, nullcontext
import importlib.util
import json
import math
import os
from pathlib import Path
import secrets
import sqlite3
import statistics
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
sys.path.insert(0, str(ROOT/'examples'))
from router_readiness import wait_for_router_ready  # noqa: E402

MODELS={'a':'model-a', 'b':'model-b'}


def module(name,path):
    spec=importlib.util.spec_from_file_location(name,path);obj=importlib.util.module_from_spec(spec);spec.loader.exec_module(obj);return obj


def percentile(values, quantile):
    ordered=sorted(values)
    return ordered[max(0, math.ceil(quantile*len(ordered))-1)] if ordered else None


class FakeUpstream(BaseHTTPRequestHandler):
    cache=set()
    def log_message(self,*_): pass
    def do_POST(self):
        body=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        content=body['messages'][0]['content']
        key=(body['model'],content[:32])
        prompt_tokens=max(10000,len(content)//4)
        cached=min(8000,prompt_tokens) if key in self.cache else 0; self.cache.add(key)
        usage={'prompt_tokens':prompt_tokens,'completion_tokens':20,'total_tokens':prompt_tokens+20,'prompt_tokens_details':{'cached_tokens':cached}}
        if body.get('stream'):
            chunks=[{'id':'mock','object':'chat.completion.chunk','created':0,'model':body['model'],'choices':[{'index':0,'delta':{'role':'assistant'},'finish_reason':None}]},
                {'id':'mock','object':'chat.completion.chunk','created':0,'model':body['model'],'choices':[{'index':0,'delta':{'content':'ok'},'finish_reason':None}]},
                {'id':'mock','object':'chat.completion.chunk','created':0,'model':body['model'],'choices':[{'index':0,'delta':{},'finish_reason':'stop'}],'usage':usage}]
            response=''.join('data: '+json.dumps(chunk)+'\n\n' for chunk in chunks)+'data: [DONE]\n\n'
            encoded=response.encode();self.send_response(200);self.send_header('Content-Type','text/event-stream');self.send_header('Content-Length',str(len(encoded)));self.end_headers();self.wfile.write(encoded);return
        response=json.dumps({'id':'mock','object':'chat.completion','created':0,'model':body['model'],
            'choices':[{'index':0,'message':{'role':'assistant','content':'ok'},'finish_reason':'stop'}], 'usage':usage}).encode()
        self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(response)));self.end_headers();self.wfile.write(response)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--router',type=Path,required=True);p.add_argument('--config',type=Path,default=Path(__file__).with_name('config.example.json'))
    p.add_argument('--backend',choices=('mock','pair','local'),default='mock');p.add_argument('--state-dir',type=Path)
    p.add_argument('--local-url');p.add_argument('--local-model-a');p.add_argument('--local-model-b')
    a=p.parse_args();s=json.loads(a.config.read_text())
    models=dict(MODELS)
    if a.backend=='local':
        u=urlparse(a.local_url or '')
        if u.scheme!='http' or u.hostname!='127.0.0.1' or u.username or u.password or u.path!='/v1' or u.query or u.fragment or not a.local_model_a or not a.local_model_b or a.local_model_a==a.local_model_b: p.error('local mode needs two distinct model IDs on a loopback /v1 server')
        models={'a':a.local_model_a,'b':a.local_model_b}
    if s['prompt_repetitions']!=1200: p.error('this bounded smoke requires 1200 synthetic repetitions')
    if a.backend=='pair' and not (os.environ.get('FIREWORKS_API_KEY') and os.environ.get('OPENROUTER_API_KEY')): p.error('FIREWORKS_API_KEY and OPENROUTER_API_KEY required')
    if a.backend!='mock' and (not a.state_dir or not a.state_dir.is_absolute()): p.error('non-mock run requires private absolute state-dir')
    if a.state_dir: a.state_dir.mkdir(mode=0o700,parents=False,exist_ok=False)
    m=module('local_pi',ROOT/'scripts/local_pi_instance.py');demo=module('tokenomics',ROOT/'examples/tokenomics/run.py');secure=module('catalog_hash',ROOT/'examples/catalog/token_hash.py')
    FakeUpstream.cache=set();upstream=ThreadingHTTPServer(('127.0.0.1',0),FakeUpstream);thread=threading.Thread(target=upstream.serve_forever,daemon=True);thread.start();process=None
    try:
        with (nullcontext(a.state_dir) if a.state_dir else tempfile.TemporaryDirectory(prefix='router-kv-')) as temp:
            state=Path(temp);port=demo.port();base=f'http://127.0.0.1:{port}'
            token='rtr_metrum_eval_cache_dev_k424_'+secrets.token_urlsafe(24)
            cfg=m.build_config(out_dir=state,listen=f'127.0.0.1:{port}',id_key_hex=secrets.token_hex(64),token=token)
            secure.apply(cfg['callers'][0],secure.prepare(ROOT),token,state)
            cfg['server']['identifiers']={'mode':'passthrough'};cfg['server']['admin_auth']={'basic':{'enabled':False,'users':[]}};cfg['server']['admin_reports']={'enabled':False};cfg['server']['default_model_group']='comparison'
            metadata={}
            providers={}
            pair_models={
                'a': {'provider':'fireworks','model':'accounts/fireworks/models/deepseek-v4p1-flash-1017',
                      'base_url':'https://api.fireworks.ai/inference/v1','api_key':'${FIREWORKS_API_KEY}','api_key_env':'FIREWORKS_API_KEY',
                      'input':s['input_price_per_million_usd'],'cached':s['cached_input_price_per_million_usd'],'output':s['output_price_per_million_usd'],'source':s['price_source_a']},
                'b': {'provider':'openrouter','model':s['openrouter_model'],
                      'base_url':'https://openrouter.ai/api/v1','api_key':'${OPENROUTER_API_KEY}','api_key_env':'OPENROUTER_API_KEY',
                      'input':s['openrouter_input_price_per_million_usd'],'cached':s['openrouter_cached_input_price_per_million_usd'],'output':s['openrouter_output_price_per_million_usd'],'source':s['openrouter_price_source']},
            }
            if a.backend=='pair':
                for key, item in pair_models.items():
                    metadata[key]={'model':item['model'],'tier':'coding','input_modalities':['text'],'output_modalities':['text'],
                      'input_price_per_million_usd':item['input'],'cached_input_price_per_million_usd':item['cached'],
                      'output_price_per_million_usd':item['output'],'pricing_source':item['source'],'pricing_updated_at':s['price_date']}
                    provider={'base_url':item['base_url'],'dialect':'openai-chat','api_key':item['api_key'],'api_key_env':item['api_key_env'],
                      'models':{key:metadata[key]}}
                    if item['provider']=='openrouter':provider['headers']={'X-Title':'Metrum Router KV Cache Example'}
                    providers[item['provider']]=provider
            else:
                for key in ('a','b'):
                    metadata[key]={'model':models[key],'tier':'coding','input_modalities':['text'],'output_modalities':['text'],'input_price_per_million_usd':s['input_price_per_million_usd'],
                      'cached_input_price_per_million_usd':s['cached_input_price_per_million_usd'],'output_price_per_million_usd':s['output_price_per_million_usd'],'pricing_source':s['price_source_'+key],'pricing_updated_at':s['price_date']}
                provider={'base_url':f'http://127.0.0.1:{upstream.server_port}/v1','dialect':'openai-chat','api_key':'mock-only','models':metadata}
                if a.backend=='local':
                    provider.update(base_url=a.local_url,api_key='unused-local')
                    for model in metadata.values():
                        model.update(input_price_per_million_usd=0,cached_input_price_per_million_usd=0,output_price_per_million_usd=0,pricing_source='https://example.invalid/local-excludes-compute')
                providers={'backend':provider}
            cfg['providers']=providers;cfg['models']={}
            if a.backend=='pair':
                targets=[{'provider':'fireworks','model_ref':'a','weight':1,'tiered_kv_cache':True,'tags':['target-a']},
                         {'provider':'openrouter','model_ref':'b','weight':1,'tiered_kv_cache':True,'tags':['target-b']}]
            else:
                targets=[{'provider':'backend','model_ref':'a','weight':1,'tiered_kv_cache':True,'tags':['target-a']},
                         {'provider':'backend','model_ref':'b','weight':1,'tiered_kv_cache':True,'tags':['target-b']}]
            cfg['models']['comparison']={'strategy':'dynamic_score','targets':targets,'routing_policy':{'dynamic_score':{
                'min_observations':1,'max_score_adjustment_percent':100,
                'signals':{'prompt_features':{'enabled':True,'max_scan_bytes':65536,'features':['summarize','extract']}},
                'score_terms':[{'name':'spread-a','when':{'prompt_feature':'summarize'},'prefer_tags':['target-a'],'expression':'0'},
                               {'name':'spread-b','when':{'prompt_feature':'extract'},'prefer_tags':['target-b'],'expression':'0'}],
                'affinity':{'enabled':True,'ttl_seconds':600,'max_entries':10000}}}}
            cfg['callers'][0]['allow']=['comparison']
            config=state/'config.yaml';run_id=secrets.token_hex(8);requests=[];output=[]
            with (state/'router.log').open('ab') as log:
                for affinity in (False,True):
                    cfg['models']['comparison']['routing_policy']['dynamic_score']['affinity']['enabled']=affinity
                    config.write_text(yaml.safe_dump(cfg,sort_keys=False));config.chmod(0o600)
                    with ExitStack() as cleanup:
                        process=subprocess.Popen([str(a.router.resolve()),'--config',str(config)],stdout=log,stderr=subprocess.STDOUT);cleanup.callback(demo.stop,process)
                        wait_for_router_ready(process, base+'/readyz', log_path=state/'router.log')
                        arm='affinity_on' if affinity else 'affinity_off'
                        prefix=f'case-{arm}-{run_id}: ' + ('This synthetic document describes an imaginary product and contains no private data. '*s['prompt_repetitions'])
                        for turn in range(4):
                            feature='summarize' if turn%2==0 else 'extract'
                            marker=' '+feature+' '
                            content=prefix[:60000]+marker+prefix[60000:]+f' turn={turn}. Reply with one word.'
                            body=json.dumps({'model':'comparison','metadata':{'session_id':f'session-{arm}-{run_id}'},'messages':[{'role':'user','content':content}],'max_tokens':16,'stream':True,'stream_options':{'include_usage':True}}).encode()
                            if len(body)>120000:raise RuntimeError('short-prefix request exceeded the 120 KB smoke bound')
                            req=Request(base+'/v1/chat/completions',data=body,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
                            try:
                                with urlopen(req,timeout=120) as r:r.read();requests.append((arm,'short',r.headers.get('X-Request-Id')))
                            except HTTPError as e:raise RuntimeError(f'request failed ({e.code}); reconcile before retry') from None
                        long_prefix=f'case-tiered-{arm}-{run_id}: summarize '
                        long_content=long_prefix + ('alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu. ' * 3000)
                        body=json.dumps({'model':'comparison','metadata':{'session_id':f'tiered-{arm}-{run_id}'},'messages':[{'role':'user','content':long_content}],'max_tokens':16,'stream':True,'stream_options':{'include_usage':True}}).encode()
                        if len(body)<=120000:raise RuntimeError('32,000-token tiered arm did not exceed the short-request bound')
                        req=Request(base+'/v1/chat/completions',data=body,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
                        try:
                            with urlopen(req,timeout=180) as r:r.read();requests.append((arm,'tiered_32000',r.headers.get('X-Request-Id')))
                        except HTTPError as e:raise RuntimeError(f'tiered request failed ({e.code}); reconcile before retry') from None
                    rows=[]
                    for _ in range(100):
                        with sqlite3.connect(state/'usage.sqlite') as db:
                            rows=[db.execute('select requested_model,target_provider,target_model,status,attempts,fallback_used,input_tokens,cached_input_tokens,recomputed_input_tokens,output_tokens,total_cost_usd,usage_source,ttfb_ms from request_usage where request_id=?',(rid,)).fetchone() for _,_,rid in requests]
                        if all(r is not None for r in rows):break
                        time.sleep(.1)
                    if any(r is None for r in rows):raise RuntimeError('missing usage; reconcile before retry')
                    for (request_arm,kind,rid),r in zip(requests[len(output):],rows[len(output):]):
                        if r[0]!='comparison' or r[3:6]!=(200,1,0) or r[11]!='reported':raise RuntimeError('unexpected group, retry or unreported usage')
                        if kind=='tiered_32000' and (r[1] not in ('fireworks','backend') or r[6] < 32000):raise RuntimeError('32,000-token request did not report at least 32,000 input tokens on the tiered target')
                        output.append({'arm':request_arm,'kind':kind,'provider':r[1],'model':r[2],'input_tokens':r[6],'cached_input_tokens':r[7],'recomputed_input_tokens':r[8],'output_tokens':r[9],'usage_cost_usd':r[10],'ttfb_ms':r[12]})
                    if a.backend=='pair' and sum(row['usage_cost_usd'] for row in output)>5.0:
                        raise RuntimeError('hosted usage-row cost exceeded the approved $5 cap; stopping before next arm')
            summaries=[]
            for arm in ('affinity_off','affinity_on'):
                samples=[row for row in output if row['arm']==arm]
                reported=[row for row in samples if row['cached_input_tokens'] is not None]
                short_samples=[row for row in samples if row['kind']=='short']
                short_reported=[row for row in short_samples if row['cached_input_tokens'] is not None]
                short_ttfb=[row['ttfb_ms'] for row in short_samples if row['ttfb_ms'] is not None]
                summaries.append({'arm':arm,'group':'comparison','affinity_enabled':arm=='affinity_on','requests':len(samples),'provider_distribution':{provider:sum(row['provider']==provider for row in samples) for provider in sorted({row['provider'] for row in samples})},'short_provider_distribution':{provider:sum(row['provider']==provider for row in short_samples) for provider in sorted({row['provider'] for row in short_samples})},'cache_hit_rate':sum(row['cached_input_tokens']>0 for row in reported)/len(reported) if reported else None,'cache_reported_requests':len(reported),'short_cache_hit_rate':sum(row['cached_input_tokens']>0 for row in short_reported)/len(short_reported) if short_reported else None,'short_ttfb_ms_p50':statistics.median(short_ttfb) if short_ttfb else None,'short_ttfb_ms_p95':percentile(short_ttfb,0.95),'short_usage_cost_usd':sum(row['usage_cost_usd'] for row in short_samples),'ttfb_ms_mean':sum(row['ttfb_ms'] or 0 for row in samples)/len(samples),'recomputed_input_tokens':sum(row['recomputed_input_tokens'] or 0 for row in samples),'usage_cost_usd':sum(row['usage_cost_usd'] for row in samples)})
            print(json.dumps({'mode':a.backend,'note':'Same dynamic_score group and target set, run with affinity disabled and enabled; only response usage fields are read. Pair-mode request costs use dated configured endpoint rates. Mock usage and costs are simulated; local costs exclude compute. The tiered arm bypasses the 120 KB request bound and verifies at least 32,000 provider-reported input tokens.', 'summary':summaries,'requests':output},indent=2))
    finally:
        if process:demo.stop(process)
        upstream.shutdown();upstream.server_close();thread.join()


if __name__=='__main__':main()
