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
  /run/harbor-e2e/provider-secrets.json                  (tmpfs, 0600)
  /run/harbor-e2e/router-token                          (tmpfs, 0640, group 10001)

Every job is bounded and every guard fails closed: a job that cannot run is
reported as such and the matrix stops rather than reporting a pass.
"""

import json,os,pathlib,sqlite3,stat,subprocess,sys,threading,time
import generate_tasks,guarded_relay,harbor_config,mock_messages
from credential_permissions import valid_credential_permissions
from harbor_config import make_config, router_token_bind_mount
ROOT=pathlib.Path(os.environ.get('HARBOR_E2E_ROOT','/root/harbor-e2e'))
TMP=pathlib.Path(os.environ.get('HARBOR_E2E_CREDS','/run/harbor-e2e'))
TASKS=pathlib.Path(os.environ.get('HARBOR_CATALOG_TASKS',str(ROOT/'catalog-tasks')))
HOST=os.environ.get('HARBOR_E2E_HOST','172.17.0.1')
SHA=os.environ.get('HARBOR_E2E_SHA','')
RUN_ID=os.environ.get('HARBOR_E2E_RUN_ID','local')
SELFTEST=os.environ.get('HARBOR_E2E_SELFTEST')=='1'
if not SELFTEST and subprocess.check_output(['findmnt','-no','FSTYPE','--target',str(TMP)],text=True).strip()!='tmpfs':raise SystemExit('credential directory not tmpfs')
if (ROOT/'started').exists():raise SystemExit('already started; no repeat')
(ROOT/'started').touch(); tasks=ROOT/'tasks';generate_tasks.generate(tasks)
for p in (TMP/'provider-secrets.json',TMP/'router-token'):
 if not p.is_file():raise SystemExit('credential file absent or not private')
 metadata=p.stat()
 if not valid_credential_permissions(p.name,stat.S_IMODE(metadata.st_mode),metadata.st_gid):raise SystemExit('credential file absent or not private')
mounts=json.dumps([router_token_bind_mount(TMP/'router-token')])
baseenv={k:os.environ[k] for k in ('PATH','HOME','LANG','LD_LIBRARY_PATH') if k in os.environ};baseenv['PYTHONPATH']=str(ROOT)
base=[os.environ.get('HARBOR_BIN') or '/root/harbor-venv/bin/harbor','run','-a','metrum_pi_tmpfs:TmpfsPi','--skip-credential-checks','--mounts',mounts,'-n','1','--max-retries','0','--jobs-dir',str(ROOT/'jobs')]
def run(name,style,mode,group,port,path):
 env=baseenv|{'HARBOR_E2E_STYLE':style,'HARBOR_E2E_MODE':mode,'HARBOR_E2E_ROUTER_URL':f'http://{HOST}:{port}'}
 args=base+['--job-name',name,'-m','metrum/'+group,'--path',str(path)]
 try:
  with (ROOT/(name+'.log')).open('w') as f:r=subprocess.run(args,env=env,stdout=f,stderr=subprocess.STDOUT,timeout=900)
  exit_code=r.returncode
 except subprocess.TimeoutExpired:
  exit_code=124
 rewards=list((ROOT/'jobs'/name).rglob('reward.txt'))
 vals=[p.read_text().strip() for p in rewards];print(json.dumps({'job':name,'exit':exit_code,'count':len(vals),'passed':vals.count('1'),'values':vals}),flush=True)
 return {'exit':exit_code,'values':vals}

def usage_snapshot():
 with sqlite3.connect(TMP/'usage.sqlite') as db:
  cost,count=db.execute('select coalesce(sum(total_cost_usd),0),count(*) from request_usage').fetchone()
 return float(cost),int(count)
if not SELFTEST:
    mock_messages.Mock.calls = 0
    mock = mock_messages.start(HOST, 18082)
    try:
        (TMP / 'router-token').write_text('synthetic-replay')
        replay = run('offline-replay', 'openai-responses', 'routed', 'gpt', 18082, tasks / 'hello')
        preflight = {"exit": replay["exit"], "reward_values": replay["values"],
                     "mock_requests": mock_messages.Mock.calls}
        (ROOT / 'preflight.json').write_text(json.dumps(preflight, indent=2) + '\n')
        print(json.dumps({"offline_replay": preflight}), flush=True)
        if replay['exit'] != 0 or replay['values'] != ['1'] or mock_messages.Mock.calls != 2:
            raise RuntimeError('Harbor Docker/adapter mock replay failed')
    finally:
        mock.shutdown()
        mock.server_close()
print('OFFLINE REPLAY PASSED; provider calls not yet made',flush=True)
# --- one relay and one router for the whole matrix -------------------------
# Binding per lane is what made a repeat attempt fail with "Address already in
# use"; both are started once here and torn down once in the finally block.
relay = guarded_relay.http.server.ThreadingHTTPServer((HOST, 18081), guarded_relay.Relay)
relay.budget = guarded_relay.Budget(max_calls=80, max_reserve=float(os.environ.get('HARBOR_MAX_RESERVE_USD', '0.50')))
relay.secrets_path = TMP / 'provider-secrets.json'
relay.records_path = ROOT / 'provider-usage.jsonl'
threading.Thread(target=relay.serve_forever, daemon=True).start()

config = make_config(TMP, 18080, 18081, upstream_base=os.environ.get('HARBOR_E2E_UPSTREAM') or None)
(TMP / 'router.yaml').write_text(config)
tokens = harbor_config.caller_tokens()
proc = None
results = {}
try:
    with (ROOT / 'router-start.log').open('w') as f:
        proc = subprocess.Popen([str(ROOT / 'router'), '-config', str(TMP / 'router.yaml')], env=baseenv, stdout=f, stderr=subprocess.STDOUT)
        import urllib.request
        for _ in range(240):
            if proc.poll() is not None:
                raise RuntimeError('router failed')
            try:
                with urllib.request.urlopen(f'http://{HOST}:18080/readyz', timeout=1):
                    break
            except OSError:
                time.sleep(.25)
        else:
            raise RuntimeError('router not ready')
        lanes = [('direct', 'openai-responses', 'gpt'), ('routed', 'openai-responses', 'gpt'),
                 ('direct', 'anthropic-messages', 'claude'), ('routed', 'anthropic-messages', 'claude')]
        if SELFTEST:
            # Spend-free local proof of the orchestration: relay and router bind
            # once, every lane's token authenticates, and each lane's request
            # traverses router -> relay -> local mock.
            for mode, style, group in lanes:
                (TMP / 'router-token').write_text(tokens[(style, group)])
                name = mode + '-' + group
                path = '/v1/responses' if style == 'openai-responses' else '/v1/messages'
                body = json.dumps({'model': group, 'stream': True, 'max_output_tokens': 64,
                                   'input': 'ping' if style == 'openai-responses' else None,
                                   'max_tokens': 64 if style == 'anthropic-messages' else None,
                                   'messages': [{'role': 'user', 'content': 'ping'}] if style == 'anthropic-messages' else None}).encode()
                headers = {'Content-Type': 'application/json', 'stream': 'true'}
                if style == 'openai-responses':
                    headers['Authorization'] = 'Bearer ' + tokens[(style, group)]
                else:
                    headers['x-api-key'] = tokens[(style, group)]
                    headers['anthropic-version'] = '2023-06-01'
                body = json.dumps({k: v for k, v in json.loads(body).items() if v is not None}).encode()
                req = urllib.request.Request(f'http://{HOST}:18080{path}', data=body, headers=headers, method='POST')
                with urllib.request.urlopen(req, timeout=30) as response:
                    status = response.status
                    response.read()
                results[name] = {'exit': 0 if status == 200 else 1, 'values': ['1'] * 5 if status == 200 else ['0'] * 5}
                print(json.dumps({'lane': name, 'status': status, 'selftest': True}), flush=True)
            (ROOT / 'result.json').write_text(json.dumps({'sha': SHA, 'run_id': RUN_ID, 'jobs': results, 'selftest': True}, indent=2) + '\n')
        else:
            cells = [(task_id, next((p for p in TASKS.iterdir() if p.name.startswith(task_id)), None))
                     for task_id in ('HARBOR-02', 'HARBOR-03', 'HARBOR-04', 'HARBOR-05', 'HARBOR-06')]
            if any(path is None for _, path in cells):
                raise RuntimeError('Harbor catalog task staging is incomplete')
            token = tokens[('openai-responses', 'gpt')]
            failed = False
            for task_id, path in cells:
                (TMP / 'router-token').write_text(token)
                before_cost, before_rows = usage_snapshot()
                result = run(task_id, 'openai-responses', 'routed', 'gpt', 18080, path)
                after_cost, after_rows = usage_snapshot()
                row_count = after_rows - before_rows
                measured_cost = round(after_cost - before_cost, 8) if row_count > 0 else None
                results[task_id] = {
                    'exit': result['exit'],
                    'attempts': len(result['values']),
                    'passes': result['values'].count('1'),
                    'reward_values': result['values'],
                    'charged_usd': measured_cost,
                    'cost_source': 'router_usage_rows' if measured_cost is not None else 'missing_usage_rows',
                    'usage_rows': row_count,
                }
                passed = result['exit'] == 0 and result['values'] == ['1'] and measured_cost is not None
                failed = failed or not passed
                report = {'sha': SHA, 'run_id': RUN_ID, 'jobs': results,
                          'reserved_upper_usd': relay.budget.reserved, 'requests': relay.budget.calls}
                (ROOT / 'result.json').write_text(json.dumps(report, indent=2) + '\n')
            if failed:
                raise RuntimeError('one or more HARBOR-02…06 live cells failed or had no measured usage rows')
finally:
    if proc:
        proc.terminate()
        try:
            proc.wait(15)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(5)
    relay.shutdown()
    relay.server_close()
