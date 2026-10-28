#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Self-hosted-mode smoke uses a local fake upstream only."""
import importlib.util
import json
import os
from http.server import ThreadingHTTPServer
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest

ROOT=Path(__file__).resolve().parents[2]


@unittest.skipUnless(os.environ.get('ROUTER_TEST_BINARY'),'set ROUTER_TEST_BINARY to a built router')
class Smoke(unittest.TestCase):
    def test_local_backend_with_mock_servers(self):
        spec=importlib.util.spec_from_file_location('demo',ROOT/'examples/tokenomics/run.py');demo=importlib.util.module_from_spec(spec);spec.loader.exec_module(demo)
        server=ThreadingHTTPServer(('127.0.0.1',0),demo.Upstream)
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        try:
            with tempfile.TemporaryDirectory() as d:
                config=json.loads(Path(__file__).with_name('config.example.json').read_text())
                config['local_urls']={k:f'http://127.0.0.1:{server.server_port}/v1' for k in config['models']}
                path=Path(d)/'config.json';path.write_text(json.dumps(config))
                cmd=[sys.executable,str(Path(__file__).with_name('run.py')),'--router',os.environ['ROUTER_TEST_BINARY'],'--backend','local','--config',str(path),'--state-dir',str(Path(d)/'state')]
                env={k:v for k,v in os.environ.items() if k not in ('FIREWORKS_API_KEY','OPENAI_API_KEY','OPENROUTER_API_KEY')}
                result=subprocess.run(cmd,capture_output=True,text=True,timeout=60,env=env)
                self.assertEqual(result.returncode,0,result.stderr)
                data=json.loads(result.stdout)
                self.assertEqual([r['declared_domain'] for r in data['routes']],['medical','legal','finance','code','general'])
                self.assertTrue(all(r['usage_cost_usd']==0 for r in data['routes']))
        finally:
            server.shutdown();server.server_close();thread.join()


if __name__=='__main__': unittest.main()
