#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest
from policy import DOMAINS, choose


class PolicyTests(unittest.TestCase):
    def test_domains_and_fallback(self):
        targets=[{'tier':tier,'keyConfigured':True,'weight':1} for tier in (*DOMAINS,'general')]
        for index,domain in enumerate(DOMAINS):
            self.assertEqual(choose({'text':f'[domain:{domain}] synthetic','targets':targets})['targetIndex'],index)
        self.assertEqual(choose({'text':'unknown','targets':targets})['targetIndex'],4)
        self.assertEqual(choose({'text':'[domain:medical] [domain:legal]','targets':targets})['targetIndex'],4)
        self.assertIsNone(choose({'text':'unknown','targets':targets[:4]}))
        self.assertIsNone(choose({'targets':targets}))


if __name__=='__main__': unittest.main()
