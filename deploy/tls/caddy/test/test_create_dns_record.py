#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Test deploy/tls/caddy/create-dns-record.sh against fake provider APIs.

Run with: python3 deploy/tls/caddy/test/test_create_dns_record.py

Every test runs for both providers (DigitalOcean v2 and Cloudflare v4). The fake
APIs record every request, so the checks assert the exact writes: the first run
claims the ownership marker, creates the record and binds the marker to the
record id; a re-run writes nothing; a value change updates in place; a foreign
record, another owner's marker and a replaced record all exit 3 without writing.
A fake `curl` on PATH records its argument vector, which proves the token is
passed through stdin and never appears in the process arguments.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

SCRIPT = Path(__file__).resolve().parents[1] / "create-dns-record.sh"
ZONE = "example.com"
NAME = "router.example.com"


class FakeProvider:
    """State for one fake provider API."""

    def __init__(self, kind: str) -> None:
        self.kind = kind  # "digitalocean" or "cloudflare"
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

    def relative(self, name: str) -> str:
        if name == ZONE:
            return "@"
        return name.removesuffix("." + ZONE)


class Handler(BaseHTTPRequestHandler):
    api: FakeProvider
    protocol_version = "HTTP/1.1"

    def log_message(self, *args) -> None:
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

    def _cloudflare(self, method: str) -> tuple[bool, str]:
        parsed = urlparse(self.path)
        query = parse_qs(parsed.query)
        if parsed.path == "/client/v4/zones":
            self._send({"success": True, "result": [{"id": "zone-1", "name": query.get("name", [""])[0]}]})
            return True, ""
        if parsed.path == "/client/v4/zones/zone-1/dns_records":
            if method == "GET":
                with self.api.lock:
                    result = self.api.find(query.get("type", [""])[0], query.get("name", [""])[0])
                self._send({"success": True, "result": result})
                return True, ""
            payload = self._payload()
            with self.api.lock:
                self.api.writes.append(("POST", self.path, payload))
                record = self.api.create(payload)
            self._send({"success": True, "result": record})
            return True, ""
        prefix = "/client/v4/zones/zone-1/dns_records/"
        if method == "PUT" and parsed.path.startswith(prefix):
            record_id = parsed.path[len(prefix):]
            payload = self._payload()
            with self.api.lock:
                self.api.writes.append(("PUT", self.path, payload))
                existing = self.api.records.get(record_id)
                if existing is None:
                    self._send({"success": False, "errors": [{"code": 81044, "message": "missing"}]}, status=404)
                    return True, ""
                existing.update(payload)
                record = existing
            self._send({"success": True, "result": record})
            return True, ""
        return False, "cloudflare"

    def _digitalocean(self, method: str) -> tuple[bool, str]:
        parsed = urlparse(self.path)
        if parsed.path == f"/v2/domains/{ZONE}":
            self._send({"domain": {"name": ZONE}})
            return True, ""
        list_path = f"/v2/domains/{ZONE}/records"
        if parsed.path == list_path and method == "GET":
            with self.api.lock:
                result = [record for record in self.api.records.values()]
            self._send({"domain_records": result, "links": {"pages": {}}})
            return True, ""
        if parsed.path == list_path and method == "POST":
            payload = self._payload()
            with self.api.lock:
                self.api.writes.append(("POST", self.path, payload))
                record = self.api.create(payload)
            self._send({"domain_record": record})
            return True, ""
        prefix = f"/v2/domains/{ZONE}/records/"
        if method == "PUT" and parsed.path.startswith(prefix):
            record_id = parsed.path[len(prefix):]
            payload = self._payload()
            with self.api.lock:
                self.api.writes.append(("PUT", self.path, payload))
                existing = self.api.records.get(record_id)
                if existing is None:
                    self._send({"id": "not_found", "message": "The resource you requested could not be found."}, status=404)
                    return True, ""
                existing.update(payload)
                record = existing
            self._send({"domain_record": record})
            return True, ""
        return False, "digitalocean"

    def _handle(self, method: str) -> None:
        if self.api.kind == "cloudflare":
            handled, _ = self._cloudflare(method)
        else:
            handled, _ = self._digitalocean(method)
        if not handled:
            self._send({"message": "not found"}, status=404)

    def do_GET(self) -> None:  # noqa: N802 - http.server API
        self._handle("GET")

    def do_POST(self) -> None:  # noqa: N802 - http.server API
        self._handle("POST")

    def do_PUT(self) -> None:  # noqa: N802 - http.server API
        self._handle("PUT")


class CreateDNSRecordTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        # A fake curl that records its argument vector and then runs the real
        # one. The script must keep the token out of those arguments.
        self.curl_log = Path(self.tempdir.name) / "curl-argv.log"
        shim_dir = Path(self.tempdir.name) / "bin"
        shim_dir.mkdir()
        real_curl = shutil.which("curl")
        self.assertIsNotNone(real_curl, "curl is required for these tests")
        shim = (
            "#!/bin/sh\n"
            "printf '%s\\n' \"$@\" >> \"$CURL_ARGV_LOG\"\n"
            f'exec "{real_curl}" \"$@\"\n'
        )
        (shim_dir / "curl").write_text(shim)
        (shim_dir / "curl").chmod(0o755)
        self.shim_dir = shim_dir

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def start_api(self, kind: str) -> FakeProvider:
        api = FakeProvider(kind)
        handler = type("BoundHandler", (Handler,), {"api": api})
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}"
        self.addCleanup(self.server.shutdown)
        self.addCleanup(self.server.server_close)
        return api

    def run_script(self, kind: str, **overrides: str) -> subprocess.CompletedProcess:
        base = getattr(self, "base", "http://127.0.0.1:1")
        provider = {
            "digitalocean": {
                "PROVIDER": "digitalocean",
                "DO_AUTH_TOKEN": "do-token-value",
                "DO_API_BASE": f"{base}/v2",
            },
            "cloudflare": {
                "PROVIDER": "cloudflare",
                "CF_API_TOKEN": "cf-token-value",
                "CF_API_BASE": f"{base}/client/v4",
            },
        }[kind]
        env = {
            "PATH": f"{self.shim_dir}:{os.environ.get('PATH', '/usr/bin:/bin')}",
            "HOME": os.environ.get("HOME", "/tmp"),
            "CURL_ARGV_LOG": str(self.curl_log),
            "ZONE": ZONE,
            "NAME": NAME,
            "TYPE": "A",
            "VALUE": "203.0.113.10",
            "TTL": "300",
            "OWNER": "router-prod-1",
        }
        env.update(provider)
        env.update(overrides)
        return subprocess.run(["bash", str(SCRIPT)], env=env, capture_output=True, text=True, check=False)

    def argv_log(self) -> str:
        return self.curl_log.read_text() if self.curl_log.exists() else ""

    def test_idempotent_create_and_conflict(self) -> None:
        for kind in ("digitalocean", "cloudflare"):
            with self.subTest(provider=kind):
                self.run_idempotent_scenario(kind)

    def run_idempotent_scenario(self, kind: str) -> None:
        api = self.start_api(kind)
        if self.curl_log.exists():
            self.curl_log.unlink()

        first = self.run_script(kind)
        self.assertEqual(first.returncode, 0, first.stderr)
        # Claim the marker, create the record, then bind the marker to the
        # record id (the same order the router's record manager uses).
        self.assertEqual([write[0] for write in api.writes], ["POST", "POST", "PUT"], api.writes)
        created = {write[2]["type"]: write[2] for write in api.writes if write[0] == "POST"}
        created_records = {record["type"]: record for record in api.records.values() if record["type"] in ("A", "TXT")}
        self.assertEqual(created["TXT"]["name"], self.expected_marker_name(kind))
        self.assertEqual(created["TXT"][self.value_field(kind)], "metrum-ai-router-managed=1;owner=router-prod-1;name=router.example.com;type=A")
        self.assertEqual(created["A"][self.value_field(kind)], "203.0.113.10")
        self.assertEqual(created["A"]["name"], self.expected_record_name(kind))
        markers = [record for record in api.records.values() if record["type"] == "TXT"]
        self.assertEqual(len(markers), 1)
        self.assertTrue(
            markers[0][self.value_field(kind)].endswith(";id=" + created_records["A"]["id"]),
            "the marker must record the managed record's id",
        )

        # The token travels through stdin: never in curl's arguments.
        argv = self.argv_log()
        self.assertNotIn(self.token(kind), argv)
        self.assertIn("@-", argv.split(), "the Authorization header must be read from stdin")

        # Re-running changes nothing: zero further write calls.
        api.writes.clear()
        second = self.run_script(kind)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(api.writes, [], "an idempotent re-run must not write")

        # A value change updates in place, without creating a second record.
        api.writes.clear()
        third = self.run_script(kind, VALUE="203.0.113.11")
        self.assertEqual(third.returncode, 0, third.stderr)
        self.assertEqual([write[0] for write in api.writes], ["PUT"])
        self.assertEqual(len([r for r in api.records.values() if r["type"] == "A"]), 1)

        # A record without the marker is somebody else's: never touched.
        api.writes.clear()
        api.create({"type": "A", "name": self.other_name(kind), "content": "203.0.113.99", "data": "203.0.113.99", "ttl": 300})
        foreign = self.run_script(kind, NAME=f"other.{ZONE}")
        self.assertEqual(foreign.returncode, 3, foreign.stderr)
        self.assertEqual(api.writes, [], "a foreign record must never be modified")

        # A marker for a different owner blocks the change too.
        api.writes.clear()
        api.create(
            {
                "type": "TXT",
                "name": self.marker_for(kind, f"other.{ZONE}"),
                "content": "metrum-ai-router-managed=1;owner=another-host;name=other.example.com;type=A",
                "data": "metrum-ai-router-managed=1;owner=another-host;name=other.example.com;type=A",
                "ttl": 300,
            }
        )
        foreign_owner = self.run_script(kind, NAME=f"other.{ZONE}")
        self.assertEqual(foreign_owner.returncode, 3, foreign_owner.stderr)
        self.assertEqual(api.writes, [], "another owner's record must never be modified")

        # A record replaced by hand (new id, marker still points at the old one)
        # is left alone.
        api.writes.clear()
        for record_id, record in list(api.records.items()):
            if record["type"] == "A" and record["name"] == self.expected_record_name(kind):
                del api.records[record_id]
        api.create({"type": "A", "name": self.expected_record_name(kind), "content": "203.0.113.50", "data": "203.0.113.50", "ttl": 300})
        replaced = self.run_script(kind)
        self.assertEqual(replaced.returncode, 3, replaced.stderr)
        self.assertEqual(api.writes, [], "a replaced record must never be modified")

    def test_usage_errors(self) -> None:
        minimal = {"PATH": os.environ.get("PATH", "/usr/bin:/bin")}
        missing = subprocess.run(["bash", str(SCRIPT)], env=minimal, capture_output=True, text=True, check=False)
        self.assertEqual(missing.returncode, 2)
        bad_type = self.run_script("digitalocean", TYPE="MX")
        self.assertEqual(bad_type.returncode, 2, bad_type.stderr)
        bad_provider = self.run_script("cloudflare", PROVIDER="route53")
        self.assertEqual(bad_provider.returncode, 2, bad_provider.stderr)

    # Provider-specific expectations.

    def token(self, kind: str) -> str:
        return {"digitalocean": "do-token-value", "cloudflare": "cf-token-value"}[kind]

    def value_field(self, kind: str) -> str:
        return {"digitalocean": "data", "cloudflare": "content"}[kind]

    def expected_record_name(self, kind: str) -> str:
        # The DigitalOcean v2 API takes names relative to the zone.
        return {"digitalocean": "router", "cloudflare": NAME}[kind]

    def expected_marker_name(self, kind: str) -> str:
        return {"digitalocean": "_metrum-router.router", "cloudflare": f"_metrum-router.{NAME}"}[kind]

    def other_name(self, kind: str) -> str:
        return {"digitalocean": "other", "cloudflare": f"other.{ZONE}"}[kind]

    def marker_for(self, kind: str, name: str) -> str:
        return {"digitalocean": "_metrum-router.other", "cloudflare": f"_metrum-router.{name}"}[kind]


if __name__ == "__main__":
    unittest.main(verbosity=2)
