# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Compare instance-scoped Ed25519/RSA keys and timing on one cheap lease.

No account key is created: Vast says account keys may reach all current
instances. A pre-armed watcher owns cleanup. Never send API credentials to SSH.
"""
import argparse
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import time
import lease
import watcher


def probe(state,host,port,key):
    argv=['ssh','-T','-i',str(key),'-p',str(port),'-o','IdentitiesOnly=yes','-o','BatchMode=yes',
          '-o','StrictHostKeyChecking=accept-new','-o',f'UserKnownHostsFile={state/"known_hosts"}',
          '-o','ConnectTimeout=8',f'root@{host}','printf diagnostic-ok']
    env={'PATH':os.environ.get('PATH','/usr/bin:/bin'),'HOME':os.environ.get('HOME','/')}
    result=subprocess.run(argv,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,env=env,timeout=15)
    return result.returncode==0 and result.stdout==b'diagnostic-ok'


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--state-dir',required=True,type=Path);a=p.parse_args();state=a.state_dir.resolve()
    result={'initial_ed25519':False,'late_ed25519':None,'late_rsa':None,'attempts':{},'key_scope':'instance-only'}
    def finish(*_):raise SystemExit('interrupted')
    signal.signal(signal.SIGTERM,finish);signal.signal(signal.SIGINT,finish)
    try:
        intent=json.loads((state/'intent.json').read_text());info=json.loads((state/'lease.json').read_text())
        if intent.get('profile')!='ssh-diagnostic':raise ValueError('diagnostic lease required')
        lease.assert_watcher(state,intent);result['instance_id']=int(info['id'])
        started=time.monotonic();host=None;port=None
        while time.monotonic()-started<300:
            data=watcher.cloud.request('vast',f"/instances/{int(info['id'])}/")
            item=data.get('instances',{})
            if isinstance(item,dict):
                h=item.get('ssh_host');q=item.get('ssh_port')
                if item.get('actual_status')=='running' and isinstance(h,str) and re.fullmatch(r'ssh[0-9]+\.vast\.ai',h) and isinstance(q,int) and 0<q<65536:
                    host,port=h,q;break
            time.sleep(10)
        if host is None:raise RuntimeError('instance did not reach SSH-ready metadata')
        early=time.monotonic()
        while time.monotonic()-early<90:
            result['attempts']['initial_ed25519']=result['attempts'].get('initial_ed25519',0)+1
            if probe(state,host,port,state/'instance-key'):
                result['initial_ed25519']=True;result['initial_ready_seconds']=round(time.monotonic()-started,1);break
            time.sleep(10)
        keys={'initial_ed25519':state/'instance-key'}
        for name,kind in (('late_ed25519','ed25519'),('late_rsa','rsa')):
            key=state/('diagnostic-'+kind);argv=['ssh-keygen','-q','-t',kind]
            if kind=='rsa':argv+=['-b','3072']
            subprocess.run(argv+['-N','','-f',str(key)],check=True)
            try:
                lease.attach_key(int(info['id']),key.with_suffix('.pub').read_text().strip())
                result[name]=False;keys[name]=key
            except Exception as error:result[name+'_attach_error']=type(error).__name__
        late=time.monotonic()
        while time.monotonic()-late<90:
            for name,key in keys.items():
                if result[name]:continue
                result['attempts'][name]=result['attempts'].get(name,0)+1
                if probe(state,host,port,key):result[name]=True;result[name+'_ready_seconds']=round(time.monotonic()-started,1)
            if all(result[name] for name in keys):break
            time.sleep(10)
    except Exception as error:
        result['error']=type(error).__name__
    finally:
        watcher.atomic(state,'diagnostic-result.json',result)
        (state/'done.txt').write_text('GPU-DONE #426\n')
        print(json.dumps(result,sort_keys=True),flush=True)


if __name__=='__main__':main()
