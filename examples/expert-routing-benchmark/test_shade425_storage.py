# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
from collections import namedtuple
from pathlib import Path
import tempfile
import unittest
import shade425_storage as storage

Usage=namedtuple('Usage','total used free')

class StorageTests(unittest.TestCase):
    def test_advertised_and_configuration_storage(self):
        for offer in ({},{'storage_in_gb':299},{'storage_in_gb':'nan'},{'storage_in_gb':850,'configuration':{'storage_in_gb':100}}):
            self.assertFalse(storage.offer_storage_ok(offer))
        self.assertTrue(storage.offer_storage_ok({'storage_in_gb':300}))
        self.assertTrue(storage.offer_storage_ok({'storage_in_gb':850,'configuration':{'storage_in_gb':850}}))

    def test_select_real_capacity_and_free_space(self):
        with tempfile.TemporaryDirectory() as root:
            a=Path(root)/'small';b=Path(root)/'lowfree';c=Path(root)/'good'
            for p in (a,b,c):p.mkdir()
            values={str(a):Usage(100_000_000_000,0,100_000_000_000),str(b):Usage(850_000_000_000,800_000_000_000,50_000_000_000),str(c):Usage(850_000_000_000,100_000_000_000,750_000_000_000)}
            usage=lambda p:values[str(p)]
            self.assertEqual(storage.select([a,b,c],usage)['base'],str(c))
            with self.assertRaisesRegex(RuntimeError,'no mounted filesystem'):storage.select([a,b],usage)
            link=Path(root)/'alias';link.symlink_to(c,target_is_directory=True)
            with self.assertRaisesRegex(RuntimeError,'no mounted filesystem'):storage.select([link],usage)

if __name__=='__main__':unittest.main()
