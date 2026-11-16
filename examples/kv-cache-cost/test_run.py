#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
import os
from http.server import ThreadingHTTPServer
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from run import FakeUpstream


@unittest.skipUnless(os.environ.get('ROUTER_TEST_BINARY'),'set ROUTER_TEST_BINARY to a built router')
class Smoke(unittest.TestCase):
    def test_local_mode_with_fake_upstream(self):
        FakeUpstream.cache=set();server=ThreadingHTTPServer(('127.0.0.1',0),FakeUpstream)
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        try:
            with tempfile.TemporaryDirectory() as d:
                cmd=[sys.executable,str(Path(__file__).with_name('run.py')),'--router',os.environ['ROUTER_TEST_BINARY'],'--backend','local','--local-url',f'http://127.0.0.1:{server.server_port}/v1','--local-model-a','mock-a','--local-model-b','mock-b','--state-dir',str(Path(d)/'state')]
                env={k:v for k,v in os.environ.items() if k not in ('FIREWORKS_API_KEY','OPENAI_API_KEY','OPENROUTER_API_KEY')}
                p=subprocess.run(cmd,capture_output=True,text=True,timeout=90,env=env)
                self.assertEqual(p.returncode,0,p.stderr)
                result=json.loads(p.stdout)
                rows=result['requests']
                self.assertEqual(len(rows),10)
                self.assertEqual({r['arm'] for r in rows},{'affinity_off','affinity_on'})
                self.assertEqual(sum(r['kind']=='tiered_32000' for r in rows),2)
                self.assertTrue(all(r['kind']=='tiered_32000' and r['model']=='mock-a' or r['kind']!='tiered_32000' for r in rows))
                off_short=[r['model'] for r in rows if r['arm']=='affinity_off' and r['kind']=='short']
                on_short=[r['model'] for r in rows if r['arm']=='affinity_on' and r['kind']=='short']
                self.assertEqual(len(off_short),4)
                self.assertEqual(len(on_short),4)
                self.assertTrue(all(r['usage_cost_usd']==0 for r in rows))
                self.assertTrue(all(r['ttfb_ms'] is not None for r in rows))
                off,on=result['summary']
                self.assertEqual((off['group'],off['affinity_enabled'],off['requests']),('comparison',False,5))
                self.assertEqual((on['group'],on['affinity_enabled'],on['requests']),('comparison',True,5))
        finally:
            server.shutdown();server.server_close();thread.join()


if __name__=='__main__':unittest.main()
