#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Credential-free synthetic tests, not savings evidence."""
import copy
import unittest

from analyze import EvidenceError, summarize


def evidence():
    def arm(prefix, model, passed):
        return {
            'tasks': [{'trial_id': 'a', 'passed': passed[0]}, {'trial_id': 'b', 'passed': passed[1]}],
            'usage': [{'trial_id': k, 'request_id': prefix+k, 'model': model, 'status': 200,
                       'latency_ms': i, 'router_cost_usd': '0.01',
                       'price_source': 'https://example.org/price', 'price_date': '2026-01-01'}
                      for i, k in enumerate(('a', 'b'), 1)]}
    return {'workload': 'support-agent', 'verifier': 'deterministic-test', 'planned_trials': ['a', 'b'],
            'frontier_model': 'frontier', 'arms': {'routed': arm('r', 'cheap', [True, False]),
                                                    'frontier': arm('f', 'frontier', [False, True])}}


class AnalysisTests(unittest.TestCase):
    def test_missing_bill_is_not_zero_and_pairs_are_preserved(self):
        report = summarize(evidence())
        self.assertIsNone(report['arms']['routed']['provider_billed_usd'])
        self.assertEqual(report['arms']['routed']['router_recorded_cost_usd'], '0.02')
        self.assertEqual(report['paired'], {'wins': 1, 'losses': 1})
        self.assertEqual(report['arms']['routed']['attempts'], 2)
        self.assertGreater(report['arms']['routed']['wilson_ci95'][1], .5)

    def test_receipt_must_cover_exact_request_set(self):
        e = evidence()
        e['arms']['routed']['billing'] = {'scope': 'exact-request-set', 'source': 'provider receipt',
                                           'currency': 'USD', 'billed_usd': '0.012', 'request_ids': ['ra', 'rb']}
        self.assertEqual(summarize(e)['arms']['routed']['provider_billed_usd'], '0.012')
        e['arms']['routed']['billing']['request_ids'] = ['ra']
        with self.assertRaises(EvidenceError): summarize(e)

    def test_missing_and_duplicate_outcomes_rejected(self):
        for mutation in ('missing_task', 'missing_usage', 'duplicate_request', 'wrong_frontier', 'bad_date'):
            with self.subTest(mutation=mutation):
                e = copy.deepcopy(evidence())
                if mutation == 'missing_task': e['arms']['routed']['tasks'].pop()
                if mutation == 'missing_usage': e['arms']['routed']['usage'].pop()
                if mutation == 'duplicate_request': e['arms']['frontier']['usage'][0]['request_id'] = 'ra'
                if mutation == 'wrong_frontier': e['frontier_model'] = 'other'
                if mutation == 'bad_date': e['arms']['routed']['usage'][0]['price_date'] = 'not-a-date'
                with self.assertRaises(EvidenceError): summarize(e)

    def test_unbounded_or_invalid_amounts_are_rejected(self):
        for value in ('NaN', '-1', 'Infinity', 0.01):
            e = evidence()
            e['arms']['routed']['usage'][0]['router_cost_usd'] = value
            with self.subTest(value=value), self.assertRaises(EvidenceError): summarize(e)

    def test_quality_and_latency_are_not_inferred_from_http_success(self):
        e = evidence()
        e['arms']['routed']['usage'][0]['latency_ms'] = float('nan')
        with self.assertRaises(EvidenceError): summarize(e)
        e = evidence()
        self.assertEqual(summarize(e)['arms']['routed']['passes'], 1)


if __name__ == '__main__':
    unittest.main()
