#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import contextlib
import io
import unittest

from compat_mock import report_differences
from compat_results import compare


class ResultsTest(unittest.TestCase):
    suites = ["models", "messages"]
    expected = {"messages": {"reason": "known bug", "issue": "https://github.com/metrum-ai/router/issues/535"}}
    mixed = '==> running suite "models"\n    PASS (1ms)\n\n1 of 2 suites passed\n  [PASS] models\n  [FAIL] messages: error\n'
    passed = '\n2 of 2 suites passed\n  [PASS] models\n  [PASS] messages\n'

    def test_expected_failure(self):
        self.assertEqual(compare(self.mixed, 1, self.suites, self.expected), [])

    def test_multiline_sdk_error(self):
        output = self.mixed.replace('messages: error\n', 'messages: error\n{\n  "message": "bad request"\n}\n')
        self.assertEqual(compare(output, 1, self.suites, self.expected), [])

    def test_new_failure_and_stale_expected(self):
        self.assertEqual(compare(self.mixed, 1, self.suites, {}), ["new failure: messages"])
        self.assertEqual(compare(self.passed, 0, self.suites, self.expected), ["expected failure now passes: messages"])

    def test_difference_verdicts_are_visible(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            report_differences('openai', compare(self.mixed, 1, self.suites, {}))
            report_differences('anthropic', compare(self.passed, 0, self.suites, self.expected))
            report_differences('openai', compare(self.mixed, 1, self.suites, self.expected))
        self.assertIn('openai compatibility differences:\n  new failure: messages', out.getvalue())
        self.assertIn('anthropic compatibility differences:\n  expected failure now passes: messages', out.getvalue())
        self.assertIn('openai compatibility: all suites match expected failures', out.getvalue())

    def test_bad_reports_fail_closed(self):
        for output, code in [(self.mixed, 2), (self.mixed, 0), (self.passed, 1),
                             (self.mixed.replace('  [FAIL] messages: error\n', ''), 1),
                             (self.mixed + '  [FAIL] messages: again\n', 1),
                             (self.mixed.replace('1 of 2', '2 of 2'), 1),
                             ('', 1), (self.mixed + '\n1 of 2 suites passed\n', 1)]:
            with self.subTest(output=output, code=code), self.assertRaises(ValueError):
                compare(output, code, self.suites, self.expected)

    def test_invalid_expectations_and_selection(self):
        bad = [{"messages": {"reason": "", "issue": "https://github.com/o/r/issues/1"}},
               {"messages": {"reason": "bug", "issue": "https://evil.example/o/r/issues/1"}},
               {"missing": {"reason": "bug", "issue": "https://github.com/o/r/issues/1"}},
               {"messages": {"reason": "bug"}}]
        for expected in bad:
            with self.subTest(expected=expected), self.assertRaises(ValueError):
                compare(self.mixed, 1, self.suites, expected)
        with self.assertRaises(ValueError):
            compare(self.mixed, 1, ["models", "models"], {})


if __name__ == "__main__":
    unittest.main()
