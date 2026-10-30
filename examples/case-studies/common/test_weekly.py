# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import datetime
import json
import os
import socket
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
import yaml
import weekly

HERE=Path(__file__).resolve().parent


class WeeklyTests(unittest.TestCase):
    def test_missing_credentials_skip_without_audit(self):
        env={k:os.environ[k] for k in ('PATH','HOME','TMPDIR','SYSTEMROOT','LD_LIBRARY_PATH') if k in os.environ}
        with tempfile.TemporaryDirectory() as temp:
            output=Path(temp)/'audit'
            result=subprocess.run([sys.executable,str(HERE/'weekly.py'),'--router',str(Path(temp)/'unused'),'--out-dir',str(output)],env=env,text=True,capture_output=True,timeout=30)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(result.stdout,'Weekly case study skipped: provider credentials unavailable.\n')
            self.assertFalse(output.exists())

    def test_workflow_gates_all_later_steps(self):
        root=HERE.parents[2];wf=yaml.load((root/'.github/workflows/weekly-case-study.yml').read_text(),Loader=yaml.BaseLoader)
        steps=wf['jobs']['rerun']['steps'];self.assertEqual(steps[0]['id'],'credentials')
        for step in steps[1:]:self.assertIn("steps.credentials.outputs.enabled == 'true'",step.get('if',''))
        with tempfile.TemporaryDirectory() as temp:
            for openai,fireworks in (('',''),('x',''),('','x'),('x','y')):
                out=Path(temp)/'output';out.write_text('')
                env={'PATH':os.environ['PATH'],'GITHUB_OUTPUT':str(out),'OPENAI_API_KEY':openai,'FIREWORKS_API_KEY':fireworks}
                result=subprocess.run(['bash','-e','-c',steps[0]['run']],env=env,text=True,capture_output=True,timeout=10)
                self.assertEqual(result.returncode,0,result.stderr)
                if openai and fireworks:self.assertEqual(out.read_text(),'enabled=true\n');self.assertEqual(result.stdout,'')
                else:self.assertEqual(out.read_text(),'enabled=false\n');self.assertEqual(result.stdout,'Weekly case study skipped: provider credentials unavailable.\n')

    def test_preflight_rejects_stale_future_and_expensive_prices(self):
        settings=json.loads((HERE/'config.example.json').read_text());day=datetime.date.fromisoformat('2026-10-28')
        weekly.preflight(settings,day)
        with self.assertRaises(ValueError):weekly.preflight(settings,day+datetime.timedelta(days=32))
        with self.assertRaises(ValueError):weekly.preflight(settings,day-datetime.timedelta(days=3))
        settings['prices']['gpt-6.1-sol']['output_per_million_usd']=11
        with self.assertRaises(ValueError):weekly.preflight(settings,day)

    def test_failure_ledger_never_claims_total(self):
        with tempfile.TemporaryDirectory() as temp:
            path=Path(temp)/'usage.sqlite'
            with sqlite3.connect(path) as db:
                db.execute('create table request_usage(status int,attempts int,fallback_used int,usage_source text,total_cost_usd real)')
                db.execute('create table request_attempts(id int)')
                db.executemany('insert into request_usage values(?,?,?,?,?)',[(200,1,0,'reported',.01),(500,1,0,'none',0),(200,2,1,'reported',.02)])
                db.executemany('insert into request_attempts values(?)',[(1,),(2,),(3,),(4,)])
            ledger=weekly.failure_ledger(path);self.assertEqual(ledger['request_rows'],3);self.assertEqual(ledger['attempt_rows'],4);self.assertEqual(ledger['known_successful_usage_cost_usd'],'0.01')
            text=weekly.audit_markdown({'status':'incomplete','ledger':ledger},'123')
            self.assertIn('not a total',text);self.assertIn('charges may be unknown',text)
            self.assertNotIn('https://github.com/metrum-ai/router/actions/runs/',weekly.audit_markdown({'status':'incomplete','ledger':ledger},'1\nsecret'))

    def test_mock_weekly_end_to_end(self):
        env={k:os.environ[k] for k in ('PATH','HOME','TMPDIR','SYSTEMROOT','LD_LIBRARY_PATH','GOCACHE','GOMODCACHE','GOROOT','GOPATH','GOPROXY','GOTOOLCHAIN') if k in os.environ}
        with tempfile.TemporaryDirectory() as temp:
            root=HERE.parents[2];binary=Path(temp)/'router';output=Path(temp)/'audit'
            subprocess.run(['go','build','-o',str(binary),'./cmd/metrum-ai-router'],cwd=root,env=env,check=True,timeout=300,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            # Mock mode must work even if the paid workflow's fixed port is busy.
            with socket.socket() as occupied:
                try: occupied.bind(('127.0.0.1',18123));occupied.listen()
                except OSError: pass  # Already occupied also exercises this case.
                result=subprocess.run([sys.executable,str(HERE/'weekly.py'),'--router',str(binary),'--out-dir',str(output),'--mock'],cwd=root,env=env,text=True,capture_output=True,timeout=240)
            report=json.loads((output/'audit.json').read_text())
            self.assertEqual(result.returncode,0,f"{result.stderr} stage={report.get('stage', 'unknown')}")
            self.assertEqual(report['status'],'complete');self.assertEqual(report['mode'],'mock')
            self.assertEqual(report['summary']['arms']['frontier']['passes'],5);self.assertEqual(report['summary']['arms']['routed']['passes'],5)
            self.assertIn('MOCK ONLY',(output/'audit.md').read_text())
            self.assertEqual({p.name for p in output.iterdir()},{'audit.md','audit.json','summary.json','usage.json','results.json'})

    def test_setup_uses_routerctl_pbkdf_hash(self):
        env={k:os.environ[k] for k in ('PATH','HOME','TMPDIR','SYSTEMROOT','LD_LIBRARY_PATH','GOCACHE','GOMODCACHE','GOROOT','GOPATH','GOPROXY','GOTOOLCHAIN') if k in os.environ}
        with tempfile.TemporaryDirectory() as temp:
            state=Path(temp)/'state'
            p=subprocess.run([sys.executable,str(HERE/'setup.py'),'--config',str(HERE/'config.example.json'),'--state-dir',str(state)],env=env,text=True,capture_output=True,timeout=300)
            self.assertEqual(p.returncode,0,p.stderr);cfg=yaml.safe_load((state/'config.yaml').read_text());caller=cfg['callers'][0]
            self.assertNotIn('token_sha256',caller);self.assertTrue(caller['token_hash'].startswith('pbkdf2-sha3-512:'))
            token=(state/'caller').read_text().strip();self.assertTrue(token.startswith(caller['token_id']+'_'));self.assertEqual((state/'caller').stat().st_mode & 0o777,0o600)
            self.assertFalse((state/'routerctl').exists());self.assertNotIn(token,(state/'config.yaml').read_text())

if __name__=='__main__':unittest.main()
