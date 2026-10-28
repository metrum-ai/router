# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Independent pre-armed #426 teardown for exactly one Vast lease intent."""
import argparse
import datetime as dt
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import time

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('cloud_adapter',HERE.parent/'fine-tune-qwen3p8-27b/standalone/cloud.py')
cloud=importlib.util.module_from_spec(spec);spec.loader.exec_module(cloud)


def atomic(state,name,value):
    tmp=state/(name+'.tmp');tmp.write_text(json.dumps(value,sort_keys=True));os.chmod(tmp,0o600);os.replace(tmp,state/name)


def watch(state,request=cloud.request,now=time.time,sleep=time.sleep):
    state=state.resolve();repo=HERE.parents[1]
    if state==repo or repo in state.parents or state.stat().st_mode & 0o077:raise ValueError('state must be private and outside the worktree')
    intent=json.loads((state/'intent.json').read_text());label=intent['label'];deadline=float(intent['deadline_unix'])
    if not re.fullmatch(r'metrum-426-[0-9a-f]{32}',label) or not math.isfinite(deadline) or deadline<=now():raise ValueError('invalid lease intent')
    def instances():
        result=request('vast','/instances/?owner=me')
        if not isinstance(result,dict) or not isinstance(result.get('instances'),list):raise RuntimeError('invalid instance listing')
        return result['instances']
    def active(i):return str(i.get('actual_status',i.get('status',''))).lower() not in ('deleted','terminated')
    def ours(i,identifier=None):return i.get('label')==label or i.get('name')==label or (identifier is not None and str(i.get('id'))==str(identifier))
    live=instances()
    if any(ours(i) and active(i) for i in live):raise RuntimeError('intent label already in use')
    probe=request('vast','/instances/0/','DELETE',execute=False)
    if probe.get('dry_run') is not True or probe.get('method')!='DELETE':raise RuntimeError('delete dry-run failed')
    atomic(state,'ready.json',{'pid':os.getpid(),'label':label,'ready_at':dt.datetime.now(dt.timezone.utc).isoformat(),
        'dry_run_verified':True,'herdr_tab':os.environ.get('HERDR_TAB_ID'),'herdr_pane':os.environ.get('HERDR_PANE_ID')})
    print('WATCHER READY: instance listing and dry-run delete succeeded',flush=True)
    last_billing=0
    while True:
        if now()>=deadline:reason='deadline';break
        done=state/'done.txt'
        if done.exists() and 'GPU-DONE #426' in done.read_text().splitlines():reason='GPU-DONE #426';break
        lease=state/'lease.json'
        if lease.exists():
            try:
                info=json.loads(lease.read_text());created=dt.datetime.fromisoformat(info['created_at']).timestamp()
                rate=float(info['hourly_quote_usd']);limit=float(intent['reservation_limit_usd'])
                if not all(math.isfinite(x) and x>0 for x in (created,rate,limit)):raise ValueError('invalid reservation')
                estimated=max(0,now()-created)*rate/3600
                if estimated>=limit:reason='estimated reservation threshold';break
            except Exception:
                reason='invalid lease reservation';break
            if now()-last_billing>=60:
                last_billing=now()
                try:
                    import charges
                    billed=charges.collect(int(info['id']),created,now(),request=request)['observed_billed_usd']
                    if billed is not None and float(billed)>=float(intent['reservation_limit_usd']):
                        reason='observed billing threshold';break
                except Exception as exc:print('billing check unavailable:',type(exc).__name__,flush=True)
        sleep(5)
    atomic(state,'triggered.json',{'reason':reason,'at':dt.datetime.now(dt.timezone.utc).isoformat()})
    print('teardown triggered:',reason,flush=True)
    started=now();last_error=None;consecutive=0;deleted_ids=set();acknowledged=set()
    while True:
        try:
            lease=state/'lease.json';info=json.loads(lease.read_text()) if lease.exists() else {};identifier=info.get('id')
            live=instances();targets=[i for i in live if active(i) and ours(i,identifier)]
            target_ids={int(i['id']) for i in targets}
            # A successful create can precede visibility in the listing. Its
            # returned ID is authoritative; issue DELETE even if not listed.
            if identifier is not None and int(identifier) not in acknowledged:target_ids.add(int(identifier))
            for iid in target_ids:
                deleted_ids.add(iid)
                try:
                    result=request('vast',f'/instances/{iid}/','DELETE',execute=True)
                    if isinstance(result,dict) and result.get('success') is False:raise RuntimeError('delete rejected')
                    acknowledged.add(iid)
                except Exception as exc:print('delete retry',type(exc).__name__,flush=True)
            if targets:consecutive=0
            else:
                consecutive+=1
                # Never declare an ambiguous timed-out create absent: keep
                # watching until the operator reconciles it and marks terminal.
                settled=(int(identifier) in acknowledged) if identifier is not None else (state/'creation-terminal').exists()
                if not settled and now()-started>=300 and consecutive==60:
                    print('URGENT: ambiguous create; manual reconciliation required; watcher remains active',flush=True)
                if consecutive>=3 and settled:
                    for key in ('instance-key','instance-key.pub'):(state/key).unlink(missing_ok=True)
                    atomic(state,'teardown.json',{'deletion_verified':True,'reason':reason,'ids':sorted(deleted_ids | ({int(identifier)} if identifier is not None else set())),
                        'verified_at':dt.datetime.now(dt.timezone.utc).isoformat(),'absence_checks':consecutive,'local_key_removed':True})
                    print('TEARDOWN VERIFIED: intent instance absent; local SSH key removed',flush=True);break
            last_error=None
        except Exception as exc:
            if type(exc).__name__!=last_error:print('URGENT teardown API retry:',type(exc).__name__,flush=True)
            last_error=type(exc).__name__;consecutive=0
        sleep(5)


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--state-dir',required=True,type=Path);a=p.parse_args();watch(a.state_dir)


if __name__=='__main__':main()
