#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Coverage and fail-closed contracts for router test sharding."""
import unittest
from unittest import mock
import ci_router_shards as shard


class ShardsTest(unittest.TestCase):
    def test_every_name_once_and_balanced(self):
        names = ['TestHeavy', 'TestMedium', 'TestLight', 'ExampleDemo', 'FuzzInput', 'TestNew']
        durations = {'TestHeavy': 10, 'TestMedium': 5, 'TestLight': 4, 'ExampleDemo': 2, 'FuzzInput': 2}
        parts = shard.partition(names, durations, 3)
        self.assertCountEqual([n for p in parts for n in p], names)
        self.assertEqual(parts, shard.partition(list(reversed(names)), durations, 3))
        self.assertEqual(parts[0], ['TestHeavy'])

    def test_zero_duration_tests_are_distributed(self):
        names = [f'TestZero{i}' for i in range(40)]
        parts = shard.partition(names, {name: 0 for name in names}, 4)
        self.assertEqual([len(part) for part in parts], [10, 10, 10, 10])
        self.assertCountEqual([name for part in parts for name in part], names)

    def test_rejects_invalid_discovery_and_weights(self):
        for names, weights, count in [([], {}, 2), (['TestX', 'TestX'], {}, 2), (['TestX|.*'], {}, 2), (['TestX'], {'TestX': -1}, 2), (['TestX'], {'TestX': float('nan')}, 2), (['TestX'], {}, 0)]:
            with self.subTest(names=names, weights=weights, count=count), self.assertRaises(ValueError):
                shard.partition(names, weights, count)

    def test_empty_shard_does_not_run_all_tests(self):
        with mock.patch.object(shard, 'TIMINGS') as timings, mock.patch.object(shard.subprocess, 'run') as listing, mock.patch.object(shard.subprocess, 'Popen') as popen:
            timings.read_text.return_value = '{"seconds":{}}'
            listing.return_value.stdout = 'TestOnly\nok  package\n'
            shard.main(['--shard-count', '2', '--shard-index', '1'])
            popen.assert_not_called()

    def test_shard_failure_propagates(self):
        with mock.patch.object(shard, 'TIMINGS') as timings, mock.patch.object(shard.subprocess, 'run') as listing, mock.patch.object(shard.subprocess, 'Popen') as popen:
            timings.read_text.return_value = '{"seconds":{}}'
            listing.return_value.stdout = 'TestOnly\n'
            popen.return_value.wait.return_value = 1
            with self.assertRaises(SystemExit):
                shard.main(['--shard-count', '1', '--shard-index', '0'])


if __name__ == '__main__':
    unittest.main()
