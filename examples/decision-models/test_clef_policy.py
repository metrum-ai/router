# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest
import clef_policy


class TestClefPolicy(unittest.TestCase):
    def setUp(self):self.payload={'targets':[{'tier':'cheap','keyConfigured':True},{'tier':'strong','keyConfigured':True}]}
    def test_tiers_and_low_confidence_escalation(self):
        def answer(choice,confidence):return {'answers':{'tier':{'choice':choice,'confidence':confidence}}}
        self.assertEqual(clef_policy.pick(self.payload,answer('cheap',.9))['targetIndex'],0)
        self.assertEqual(clef_policy.pick(self.payload,answer('strong',.9))['targetIndex'],1)
        self.assertEqual(clef_policy.pick(self.payload,answer('cheap',.2))['targetIndex'],1)
    def test_reject_malformed_and_ineligible(self):
        for value in (-.1,1.1,float('nan'),True,'0.9'):
            with self.assertRaises(ValueError):clef_policy.pick(self.payload,{'answers':{'tier':{'choice':'cheap','confidence':value}}})
        self.payload['targets'][1]['keyConfigured']=False
        with self.assertRaises(ValueError):clef_policy.pick(self.payload,{'answers':{'tier':{'choice':'strong','confidence':.9}}})

if __name__=='__main__':unittest.main()
