# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Synthetic router startup regressions; no model/provider requests."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import MagicMock, patch
from urllib.error import URLError

spec = importlib.util.spec_from_file_location('ft_router_e2e', Path(__file__).parent/'eval/router_e2e.py')
router = importlib.util.module_from_spec(spec)
spec.loader.exec_module(router)


class BudgetTests(unittest.TestCase):
    def test_floor_is_never_lower_than_the_harness_budget(self):
        import os
        self.assertEqual(router.ready_budget(), 60.0)
        self.assertEqual(router.ready_budget(timeout=7), 7)
        previous = os.environ.get(router.readiness.READY_TIMEOUT_ENV)
        os.environ[router.readiness.READY_TIMEOUT_ENV] = '5'
        try:
            self.assertEqual(router.ready_budget(), 60.0)
        finally:
            if previous is None:
                os.environ.pop(router.readiness.READY_TIMEOUT_ENV, None)
            else:
                os.environ[router.readiness.READY_TIMEOUT_ENV] = previous

    def test_a_larger_shared_budget_is_used(self):
        import os
        previous = os.environ.get(router.readiness.READY_TIMEOUT_ENV)
        os.environ[router.readiness.READY_TIMEOUT_ENV] = '90'
        try:
            self.assertEqual(router.ready_budget(), 90.0)
        finally:
            if previous is None:
                os.environ.pop(router.readiness.READY_TIMEOUT_ENV, None)
            else:
                os.environ[router.readiness.READY_TIMEOUT_ENV] = previous


class ReadinessTests(unittest.TestCase):
    def test_slow_start_beyond_old_ten_second_window(self):
        process, opener = MagicMock(), MagicMock()
        process.poll.return_value = None
        response = MagicMock(); response.__enter__.return_value.status = 200
        opener.open.side_effect = [URLError('not listening')]*12 + [response]
        with patch.object(router.time, 'monotonic', side_effect=range(100)), patch.object(router.time, 'sleep'):
            router.wait_ready(process, 'http://127.0.0.1:18530/readyz', opener=opener)
        self.assertEqual(opener.open.call_count, 13)

    def test_deadline_fails_closed_instead_of_falling_through(self):
        process, opener = MagicMock(), MagicMock()
        process.poll.return_value = None
        opener.open.side_effect = URLError('not listening')
        with patch.object(router.time, 'monotonic', side_effect=range(100)), patch.object(router.time, 'sleep'):
            with self.assertRaisesRegex(RuntimeError, 'no test traffic sent'):
                router.wait_ready(process, 'http://127.0.0.1:18530/readyz', timeout=3, opener=opener)
        self.assertEqual(opener.open.call_count, 2)

    def test_child_exit_fails_immediately_without_readiness_request(self):
        process, opener = MagicMock(), MagicMock()
        process.poll.return_value = 7
        with self.assertRaisesRegex(RuntimeError, 'exit=7'):
            router.wait_ready(process, 'http://127.0.0.1:18530/readyz', opener=opener)
        opener.open.assert_not_called()
