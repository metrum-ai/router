#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Live-router tests use only synthetic local upstreams."""
import json
import os
from http.server import ThreadingHTTPServer
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest

from run import Upstream


@unittest.skipUnless(os.environ.get('ROUTER_TEST_BINARY'), 'set ROUTER_TEST_BINARY to a built router')
class ExampleSmoke(unittest.TestCase):
    def test_mock_and_self_hosted(self):
        server=ThreadingHTTPServer(('127.0.0.1',0),Upstream)
        thread=threading.Thread(target=server.serve_forever,daemon=True); thread.start()
        try:
            with tempfile.TemporaryDirectory() as temp:
                base=[sys.executable,str(Path(__file__).with_name('run.py')),'--router',os.environ['ROUTER_TEST_BINARY']]
                env={k:v for k,v in os.environ.items() if k not in ('FIREWORKS_API_KEY','OPENAI_API_KEY','OPENROUTER_API_KEY')}
                for args,label in [([], 'synthetic-mock-not-provider-billing'),
                                   (['--backend','local','--local-url',f'http://127.0.0.1:{server.server_port}/v1','--local-model','mock-model','--state-dir',str(Path(temp)/'state')], 'self-hosted-excludes-compute')]:
                    p=subprocess.run(base+args,capture_output=True,text=True,timeout=60,env=env)
                    self.assertEqual(p.returncode,0,p.stderr)
                    result=json.loads(p.stdout)
                    self.assertEqual(result['mode'],label)
                    self.assertEqual(result['statuses'],[200,200,429])
                    self.assertEqual([r['owner'] for r in result['attribution']],['alice','bob'])
                    if label=='self-hosted-excludes-compute':
                        self.assertTrue(all(row['usage_cost_usd']==0 for row in result['attribution']))
                bad=subprocess.run(base+['--backend','local','--local-url','http://example.com/v1','--state-dir',str(Path(temp)/'bad')],capture_output=True,text=True,timeout=30,env=env)
                self.assertNotEqual(bad.returncode,0)
        finally:
            server.shutdown(); server.server_close(); thread.join()


if __name__=='__main__': unittest.main()
