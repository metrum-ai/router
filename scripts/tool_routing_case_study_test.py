#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import unittest

import tool_routing_case_study as study


class SchemaConversionTest(unittest.TestCase):
    def test_bfcl_dialect_maps_to_openai_subset(self):
        schema = {
            "type": "dict",
            "properties": {
                "items": {"type": "array", "items": {"type": "dict", "properties": {"n": {"type": "float"}}}},
                "mode": {"type": "string", "enum": ["a", "b"]},
            },
            "required": ["items"],
            "optional": ["mode"],
        }
        converted = study._normalize_schema(schema)
        self.assertEqual(converted["type"], "object")
        self.assertNotIn("optional", converted)
        items = converted["properties"]["items"]
        self.assertEqual(items["type"], "array")
        self.assertEqual(items["items"]["type"], "object")
        self.assertEqual(items["items"]["properties"]["n"]["type"], "number")

    def test_openai_tools_wrap_function_schema(self):
        tools = study.openai_tools([
            {
                "name": "read_file",
                "description": "Read a file",
                "parameters": {"type": "dict", "properties": {"path": {"type": "string"}}, "required": ["path"]},
            }
        ])
        self.assertEqual(len(tools), 1)
        self.assertEqual(tools[0]["type"], "function")
        self.assertEqual(tools[0]["function"]["name"], "read_file")
        self.assertEqual(tools[0]["function"]["parameters"]["type"], "object")
        self.assertEqual(tools[0]["function"]["parameters"]["required"], ["path"])

    def test_dotted_function_names_are_rewritten(self):
        tools = study.openai_tools([{"name": "geometry.circle_area", "description": ""}])
        self.assertEqual(tools[0]["function"]["name"], "geometry_circle_area")

    def test_openai_tools_tolerate_missing_parameters(self):
        tools = study.openai_tools([{"name": "ping", "description": ""}])
        self.assertEqual(tools[0]["function"]["parameters"]["type"], "object")


class ModelOutputTest(unittest.TestCase):
    def test_tool_calls_are_decoded(self):
        response = {
            "choices": [
                {
                    "message": {
                        "tool_calls": [
                            {"function": {"name": "read_file", "arguments": '{"path":"a.txt"}'}},
                        ]
                    }
                }
            ]
        }
        output, failure = study.model_output(response)
        self.assertEqual(failure, "")
        self.assertEqual(output, [{"read_file": {"path": "a.txt"}}])

    def test_classifications(self):
        cases = [
            ({"__error__": "http_503"}, "http_503"),
            ({"choices": [{"message": {"tool_calls": []}}]}, "no_tool_call"),
            ({"choices": [{"message": {"tool_calls": [{"function": {"name": "f", "arguments": "{"}}]}}]}, "invalid_json"),
            ({"unexpected": True}, "no_choices"),
        ]
        for response, want in cases:
            output, failure = study.model_output(response)
            self.assertEqual(failure, want, response)
            self.assertEqual(output, [])


class CategoryTest(unittest.TestCase):
    def test_categories_are_the_documented_ast_ones(self):
        self.assertEqual(set(study.CATEGORIES), {"simple_python", "multiple", "parallel"})


if __name__ == "__main__":
    unittest.main(verbosity=1)
