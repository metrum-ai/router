# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Historical run-3 lease helpers; new Axolotl instance creation is retired.

The independent watcher remains available for existing resource cleanup.
"""
import argparse
import json
import math
import os
from pathlib import Path
import time
import uuid
import watcher


def init(state,minutes,reservation_usd):
    repo=watcher.HERE.parents[3];state=state.resolve()
    if state==repo or repo in state.parents or not (0<minutes<=240):raise ValueError('invalid state path or deadline')
    if not math.isfinite(reservation_usd) or not (0<reservation_usd<=130):raise ValueError('invalid GPU reservation')
    state.mkdir(mode=0o700,parents=False,exist_ok=False)
    intent={'label':'metrum-425-'+uuid.uuid4().hex,'deadline_unix':time.time()+minutes*60,
            'reservation_limit_usd':reservation_usd,'profile':'run3-training'}
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
    # Retain a fail-closed entry point for callers of the former launcher.
    # The independent watcher remains available to clean up existing leases.
    raise RuntimeError('Axolotl run-3 creation is retired in v4.5; replacement deferred to v4.6')


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('action',choices=['init','create']);p.add_argument('--state-dir',required=True,type=Path)
    p.add_argument('--minutes',type=float,default=180);p.add_argument('--offer',type=int);p.add_argument('--max-hourly',type=float,default=30)
    p.add_argument('--reservation-usd',type=float,default=130);a=p.parse_args()
    if a.action=='init':init(a.state_dir,a.minutes,a.reservation_usd)
    else:create(a.state_dir,a.offer,a.max_hourly)


if __name__=='__main__':main()
