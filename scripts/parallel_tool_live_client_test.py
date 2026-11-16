#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline contract tests for the real SDK tool-loop cells."""

from __future__ import annotations

import json
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest.mock import patch

from parallel_live_budget import Budget
from parallel_tool_live import Gate, client_cells_for_set, router_config, sanitize_gate_report


class Response:
    def __init__(self, data):
        self.data = data
        for key, value in data.items():
            if key == "choices":
                value = [types.SimpleNamespace(message=types.SimpleNamespace(**(choice["message"] | {"tool_calls": choice["message"].get("tool_calls")}))) for choice in value]
            setattr(self, key, value)

    def model_dump(self, mode="json"):
        def plain(value):
            if isinstance(value, dict):
                return {key: plain(item) for key, item in value.items()}
            if isinstance(value, list):
                return [plain(item) for item in value]
            if isinstance(value, types.SimpleNamespace):
                if hasattr(value, "model_dump"):
                    return plain(value.model_dump(mode="json"))
                return plain(vars(value))
            return value
        return plain(self.data)


class SDKClient:
    instances = []

    def __init__(self, **_kwargs):
        self.calls = 0
        self.requests = []
        type(self).instances.append(self)
        self.chat = types.SimpleNamespace(completions=types.SimpleNamespace(create=self.chat_create))
        self.responses = types.SimpleNamespace(create=self.responses_create)
        self.messages = types.SimpleNamespace(create=self.messages_create)

    @staticmethod
    def usage():
        return {"prompt_tokens": 10, "completion_tokens": 5, "input_tokens": 10, "output_tokens": 5}

    def chat_create(self, **kwargs):
        self.calls += 1
        self.requests.append(kwargs)
        if self.calls == 1:
            call = types.SimpleNamespace(id="call-1", function=types.SimpleNamespace(name="read_file", arguments='{"path":"notes/a.txt"}'))
            msg = {"tool_calls": [call]}
            return Response({"choices": [{"message": msg}], "usage": self.usage()})
        return Response({"choices": [{"message": {"content": "LIVE-EVAL-MARKER-A"}}], "usage": self.usage()})

    def responses_create(self, **kwargs):
        self.calls += 1
        self.requests.append(kwargs)
        if self.calls == 1:
            output = [types.SimpleNamespace(type="function_call", name="read_file", call_id="call-1", arguments='{"path":"notes/a.txt"}', model_dump=lambda mode="json": {"type": "function_call", "name": "read_file", "call_id": "call-1", "arguments": '{"path":"notes/a.txt"}'})]
            return Response({"output": output, "output_text": "", "usage": {"input_tokens": 10, "output_tokens": 5}})
        return Response({"output": [], "output_text": "LIVE-EVAL-MARKER-A", "usage": {"input_tokens": 10, "output_tokens": 5}})

    def messages_create(self, **kwargs):
        self.calls += 1
        self.requests.append(kwargs)
        if self.calls == 1:
            content = [types.SimpleNamespace(type="tool_use", id="call-1", name="read_file", input={"path": "notes/a.txt"}, model_dump=lambda mode="json", exclude_none=False: {"type": "tool_use", "id": "call-1", "name": "read_file", "input": {"path": "notes/a.txt"}})]
            return Response({"content": content, "usage": {"input_tokens": 10, "output_tokens": 5}})
        return Response({"content": [types.SimpleNamespace(type="text", text="LIVE-EVAL-MARKER-A")], "usage": {"input_tokens": 10, "output_tokens": 5}})


