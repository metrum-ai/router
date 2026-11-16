#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline contract test for the Harbor OpenAI Responses replay mock."""

from __future__ import annotations

import json
import urllib.request
import unittest

import mock_messages


def events_for(server, body: dict) -> list[dict]:
    request = urllib.request.Request(
        f"http://127.0.0.1:{server.server_address[1]}/v1/responses",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=2) as response:
        payload = response.read().decode()
    return [json.loads(line[6:]) for line in payload.splitlines() if line.startswith("data: ")]


class ResponsesReplayMockTest(unittest.TestCase):
    def test_tool_call_and_continuation_complete_without_provider(self):
        mock_messages.Mock.calls = 0
        server = mock_messages.start("127.0.0.1", 0)
        try:
            first_events = events_for(server, {"stream": True, "input": "Create hello.txt."})
            first = next(event["response"] for event in first_events if event.get("type") == "response.completed")
            call = first["output"][0]
            self.assertEqual(call["type"], "function_call")
            self.assertEqual(call["name"], "write")
            self.assertEqual(json.loads(call["arguments"])["path"], "/app/hello.txt")

            second_events = events_for(server, {"stream": True, "input": [
                call, {"type": "function_call_output", "call_id": call["call_id"], "output": "written"},
            ]})
            second = next(event["response"] for event in second_events if event.get("type") == "response.completed")
            self.assertEqual(second["output_text"], "Done.")
            self.assertEqual(mock_messages.Mock.calls, 2)
        finally:
            server.shutdown()
            server.server_close()


if __name__ == "__main__":
    unittest.main()
