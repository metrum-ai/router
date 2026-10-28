#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline verifier controls; no provider requests are made."""
import unittest

from backend import Backend, ORDERS, credit


class BackendTests(unittest.TestCase):
    def answer(self, order_id):
        return {'order_id': order_id, 'credit_usd': credit(ORDERS[order_id]),
                'ticket_id': 'CASE-' + order_id}

    def solve(self, backend):
        oid = backend.order_id
        backend.execute('lookup_order', {'order_id': oid})
        amount = backend.execute('calculate_credit', {'order_id': oid})['credit_usd']
        backend.execute('apply_credit', {'order_id': oid, 'credit_usd': amount})
        backend.execute('create_ticket', {'order_id': oid})

    def test_reference_for_each_fixture(self):
        for oid in ORDERS:
            with self.subTest(order=oid):
                b = Backend(oid)
                self.solve(b)
                self.assertTrue(b.verify(self.answer(oid)))

    def test_noop_wrong_answer_missing_step_and_replay_fail(self):
        oid = 'ORD-101'
        self.assertFalse(Backend(oid).verify(self.answer(oid)))
        b = Backend(oid)
        self.solve(b)
        self.assertFalse(b.verify({**self.answer(oid), 'credit_usd': '0.00'}))
        b = Backend(oid)
        b.execute('lookup_order', {'order_id': oid})
        self.assertFalse(b.verify(self.answer(oid)))
        b = Backend(oid)
        self.solve(b)
        b.execute('create_ticket', {'order_id': oid})
        self.assertFalse(b.verify(self.answer(oid)))

    def test_state_is_isolated_and_invalid_arguments_fail_closed(self):
        one, two = Backend('ORD-101'), Backend('ORD-102')
        self.solve(one)
        self.assertFalse(two.verify(self.answer('ORD-102')))
        self.assertIn('error', two.execute('lookup_order', {'order_id': 'ORD-101'}))
        self.solve(two)
        self.assertFalse(two.verify(self.answer('ORD-102')))
        self.assertTrue(one.verify(self.answer('ORD-101')))

    def test_untrusted_tool_names_and_amount_types(self):
        b = Backend('ORD-101')
        self.assertIn('error', b.execute('shell', {'order_id': 'ORD-101', 'command': 'bad'}))
        b = Backend('ORD-101')
        b.execute('lookup_order', {'order_id': 'ORD-101'})
        b.execute('calculate_credit', {'order_id': 'ORD-101'})
        self.assertIn('error', b.execute('apply_credit', {'order_id': 'ORD-101', 'credit_usd': 6.0}))
        self.assertFalse(b.verify(self.answer('ORD-101')))


if __name__ == '__main__':
    unittest.main()