class ClientCellTest(unittest.TestCase):
    def test_gate_artifact_keeps_cells_and_drops_response_bodies(self):
        report = sanitize_gate_report({
            "cell_set": "release",
            "cells": [{"cell": "openai-chat-sdk", "group": "openai-chat-sdk", "problems": ["timeout"], "cost_usd": 0.0, "provider_response": "private body"}],
            "router_usage_rows": [{"total_cost_usd": 0.001, "request_id": "private-id", "prompt": "private prompt"}],
            "reserved_usd": 0.1,
            "list_rate_usage_usd": 0.0,
            "unknown_usage_turns": 0,
            "halted": False,
            "cap_usd": 1.0,
        })
        self.assertEqual(report["cells"][0]["problems"], ["timeout"])
        self.assertEqual(report["router_usage_rows"], [{"total_cost_usd": 0.001}])
        self.assertNotIn("private", json.dumps(report))

    def test_sdk_cells_are_release_only_and_provider_pinned(self):
        self.assertEqual(client_cells_for_set("nightly"), ())
        cells = client_cells_for_set("release")
        self.assertEqual([cell[0] for cell in cells], ["openai-chat-sdk", "openai-responses-sdk", "anthropic-messages-sdk"])
        with tempfile.TemporaryDirectory() as directory:
            yaml_stub = types.SimpleNamespace(safe_dump=json.dumps, safe_load=json.loads)
            with patch.dict(sys.modules, {"yaml": yaml_stub}):
                config_path, _ = router_config(Path(directory), "openai-key", "anthropic-key")
            config = json.loads(config_path.read_text(encoding="utf-8"))
        self.assertEqual(config["providers"]["openai"]["dialect"], "openai-chat")
        self.assertEqual(config["providers"]["openai-responses"]["dialect"], "openai-responses")
        self.assertEqual({target["provider"] for target in config["models"]["openai-chat-sdk"]["targets"]}, {"openai"})
        self.assertEqual({target["provider"] for target in config["models"]["openai-responses-sdk"]["targets"]}, {"openai-responses"})
        self.assertEqual({target["provider"] for target in config["models"]["anthropic-messages-sdk"]["targets"]}, {"anthropic"})

    def test_chat_responses_and_messages_sdk_tool_loops(self):
        fake_openai = types.SimpleNamespace(OpenAI=SDKClient)
        fake_anthropic = types.SimpleNamespace(Anthropic=SDKClient)
        with patch.dict(sys.modules, {"openai": fake_openai, "anthropic": fake_anthropic}):
            for name, surface, group in (
                ("openai-chat-sdk", "openai-chat", "openai-chat-sdk"),
                ("openai-responses-sdk", "openai-responses", "openai-responses-sdk"),
                ("anthropic-messages-sdk", "anthropic-messages", "anthropic-messages-sdk"),
            ):
                gate = Gate(Budget(), Path("unused"))
                gate.client_cell(name, surface, "http://127.0.0.1:1", group=group)
                self.assertEqual(gate.cells[0]["problems"], [], name)
                self.assertEqual(gate.cells[0]["group"], group)
                expected_cost = 0.00007 if surface == "anthropic-messages" else 0.000009
                self.assertAlmostEqual(gate.cells[0]["cost_usd"], expected_cost, places=10, msg=name)
                first_request = SDKClient.instances[-1].requests[0]
                self.assertEqual(first_request.get("temperature", first_request.get("extra_body", {}).get("temperature")), 0)
                self.assertEqual(first_request["tool_choice"]["name"] if surface != "openai-chat" else first_request["tool_choice"]["function"]["name"], "read_file")

    def test_missing_tool_result_is_a_sanitized_failure(self):
        class BadClient(SDKClient):
            def chat_create(self, **_kwargs):
                return Response({"choices": [{"message": {"content": "no tool"}}], "usage": self.usage()})

        with patch.dict(sys.modules, {"openai": types.SimpleNamespace(OpenAI=BadClient)}):
            gate = Gate(Budget(), Path("unused"))
            gate.client_cell("openai-chat-sdk", "openai-chat", "http://127.0.0.1:1")
        self.assertEqual(gate.cells[0]["problems"], ["tool-call-mismatch"])
        self.assertNotIn("LIVE-EVAL", json.dumps(gate.cells[0]))

    def test_timeout_is_reported_without_exception_text(self):
        class SlowClient(SDKClient):
            def chat_create(self, **_kwargs):
                raise TimeoutError("secret prompt and token must not escape")

        with patch.dict(sys.modules, {"openai": types.SimpleNamespace(OpenAI=SlowClient)}):
            gate = Gate(Budget(), Path("unused"))
            gate.client_cell("openai-chat-sdk", "openai-chat", "http://127.0.0.1:1")
        output = json.dumps(gate.cells[0])
        self.assertIn("request-failed", gate.cells[0]["problems"])
        self.assertNotIn("secret", output)
        self.assertNotIn("prompt", output)
        self.assertNotIn("token", output)


if __name__ == "__main__":
    unittest.main()
