# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
from pathlib import Path
import tempfile
import unittest
import watcher
import charges


class TestWatcher(unittest.TestCase):
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
