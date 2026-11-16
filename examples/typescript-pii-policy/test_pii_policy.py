# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest
from http.server import ThreadingHTTPServer
from threading import Thread
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from pii_policy import NoSensitiveTarget, PolicyHandler, route


def target(tier: str) -> dict:
    return {"tier": tier, "keyConfigured": True, "weight": 1}


class PIIPolicyTest(unittest.TestCase):
    def test_pii_routes_only_to_sensitive_targets_and_fallbacks(self):
        result = route({"text": "Contact Jane Doe at jane@example.com", "targets": [
            target("normal"), target("private"), target("sensitive"),
        ]})
        self.assertEqual(result["targetIndex"], 1)
        self.assertEqual(result["fallbackIndexes"], [2])
        self.assertEqual(result["classLabel"], "pii-detected:sensitive-route")
        self.assertNotIn("jane", str(result).lower())

    def test_pii_fails_closed_without_sensitive_target(self):
        with self.assertRaises(NoSensitiveTarget):
            route({"text": "jane@example.com", "targets": [target("normal")]})

    def test_non_pii_uses_normal_target(self):
        result = route({"text": "hello there", "targets": [target("private"), target("normal")]})
        self.assertEqual(result["targetIndex"], 1)
        self.assertEqual(result["classLabel"], "pii-detected:none")

    def test_http_route_fails_closed_without_exposing_request_text(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), PolicyHandler)
        worker = Thread(target=server.serve_forever, daemon=True)
        worker.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        request = Request(
            f"http://127.0.0.1:{server.server_port}/route",
            data=b'{"text":"jane@example.com","targets":[{"tier":"normal","keyConfigured":true,"weight":1}]}',
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with self.assertRaises(HTTPError) as raised:
            urlopen(request, timeout=2)
        self.assertEqual(raised.exception.code, 422)
        body = raised.exception.read()
        self.assertIn(b"pii-detected:no-sensitive-target", body)
        self.assertNotIn(b"jane@example.com", body)


if __name__ == "__main__":
    unittest.main()
