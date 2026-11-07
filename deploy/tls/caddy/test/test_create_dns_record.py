#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Test deploy/tls/caddy/create-dns-record.sh against a fake Cloudflare API.

Run with: python3 deploy/tls/caddy/test/test_create_dns_record.py

The fake API records every request, so the test asserts the exact write calls:
the first run creates the record and its ownership marker, a second run writes
nothing, a value change updates in place, and a record this deployment did not
create is never modified or deleted.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

SCRIPT = Path(__file__).resolve().parents[1] / "create-dns-record.sh"


class FakeCloudflare:
    def __init__(self) -> None:
        self.records: dict[str, dict] = {}
        self.writes: list[tuple[str, str, dict]] = []
        self.next_id = 0
        self.lock = threading.Lock()

    def create(self, payload: dict) -> dict:
        self.next_id += 1
        record = dict(payload)
        record["id"] = f"rec-{self.next_id}"
        self.records[record["id"]] = record
        return record

    def find(self, record_type: str, name: str) -> list[dict]:
        return [
            record
            for record in self.records.values()
            if record["type"] == record_type and record["name"] == name
        ]


class Handler(BaseHTTPRequestHandler):
    api: FakeCloudflare
    protocol_version = "HTTP/1.1"

    def log_message(self, *args) -> None:  # keep unittest output clean
        return

    def _send(self, payload: dict, status: int = 200) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _payload(self) -> dict:
        length = int(self.headers.get("Content-Length") or 0)
        if not length:
            return {}
        return json.loads(self.rfile.read(length) or b"{}")

    def do_GET(self) -> None:  # noqa: N802 - http.server API
        parsed = urlparse(self.path)
        query = parse_qs(parsed.query)
        if parsed.path == "/client/v4/zones":
            self._send({"success": True, "result": [{"id": "zone-1", "name": query.get("name", [""])[0]}]})
            return
        if parsed.path == "/client/v4/zones/zone-1/dns_records":
            record_type = query.get("type", [""])[0]
            name = query.get("name", [""])[0]
            with self.api.lock:
                result = self.api.find(record_type, name)
            self._send({"success": True, "result": result})
            return
        self._send({"success": False, "errors": [{"code": 7000, "message": "not found"}]}, status=404)

    def do_POST(self) -> None:  # noqa: N802 - http.server API
        if self.path != "/client/v4/zones/zone-1/dns_records":
            self._send({"success": False, "errors": [{"code": 7000, "message": "not found"}]}, status=404)
            return
        payload = self._payload()
        with self.api.lock:
            self.api.writes.append(("POST", self.path, payload))
            record = self.api.create(payload)
        self._send({"success": True, "result": record})

    def do_PUT(self) -> None:  # noqa: N802 - http.server API
        prefix = "/client/v4/zones/zone-1/dns_records/"
        if not self.path.startswith(prefix):
            self._send({"success": False, "errors": [{"code": 7000, "message": "not found"}]}, status=404)
            return
        record_id = self.path[len(prefix):]
        payload = self._payload()
        with self.api.lock:
            self.api.writes.append(("PUT", self.path, payload))
            existing = self.api.records.get(record_id)
            if existing is None:
                self._send({"success": False, "errors": [{"code": 81044, "message": "missing"}]}, status=404)
                return
            existing.update(payload)
            record = existing
        self._send({"success": True, "result": record})


