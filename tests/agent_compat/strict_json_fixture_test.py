#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Negative controls for the strict JSON fixture's upstream assertions."""
import copy
import unittest

from run_chat_smoke import has_strict_json_schema


class StrictJSONFixtureTest(unittest.TestCase):
    def test_schema_guard(self):
        body = {"response_format": {"type": "json_schema", "json_schema": {
            "strict": True, "schema": {"type": "object", "additionalProperties": False,
                "required": ["status", "count"], "properties": {
                    "status": {"type": "string"}, "count": {"type": "integer"}}}}}}
        self.assertTrue(has_strict_json_schema(body))
        cases = [{}, {"response_format": []}, {"response_format": {"json_schema": "broken"}}]
        for mutate in (
            lambda d: d["response_format"]["json_schema"].update(strict=False),
            lambda d: d["response_format"]["json_schema"]["schema"].update(additionalProperties=True),
            lambda d: d["response_format"]["json_schema"]["schema"]["properties"].update(count={"type": "string"}),
            lambda d: d["response_format"]["json_schema"]["schema"]["properties"].update(extra={"type": "string"}),
            lambda d: d["response_format"]["json_schema"]["schema"].update(required=["status"]),
        ):
            altered = copy.deepcopy(body)
            mutate(altered)
            cases.append(altered)
        for altered in cases:
            with self.subTest(body=altered):
                self.assertFalse(has_strict_json_schema(altered))


if __name__ == "__main__":
    unittest.main()
