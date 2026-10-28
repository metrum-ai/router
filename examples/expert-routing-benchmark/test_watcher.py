# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch
import watcher
import charges
import lease
import diagnose_ssh


class TestWatcher(unittest.TestCase):
    def test_attachment_must_return_explicit_success(self):
        for response in ({'success':False,'msg':'untrusted'}, {}, None):
            with self.assertRaisesRegex(RuntimeError,'attachment not confirmed'):
                lease.attach_key(42,'test-public-key',request=lambda *a:response)
        calls=[]
        lease.attach_key(42,'test-public-key',request=lambda *a:(calls.append(a) or {'success':True}))
        self.assertEqual(calls[0][1],'/instances/42/ssh/')
        self.assertTrue(calls[0][-1])

    def test_failed_attachment_triggers_teardown_intent(self):
        with tempfile.TemporaryDirectory() as directory:
            state=Path(directory);(state/'intent.json').write_text(json.dumps({'label':'metrum-426-'+'d'*32,
                'deadline_unix':time.time()+3600,'reservation_limit_usd':75}))
            def api(cloud,path,method='GET',payload=None,execute=False):
                if path.startswith('/bundles/'):return {'offers':[{'id':123,'dph_total':10}]}
                if path=='/asks/123/':return {'new_contract':42}
                if path=='/instances/42/ssh/':return {'success':False}
                raise AssertionError('unexpected API request')
            def keygen(*args,**kwargs):(state/'instance-key.pub').write_text('public-test-key')
            with patch.object(lease,'assert_watcher'),patch.object(lease.subprocess,'run',side_effect=keygen),patch.object(lease.watcher.cloud,'request',side_effect=api):
                with self.assertRaisesRegex(RuntimeError,'attachment not confirmed'):lease.create(state,123,30)
            self.assertEqual((state/'done.txt').read_text(),'GPU-DONE #426\n')
            self.assertTrue((state/'creation-terminal').exists());self.assertEqual(json.loads((state/'lease.json').read_text())['id'],42)

    def test_diagnostic_uses_only_instance_scoped_key(self):
        with tempfile.TemporaryDirectory() as directory:
            state=Path(directory);(state/'intent.json').write_text(json.dumps({'label':'metrum-426-'+'e'*32,
                'deadline_unix':time.time()+600,'reservation_limit_usd':1,'profile':'ssh-diagnostic'}))
            calls=[]
            def api(cloud,path,method='GET',payload=None,execute=False):
                calls.append((path,method))
                if path.startswith('/bundles/'):return {'offers':[{'id':123,'dph_total':0.1}]}
                if path=='/asks/123/':return {'new_contract':42}
                if path=='/instances/42/ssh/':return {'success':True}
                raise AssertionError('unexpected API request')
            def keygen(*args,**kwargs):(state/'instance-key.pub').write_text('ssh-ed25519 TESTKEY comment')
            with patch.object(lease,'assert_watcher'),patch.object(lease.subprocess,'run',side_effect=keygen),patch.object(lease.watcher.cloud,'request',side_effect=api):lease.create(state,123,1)
            self.assertIn(('/instances/42/ssh/','POST'),calls)
            self.assertTrue((state/'creation-terminal').exists());self.assertFalse(any(path=='/ssh/' for path,_ in calls))

    def test_diagnostic_records_formats_and_requests_teardown(self):
        import sys
        with tempfile.TemporaryDirectory() as directory:
            state=Path(directory)
            (state/'intent.json').write_text(json.dumps({'profile':'ssh-diagnostic'}));(state/'lease.json').write_text(json.dumps({'id':42}))
            (state/'instance-key').write_text('test');now=[0]
            def keygen(argv,**kwargs):Path(argv[-1]+'.pub').write_text('ssh-ed25519 TEST')
            def api(*a,**k):return {'instances':{'actual_status':'running','ssh_host':'ssh6.vast.ai','ssh_port':11111}}
            def probe(state,host,port,key):return key.name in ('instance-key','diagnostic-rsa')
            def sleep(seconds):now[0]+=seconds
            with patch.object(sys,'argv',['diagnose_ssh','--state-dir',str(state)]),patch.object(lease,'assert_watcher'),patch.object(lease,'attach_key'),patch.object(watcher.cloud,'request',side_effect=api),patch.object(diagnose_ssh,'probe',side_effect=probe),patch.object(diagnose_ssh.subprocess,'run',side_effect=keygen),patch.object(diagnose_ssh.time,'monotonic',side_effect=lambda:now[0]),patch.object(diagnose_ssh.time,'sleep',side_effect=sleep),patch.object(diagnose_ssh.signal,'signal'):
                diagnose_ssh.main()
            result=json.loads((state/'diagnostic-result.json').read_text());self.assertTrue(result['initial_ed25519']);self.assertFalse(result['late_ed25519']);self.assertTrue(result['late_rsa'])
            self.assertEqual((state/'done.txt').read_text(),'GPU-DONE #426\n')

    def test_billing_filters_instance_and_missing_is_unknown(self):
        def fake(cloud,path):
            return {'success':True,'results':[{'source':'instance-42','amount':1.25},
                    {'source':'instance-99','amount':999},{'source':'instance-42','amount':0.01}],'next_token':None}
        self.assertEqual(charges.collect(42,100,200,request=fake)['observed_billed_usd'],'1.26')
        self.assertIsNone(charges.collect(123,100,200,request=fake)['observed_billed_usd'])

    def test_deadline_deletes_only_exact_instance_and_removes_key(self):
        with tempfile.TemporaryDirectory() as directory:
            state=Path(directory);label='metrum-426-'+'a'*32
            (state/'intent.json').write_text(json.dumps({'label':label,'deadline_unix':1005,'reservation_limit_usd':75}))
            (state/'lease.json').write_text(json.dumps({'id':42,'created_at':'1970-01-01T00:16:40+00:00','hourly_quote_usd':30}));(state/'creation-terminal').touch();(state/'instance-key').write_text('test')
            now=[1000];calls=[];live=[False]
            def fake(cloud,path,method='GET',payload=None,execute=False):
                calls.append((path,method,execute))
                if method=='DELETE' and not execute:return {'dry_run':True,'method':'DELETE'}
                if method=='DELETE':self.assertEqual(path,'/instances/42/');live[0]=False;return {'success':True}
                # First listing proves no same label; thereafter lease is visible until deleted.
                if len(calls)==1:live[0]=True;return {'instances':[{'id':99,'label':'other','actual_status':'running'}]}
                items=[{'id':99,'label':'other','actual_status':'running'}]
                if live[0]:items.append({'id':42,'label':label,'actual_status':'running'})
                return {'instances':items}
            def sleep(seconds):now[0]+=seconds
            watcher.watch(state,request=fake,now=lambda:now[0],sleep=sleep)
            result=json.loads((state/'teardown.json').read_text());self.assertTrue(result['deletion_verified']);self.assertEqual(result['reason'],'deadline')
            self.assertFalse((state/'instance-key').exists())
            self.assertIn(('/instances/0/','DELETE',False),calls)
            self.assertEqual([c for c in calls if c[2]], [('/instances/42/','DELETE',True)])

    def test_billed_threshold_triggers_early(self):
        with tempfile.TemporaryDirectory() as directory:
            state=Path(directory);label='metrum-426-'+'c'*32
            (state/'intent.json').write_text(json.dumps({'label':label,'deadline_unix':2000,'reservation_limit_usd':75}))
            (state/'lease.json').write_text(json.dumps({'id':42,'created_at':'1970-01-01T00:16:40+00:00','hourly_quote_usd':30}));(state/'creation-terminal').touch()
            now=[1000];deleted=[False]
            def fake(cloud,path,method='GET',payload=None,execute=False):
                if method=='DELETE' and not execute:return {'dry_run':True,'method':'DELETE'}
                if method=='DELETE':deleted[0]=True;return {'success':True}
                if path.startswith('/charges?'):return {'success':True,'results':[{'source':'instance-42','amount':75}],'next_token':None}
                return {'instances':[]}
            watcher.watch(state,request=fake,now=lambda:now[0],sleep=lambda s:now.__setitem__(0,now[0]+s))
            result=json.loads((state/'teardown.json').read_text());self.assertEqual(result['reason'],'observed billing threshold');self.assertTrue(deleted[0]);self.assertLess(now[0],2000)

    def test_ambiguous_creation_waits_for_settling(self):
        with tempfile.TemporaryDirectory() as directory:
            state=Path(directory);label='metrum-426-'+'b'*32
            (state/'intent.json').write_text(json.dumps({'label':label,'deadline_unix':10}))
            now=[0]
            def fake(cloud,path,method='GET',payload=None,execute=False):
                if method=='DELETE':return {'dry_run':True,'method':'DELETE'}
                return {'instances':[]}
            def sleep(seconds):
                now[0]+=seconds
                if now[0]>=400:
                    self.assertFalse((state/'teardown.json').exists())
                    (state/'creation-terminal').touch()
            watcher.watch(state,request=fake,now=lambda:now[0],sleep=sleep)
            self.assertGreaterEqual(now[0],400)
            self.assertTrue(json.loads((state/'teardown.json').read_text())['deletion_verified'])


if __name__=='__main__':unittest.main()
