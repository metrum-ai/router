# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Create loopback-only matched-arm configs and four private caller keys."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import secrets

ROOT=Path(__file__).resolve().parents[2]; HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('catalog_hash',ROOT/'examples/catalog/token_hash.py')
secure=importlib.util.module_from_spec(spec);spec.loader.exec_module(secure)
PERSONAS=('biomedical','finance','math','coding')


def build(state, tokens):
    if not tokens.resolve().is_relative_to(Path('/dev/shm').resolve()): raise ValueError('tokens must be in tmpfs')
    state.mkdir(mode=0o700,parents=False,exist_ok=False);tokens.mkdir(mode=0o700,parents=False,exist_ok=False)
    users=[];projects=[];memberships=[];callers=[];ctl=secure.prepare(ROOT)
    for persona in PERSONAS:
        owner='bench-'+persona;project=owner
        users.append({'id':owner,'type':'service_account','status':'active'})
        projects.append({'id':project,'status':'active'})
        memberships.append({'user_id':owner,'project':project,'role':'developer','status':'active'})
        token=f'rtr_metrum_{owner}_{project}_test_k426_{secrets.token_urlsafe(32)}'
        caller={'id':owner,'owner_user':owner,'project':project,'environment':'test','allow':['expert-study'],
                'rate':{'rpm':20000,'tpm':100000000,'concurrent':64},
                'quota':{'day':{'requests':100000,'tokens':1000000000},'month':{'requests':1000000,'tokens':10000000000}}}
        secure.apply(caller,ctl,token,tokens);callers.append(caller)
        fd=os.open(tokens/persona,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
        with os.fdopen(fd,'w') as file:file.write(token+'\n')
    providers={name:{'dialect':'openai-chat','base_url':f'http://127.0.0.1:{port}/v1','api_key':'local-placeholder'}
               for name,port in [('biomedical',8001),('finance',8002),('math',8003),('coding',8004),('baseline',8000)]}
    targets=[{'provider':name,'model':name+'-ft','context_tokens':8192,'weight':1,
              'tool_support':{'openai_chat':['tools','tool_choice']}} for name in PERSONAS]
    model_group={'strategy':'script','script':str((HERE/'policy.ts').resolve()),'script_max_concurrent':64,'targets':targets}
    base={'server':{'listen':'127.0.0.1:18426','default_model_group':'expert-study','identifiers':{'mode':'passthrough'},
          'cache':{'enabled':False},'decision_telemetry':{'enabled':True},
          'usage_db':{'enabled':True,'driver':'sqlite','path':str(state/'usage.sqlite'),'migration_policy':'auto-safe'}},
          'providers':providers,'models':{'expert-study':model_group},'users':users,'projects':projects,
          'project_memberships':memberships,'callers':callers,'state_path':str(state/'router-state.json')}
    for arm in ('experts','baseline'):
        cfg=json.loads(json.dumps(base))
        if arm=='baseline':
            cfg['models']['expert-study']={'strategy':'static','targets':[{'provider':'baseline','model':'general-base','context_tokens':8192}]}
            cfg['server']['usage_db']['path']=str(state/'baseline-usage.sqlite')
            cfg['state_path']=str(state/'baseline-state.json')
        else: cfg['server']['usage_db']['path']=str(state/'experts-usage.sqlite')
        path=state/(arm+'.json');fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
        with os.fdopen(fd,'w') as file:json.dump(cfg,file,indent=2)
    return state


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--state',type=Path,required=True);p.add_argument('--tokens',type=Path,required=True);a=p.parse_args()
    os.umask(0o077);build(a.state.resolve(),a.tokens)
    print(json.dumps({'status':'prepared','arms':['experts','baseline'],'callers':len(PERSONAS)}))


if __name__=='__main__':main()
