# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Independent #425 teardown for one Shadeform instance and our one public key.

Never list SSH keys. A 404 from the documented exact-key delete endpoint proves
that ID is absent. Ambiguous key additions and creations require reconciliation.
"""
import argparse
import datetime as dt
import json
import math
import os
from pathlib import Path
import re
import time
import urllib.error
import uuid
import watcher


def id_string(value):
    return str(uuid.UUID(str(value)))


def watch(state,request=watcher.cloud.request,now=time.time,sleep=time.sleep):
    state=state.resolve();repo=Path(__file__).resolve().parents[2]
    if state==repo or repo in state.parents or state.stat().st_mode & 0o077:raise ValueError('state must be private and outside the worktree')
    intent=json.loads((state/'intent.json').read_text());name=intent['name'];deadline=float(intent['deadline_unix'])
    if not re.fullmatch(r'metrum-425-[0-9a-f]{32}',name) or not math.isfinite(deadline) or deadline<=now():raise ValueError('invalid intent')
    def instances():
        result=request('shadeform','/instances')
        if not isinstance(result,dict) or 'instances' not in result or result['instances'] is not None and not isinstance(result['instances'],list):raise RuntimeError('invalid instance listing')
        return result['instances'] or []
    if any(i.get('name')==name for i in instances()):raise RuntimeError('intent name already exists')
    zero='00000000-0000-0000-0000-000000000000'
    for path in (f'/instances/{zero}/delete',f'/sshkeys/{zero}/delete'):
        probe=request('shadeform',path,'POST',execute=False)
        if probe.get('dry_run') is not True or probe.get('method')!='POST':raise RuntimeError('delete dry-run failed')
    watcher.atomic(state,'ready.json',{'pid':os.getpid(),'name':name,'dry_run_verified':True,'herdr_tab':os.environ.get('HERDR_TAB_ID')})
    print('WATCHER READY: instance listing and both simulated deletes succeeded',flush=True)
    last_cost_check=0
    while True:
        if now()>=deadline:reason='deadline';break
        done=state/'done.txt'
        if done.exists() and 'GPU-DONE #425' in done.read_text().splitlines():reason='GPU-DONE #425';break
        lease=state/'lease.json'
        if lease.exists():
            try:
                info=json.loads(lease.read_text());created=dt.datetime.fromisoformat(info['created_at']).timestamp()
                rate=float(info['hourly_quote_usd']);limit=float(intent['reservation_usd'])
                if not all(math.isfinite(x) and x>0 for x in (created,rate,limit)):raise ValueError('invalid reservation')
                if max(0,now()-created)*rate/3600>=limit:reason='elapsed reservation estimate';break
                if now()-last_cost_check>=60:
                    last_cost_check=now();matches=[i for i in instances() if str(i.get('id'))==str(info['id'])]
                    if matches and matches[0].get('cost_estimate') is not None:
                        value=float(matches[0]['cost_estimate'])
                        if not math.isfinite(value) or value<0:raise ValueError('invalid cost estimate')
                        if value>=limit:reason='provider cost estimate threshold';break
            except (ValueError,KeyError,TypeError):reason='invalid lease reservation';break
            except Exception as exc:print('cost check unavailable:',type(exc).__name__,flush=True)
        sleep(5)
    watcher.atomic(state,'triggered.json',{'reason':reason,'at':dt.datetime.now(dt.timezone.utc).isoformat()})
    print('teardown triggered:',reason,flush=True)
    consecutive=0;instance_deletes=set();seen_ids=set();removal_ack=False;key_absent=False;last_error=None;warned=False
    while True:
        try:
            lease=state/'lease.json';info=json.loads(lease.read_text()) if lease.exists() else {};identifier=id_string(info['id']) if info.get('id') else None
            live=instances();targets=[i for i in live if i.get('name')==name or identifier is not None and str(i.get('id'))==identifier]
            ids={id_string(i['id']) for i in targets};seen_ids.update(ids)
            if identifier and identifier not in instance_deletes:ids.add(identifier)
            for iid in ids:
                try:
                    request('shadeform',f'/instances/{iid}/delete','POST',execute=True);instance_deletes.add(iid)
                except urllib.error.HTTPError as error:
                    if error.code==404 and iid in seen_ids:instance_deletes.add(iid)
                    elif error.code!=404:raise
            consecutive=0 if targets else consecutive+1
            key=state/'key.json'
            if key.exists() and not key_absent:
                kid=id_string(json.loads(key.read_text())['id'])
                try:
                    request('shadeform',f'/sshkeys/{kid}/delete','POST',execute=True);removal_ack=True
                except urllib.error.HTTPError as error:
                    if error.code==404 and removal_ack:key_absent=True
                    elif error.code!=404:raise
            registration_settled=key_absent if key.exists() else (state/'key-terminal').exists()
            instance_settled=(identifier in instance_deletes) if identifier else (state/'creation-terminal').exists()
            if consecutive>=3 and instance_settled and registration_settled:
                for file in ('instance-key','instance-key.pub'):(state/file).unlink(missing_ok=True)
                watcher.atomic(state,'teardown.json',{'deletion_verified':True,'key_absent_verified':True,'local_key_removed':True,
                    'reason':reason,'verified_at':dt.datetime.now(dt.timezone.utc).isoformat(),'instance_id':identifier})
                print('TEARDOWN VERIFIED: instance absent, exact key absent, local key removed',flush=True);break
            if not warned and (not registration_settled or not instance_settled):
                print('Reconciliation pending if creation or key registration remains ambiguous',flush=True);warned=True
            last_error=None
        except Exception as exc:
            if type(exc).__name__!=last_error:print('URGENT teardown API retry:',type(exc).__name__,flush=True)
            last_error=type(exc).__name__;consecutive=0
        sleep(5)


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--state-dir',required=True,type=Path);a=p.parse_args();watch(a.state_dir)


if __name__=='__main__':main()
