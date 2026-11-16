# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest
from http.server import ThreadingHTTPServer
from threading import Thread
from urllib.request import Request, urlopen

from request_shape_policy import PolicyHandler, route


def target(tier: str, *, configured: bool = True, weight: int = 1) -> dict:
    return {"tier": tier, "keyConfigured": configured, "weight": weight}


class RequestShapePolicyTest(unittest.TestCase):
    def test_chat_selects_cheap_and_returns_other_eligible_targets_as_fallbacks(self):
        result = route({"context": {"textChars": 10}, "targets": [
            target("heavy"), target("cheap"), target("tool"),
            target("cheap", configured=False), target("cheap", weight=0),
        ]})
        self.assertEqual(result["targetIndex"], 1)
        self.assertEqual(result["fallbackIndexes"], [0, 2])
        self.assertEqual(result["classLabel"], "request-shape:chat")

    def test_request_shapes_choose_expected_tier(self):
        targets = [target(tier) for tier in ("vision", "reasoning", "tool", "structured", "heavy", "cheap")]
        cases = [
            ({"imageCount": 1}, "image", 0),
            ({"reasoning": {"requested": True}}, "reasoning", 1),
            ({"toolCount": 1}, "tools", 2),
            ({"hasStructuredOutput": True}, "structured", 3),
            ({"textChars": 8001}, "long-context", 4),
        ]
        for context, shape, expected in cases:
            with self.subTest(shape=shape):
                result = route({"context": context, "targets": targets})
                self.assertEqual(result["targetIndex"], expected)
                self.assertEqual(result["classLabel"], f"request-shape:{shape}")

    def test_http_route_endpoint_returns_policy_decision(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), PolicyHandler)
        worker = Thread(target=server.serve_forever, daemon=True)
        worker.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        request = Request(
            f"http://127.0.0.1:{server.server_port}/route",
            data=b'{"context":{"imageCount":1},"targets":[{"tier":"vision","keyConfigured":true,"weight":1}]}',
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urlopen(request, timeout=2) as response:
            self.assertEqual(response.status, 200)
            self.assertEqual(response.read(), b'{"targetIndex": 0, "fallbackIndexes": [], "classLabel": "request-shape:image"}')


if __name__ == "__main__":
    unittest.main()
