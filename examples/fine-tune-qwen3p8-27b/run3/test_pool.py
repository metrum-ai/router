# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
from pathlib import Path
import tempfile
import unittest

import build_pool
import freeze


class FreshPoolTests(unittest.TestCase):
    def test_reproducible_frozen_disjoint_plan(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); pool = root/'pool'; out = root/'plan'
            manifest = build_pool.build(pool)
            self.assertEqual(manifest['seed_file_sha256'], freeze.SEED_SHA)
            selected = freeze.freeze(pool, out)
            self.assertEqual([len(selected[s]) for s in ('train','val','test')], [3000,500,300])
            for group in selected.values():
                for task in group:
                    if task['class'] == 'reviewed_general': self.assertNotIn(task['reference'], json.dumps(task['initial_messages']))
                    if task['class'] == 'native_tool': self.assertEqual(len(task['expected_calls']), 1)
            self.assertFalse((out/'precollection-freeze.json').exists())

    def test_changed_pool_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); pool = root/'pool'; build_pool.build(pool)
            with (pool/'seed-canonical.jsonl').open('a') as stream: stream.write(' ')
            with self.assertRaisesRegex(ValueError, 'does not match'): freeze.freeze(pool, root/'plan')
            self.assertFalse((root/'plan').exists())


if __name__ == '__main__': unittest.main()
