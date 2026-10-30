# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest
import benchmark_policy as bp


class BenchmarkPolicyTests(unittest.TestCase):
    def setUp(self):
        self.cases=bp.load_cases()
        self.targets=[{'tier':t,'keyConfigured':True,'weight':1} for t in ('local','datacenter','frontier')]
    def payload(self,case):
        text=f"SYNTHETIC427:{case['id']}\n{case['prompt']}\nReturn only the number."
        return {'request':{'messages':[{'role':'user','content':text}]},'targets':self.targets}
    def test_cases_are_unique_and_cover_three_tiers_and_overrides(self):
        counts={}
        for case in self.cases:
            result=bp.choose(self.payload(case),self.cases);tier=self.targets[result['targetIndex']]['tier']
            counts[tier]=counts.get(tier,0)+1
            if case['sensitive']:self.assertEqual((tier,result['classLabel']),('local','synthetic-sensitive-local'))
            elif case['latency_budget_ms']<1000:self.assertEqual((tier,result['classLabel']),('local','synthetic-latency-local'))
        self.assertEqual(counts,{'local':6,'datacenter':3,'frontier':3})
    def test_rejects_unknown_tampered_or_ineligible(self):
        case=self.cases[0];payload=self.payload(case)
        payload['request']['messages'][0]['content']+=' tampered'
        self.assertIsNone(bp.choose(payload,self.cases))
        payload=self.payload(case);payload['targets'][0]['keyConfigured']=False
        self.assertIsNone(bp.choose(payload,self.cases))
        self.assertIsNone(bp.choose({'request':{'messages':[]}},self.cases))

if __name__=='__main__':unittest.main()
