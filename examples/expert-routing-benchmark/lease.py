# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Pre-intent, then one budget-bounded Vast creation with independent watcher.

No credentials enter the instance payload. A fresh public SSH key is attached;
private keys and state remain in a private local directory outside the repo.
"""
import argparse
import datetime as dt
import json
import math
import os
from pathlib import Path
import subprocess
import time
import urllib.parse
import uuid
import watcher

IMAGE='vllm/vllm-openai@sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34'


def init(state,minutes):
    repo=watcher.HERE.parents[1];state=state.resolve()
    if state==repo or repo in state.parents or not (0<minutes<=150):raise ValueError('invalid state path or deadline')
    state.mkdir(mode=0o700,parents=False,exist_ok=False)
    intent={'label':'metrum-426-'+uuid.uuid4().hex,'deadline_unix':time.time()+minutes*60,'budget_usd':90,'reservation_limit_usd':75}
    watcher.atomic(state,'intent.json',intent)
    print('intent initialized; arm watcher in its own Herdr tab before create')


def assert_watcher(state,intent):
    ready=json.loads((state/'ready.json').read_text());pid=int(ready['pid'])
    if ready.get('label')!=intent['label'] or ready.get('dry_run_verified') is not True:raise RuntimeError('watcher dry-run not verified')
    os.kill(pid,0)
    args=Path(f'/proc/{pid}/cmdline').read_bytes().split(b'\0')
    if os.fsencode(str(watcher.HERE/'watcher.py')) not in args or os.fsencode(str(state)) not in args:raise RuntimeError('watcher process mismatch')
    if os.environ.get('HERDR_ENV')!='1' or not ready.get('herdr_tab') or ready['herdr_tab']==os.environ.get('HERDR_TAB_ID'):
        raise RuntimeError('watcher must be running in a separate Herdr tab')
    if time.time()>=float(intent['deadline_unix']) or any((state/name).exists() for name in ('done.txt','triggered.json','teardown.json')):
        raise RuntimeError('watcher already triggered or deadline reached')


def attach_key(identifier,public,request=None):
    if request is None:request=watcher.cloud.request
    result=request('vast',f'/instances/{identifier}/ssh/','POST',{'ssh_key':public},True)
    if not isinstance(result,dict) or result.get('success') is not True:
        raise RuntimeError('SSH key attachment not confirmed')


def create(state,offer_id,max_hourly):
    state=state.resolve();intent=json.loads((state/'intent.json').read_text())
    if state.stat().st_mode & 0o077 or (state/'lease.json').exists() or (state/'creation-started').exists():raise ValueError('state invalid or creation already attempted')
    if not isinstance(offer_id,int) or offer_id<=0 or not math.isfinite(max_hourly) or max_hourly<=0:raise ValueError('invalid offer or limit')
    assert_watcher(state,intent)
    query={'gpu_name':{'eq':'H200'},'num_gpus':{'eq':4},'rentable':{'eq':True},'rented':{'eq':False},'disk_space':{'gte':500},
        'allocated_storage':500,'inet_down':{'gte':1000},'inet_up':{'gte':500},'cuda_max_good':{'gte':12.8},'order':[['dph_total','asc']],'limit':25,'type':'on-demand'}
    data=watcher.cloud.request('vast','/bundles/?'+urllib.parse.urlencode({'q':json.dumps(query)}))
    offers=[x for x in data.get('offers',[]) if x.get('id')==offer_id]
    if len(offers)!=1:raise RuntimeError('chosen offer not currently available')
    hourly=float(offers[0]['dph_total']);remaining=float(intent['deadline_unix'])-time.time()
    if not math.isfinite(hourly) or hourly<=0 or hourly>max_hourly or remaining<60 or hourly*remaining/3600>intent['reservation_limit_usd']:
        raise RuntimeError('offer or reservation outside approved budget')
    try:
        subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-f',str(state/'instance-key')],check=True)
        assert_watcher(state,intent)
        payload={'client_id':'me','image':IMAGE,'disk':500,'runtype':'ssh','env':{},'cancel_unavail':True,'onstart':'mkdir -p /workspace','label':intent['label']}
        started=dt.datetime.now(dt.timezone.utc).isoformat()
        (state/'creation-started').write_text(started)
        response=watcher.cloud.request('vast',f'/asks/{offer_id}/','PUT',payload,True)
        identifier=int(response['new_contract'])
        if identifier<=0:raise ValueError('invalid contract id')
        watcher.atomic(state,'lease.json',{'id':identifier,'offer_id':offer_id,'label':intent['label'],
            'created_at':started,'hourly_quote_usd':hourly,'deadline_unix':intent['deadline_unix']})
        (state/'creation-terminal').touch(mode=0o600)
        assert_watcher(state,intent)
        public=(state/'instance-key.pub').read_text().strip()
        attach_key(identifier,public)
        print(json.dumps({'id':identifier,'state_dir':str(state),'status':'created'}))
    except Exception:
        if not (state/'creation-started').exists():(state/'creation-terminal').touch(mode=0o600)
        (state/'done.txt').write_text('GPU-DONE #426\n')
        raise


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('action',choices=['init','create']);p.add_argument('--state-dir',required=True,type=Path)
    p.add_argument('--minutes',type=float,default=150);p.add_argument('--offer',type=int);p.add_argument('--max-hourly',type=float,default=30);a=p.parse_args()
    if a.action=='init':init(a.state_dir,a.minutes)
    else:create(a.state_dir,a.offer,a.max_hourly)


if __name__=='__main__':main()
