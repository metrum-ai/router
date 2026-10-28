# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Create one #426 Shadeform instance with a locally generated key only.

The independent watcher must be armed before public-key registration. Never
read or register a private key with the provider; never list SSH keys.
"""
import argparse
import datetime as dt
import json
import math
import os
from pathlib import Path
import subprocess
import time
import uuid
import charges
import shade_watcher
import watcher


def init(state,minutes):
    state=state.resolve();repo=Path(__file__).resolve().parents[2]
    if state==repo or repo in state.parents or not 0<minutes<=150:raise ValueError('invalid state or deadline')
    state.mkdir(mode=0o700,parents=False,exist_ok=False)
    watcher.atomic(state,'intent.json',{'name':'metrum-426-'+uuid.uuid4().hex,'deadline_unix':time.time()+60*minutes,'reservation_usd':65})
    print('intent initialized; arm the Shadeform watcher in another Herdr tab')


def assert_watcher(state,intent):
    ready=json.loads((state/'ready.json').read_text());pid=int(ready['pid'])
    if ready.get('name')!=intent['name'] or ready.get('dry_run_verified') is not True:raise RuntimeError('watcher not ready')
    os.kill(pid,0);args=Path(f'/proc/{pid}/cmdline').read_bytes().split(b'\0')
    if os.fsencode(str(Path(__file__).resolve().parent/'shade_watcher.py')) not in args or os.fsencode(str(state)) not in args:raise RuntimeError('watcher process mismatch')
    if os.environ.get('HERDR_ENV')!='1' or not ready.get('herdr_tab') or ready['herdr_tab']==os.environ.get('HERDR_TAB_ID'):raise RuntimeError('watcher must run in a separate Herdr tab')
    if time.time()>=float(intent['deadline_unix']) or any((state/x).exists() for x in ('done.txt','triggered.json','teardown.json')):raise RuntimeError('watcher already triggered')


def create(state,cloud,kind,region,max_hourly):
    state=state.resolve();intent=json.loads((state/'intent.json').read_text());assert_watcher(state,intent)
    if state.stat().st_mode & 0o077 or any((state/n).exists() for n in ('key-started','key.json','creation-started','lease.json')):raise ValueError('invalid or already used state')
    if not math.isfinite(max_hourly) or max_hourly<=0:raise ValueError('invalid hourly cap')
    # The only prior #426 instance IDs; no missing row is assumed zero.
    prior=[charges.collect(i,1793157600,time.time())['observed_billed_usd'] for i in (58044460,58046938,58047773)]
    if any(x is None for x in prior):raise RuntimeError('previous posted charges not available')
    posted=sum(float(x) for x in prior)
    if posted+float(intent['reservation_usd'])>80:raise RuntimeError('prior charges leave insufficient conservative reserve')
    data=watcher.cloud.request('shadeform','/instances/types?available=true')
    selected=[x for x in data.get('instance_types',[]) if x.get('cloud')==cloud and x.get('shade_instance_type')==kind and x.get('num_gpus')==4
        and any(s in str(x.get('gpu_type','')).upper() for s in ('H100','H200')) and any(a.get('region')==region and a.get('available') is True and a.get('rental_type')=='on_demand' for a in x.get('availability',[]))]
    if len(selected)!=1:raise RuntimeError('selected offer unavailable')
    offer=selected[0];hourly=float(offer['hourly_price'])/100;remaining=float(intent['deadline_unix'])-time.time()
    if not math.isfinite(hourly) or hourly<=0 or hourly>max_hourly or remaining<60 or hourly*remaining/3600>float(intent['reservation_usd']):raise RuntimeError('offer exceeds reserve')
    try:
        subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-f',str(state/'instance-key')],check=True)
        public=(state/'instance-key.pub').read_text().strip();assert_watcher(state,intent)
        (state/'key-started').touch(mode=0o600)
        result=watcher.cloud.request('shadeform','/sshkeys/add','POST',{'name':intent['name'],'public_key':public},True)
        key_id=shade_watcher.id_string(result['id']);watcher.atomic(state,'key.json',{'id':key_id});(state/'key-terminal').touch(mode=0o600)
        assert_watcher(state,intent)
        deadline=dt.datetime.fromtimestamp(float(intent['deadline_unix']),dt.timezone.utc).isoformat()
        body={'cloud':cloud,'region':region,'shade_instance_type':kind,'shade_cloud':True,'name':intent['name'],'ssh_key_id':key_id,
            'auto_delete':{'date_threshold':deadline,'spend_threshold':str(intent['reservation_usd'])}}
        (state/'creation-started').touch(mode=0o600)
        response=watcher.cloud.request('shadeform','/instances/create','POST',body,True)
        identifier=shade_watcher.id_string(response['id'])
        watcher.atomic(state,'lease.json',{'id':identifier,'name':intent['name'],'created_at':dt.datetime.now(dt.timezone.utc).isoformat(),
            'hourly_quote_usd':hourly,'prior_observed_usd':posted,'deadline_unix':intent['deadline_unix']})
        (state/'creation-terminal').touch(mode=0o600)
        print(json.dumps({'id':identifier,'status':'created'}))
    except Exception:
        if not (state/'key-started').exists():(state/'key-terminal').touch(mode=0o600)
        if not (state/'creation-started').exists():(state/'creation-terminal').touch(mode=0o600)
        (state/'done.txt').write_text('GPU-DONE #426\n')
        raise


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('action',choices=['init','create']);p.add_argument('--state-dir',required=True,type=Path)
    p.add_argument('--minutes',type=float,default=150);p.add_argument('--cloud');p.add_argument('--kind');p.add_argument('--region');p.add_argument('--max-hourly',type=float,default=25)
    a=p.parse_args()
    if a.action=='init':init(a.state_dir,a.minutes)
    else:create(a.state_dir,a.cloud,a.kind,a.region,a.max_hourly)


if __name__=='__main__':main()
