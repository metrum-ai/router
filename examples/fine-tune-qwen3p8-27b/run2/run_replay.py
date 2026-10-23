#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Replay proposed #358 export through localhost router; dry-run by default.

Trusted backend interface: initialize(descriptor)->state,
execute(state,tool_name,arguments)->JSON value, success(state,final_message)->bool.
Actual exporter/backend agreement and NEW signed preregistration are mandatory.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor, as_completed
import importlib.util
import json
import os
from pathlib import Path
import time
import urllib.parse
import urllib.request

from router_export import load_export, sha, ROOT
from replay import run_case, attach_cost


def evaluation_environment(environment):
    """Backend receives no upstream/cloud credentials; caller token is file-only."""
    return {k:v for k,v in environment.items() if not (
        any(part in k.upper() for part in ('KEY','TOKEN','SECRET','PASSWORD','CREDENTIAL'))
        or k.upper().startswith(('OPENAI_','ANTHROPIC_','FIREWORKS_','SHADEFORM_',
            'VAST_','HF_','HUGGING_FACE_','AWS_','AZURE_','GOOGLE_','GEMINI_','GH_','GITHUB_')))}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--export',type=Path,required=True)
    p.add_argument('--preregistration',type=Path,required=True)
    p.add_argument('--backend',type=Path,required=True)
    p.add_argument('--arm',choices=['candidate','teacher','base'],required=True)
    p.add_argument('--router',default='http://127.0.0.1:18525')
    p.add_argument('--token-file',type=Path)
    p.add_argument('--cost-ledger-json',type=Path,help='authenticated router usage export normalized by request ID')
    p.add_argument('--out',type=Path,required=True)
    p.add_argument('--execute',action='store_true')
    a = p.parse_args()
    if not a.execute:
        _,rows=load_export(a.export)
        print(json.dumps({'dry_run':True,'tasks':len(rows['test']),'arm':a.arm,'no_inference':True}))
        return
    reg=json.loads(a.preregistration.read_text())
    if reg.get('status')!='frozen' or reg.get('router_export_contract_confirmed') is not True:
        p.error('blocked until #358 export/backend agreement and new signed preregistration')
    if sha(a.export/'manifest.json')!=reg['datasets']['router_export_manifest_sha256']:
        p.error('export differs from frozen protocol')
    if sha(a.backend)!=reg['replay_backend_sha256'] or reg['model_group']!='support-agent':
        p.error('frozen trusted backend and same support-agent group required')
    for name in ('run2/router_export.py','run2/replay.py','run2/run_replay.py','schema.json'):
        if sha(ROOT/name)!=reg['code_sha256'][name]:p.error('replay source drift')
    url=urllib.parse.urlsplit(a.router)
    if (url.scheme!='http' or url.hostname not in ('127.0.0.1','::1') or url.username or url.password
            or url.query or url.fragment or url.path not in ('','/')):
        p.error('literal localhost router HTTP origin required')
    if a.token_file is None or a.token_file.stat().st_mode & 0o077:
        p.error('private mode0600 caller token file required')
    if a.out.resolve().is_relative_to(ROOT.parents[1]):p.error('keep private replay records outside Git')
    turns=reg['max_agent_turns']
    if type(turns) is not int or not 1<=turns<=16:p.error('bounded frozen agent-turn limit required')
    concurrency=reg['replay_concurrency']
    if type(concurrency) is not int or not 1<=concurrency<=32:p.error('bounded frozen replay concurrency required')
    parameters=reg['request_parameters'][a.arm]
    if any(k in parameters for k in ('model','messages','tools','stream')):p.error('reserved request parameter')
    _,rows=load_export(a.export)
    token=a.token_file.read_text().strip()
    safe_env=evaluation_environment(dict(os.environ));os.environ.clear();os.environ.update(safe_env)
    spec=importlib.util.spec_from_file_location('frozen_replay_backend',a.backend)
    backend=importlib.util.module_from_spec(spec);spec.loader.exec_module(backend)
    ledger=json.loads(a.cost_ledger_json.read_text()) if a.cost_ledger_json else {}
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self,req,fp,code,msg,headers,newurl):return None
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
    def routed(messages,tools):
        body={**parameters,'model':'support-agent','messages':messages,'stream':False}
        if tools:body['tools']=tools
        req=urllib.request.Request(a.router.rstrip('/')+'/v1/chat/completions',data=json.dumps(body).encode(),
            headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
        with opener.open(req,timeout=120) as response:
            value=json.load(response)
            return value['choices'][0]['message'],response.headers.get('X-Request-Id')
    # Exclusive, private, incrementally persisted metadata; no prompts/answers/args.
    fd=os.open(a.out,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    export_identity=sha(a.export/'manifest.json');protocol_identity=sha(a.preregistration)
    def episode(row):
        start=time.perf_counter()
        result=run_case(row,routed,backend,max_turns=turns)
        result.update({'arm':a.arm,'task_latency_ms':(time.perf_counter()-start)*1000,
            'export_manifest_sha256':export_identity,'preregistration_sha256':protocol_identity})
        return attach_cost(result,ledger)
    # Backend functions must use their per-episode state, never shared mutable RNG/state.
    with os.fdopen(fd,'w') as output, ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures=[pool.submit(episode,row) for row in rows['test']]
        try:
            for future in as_completed(futures):
                output.write(json.dumps(future.result())+'\n');output.flush()
        except BaseException:
            for future in futures:future.cancel()
            raise
    print(json.dumps({'complete':True,'arm':a.arm,'tasks':len(rows['test']),
        'costs':'reconcile authenticated post-run router ledger; missing costs remain unknown',
        'promotion_decision':None}))


if __name__=='__main__':main()
