# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest
import time
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

    def test_case_insensitive_address_and_birthdate_patterns(self):
        for text in (
            "ship to 12 main street", "Ship to 12 MAIN STREET", "dob: march 3, 2001",
            "Contact Jane Doe", "call 212-555-1212", "SSN 123-45-6789",
            "card 4111 1111 1111 1111", "MRN: ABCD-1234", "mrn: abc12345",
            "account number 123456789", "passport number X12345678", "passport number xk12345",
        ):
            with self.subTest(text=text):
                result = route({"text": text, "targets": [target("normal"), target("sensitive")]})
                self.assertEqual(result["targetIndex"], 1)

    def test_non_private_tier_is_not_sensitive(self):
        with self.assertRaises(NoSensitiveTarget):
            route({"text": "jane@example.com", "targets": [target("non-private")]})

    def test_long_address_input_is_bounded(self):
        text = "12 " + ("main " * 50000) + "not-an-address jane@example.com"
        started = time.monotonic()
        result = route({"text": text, "targets": [target("normal"), target("sensitive")]})
        self.assertLess(time.monotonic() - started, 1.0)
        self.assertEqual(result["targetIndex"], 1)

    def test_long_prompt_scans_prefix_and_suffix_without_rejecting(self):
        prefix = "Ship to 12 MAIN STREET. " + ("ordinary words " * 10000)
        suffix = ("ordinary words " * 10000) + " passport number xk12345"
        for index, text in enumerate((prefix, suffix)):
            with self.subTest(position="prefix" if index == 0 else "suffix"):
                result = route({"text": text, "targets": [target("normal"), target("sensitive")]})
                self.assertEqual(result["targetIndex"], 1)

    def test_long_address_like_input_scans_in_bounded_time(self):
        text = "12 " + ("main " * 1200) + "not-an-address"
        started = time.monotonic()
        result = route({"text": text, "targets": [target("normal"), target("sensitive")]})
        self.assertLess(time.monotonic() - started, 1.0)
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

    def test_http_rejects_non_object_json(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), PolicyHandler)
        worker = Thread(target=server.serve_forever, daemon=True)
        worker.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        request = Request(f"http://127.0.0.1:{server.server_port}/route", data=b"[]", method="POST")
        with self.assertRaises(HTTPError) as raised:
            urlopen(request, timeout=2)
        self.assertEqual(raised.exception.code, 400)


if __name__ == "__main__":
    unittest.main()
