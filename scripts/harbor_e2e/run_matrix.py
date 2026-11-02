#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Harbor matrix for the release gate (stage B).

Port of the v4.5 gate harness: the Pi agent adapter, five fixed synthetic tasks,
direct versus routed, in both API styles. Runs on the Docker host, next to the
router, and writes one JSON result per lane.

Layout expected on the host (created by scripts/harbor_e2e/setup_remote.sh):

  /root/harbor-e2e/{generate_tasks.py,metrum_pi_tmpfs.py,guarded_relay.py,
                    mock_messages.py,router,router.yaml,run_matrix.py}
  /run/harbor-e2e/{provider-secrets.json,router-token}   (tmpfs, 0600)

Every job is bounded and every guard fails closed: a job that cannot run is
reported as such and the matrix stops rather than reporting a pass.
"""

import json,os,pathlib,subprocess,sys,threading,time
import generate_tasks,guarded_relay,mock_messages
from harbor_config import make_config
ROOT=pathlib.Path(os.environ.get('HARBOR_E2E_ROOT','/root/harbor-e2e'))
TMP=pathlib.Path(os.environ.get('HARBOR_E2E_CREDS','/run/harbor-e2e'))
HOST=os.environ.get('HARBOR_E2E_HOST','172.17.0.1')
SHA=os.environ.get('HARBOR_E2E_SHA','')
if subprocess.check_output(['findmnt','-no','FSTYPE','--target',str(TMP)],text=True).strip()!='tmpfs':raise SystemExit('credential directory not tmpfs')
if (ROOT/'started').exists():raise SystemExit('already started; no repeat')
(ROOT/'started').touch(); tasks=ROOT/'tasks';generate_tasks.generate(tasks)
for p in (TMP/'provider-secrets.json',TMP/'router-token'):
 if not p.is_file() or p.stat().st_mode&0o077:raise SystemExit('credential file absent or not private')
mounts=json.dumps([{'type':'bind','source':str(TMP/'router-token'),'target':str(TMP/'router-token'),'read_only':True}])
baseenv={k:os.environ[k] for k in ('PATH','HOME','LANG','LD_LIBRARY_PATH') if k in os.environ};baseenv['PYTHONPATH']=str(ROOT)
base=[os.environ.get('HARBOR_BIN') or '/root/harbor-venv/bin/harbor','run','-a','metrum_pi_tmpfs:TmpfsPi','--skip-credential-checks','--mounts',mounts,'-n','1','--max-retries','0','--jobs-dir',str(ROOT/'jobs')]
def run(name,style,mode,group,port,path):
 env=baseenv|{'HARBOR_E2E_STYLE':style,'HARBOR_E2E_MODE':mode,'HARBOR_E2E_ROUTER_URL':f'http://{HOST}:{port}'}
 args=base+['--job-name',name,'-m','metrum/'+group,'--path',str(path)]
 with (ROOT/(name+'.log')).open('w') as f:r=subprocess.run(args,env=env,stdout=f,stderr=subprocess.STDOUT,timeout=3600)
 rewards=list((ROOT/'jobs'/name).rglob('reward.txt'))
 vals=[p.read_text().strip() for p in rewards];print(json.dumps({'job':name,'exit':r.returncode,'count':len(vals),'passed':vals.count('1'),'values':vals}),flush=True)
 return {'exit':r.returncode,'values':vals}
mock=mock_messages.start(HOST,18082)
try:
 (TMP/'router-token').write_text('synthetic-replay')
 replay=run('offline-replay','anthropic-messages','routed','claude',18082,tasks/'hello')
 if replay['exit']!=0 or replay['values']!=['1'] or mock_messages.Mock.calls!=2:raise RuntimeError('Harbor Docker/adapter mock replay failed')
finally:mock.shutdown();mock.server_close()
print('OFFLINE REPLAY PASSED; provider calls not yet made',flush=True)
relay=guarded_relay.http.server.ThreadingHTTPServer((HOST,18081),guarded_relay.Relay);relay.budget=guarded_relay.Budget(max_calls=80,max_reserve=1.5);relay.secrets_path=TMP/'provider-secrets.json';relay.records_path=ROOT/'provider-usage.jsonl'
threading.Thread(target=relay.serve_forever,daemon=True).start()
config=make_config(TMP,18080,18081).replace('127.0.0.1:',HOST+':');(TMP/'router.yaml').write_text(config)
proc=None;results={}
try:
 with (ROOT/'router-start.log').open('w') as f:
  proc=subprocess.Popen([str(ROOT/'router'),'-config',str(TMP/'router.yaml')],env=baseenv,stdout=f,stderr=subprocess.STDOUT)
  import urllib.request
  for _ in range(240):
   if proc.poll() is not None:raise RuntimeError('router failed')
   try:
    with urllib.request.urlopen(f'http://{HOST}:18080/readyz',timeout=1):break
   except OSError:time.sleep(.25)
  else:raise RuntimeError('router not ready')
  for mode,style,group in [('routed','openai-responses','gpt')]:
   (TMP/'router-token').write_text('e2e-'+('gpt' if group=='gpt' else 'claude')+'-'+group)
   name=mode+'-'+group;results[name]=run(name,style,mode,group,18081 if mode=='direct' else 18080,tasks)
   (ROOT/'result.json').write_text(json.dumps({'sha':SHA,'jobs':results,'reserved_upper_usd':relay.budget.reserved,'requests':relay.budget.calls},indent=2)+'\n')
   if len(results[name]['values'])!=5 or results[name]['exit']!=0:raise RuntimeError('incomplete Harbor job '+name)
finally:
 if proc:
  proc.terminate()
  try:proc.wait(15)
  except subprocess.TimeoutExpired:proc.kill();proc.wait(5)
 relay.shutdown();relay.server_close()
print('ALL HARBOR JOBS COMPLETE',flush=True)