class CreateDNSRecordTest(unittest.TestCase):
    def setUp(self) -> None:
        self.api = FakeCloudflare()
        handler = type("BoundHandler", (Handler,), {"api": self.api})
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}/client/v4"

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()

    def run_script(self, **overrides: str) -> subprocess.CompletedProcess:
        env = {
            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
            "HOME": os.environ.get("HOME", "/tmp"),
            "CF_API_TOKEN": "test-token",
            "CF_API_BASE": self.base,
            "ZONE": "example.com",
            "NAME": "router.example.com",
            "TYPE": "A",
            "VALUE": "203.0.113.10",
            "TTL": "300",
            "OWNER": "router-prod-1",
        }
        env.update(overrides)
        return subprocess.run(["bash", str(SCRIPT)], env=env, capture_output=True, text=True, check=False)

    def test_idempotent_create_and_conflict(self) -> None:
        first = self.run_script()
        self.assertEqual(first.returncode, 0, first.stderr)
        # Claim the marker, create the record, then bind the marker to the
        # record id (the same order the router's record manager uses).
        self.assertEqual([write[0] for write in self.api.writes], ["POST", "POST", "PUT"])
        created = {write[2]["type"]: write[2] for write in self.api.writes if write[0] == "POST"}
        self.assertEqual(created["A"]["content"], "203.0.113.10")
        self.assertEqual(created["A"]["name"], "router.example.com")
        self.assertEqual(created["TXT"]["name"], "_metrum-router.router.example.com")
        self.assertEqual(
            created["TXT"]["content"],
            "metrum-ai-router-managed=1;owner=router-prod-1;name=router.example.com;type=A",
        )
        markers = self.api.find("TXT", "_metrum-router.router.example.com")
        self.assertEqual(len(markers), 1)
        self.assertEqual(
            markers[0]["content"],
            "metrum-ai-router-managed=1;owner=router-prod-1;name=router.example.com;type=A;id=rec-2",
            "the marker must record the managed record's id",
        )

        # Re-running changes nothing: zero further write calls (ACP-N08c).
        self.api.writes.clear()
        second = self.run_script()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(self.api.writes, [], "an idempotent re-run must not write")

        # A value change updates in place, without creating a second record.
        self.api.writes.clear()
        third = self.run_script(VALUE="203.0.113.11")
        self.assertEqual(third.returncode, 0, third.stderr)
        self.assertEqual([write[0] for write in self.api.writes], ["PUT"])
        self.assertEqual(len(self.api.find("A", "router.example.com")), 1)

        # A record without the marker is somebody else's: never touched.
        self.api.writes.clear()
        self.api.create({"type": "A", "name": "other.example.com", "content": "203.0.113.99", "ttl": 300})
        foreign = self.run_script(NAME="other.example.com")
        self.assertEqual(foreign.returncode, 3, foreign.stderr)
        self.assertEqual(self.api.writes, [], "a foreign record must never be modified")
        self.assertEqual(self.api.find("A", "other.example.com")[0]["content"], "203.0.113.99")

        # A record replaced outside the script (its id no longer matches the
        # marker) is left alone instead of taken over.
        self.api.writes.clear()
        for record_id, record in list(self.api.records.items()):
            if record["type"] == "A" and record["name"] == "router.example.com":
                del self.api.records[record_id]
        replaced = self.api.create({"type": "A", "name": "router.example.com", "content": "203.0.113.11", "ttl": 300})
        self.assertNotEqual(replaced["id"], "rec-2")
        replaced_run = self.run_script(VALUE="203.0.113.12")
        self.assertEqual(replaced_run.returncode, 3, replaced_run.stderr)
        self.assertEqual(self.api.writes, [], "a replaced record must never be modified")

        # A marker for a different owner blocks the change too.
        self.api.writes.clear()
        marker = self.api.find("TXT", "_metrum-router.other.example.com")
        self.assertEqual(marker, [])
        self.api.create(
            {
                "type": "TXT",
                "name": "_metrum-router.other.example.com",
                "content": "metrum-ai-router-managed=1;owner=another-host;name=other.example.com;type=A",
                "ttl": 300,
            }
        )
        foreign_owner = self.run_script(NAME="other.example.com")
        self.assertEqual(foreign_owner.returncode, 3, foreign_owner.stderr)
        self.assertEqual(self.api.writes, [], "another owner's record must never be modified")

    def test_usage_errors(self) -> None:
        missing = subprocess.run(["bash", str(SCRIPT)], env={"PATH": os.environ.get("PATH", "/usr/bin:/bin")},
                                 capture_output=True, text=True, check=False)
        self.assertEqual(missing.returncode, 2)
        bad_type = self.run_script(TYPE="MX")
        self.assertEqual(bad_type.returncode, 2, bad_type.stderr)


if __name__ == "__main__":
    unittest.main(verbosity=2)
