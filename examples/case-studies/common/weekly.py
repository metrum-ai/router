#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""One bounded paid document-Q&A rerun; publish only sanitized evidence."""
import argparse
import datetime
from decimal import Decimal, InvalidOperation
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import URLError
from urllib.request import urlopen
import yaml

ROOT=Path(__file__).resolve().parents[3]
COMMON=ROOT/'examples/case-studies/common'
WORKLOAD=ROOT/'examples/case-studies/document-qa'
MODELS={'deepseek-v4p1-flash-1017':'https://fireworks.ai/models/deepseek-ai/deepseek-v4p1-flash-1017',
        'gpt-6.1-sol':'https://developers.openai.com/api/docs/models/gpt-6.1-sol'}


def preflight(settings, today):
    if settings.get('router_listen')!='127.0.0.1:18123' or set(settings.get('prices',{}))!=set(MODELS):raise ValueError('unexpected setup')
    for model,source in MODELS.items():
        price=settings['prices'][model]
        if price.get('source')!=source:raise ValueError('unexpected price source')
        try:
            age=(today-datetime.date.fromisoformat(price['date'])).days
            rates=[Decimal(str(price[k])) for k in ('input_per_million_usd','cached_input_per_million_usd','output_per_million_usd')]
        except (KeyError,ValueError,InvalidOperation):raise ValueError('invalid price') from None
        if age<0 or age>31 or any(not r.is_finite() or r<0 or r>10 for r in rates) or rates[1]>rates[0]:raise ValueError('stale or out-of-bounds price')


def failure_ledger(path):
    """Known subset only. Failed/time-out provider charges remain unknown."""
    if not path.exists():return {'request_rows':0,'attempt_rows':0,'known_successful_usage_cost_usd':None}
    try:
        with sqlite3.connect(f'{path.as_uri()}?mode=ro',uri=True) as db:
            rows=db.execute('select status,attempts,fallback_used,usage_source,total_cost_usd from request_usage').fetchall()
            attempts=db.execute('select count(*) from request_attempts').fetchone()[0]
        safe=[r for r in rows if r[0]==200 and r[1]==1 and not r[2] and r[3]=='reported' and r[4] is not None]
        known=sum((Decimal(str(r[4])) for r in safe),Decimal(0)) if safe else None
        return {'request_rows':len(rows),'attempt_rows':attempts,'known_successful_usage_cost_usd':str(known) if known is not None else None}
    except (sqlite3.Error,ValueError,InvalidOperation):return {'request_rows':None,'attempt_rows':None,'known_successful_usage_cost_usd':None}


def audit_markdown(result, run_id):
    if not run_id.isdigit():run_id='unknown'
    lines=[f'### Weekly document-Q&A rerun (Actions run {run_id})',f"Status: **{result['status']}**."]
    if result['status']=='complete':
        if result.get('mode')=='mock':lines.append('MOCK ONLY: synthetic tokens and configured prices; no paid provider was called.')
        for name in ('frontier','routed'):
            arm=result['summary']['arms'][name]
            lines.append(f"- {name}: {arm['passes']}/{arm['request_count']} exact matches; usage-derived list-price cost ${arm['router_recorded_cost_usd']}; p50/p95 {arm['latency_ms']['p50']}/{arm['latency_ms']['p95']} ms.")
        lines.append('Provider invoices are not attributed per run. Small samples do not establish parity; see the run artifact for routing mix, paired results, dated prices and confidence intervals.')
    else:
        ledger=result['ledger'];lines.append(f"- Incomplete; request rows: {ledger['request_rows']}, attempt rows: {ledger['attempt_rows']}; known successful subset cost: {ledger['known_successful_usage_cost_usd']} USD (not a total).")
        lines.append('Provider charges may be unknown, including failures and timeouts. Reconcile before retrying; this run is not a scored comparison.')
    if run_id!='unknown':lines.append(f'https://github.com/metrum-ai/router/actions/runs/{run_id}')
    return '\n'.join(lines)+'\n'


