# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch
import urllib.error
import shade_lease
import shade_watcher

KEY='11111111-1111-4111-8111-111111111111'
INSTANCE='22222222-2222-4222-8222-222222222222'
OTHER='33333333-3333-4333-8333-333333333333'

def not_found():return urllib.error.HTTPError('https://example.invalid',404,'missing',{},None)

class ShadeGuardTests(unittest.TestCase):
    def test_exact_key_and_instance_cleanup_without_key_listing(self):
        with tempfile.TemporaryDirectory() as directory:
            s=Path(directory);name='metrum-426-'+'a'*32
            (s/'intent.json').write_text(json.dumps({'name':name,'deadline_unix':1005,'reservation_usd':65}))
            (s/'lease.json').write_text(json.dumps({'id':INSTANCE,'created_at':'1970-01-01T00:16:40+00:00','hourly_quote_usd':10}))
            (s/'key.json').write_text(json.dumps({'id':KEY}));(s/'instance-key').write_text('secret');(s/'instance-key.pub').write_text('public')
            now=[1000];present=[False];key=[True];calls=[]
            def api(cloud,path,method='GET',payload=None,execute=False):
                self.assertFalse(method=='GET' and path.startswith('/sshkeys'));calls.append((path,method,execute))
                if method=='POST' and not execute:return {'dry_run':True,'method':'POST'}
                if method=='POST' and path==f'/instances/{INSTANCE}/delete':present[0]=False;return {}
                if method=='POST' and path==f'/sshkeys/{KEY}/delete':
                    if not key[0]:raise not_found()
                    key[0]=False;return {}
                if path=='/instances':
                    if len(calls)==1:present[0]=True;return {'instances':[{'id':OTHER,'name':'unrelated'}]}
                    return {'instances':[{'id':OTHER,'name':'unrelated'}]+([{'id':INSTANCE,'name':name}] if present[0] else [])}
                raise AssertionError('unexpected mutation')
            shade_watcher.watch(s,request=api,now=lambda:now[0],sleep=lambda seconds:now.__setitem__(0,now[0]+seconds))
            self.assertTrue(json.loads((s/'teardown.json').read_text())['key_absent_verified'])
            self.assertFalse((s/'instance-key').exists());self.assertFalse((s/'instance-key.pub').exists())
            self.assertTrue(all(OTHER not in path for path,_,execute in calls if execute))
            self.assertEqual([path for path,method,execute in calls if execute and path.startswith('/sshkeys/')],[f'/sshkeys/{KEY}/delete']*2)

    def test_ambiguous_key_add_does_not_claim_absence(self):
        with tempfile.TemporaryDirectory() as directory:
            s=Path(directory);name='metrum-426-'+'b'*32
            (s/'intent.json').write_text(json.dumps({'name':name,'deadline_unix':5,'reservation_usd':65}));(s/'key-started').touch();(s/'creation-terminal').touch();now=[0]
            def api(cloud,path,method='GET',payload=None,execute=False):
                if method=='POST':return {'dry_run':True,'method':'POST'}
                return {'instances':[]}
            def sleep(sec):
                now[0]+=sec
                if now[0]>=400:
                    self.assertFalse((s/'teardown.json').exists());(s/'key-terminal').touch()
            shade_watcher.watch(s,request=api,now=lambda:now[0],sleep=sleep)
            self.assertGreaterEqual(now[0],400)

    def test_lost_key_registration_response_stays_ambiguous(self):
        with tempfile.TemporaryDirectory() as directory:
            s=Path(directory);intent={'name':'metrum-426-'+'d'*32,'deadline_unix':time.time()+600,'reservation_usd':65}
            (s/'intent.json').write_text(json.dumps(intent))
            def api(cloud,path,method='GET',payload=None,execute=False):
                if path=='/instances/types?available=true':return {'instance_types':[{'cloud':'provider','shade_instance_type':'H200x4','num_gpus':4,'gpu_type':'H200','hourly_price':1600,'availability':[{'region':'r','available':True,'rental_type':'on_demand'}]}]}
                if path=='/sshkeys/add':raise TimeoutError('ambiguous')
                raise AssertionError('instance must not be created')
            def keygen(*a,**k):(s/'instance-key.pub').write_text('ssh-ed25519 TEST')
            with patch.object(shade_lease,'assert_watcher'),patch.object(shade_lease.subprocess,'run',side_effect=keygen),patch.object(shade_lease.watcher.cloud,'request',side_effect=api),patch.object(shade_lease.charges,'collect',return_value={'observed_billed_usd':'2'}):
                with self.assertRaises(TimeoutError):shade_lease.create(s,'provider','H200x4','r',20)
            self.assertTrue((s/'key-started').exists());self.assertFalse((s/'key-terminal').exists())
            self.assertTrue((s/'creation-terminal').exists());self.assertTrue((s/'done.txt').exists())

    def test_create_uses_own_key_and_no_private_material(self):
        with tempfile.TemporaryDirectory() as directory:
            s=Path(directory);intent={'name':'metrum-426-'+'c'*32,'deadline_unix':time.time()+600,'reservation_usd':65}
            (s/'intent.json').write_text(json.dumps(intent));calls=[]
            def api(cloud,path,method='GET',payload=None,execute=False):
                self.assertFalse(method=='GET' and path.startswith('/sshkeys'));calls.append((path,method,payload))
                if path=='/instances/types?available=true':return {'instance_types':[{'cloud':'provider','shade_instance_type':'H200x4','num_gpus':4,'gpu_type':'H200','hourly_price':1600,'availability':[{'region':'r','available':True,'rental_type':'on_demand'}]}]}
                if path=='/sshkeys/add':return {'id':KEY}
                if path=='/instances/create':return {'id':INSTANCE}
                raise AssertionError('unexpected API request')
            def keygen(*a,**k):(s/'instance-key.pub').write_text('ssh-ed25519 TEST')
            with patch.object(shade_lease,'assert_watcher'),patch.object(shade_lease.subprocess,'run',side_effect=keygen),patch.object(shade_lease.watcher.cloud,'request',side_effect=api),patch.object(shade_lease.charges,'collect',return_value={'observed_billed_usd':'2'}):
                shade_lease.create(s,'provider','H200x4','r',20)
            add=[p for path,_,p in calls if path=='/sshkeys/add'][0];body=[p for path,_,p in calls if path=='/instances/create'][0]
            self.assertEqual(set(add),{'name','public_key'});self.assertEqual(body['ssh_key_id'],KEY)
            self.assertNotIn('envs',body);self.assertEqual(body['auto_delete']['spend_threshold'],'65')

if __name__=='__main__':unittest.main()
