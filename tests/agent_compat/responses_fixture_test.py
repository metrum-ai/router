#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Exercise both client tool shapes in the scripted Responses fixture."""
import json
import threading
import unittest
from http.server import ThreadingHTTPServer
from urllib.request import Request, urlopen

from codex_responses_smoke import Mock


class ResponsesFixtureTest(unittest.TestCase):
    def test_first_stream_uses_requested_tool_and_arguments(self):
        for name, arguments in [("exec_command", {"cmd": "printf fixture"}), ("write", {"path": "result.txt", "content": "fixture"})]:
            with self.subTest(name=name):
                Mock.calls.clear()
                Mock.errors.clear()
                Mock.tool_name = name
                Mock.tool_arguments = arguments
                server = ThreadingHTTPServer(("127.0.0.1", 0), Mock)
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                try:
                    body = json.dumps({"stream": True, "tools": [{"type": "function", "name": name}]}).encode()
                    request = Request(f"http://127.0.0.1:{server.server_port}/v1/responses", data=body, headers={"Content-Type": "application/json"})
                    with urlopen(request, timeout=5) as response:
                        events = [json.loads(line[6:]) for line in response.read().decode().splitlines() if line.startswith("data: ")]
                    call = next(event["item"] for event in events if event.get("type") == "response.output_item.done")
                    self.assertEqual(call["name"], name)
                    self.assertEqual(json.loads(call["arguments"]), arguments)
                    self.assertEqual(Mock.errors, [])
                finally:
                    server.shutdown()
                    server.server_close()
                    thread.join(timeout=5)
        Mock.tool_name = "exec_command"
        Mock.tool_arguments = {"cmd": "printf 'tool-result-42\\n' > result.txt"}


if __name__ == "__main__":
    unittest.main()
