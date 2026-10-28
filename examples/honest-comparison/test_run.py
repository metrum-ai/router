# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest
from contextlib import ExitStack
import tempfile

HERE=Path(__file__).resolve().parent

class ComparisonTests(unittest.TestCase):
    def test_hosted_cost_requires_reported_cached_split(self):
        spec=importlib.util.spec_from_file_location('hosted',HERE/'hosted.py');hosted=importlib.util.module_from_spec(spec);spec.loader.exec_module(hosted)
        self.assertIsNone(hosted.cost({'prompt_tokens':100,'completion_tokens':10}))
        self.assertEqual(hosted.cost({'prompt_tokens':100,'completion_tokens':10,'prompt_tokens_details':{'cached_tokens':20}}),0.00003612)

    def test_real_library_and_mock_backend(self):
        binary=os.environ.get('ROUTER_TEST_BINARY')
        if not binary:self.skipTest('set ROUTER_TEST_BINARY')
        env={k:v for k,v in os.environ.items() if k not in ('OPENAI_API_KEY','FIREWORKS_API_KEY','OPENROUTER_API_KEY','AWS_BEARER_TOKEN_BEDROCK')}
        p=subprocess.run([sys.executable,str(HERE/'run.py'),'--router',binary],env=env,text=True,capture_output=True,timeout=90)
        self.assertEqual(p.returncode,0,p.stderr);out=json.loads(p.stdout)
        self.assertEqual(out['backend'],'synthetic_mock');self.assertEqual(out['competitor'],'llm-router-lite 0.1.0')
        self.assertEqual(len(out['results']),12)
        self.assertEqual(out['summary']['metrum']['successes'],4);self.assertEqual(out['summary']['lite']['successes'],5)
        self.assertTrue(all(row['elapsed_ms']>0 for row in out['results']))

    def test_local_mode_with_fake_upstream(self):
        binary=os.environ.get('ROUTER_TEST_BINARY')
        if not binary:self.skipTest('set ROUTER_TEST_BINARY')
        root=HERE.parents[1];spec=importlib.util.spec_from_file_location('d3_e2e',root/'scripts/run_d3_policy_e2e.py');d=importlib.util.module_from_spec(spec);spec.loader.exec_module(d)
        env={k:v for k,v in os.environ.items() if k not in ('OPENAI_API_KEY','FIREWORKS_API_KEY','OPENROUTER_API_KEY','AWS_BEARER_TOKEN_BEDROCK')}
        with ExitStack() as stack,tempfile.TemporaryDirectory() as temp:
            server=d.upstream_server(stack)
            p=subprocess.run([sys.executable,str(HERE/'hosted.py'),'--router',binary,'--backend','local','--local-url',f'http://127.0.0.1:{server.server_port}/v1','--local-model','fake-model','--state-dir',str(Path(temp)/'private')],env=env,text=True,capture_output=True,timeout=90)
            self.assertEqual(p.returncode,0,p.stderr);out=json.loads(p.stdout)
            self.assertEqual(out['backend'],'local');self.assertEqual(len(out['results']),12);self.assertEqual(len(server.calls),12)
            self.assertIsNone(out['summary']['metrum']['usage_cost_usd']);self.assertIsNone(out['summary']['lite']['usage_cost_usd'])

if __name__=='__main__':unittest.main()
