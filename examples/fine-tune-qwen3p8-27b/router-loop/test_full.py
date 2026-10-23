# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import full
import backend


class FullProducerTests(unittest.TestCase):
    def test_budget_reservations_fail_before_dispatch(self):
        ledger=full.Ledger()
        ledger.spent=19.4999
        with self.assertRaises(RuntimeError): ledger.reserve({'messages':[]})
        self.assertEqual(ledger.calls,0)
        ledger.spent=0
        reservation=ledger.reserve({'messages':[]})
        ledger.settle(reservation,{'usage':{'prompt_tokens':100,'completion_tokens':10}})
        self.assertAlmostEqual(ledger.reserved,0)
        self.assertAlmostEqual(ledger.spent,100*.15/1e6+10*.6/1e6)
        ledger.calls=9400
        with self.assertRaises(RuntimeError): ledger.reserve({})

    def test_plan_is_fixed_group_disjoint_and_gold_is_not_input(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);pool=root/'pool';pool.mkdir();out=root/'out';out.mkdir()
            rows=[];classes={}
            for kind in ('native_tool','reviewed_general','computed_letter'):
                for index in range(3):
                    row={'messages':[{'role':'user','content':kind+' task '+str(index)}], 'tools':[]}
                    if kind=='native_tool':
                        row['tools']=[{'type':'function','function':{'name':'lookup','parameters':{'type':'object'}}}]
                        row['messages'].append({'role':'assistant','tool_calls':[{'id':'x','type':'function','function':{'name':'lookup','arguments':'{}'}}]})
                    else: row['messages'].append({'role':'assistant','content':'A' if kind=='computed_letter' else 'HIDDEN_GOLD'})
                    classes[full.user_hash(row)]=kind;rows.append(row)
            path=pool/'seed-canonical.jsonl';path.write_text(''.join(json.dumps(r)+'\n' for r in rows))
            (pool/'class-by-normalized-user-sha256.json').write_text(json.dumps(classes));(pool/'manifest.json').write_text('{}')
            matrix={s:{'native_tool':1,'reviewed_general':1,'computed_letter':1} for s in ('train','val','test')}
            with patch.object(full,'MATRIX',matrix),patch.object(full,'SEED_SHA',hashlib.sha256(path.read_bytes()).hexdigest()):
                selected=full.prepare(pool,out)
            self.assertEqual([len(v) for v in selected.values()],[3,3,3])
            hashes=[t['task_hash'] for group in selected.values() for t in group]
            self.assertEqual(len(set(hashes)),9)
            for group in selected.values():
                for task in group:
                    self.assertNotIn('HIDDEN_GOLD',json.dumps(task['initial_messages']))
            self.assertEqual(len(json.loads((out/'heldout-gold.json').read_text())),3)

    def test_a_to_j_and_remote_dynamic_refs(self):
        fixture={'version':'router-replay-v1','backend':'acme-support-v1','checker':'expected-tool-calls-v1',
                 'seed':1,'tools':[],'expected_calls':[],'expected_final':'J'}
        self.assertTrue(backend.success(backend.initialize(fixture),{'content':'J','finish_reason':'stop'}))
        self.assertFalse(backend.success(backend.initialize(fixture),{'content':'J','finish_reason':'length'}))
        with self.assertRaises(ValueError): backend.local_refs({'$dynamicRef':'https://invalid.example/schema'})

if __name__=='__main__':unittest.main()
