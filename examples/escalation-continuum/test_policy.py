#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest
from policy import choose


class PolicyTests(unittest.TestCase):
    def test_length_boundaries_and_missing_tier(self):
        targets=[{'tier':tier,'keyConfigured':True,'weight':1} for tier in ('local','datacenter','frontier')]
        for chars,expected in [(0,0),(200,0),(201,1),(1000,1),(1001,2)]:
            self.assertEqual(choose({'context':{'textChars':chars},'targets':targets})['targetIndex'],expected)
        self.assertIsNone(choose({'context':{'textChars':1001},'targets':targets[:2]}))
        self.assertIsNone(choose({'context':{},'targets':targets}))
        self.assertIsNone(choose({'context':{'textChars':-1},'targets':targets}))


if __name__=='__main__': unittest.main()
