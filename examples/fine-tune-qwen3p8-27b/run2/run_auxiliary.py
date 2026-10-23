#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Frozen original JSON-text/semantic/regression probes via SAME router group.

Transport-only adapter: unique SDK display IDs, caller-bound actual model group.
No recorded teacher continuation, output normalization, scorer/gate modification,
or neural weights. Private config/results, no automatic promotion decision.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import urllib.parse
import yaml

ROOT=Path(__file__).resolve().parents[1]
LABELS={'base':'qwen3p8-27b-base','teacher':'qwen3p8-27b-teacher',
        'nvidia_onprem':'qwen3p8-27b-ft-onprem','nvidia_fireworks':'qwen3p8-27b-ft-fireworks',
        'nvidia_weighted':'qwen3p8-27b-ft','amd_onprem':'qwen3p8-27b-ft-amd'}


def config_for(tests,arm,mode,seed,api_base):
    u=urllib.parse.urlsplit(api_base)
    if u.scheme!='http' or u.hostname not in ('127.0.0.1','::1') or u.username or u.password or u.query or u.fragment or u.path!='/v1':
        raise ValueError('literal localhost router /v1 origin required')
    config=yaml.safe_load((ROOT/'eval/promptfooconfig.yaml').read_text())
    if mode=='sampling':
        tests=[t for t in tests if any(a['type']=='llm-rubric' for a in t['assert'])]
        if len(tests)!=100:raise ValueError('same100 non-tools required')
    for test in tests:
        for assertion in test['assert']:
            value=assertion.get('value')
            if isinstance(value,str) and value.startswith('file://scorers/'):
                assertion['value']='file://'+str((ROOT/'eval'/value[7:]).resolve())
    extra={'model':'support-agent','top_k':20}
    if arm!='teacher':extra['chat_template_kwargs']={'enable_thinking':False}
    config['providers']=[{'id':'openai:chat:'+LABELS[arm],'config':{
        'apiBaseUrl':api_base,'apiKeyEnvar':'ROUTER_CALLER_KEY','temperature':.7 if mode=='sampling' else 0,
        'top_p':.95,'seed':seed,'max_tokens':2048,'maxRetries':0,'showThinking':False,'passthrough':extra}}]
    grader=config['defaultTest']['options']['provider']['config']
    grader.update(apiBaseUrl=api_base,maxRetries=0,showThinking=False)
    config['tests']=tests
    config['extensions']=['file://'+str((ROOT/'eval/telemetry.cjs').resolve())]
    return config


def child_environment(parent,key,journal):
    safe={k:v for k,v in parent.items() if k in ('PATH','HOME','LANG','LC_ALL','TERM','TZ','NLTK_DATA','IFEVAL_VERIFIER_ROOT') or k.startswith('LC_')}
    safe.update(ROUTER_CALLER_KEY=key,PROMPTFOO_PYTHON=sys.executable,
        PROMPTFOO_DISABLE_TELEMETRY='1',PROMPTFOO_DISABLE_TEMPLATE_ENV_VARS='1',
        PROMPTFOO_DISABLE_VAR_EXPANSION='1',PROMPTFOO_RETRY_5XX='false',
        FT_ROUTER_JOURNAL=str(journal),NO_PROXY='127.0.0.1,localhost,::1')
    return safe


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--tests',type=Path,required=True);p.add_argument('--preregistration',type=Path,required=True)
    p.add_argument('--token-file',type=Path,required=True);p.add_argument('--out',type=Path,required=True)
    p.add_argument('--arm',choices=LABELS,required=True);p.add_argument('--mode',choices=['primary','regressions','sampling'],required=True)
    p.add_argument('--seed',type=int,choices=[326,327,328],default=326)
    p.add_argument('--api-base',default='http://127.0.0.1:18625/v1');p.add_argument('--execute',action='store_true')
    a=p.parse_args();r=json.loads(a.preregistration.read_text())
    if r['status']!='frozen' or not r['router_export_contract_confirmed']:p.error('frozen governed cohort required')
    expected=r['fresh_regressions']['sha256'] if a.mode=='regressions' else r['heldout_projection_sha256']
    if hashlib.sha256(a.tests.read_bytes()).hexdigest()!=expected:p.error('frozen probe hash differs')
    if a.mode!='sampling' and a.seed!=326:p.error('objective seed326 required')
    if a.out.resolve().is_relative_to(ROOT.parents[1]):p.error('private results outside worktree required')
    if stat.S_IMODE(a.token_file.stat().st_mode)!=0o600:p.error('caller token must be mode0600')
    tests=yaml.safe_load(a.tests.read_text());config=config_for(tests,a.arm,a.mode,a.seed,a.api_base)
    if not a.execute:
        print(json.dumps({'dry_run':True,'arm':a.arm,'mode':a.mode,'tests':len(config['tests']),'physical_group':'support-agent','retries':0}));return 0
    a.out.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
    cfg=a.out.with_suffix('.private-config.yaml');cfg.write_text(yaml.safe_dump(config,sort_keys=False));cfg.chmod(0o600)
    journal=a.out.with_suffix('.router-ids.jsonl');journal.write_text('');journal.chmod(0o600)
    env=child_environment(os.environ,a.token_file.read_text().strip(),journal)
    cmd=[str(ROOT/'.cache/promptfoo/node_modules/.bin/promptfoo'),'eval','-c',str(cfg),'--no-cache','--max-concurrency','16','-o',str(a.out)]
    result=subprocess.run(cmd,env=env,umask=0o077)
    if a.out.exists() and a.out.stat().st_size:
        rows=json.loads(a.out.read_text())['results']['results']
        if len(journal.read_text().splitlines())!=len(rows):p.error('incomplete request journal; do not claim costs')
    return result.returncode


if __name__=='__main__':raise SystemExit(main())
