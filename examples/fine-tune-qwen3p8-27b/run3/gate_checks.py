# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import json
from pathlib import Path
import unittest
import build_gate

spec=importlib.util.spec_from_file_location('tools',Path(__file__).resolve().parents[1]/'eval/scorers/tools.py')
tools=importlib.util.module_from_spec(spec);spec.loader.exec_module(tools)


class TestBuildGate(unittest.TestCase):
    def rows(self):
        rows=[]
        for n in range(300):
            native=n<200;klass='native_tool' if native else ('reviewed_general' if n<275 else 'computed_letter')
            rows.append({'task_hash':str(n),'class':klass,'initial_messages':[{'role':'user','content':'question'}],
                'tools':[{'function':{'name':'lookup'}}] if native else [],
                'expected_calls':[{'name':'lookup','arguments':{'key':n}}] if native else [],'reference':'answer'})
        return rows

    def test_uses_frozen_reference_for_exact_tool_gate(self):
        tests=build_gate.render(self.rows());self.assertEqual(len(tests),300)
        target=tests[0]['vars']['expected'];self.assertEqual(tools.score({'tool_calls':target},target,['lookup'])['exact'],1)
        self.assertEqual(tools.score({'tool_calls':[{'function':{'name':'lookup','arguments':{'key':'0'}}}]},target,['lookup'])['exact'],0)
        self.assertIn('Reference answer: answer',tests[-1]['assert'][0]['value'])
        self.assertEqual(json.loads(tests[0]['vars']['messages'])[-1]['content'],'question')

    def test_fails_closed_for_duplicate_or_bad_mix(self):
        rows=self.rows();rows[1]['task_hash']=rows[0]['task_hash']
        with self.assertRaises(ValueError):build_gate.render(rows)
        rows=self.rows();rows[0]['class']='reviewed_general'
        with self.assertRaises(ValueError):build_gate.render(rows)


if __name__=='__main__':unittest.main()
