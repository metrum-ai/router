# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import io
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

import reconcile
import run
import scoring
import suite


class BenchmarkTests(unittest.TestCase):
    def test_strict_scores_do_not_execute_code(self):
        self.assertTrue(scoring.score({'domain':'coding','expected':'B'}, {'content':'b'}))
        self.assertFalse(scoring.score({'domain':'coding','expected':'B'}, {'content':'B because ...'}))
        self.assertTrue(scoring.score({'domain':'math','expected':'1234'}, {'content':'1,234.0'}))
        self.assertFalse(scoring.score({'domain':'math','expected':'2'}, {'content':'1+1'}))
        case={'domain':'biomedical','tools':[{}],'expected':{'name':'lookup','arguments':{'x':1}}}
        message={'tool_calls':[{'function':{'name':'lookup','arguments':'{"x":1}'}}]}
        self.assertTrue(scoring.score(case,message));self.assertFalse(scoring.score(case,{'content':'lookup'}))

    def test_sse_ttft_and_incomplete_fail_closed(self):
        stream=io.BytesIO(b'data: {"choices":[{"delta":{"content":"A"}}]}\n\ndata: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
        import time
        result,ttft,usage=run.read_stream(stream,time.monotonic())
        self.assertEqual(result['content'],'A');self.assertIsNotNone(ttft);self.assertIsNone(usage)
        with self.assertRaises(ValueError):run.read_stream(io.BytesIO(b'data: [DONE]\n\n'),time.monotonic())

    def test_missing_decisions_remain_in_denominator(self):
        with tempfile.TemporaryDirectory() as temp:
            db=Path(temp)/'usage.db'
            with sqlite3.connect(db) as c:
                c.execute('create table request_policy_executions(request_id text,seq int,class_label text,duration_ms int)')
                c.execute('create table request_usage(request_id text,target_provider text)')
                c.executemany('insert into request_policy_executions values(?,?,?,?)',[('1',1,'domain:finance',2),('2',1,'domain:unknown',3)])
                c.executemany('insert into request_usage values(?,?)',[('1','finance'),('2','coding')])
            records=[{'case_id':'a','domain':'finance','request_id':'1'}, {'case_id':'b','domain':'out-of-domain','request_id':'2'},
                     {'case_id':'c','domain':'math','request_id':'missing'}]
            result=reconcile.reconcile(records,db)
            self.assertEqual((result['correct'],result['attempted'],result['missing_decisions']),(2,3,1))
            self.assertEqual(result['misroutes'][0]['case_id'],'c');self.assertEqual(result['latency_ms']['count'],2)

    def test_incomplete_cost_is_not_free_and_failure_not_success(self):
        rows=[{'domain':'coding','status':200,'passed':True,'latency_ms':10,'ttft_ms':2},
              {'domain':'coding','status':500,'passed':True,'latency_ms':20,'ttft_ms':None}]
        summary=scoring.summarize(rows,2)
        self.assertEqual(summary['quality_passed'],1);self.assertEqual(summary['ttft_ms']['count'],1)
        self.assertIsNone(summary['actual_gpu_cost_usd']);self.assertIsNone(summary['actual_gpu_cost_per_quality_pass_usd'])
        self.assertEqual(scoring.summarize(rows,2,actual_gpu_cost_usd=4)['actual_gpu_cost_per_quality_pass_usd'],4)

    def test_public_selection_reproducible(self):
        rows=[{'value':i} for i in range(30)]
        self.assertEqual(suite.pick('math',rows),suite.pick('math',list(reversed(rows))))
        with self.assertRaises(ValueError):suite.pick('math',[{'value':1}])


if __name__=='__main__':unittest.main()
