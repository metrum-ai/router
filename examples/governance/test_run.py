# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import ThreadingHTTPServer

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('governance_run',HERE/'run.py');run=importlib.util.module_from_spec(spec);spec.loader.exec_module(run)


class TestGovernance(unittest.TestCase):
    def test_mock_and_local(self):
        binary=os.environ.get('ROUTER_TEST_BINARY')
        if not binary:self.skipTest('set ROUTER_TEST_BINARY to run integration')
        env={k:v for k,v in os.environ.items() if k not in ('OPENAI_API_KEY','FIREWORKS_API_KEY','OPENROUTER_API_KEY','AWS_BEARER_TOKEN_BEDROCK')}
        def invoke(*args):
            p=subprocess.run([sys.executable,str(HERE/'run.py'),'--router',binary,*args],env=env,text=True,capture_output=True,timeout=90)
            self.assertEqual(p.returncode,0,p.stderr);return json.loads(p.stdout)
        mock=invoke()
        self.assertTrue(mock['mock_upstream_redaction_verified'])
        self.assertEqual(mock['statuses'],[200,403,429,429,200,429]);self.assertEqual(mock['ordinary_export_status'],403)
        run.Upstream.seen=[];server=ThreadingHTTPServer(('127.0.0.1',0),run.Upstream);thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        try:
            with tempfile.TemporaryDirectory() as temp:
                local=invoke('--backend','local','--local-url',f'http://127.0.0.1:{server.server_port}/v1','--local-model','fake','--state-dir',str(Path(temp)/'private'))
                self.assertEqual(local['mode'],'local');self.assertFalse(local['mock_upstream_redaction_verified'])
                self.assertEqual(len(run.Upstream.seen),2);self.assertTrue(run.Upstream.seen[0]['no_raw_pii']);self.assertTrue(run.Upstream.seen[0]['redacted'])
        finally:server.shutdown();server.server_close();thread.join()

if __name__=='__main__':unittest.main()
