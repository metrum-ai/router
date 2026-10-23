#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Ephemeral GPU lease: fresh SSH key, bounded lifetime, verified API teardown.

State belongs outside Git. Dry-run by default. Invoke delete promptly at step
end; the independent watchdog is a last resort, not permission to idle GPUs.
"""
import argparse
import datetime
import json
import math
import subprocess
import sys
import time
import uuid
from pathlib import Path
from cloud import request


def active_instances(provider):
    result = request(provider, '/instances/?owner=me' if provider == 'vast' else '/instances')
    return result.get('instances', [])


def delete(directory):
    state = json.loads((directory/'lease.json').read_text())
    provider, identifier = state['provider'], state['id']
    for _ in range(60):
        try:
            if not state.get('deletion_verified'):
                request(provider, f'/instances/{identifier}/' if provider == 'vast' else f'/instances/{identifier}/delete',
                        'DELETE' if provider == 'vast' else 'POST', execute=True)
            live = active_instances(provider)
            if not any(str(i['id']) == str(identifier) and i.get('status') not in ('deleted','terminated') for i in live):
                state['deleted_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
                state['deletion_verified'] = True
                hours = (datetime.datetime.now(datetime.timezone.utc)-datetime.datetime.fromisoformat(state['created_at'])).total_seconds()/3600
                state['hours'] = hours;state['estimated_cost_usd'] = hours*state['hourly_usd']
                (directory/'lease.json').write_text(json.dumps(state,indent=2))
                if provider == 'shadeform':
                    request(provider,f"/sshkeys/{state['ssh_key_id']}/delete",'POST',{},True)
                    remaining = request(provider,'/sshkeys').get('ssh_keys',[])
                    if any(k['id']==state['ssh_key_id'] for k in remaining):
                        raise RuntimeError('provider SSH key deletion not verified')
                for name in ['instance-key','instance-key.pub']:
                    (directory/name).unlink(missing_ok=True)
                print('instance and fresh SSH key deletion verified')
                return
        except Exception as exc:
            # Never print provider response bodies/URLs; they may contain credentials.
            print('teardown retry:',type(exc).__name__,file=sys.stderr)
        time.sleep(3)
    raise RuntimeError('TEARDOWN NOT VERIFIED; operator intervention required')


def create(a):
    profile = getattr(a, 'budget_profile', 'run1')
    if profile not in ('run1','run2'):
        raise ValueError('unknown budget profile')
    stop, hard_cap, max_hours = (120,150,4) if profile == 'run2' else (800,1000,3)
    reserved_other = getattr(a, 'reserved_other_usd', 0)
    if (not all(math.isfinite(v) for v in (a.hourly_usd,a.hours,a.spent_usd,reserved_other)) or
            a.spent_usd < 0 or reserved_other < 0 or a.hourly_usd <= 0 or a.hours <= 0 or a.hours > max_hours or
            a.spent_usd + reserved_other + a.hourly_usd*a.hours >= stop):
        raise ValueError(f'invalid lease or reservation reaches ${stop} stop threshold')
    payload = json.loads(a.payload.read_text())
    if not a.execute:
        print(json.dumps({'dry_run':True,'provider':a.provider,'max_hours':a.hours,
            'reservation_usd':a.hourly_usd*a.hours,'reserved_other_usd':reserved_other,
            'budget_profile':profile,'stop_usd':stop,'hard_cap_usd':hard_cap,
            'fresh_key_planned':True,'verified_teardown_planned':True}))
        return
    if profile == 'run2':
        registration_path = getattr(a,'preregistration',None)
        export_dir = getattr(a,'router_export',None)
        if registration_path is None or export_dir is None:
            raise ValueError('run2 rental blocked until #358 export and new preregistration')
        registration = json.loads(registration_path.read_text())
        if registration.get('status')!='frozen' or registration.get('router_export_contract_confirmed') is not True:
            raise ValueError('run2 rental blocked: confirmed router export freeze required')
        import importlib.util
        spec = importlib.util.spec_from_file_location('lease_export_contract',Path(__file__).resolve().parents[1]/'run2/router_export.py')
        consumer = importlib.util.module_from_spec(spec);spec.loader.exec_module(consumer)
        consumer.load_export(export_dir)
        if consumer.sha(export_dir/'manifest.json') != registration['datasets']['router_export_manifest_sha256']:
            raise ValueError('run2 rental blocked: export differs from preregistration')
    directory = a.state_dir.resolve()
    root = Path(__file__).resolve().parents[3]
    if directory == root or root in directory.parents:
        raise ValueError('store private lease state outside the worktree')
    directory.mkdir(parents=True,exist_ok=False,mode=0o700)
    key = directory/'instance-key'
    subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-f',str(key)],check=True)
    tag = 'metrum-ft-qwen3p8-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%d') + '-' + uuid.uuid4().hex[:8]
    key_id = None;identifier = None
    try:
        if a.provider == 'shadeform':
            key_id = request(a.provider,'/sshkeys/add','POST',{'name':tag,'public_key':key.with_suffix('.pub').read_text().strip()},True)['id']
            payload.update({'ssh_key_id':key_id,'name':tag})
            result = request(a.provider,'/instances/create','POST',payload,True)
            identifier = result['id']
        else:
            if not a.offer:
                raise ValueError('Vast offer ID required')
            payload['label'] = tag
            result = request(a.provider,f'/asks/{a.offer}/','PUT',payload,True)
            identifier = result['new_contract']
        now = datetime.datetime.now(datetime.timezone.utc)
        state = {'provider':a.provider,'id':identifier,'ssh_key_id':key_id,'name':tag,
            'created_at':now.isoformat(),'hourly_usd':a.hourly_usd,
            'budget_profile':profile,'stop_usd':stop,'hard_cap_usd':hard_cap,
            'reserved_other_usd':reserved_other,'spent_usd_before':a.spent_usd,
            'deadline':(now+datetime.timedelta(hours=a.hours)).isoformat()}
        (directory/'lease.json').write_text(json.dumps(state,indent=2))
        # Credential-free argv; environment credentials stay local, never copied to GPU.
        subprocess.Popen([sys.executable,str(Path(__file__).resolve()),'watchdog','--state-dir',str(directory),'--execute'],
            stdout=(directory/'watchdog.log').open('w'),stderr=subprocess.STDOUT,start_new_session=True)
        if a.provider == 'vast':
            request(a.provider,f'/instances/{identifier}/ssh/','POST',{'ssh_key':key.with_suffix('.pub').read_text()},True)
        print(json.dumps({'id':identifier,'provider':a.provider,'state_dir':str(directory),'max_hours':a.hours}))
    except Exception:
        if identifier is not None:
            delete(directory)
        else:
            # Creation may have succeeded despite a transport failure: reconcile only our unique tag.
            matches = [i for i in active_instances(a.provider) if i.get('name',i.get('label')) == tag]
            if matches:
                i = matches[0]
                (directory/'lease.json').write_text(json.dumps({'provider':a.provider,'id':i['id'],'ssh_key_id':key_id,
                    'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'hourly_usd':a.hourly_usd}))
                delete(directory)
            else:
                if key_id:
                    request(a.provider,f'/sshkeys/{key_id}/delete','POST',{},True)
                key.unlink(missing_ok=True);key.with_suffix('.pub').unlink(missing_ok=True)
        raise


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('action',choices=['create','delete','watchdog'])
    p.add_argument('--provider',choices=['vast','shadeform'],default='shadeform')
    p.add_argument('--state-dir',type=Path,required=True)
    p.add_argument('--payload',type=Path)
    p.add_argument('--offer')
    p.add_argument('--hourly-usd',type=float,default=0)
    p.add_argument('--spent-usd',type=float,default=0)
    p.add_argument('--hours',type=float,default=3)
    p.add_argument('--budget-profile',choices=['run1','run2'],default='run1',
                   help='run2 enforces $120 stop / $150 cap and maximum4h; run1 retains old limits')
    p.add_argument('--router-export',type=Path,help='run2: verified #358 export directory required for paid create')
    p.add_argument('--preregistration',type=Path,help='run2: confirmed export/new frozen protocol required for paid create')
    p.add_argument('--reserved-other-usd',type=float,default=0,
                   help='reserve dedicated serving/judge or other known costs before creating a GPU')
    p.add_argument('--execute',action='store_true')
    a = p.parse_args()
    if a.action == 'create':
        if a.payload is None:
            p.error('--payload required')
        create(a)
    elif a.execute:
        if a.action == 'watchdog':
            state = json.loads((a.state_dir/'lease.json').read_text())
            deadline = datetime.datetime.fromisoformat(state['deadline'])
            while datetime.datetime.now(datetime.timezone.utc) < deadline:
                current = json.loads((a.state_dir/'lease.json').read_text())
                if current.get('deletion_verified'):
                    return
                time.sleep(min(10,max(0,(deadline-datetime.datetime.now(datetime.timezone.utc)).total_seconds())))
        delete(a.state_dir)
    else:
        print('dry-run: would verify instance/key deletion')

if __name__ == '__main__':
    main()
