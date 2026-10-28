# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline failure-path tests; no SSH, GPU or provider calls."""
import contextlib,hashlib,io,subprocess,tempfile,unittest
from pathlib import Path
import retrieve_adapter as retrieve

class RetrievalTests(unittest.TestCase):
    def test_failed_retrieval_retries_and_never_requests_teardown(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);out=root/'out';calls=[];sleeps=[];errors=io.StringIO();expected=hashlib.sha256(b'weights').hexdigest()
            def runner(argv,**kwargs):
                calls.append(argv)
                if argv[0]=='ssh' and 'sha256sum' in argv[-1]:return subprocess.CompletedProcess(argv,0,(expected+'  /data/adapter/adapter_config.json\n').encode(),b'')
                return subprocess.CompletedProcess(argv,1,b'',b'Permission denied\n')
            with contextlib.redirect_stderr(errors),self.assertRaisesRegex(RuntimeError,'VM remains under watcher'):
                retrieve.retrieve('user@127.0.0.1',22,root/'key',root/'known','/data',out,runner=runner,sleep=sleeps.append)
            self.assertEqual(sum(a[0]=='scp' for a in calls),3);self.assertEqual(sleeps,[2,4]);self.assertIn('Permission denied',errors.getvalue())
            self.assertFalse((root/'done.txt').exists());self.assertFalse((out/'manifest.json').exists())

    def test_fallback_and_sha_verification(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);out=root/'out';payload=b'test-adapter';expected=hashlib.sha256(payload).hexdigest();calls=[]
            def runner(argv,**kwargs):
                calls.append(argv)
                if argv[0]=='ssh' and 'sha256sum' in argv[-1]:return subprocess.CompletedProcess(argv,0,(expected+'  /data/adapter/file\n').encode(),b'')
                if argv[0]=='scp':return subprocess.CompletedProcess(argv,1,b'',b'permission denied')
                kwargs['stdout'].write(payload);return subprocess.CompletedProcess(argv,0,None,b'')
            with contextlib.redirect_stderr(io.StringIO()):manifest=retrieve.retrieve('user@127.0.0.1',22,root/'key',root/'known','/data',out,runner=runner,sleep=lambda _:None)
            self.assertEqual(sum(a[0]=='scp' for a in calls),6)
            self.assertEqual(set(manifest),set(retrieve.FILES))
            for name in retrieve.FILES:self.assertEqual((out/name).read_bytes(),payload);self.assertEqual(manifest[name]['sha256'],expected)
            self.assertTrue((out/'manifest.json').exists())

if __name__=='__main__':unittest.main()
