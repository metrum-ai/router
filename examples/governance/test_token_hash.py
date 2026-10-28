# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import importlib.util
from pathlib import Path
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[2]
spec=importlib.util.spec_from_file_location('catalog_hash',ROOT/'examples/catalog/token_hash.py');secure=importlib.util.module_from_spec(spec);spec.loader.exec_module(secure)


class TokenHashTests(unittest.TestCase):
    def test_routerctl_replaces_legacy_and_removes_raw_token_file(self):
        with tempfile.TemporaryDirectory(dir='/dev/shm') as tmp:
            state=Path(tmp);ctl=secure.prepare(ROOT)
            caller={'token_sha256':'obsolete'};token='rtr_metrum_demo_project_dev_k424_'+'x'*32
            secure.apply(caller,ctl,token,state)
            self.assertNotIn('token_sha256',caller)
            self.assertTrue(caller['token_hash'].startswith('pbkdf2-sha3-512:'))
            self.assertEqual(caller['token_id'],'rtr_metrum_demo_project_dev_k424')
            self.assertFalse(list(state.glob('caller-*')))
            self.assertNotIn(token,str(caller))
            with self.assertRaises(RuntimeError):secure.apply({},ctl,'unstructured-token',state)
            self.assertFalse(list(state.glob('caller-*')))

if __name__=='__main__':unittest.main()
