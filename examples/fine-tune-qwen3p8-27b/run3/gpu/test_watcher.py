# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch
import charges
import lease
import watcher


class SafetyTests(unittest.TestCase):
    def test_reject_missing_attachment_confirmation(self):
        for result in ({}, {'success':False}, None):
            with self.assertRaisesRegex(RuntimeError,'attachment not confirmed'):
                lease.attach_key(42,'test-public',request=lambda *a:result)

    def test_creation_failure_signals_teardown(self):
        with tempfile.TemporaryDirectory() as directory:
            state=Path(directory)
            (state/'intent.json').write_text(json.dumps({'label':'metrum-425-'+'a'*32,'deadline_unix':time.time()+3600,
                'reservation_limit_usd':100,'profile':'run3-training'}))
            def api(cloud,path,method='GET',payload=None,execute=False):
                if path.startswith('/bundles/'):return {'offers':[{'id':123,'dph_total':5,'gpu_name':'H200'}]}
                if path=='/asks/123/':return {'new_contract':42}
                if path=='/instances/42/ssh/':return {'success':False}
                raise AssertionError('unexpected API request')
            def keygen(*a,**k):(state/'instance-key.pub').write_text('test-public')
            with patch.object(lease,'assert_watcher'),patch.object(lease.subprocess,'run',side_effect=keygen),patch.object(lease.watcher.cloud,'request',side_effect=api):
                with self.assertRaisesRegex(RuntimeError,'attachment not confirmed'):lease.create(state,123,10)
            self.assertEqual((state/'done.txt').read_text(),'GPU-DONE #425\n')
            self.assertEqual(json.loads((state/'lease.json').read_text())['id'],42)

    def test_watcher_deletes_only_own_instance_and_key(self):
        with tempfile.TemporaryDirectory() as directory:
            state=Path(directory);label='metrum-425-'+'a'*32
            (state/'intent.json').write_text(json.dumps({'label':label,'deadline_unix':1005,'reservation_limit_usd':100}))
            (state/'lease.json').write_text(json.dumps({'id':42,'created_at':'1970-01-01T00:16:40+00:00','hourly_quote_usd':5}))
            (state/'creation-terminal').touch();(state/'instance-key').write_text('test');(state/'instance-key.pub').write_text('public')
            now=[1000];live=[False];calls=[]
            def api(cloud,path,method='GET',payload=None,execute=False):
                calls.append((path,method,execute))
                if method=='DELETE' and not execute:return {'dry_run':True,'method':'DELETE'}
                if method=='DELETE':live[0]=False;return {'success':True}
                if len(calls)==1:live[0]=True;return {'instances':[{'id':99,'label':'other','actual_status':'running'}]}
                return {'instances':([{'id':99,'label':'other','actual_status':'running'}]+([{'id':42,'label':label,'actual_status':'running'}] if live[0] else []))}
            watcher.watch(state,request=api,now=lambda:now[0],sleep=lambda x:now.__setitem__(0,now[0]+x))
            self.assertEqual([c for c in calls if c[2]],[('/instances/42/','DELETE',True)])
            self.assertTrue(json.loads((state/'teardown.json').read_text())['deletion_verified'])
            self.assertFalse((state/'instance-key').exists());self.assertFalse((state/'instance-key.pub').exists())

    def test_charge_accounting_is_instance_specific_and_unknown_is_not_zero(self):
        def api(*a):return {'success':True,'results':[{'source':'instance-42','amount':1.25},{'source':'instance-99','amount':999}],'next_token':None}
        self.assertEqual(charges.collect(42,100,200,request=api)['observed_billed_usd'],'1.25')
        self.assertIsNone(charges.collect(123,100,200,request=api)['observed_billed_usd'])

    def test_ambiguous_creation_never_claims_early_absence(self):
        with tempfile.TemporaryDirectory() as directory:
            state=Path(directory);label='metrum-425-'+'b'*32
            (state/'intent.json').write_text(json.dumps({'label':label,'deadline_unix':10}));now=[0]
            def api(cloud,path,method='GET',payload=None,execute=False):
                if method=='DELETE':return {'dry_run':True,'method':'DELETE'}
                return {'instances':[]}
            def sleep(seconds):
                now[0]+=seconds
                if now[0]>=400:
                    self.assertFalse((state/'teardown.json').exists());(state/'creation-terminal').touch()
            watcher.watch(state,request=api,now=lambda:now[0],sleep=sleep)
            self.assertGreaterEqual(now[0],400)
            self.assertTrue(json.loads((state/'teardown.json').read_text())['deletion_verified'])

    def test_explicit_budget_ceiling(self):
        with tempfile.TemporaryDirectory() as parent:
            with self.assertRaisesRegex(ValueError,'reservation'):
                lease.init(Path(parent)/'state',60,131)


if __name__=='__main__':unittest.main()
