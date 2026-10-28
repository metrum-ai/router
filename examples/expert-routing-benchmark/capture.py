# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Continuously retain labeled container logs and inspect before GPU teardown."""
import json,os,re,subprocess,sys,tempfile,time
from pathlib import Path
if len(sys.argv) not in (4,5) or sys.argv[3] not in ('loop','once'):raise SystemExit('usage: capture.py STATE_DIR EVIDENCE_DIR loop|once [NAME]')
s=Path(sys.argv[1]).resolve();evidence=Path(sys.argv[2]).resolve();mode=sys.argv[3];names=('baseline','biomedical','finance','math','coding')
if len(sys.argv)==5:
 if mode!='once' or sys.argv[4] not in names:raise SystemExit('invalid container name')
 names=(sys.argv[4],)
w=json.loads((s/'workspace.json').read_text());c=json.loads((s/'connection.json').read_text());info=json.loads((s/'lease.json').read_text());proof=json.loads((s/'ssh-proof.json').read_text())
if proof.get('success') is not True or proof.get('instance_id')!=info['id']:raise SystemExit('GPU proof mismatch')
root=w.get('root','')
if not re.fullmatch(r'/[A-Za-z0-9_./-]+',root) or '/..' in root:raise SystemExit('unsafe model root')
os.umask(0o077);evidence.mkdir(mode=0o700,parents=False,exist_ok=True)
opts=['-i',str(s/'instance-key'),'-o','IdentitiesOnly=yes','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o',f'UserKnownHostsFile={s/"known_hosts"}','-o','ConnectTimeout=5'];docker='sudo -n docker' if w['docker_sudo'] else 'docker';env={k:os.environ[k] for k in ('PATH','HOME') if k in os.environ}
def ssh(command):
 return subprocess.run(['ssh',*opts,'-p',str(c['port']),f"{c['user']}@{c['ip']}",command],env=env,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=30)
def atomic(path,content):
 fd,temp=tempfile.mkstemp(prefix='.'+path.name+'.',dir=path.parent)
 try:
  with os.fdopen(fd,'wb') as f:f.write(content)
  os.replace(temp,path)
 finally:
  if os.path.exists(temp):os.unlink(temp)
def capture(name):
 ident='proof426-'+name;inspect=ssh(f'{docker} inspect {ident}')
 if inspect.returncode!=0:return False
 data=json.loads(inspect.stdout)
 if len(data)!=1 or data[0].get('Name')!='/'+ident or data[0].get('Config',{}).get('Labels',{}).get('metrum.task')!='issue-426':raise ValueError('container identity or label mismatch')
 # Environment values may contain secrets in other images: never retain them.
 data[0].get('Config',{}).pop('Env',None)
 logs=ssh(f'{docker} logs {ident} 2>&1')
 if logs.returncode!=0:return False
 # Host-side tee is an independent durable copy; copy it before signalling
 # teardown. Fixed container names and a validated root prevent shell injection.
 path=f'{root}/weights/{ident}.host.log';priv='sudo -n ' if w['docker_sudo'] else ''
 hostlog=ssh(f'for i in 1 2 3; do {priv}test -r {path} && break; sleep 1; done; {priv}cat {path}')
 atomic(evidence/(name+'.inspect.json'),json.dumps(data,indent=2).encode());atomic(evidence/(name+'.log'),logs.stdout)
 if hostlog.returncode!=0:return False
 atomic(evidence/(name+'.host.log'),hostlog.stdout)
 return True
if mode=='once':
 found=[name for name in names if capture(name)];print('captured:',','.join(found),flush=True)
 if len(found)!=len(names):raise SystemExit('requested container evidence unavailable')
else:
 (s/'capture-ready').write_text(json.dumps({'pid':os.getpid()}))
 print('continuous evidence collector started',flush=True)
 while not (s/'capture-stop').exists() and not (s/'teardown.json').exists():
  for name in names:
   try:capture(name)
   except (OSError,ValueError,subprocess.SubprocessError):pass
  time.sleep(10)
 print('continuous evidence collector stopped',flush=True)