def run_cmd(args, log, env):
    with log.open('ab') as output:
        proc=subprocess.run(args,cwd=ROOT,env=env,stdout=output,stderr=output,timeout=900)
    if proc.returncode:raise RuntimeError('step failed')


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--router',required=True,type=Path);p.add_argument('--out-dir',required=True,type=Path);p.add_argument('--mock',action='store_true',help='offline synthetic CI only');a=p.parse_args()
    if not a.mock and not all(os.environ.get(k) for k in ('OPENAI_API_KEY','FIREWORKS_API_KEY')):
        print('Weekly case study skipped: provider credentials unavailable.')
        return
    os.umask(0o077);a.out_dir.mkdir(mode=0o700,parents=False,exist_ok=False)
    result={'status':'incomplete','ledger':{'request_rows':0,'attempt_rows':0,'known_successful_usage_cost_usd':None}}
    process=None;server=None;thread=None
    with tempfile.TemporaryDirectory(prefix='case-study-weekly-',dir='/dev/shm') as directory:
        parent=Path(directory);state=parent/'private';log=parent/'private.log';env=dict(os.environ);stage='preflight'
        try:
            settings=json.loads((COMMON/'config.example.json').read_text())
            preflight(settings,datetime.date.fromisoformat('2026-10-28') if a.mock else datetime.datetime.now(datetime.timezone.utc).date())
            if a.mock:
                env.pop('OPENAI_API_KEY',None);env.pop('FIREWORKS_API_KEY',None)
                tasks=json.loads((WORKLOAD/'tasks.json').read_text())
                class FakeUpstream(BaseHTTPRequestHandler):
                    def log_message(self,*_):pass
                    def do_POST(self):
                        body=json.loads(self.rfile.read(int(self.headers['Content-Length'])));text=body['messages'][1]['content'];task=next(t for t in tasks if t['question'] in text)
                        raw=json.dumps({'id':'mock','object':'chat.completion','model':body['model'],'choices':[{'index':0,'message':{'role':'assistant','content':task['answer']},'finish_reason':'stop'}],'usage':{'prompt_tokens':30,'completion_tokens':1,'total_tokens':31,'prompt_tokens_details':{'cached_tokens':0}}}).encode()
                        self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
                server=ThreadingHTTPServer(('127.0.0.1',0),FakeUpstream);thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
            elif not all(env.get(k) for k in ('OPENAI_API_KEY','FIREWORKS_API_KEY')):raise RuntimeError('credentials unavailable')
            stage='setup'
            run_cmd([sys.executable,str(COMMON/'setup.py'),'--config',str(COMMON/'config.example.json'),'--state-dir',str(state)],log,env)
            config=state/'config.yaml';cfg=yaml.safe_load(config.read_text());cfg['callers'][0]['quota']={'day':{'requests':10,'tokens':25000},'month':{'requests':10,'tokens':25000}}
            if a.mock:
                for name in ('openai','fireworks'):
                    provider=cfg['providers'][name];provider.update(base_url=f'http://127.0.0.1:{server.server_port}/v1',dialect='openai-chat',api_key='mock-only');provider.pop('api_key_env',None)
                cfg['models']={'frontier':{'strategy':'static','targets':[{'provider':'openai','model_ref':'gpt-6.1-sol','weight':1}]},'routed':{'strategy':'weighted','targets':[{'provider':'fireworks','model_ref':'deepseek-v4p1-flash-1017','weight':70},{'provider':'openai','model_ref':'gpt-6.1-sol','weight':30}]}}
            config.write_text(yaml.safe_dump(cfg,sort_keys=False));config.chmod(0o600)
            stage='router-start'
            with log.open('ab') as output:
                process=subprocess.Popen([str(a.router.resolve()),'--config',str(config)],cwd=ROOT,env=env,stdout=output,stderr=output)
            for _ in range(150):
                if process.poll() is not None:raise RuntimeError('router startup failed')
                try:
                    with urlopen('http://127.0.0.1:18123/readyz',timeout=1) as r:
                        if r.status==200:break
                except (URLError,TimeoutError):pass
                time.sleep(.1)
            else:raise RuntimeError('router readiness timeout')
            stage='requests';results=state/'results.jsonl'
            run_cmd([sys.executable,str(WORKLOAD/'run.py'),'--url','http://127.0.0.1:18123','--token-file',str(state/'caller'),'--output',str(results)],log,env)
            for _ in range(100):
                ledger=failure_ledger(state/'usage.sqlite')
                if ledger['request_rows']==10:break
                time.sleep(.1)
            stage='extract';evidence=state/'sanitized'
            run_cmd([sys.executable,str(COMMON/'extract.py'),'--workload','document-qa','--results',str(results),'--database',str(state/'usage.sqlite'),'--out-dir',str(evidence)],log,env)
            summary=json.loads((evidence/'summary.json').read_text())
            result={'status':'complete','mode':'mock' if a.mock else 'paid','summary':summary}
            for name in ('summary.json','usage.json','results.json'):shutil.copyfile(evidence/name,a.out_dir/name)
        except (OSError,ValueError,RuntimeError,subprocess.SubprocessError):
            result['stage']=stage;result['ledger']=failure_ledger(state/'usage.sqlite')
        finally:
            if process is not None and process.poll() is None:
                process.terminate()
                try:process.wait(timeout=15)
                except subprocess.TimeoutExpired:process.kill();process.wait()
            if server is not None:
                server.shutdown();server.server_close();thread.join()
        (a.out_dir/'audit.json').write_text(json.dumps(result,indent=2,sort_keys=True)+'\n')
        (a.out_dir/'audit.md').write_text(audit_markdown(result,os.environ.get('GITHUB_RUN_ID','unknown')))
    if result['status']!='complete':raise SystemExit('weekly run incomplete; see sanitized audit and reconcile before retrying')

if __name__=='__main__':main()
