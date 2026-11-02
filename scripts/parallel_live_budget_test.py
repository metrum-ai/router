# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline tests for the live gate's reservation and usage accounting."""
import json
import threading
import unittest
from decimal import Decimal

from parallel_live_budget import Budget, list_cost, read_usage


class BudgetTest(unittest.TestCase):
    def test_reservation_rejects_unbounded_calls_and_hard_caps(self):
        for cap in (Decimal("0"), Decimal("5.01"), Decimal("NaN")):
            with self.assertRaises(ValueError):
                Budget(cap)
        b = Budget()
        for payload in ({}, {"max_tokens": 255}, {"max_tokens": True}, {"max_tokens": 2049}):
            with self.assertRaises(ValueError):
                b.reserve("anthropic", json.dumps(payload).encode())
        self.assertEqual(b.reserved, 0)
        a = b.reserve("anthropic", b'{"max_tokens":256}')
        self.assertGreater(a, 0)
        self.assertEqual(b.reserved, a)
        b.reserve("openai-chat", b'{"max_tokens":256}')
        b.reserve("openai-responses", b'{"max_output_tokens":256}')

    def test_concurrent_reservations_are_never_above_the_cap(self):
        body = b'{"max_tokens":256}'
        probe = Budget().reserve("anthropic", body)
        budget = Budget(probe * 2)
        successes = []
        def reserve():
            try:
                successes.append(budget.reserve("anthropic", body))
            except RuntimeError:
                pass
        threads = [threading.Thread(target=reserve) for _ in range(20)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        self.assertEqual(len(successes), 2)
        self.assertEqual(budget.reserved, budget.cap)
        # A failed or unknown request cannot release a reservation.
        with self.assertRaises(RuntimeError):
            budget.reserve("anthropic", body)

    def test_unknown_or_excess_usage_halts_subsequent_dispatch(self):
        body = b'{"max_tokens":256}'
        # Unknown usage keeps the reservation but must not stop the run.
        budget = Budget()
        reservation = budget.reserve("anthropic", body)
        self.assertIsNone(budget.settle("anthropic", None, reservation))
        self.assertFalse(budget.blocked)
        self.assertEqual(budget.unknown_usage, 1)
        self.assertGreater(budget.reserve("anthropic", body), 0)
        # Reported usage above the reservation is an accounting surprise: halt.
        budget = Budget()
        reservation = budget.reserve("anthropic", body)
        budget.settle("anthropic", (100_000_000, 2048), reservation)
        self.assertTrue(budget.blocked)
        with self.assertRaises(RuntimeError):
            budget.reserve("anthropic", body)
        budget = Budget()
        reservation = budget.reserve("openai-chat", body)
        self.assertEqual(budget.settle("openai-chat", (1000, 500), reservation), Decimal("0.00045"))
        self.assertFalse(budget.blocked)
        self.assertEqual(budget.list_usage_cost, Decimal("0.00045"))

    def test_reported_usage_not_billed_invoice(self):
        self.assertEqual(list_cost("openai", 1000, 500), Decimal("0.00045"))
        self.assertEqual(list_cost("anthropic", 1000, 500), Decimal("0.0035"))
        with self.assertRaises(ValueError):
            list_cost("anthropic", -1, 500)
        with self.assertRaises(ValueError):
            list_cost("anthropic", True, 500)


class UsageTest(unittest.TestCase):
    def test_unary(self):
        self.assertEqual(read_usage("openai-chat", b'{"usage":{"prompt_tokens":5,"completion_tokens":3}}', False), (5, 3))
        self.assertEqual(read_usage("openai-responses", b'{"usage":{"input_tokens":5,"output_tokens":3}}', False), (5, 3))
        self.assertEqual(read_usage("anthropic", b'{"usage":{"input_tokens":5,"output_tokens":3}}', False), (5, 3))
        self.assertIsNone(read_usage("anthropic", b'{"usage":{"input_tokens":5}}', False))

    def test_streams_and_truncation(self):
        chat = b'data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3}}\n\ndata: [DONE]\n\n'
        responses = b'event: response.completed\ndata: {"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":3}}}\n\n'
        anth = b'event: message_start\ndata: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0}}}\n\nevent: message_delta\ndata: {"type":"message_delta","usage":{"output_tokens":3}}\n\nevent: message_stop\ndata: {"type":"message_stop"}\n\n'
        for dialect, value in (("openai-chat", chat), ("openai-responses", responses), ("anthropic", anth)):
            self.assertEqual(read_usage(dialect, value, True), (5, 3))
        self.assertIsNone(read_usage("openai-chat", chat.split(b'data: [DONE]')[0], True))
        self.assertIsNone(read_usage("anthropic", anth.split(b'event: message_stop')[0], True))
        no_final_usage = anth.split(b'event: message_delta')[0] + b'data: {"type":"message_stop"}\n\n'
        self.assertIsNone(read_usage("anthropic", no_final_usage, True))
        self.assertIsNone(read_usage("openai-responses", b'data: {"type":"response.in_progress"}\n\n', True))
        self.assertIsNone(read_usage("anthropic", b'data: {bad json}\n\n', True))
        self.assertIsNone(read_usage("anthropic", b'data: {"type":"message_start","message":1}\n\n', True))


if __name__ == "__main__":
    unittest.main()
