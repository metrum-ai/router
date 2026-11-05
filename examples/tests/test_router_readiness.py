#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Tests for the shared example-harness readiness wait (issue #787)."""

from __future__ import annotations

import sys
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "examples"))

import router_readiness  # noqa: E402


class _StubProcess:
    """Minimal subprocess stand-in: alive until told otherwise."""

    def __init__(self, exit_after: float | None = None):
        self.exited = False
        self.exit_after = exit_after
        self._start = time.monotonic()

    def poll(self):
        if self.exit_after is not None and time.monotonic() - self._start >= self.exit_after:
            self.exited = True
        return 0 if self.exited else None


class _Health(BaseHTTPRequestHandler):
    ready_after = 0.0
    requests = 0

    def do_GET(self):  # noqa: N802 - stdlib handler name
        type(self).requests += 1
        if time.monotonic() - self._start < type(self).ready_after:
            self.send_response(503)
            self.end_headers()
            return
        self.send_response(200)
        self.end_headers()

    def log_message(self, *_args):
        pass


def serve(ready_after: float) -> tuple[ThreadingHTTPServer, str]:
    handler = type("Handler", (_Health,), {"ready_after": ready_after, "_start": time.monotonic(), "requests": 0})
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, f"http://127.0.0.1:{server.server_address[1]}/readyz"


class ReadyTimeoutTests(unittest.TestCase):
    def test_returns_elapsed_when_router_becomes_ready(self):
        server, url = serve(ready_after=0.3)
        try:
            elapsed = router_readiness.wait_for_router_ready(_StubProcess(), url, timeout_s=5, quiet=True)
        finally:
            server.shutdown()
        self.assertGreaterEqual(elapsed, 0.25)
        self.assertLess(elapsed, 5)

    def test_timeout_reports_elapsed_and_router_log_tail(self):
        server, url = serve(ready_after=60)
        log = Path(self.id().replace(".", "_") + "_router.log")
        log.write_text("line one\nrouter: starting\nrouter: still starting\n", encoding="utf-8")
        try:
            started = time.monotonic()
            with self.assertRaises(router_readiness.ReadinessTimeout) as caught:
                router_readiness.wait_for_router_ready(_StubProcess(), url, log_path=log, timeout_s=0.4, quiet=True)
            elapsed = time.monotonic() - started
        finally:
            server.shutdown()
            log.unlink(missing_ok=True)
        self.assertLess(elapsed, 3)
        message = str(caught.exception)
        self.assertIn("readiness timeout after", message)
        self.assertIn("budget 0.4s", message)
        self.assertIn("router: still starting", message)

    def test_timeout_is_not_masked_when_the_router_is_slow(self):
        # A generous budget still fails on timeout rather than hanging.
        server, url = serve(ready_after=60)
        try:
            with self.assertRaises(router_readiness.ReadinessTimeout):
                router_readiness.wait_for_router_ready(_StubProcess(), url, timeout_s=0.3, quiet=True)
        finally:
            server.shutdown()

    def test_process_exit_is_reported_with_the_log_tail(self):
        server, url = serve(ready_after=60)
        log = Path(self.id().replace(".", "_") + "_router.log")
        log.write_text("panic: bind: address already in use\n", encoding="utf-8")
        try:
            with self.assertRaises(RuntimeError) as caught:
                router_readiness.wait_for_router_ready(_StubProcess(exit_after=0.0), url, log_path=log, timeout_s=5, quiet=True)
        finally:
            server.shutdown()
            log.unlink(missing_ok=True)
        self.assertIn("process exited", str(caught.exception))
        self.assertIn("address already in use", str(caught.exception))

    def test_env_override_is_used_and_invalid_values_fail_loudly(self):
        self.assertEqual(router_readiness.ready_timeout_s({}), router_readiness.DEFAULT_READY_TIMEOUT_S)
        self.assertEqual(router_readiness.ready_timeout_s({router_readiness.READY_TIMEOUT_ENV: "12.5"}), 12.5)
        for bad in ("soon", "0", "-3"):
            with self.assertRaises(ValueError):
                router_readiness.ready_timeout_s({router_readiness.READY_TIMEOUT_ENV: bad})
        self.assertGreater(router_readiness.DEFAULT_READY_TIMEOUT_S, 15.0)

    def test_env_override_reaches_the_wait(self):
        server, url = serve(ready_after=60)
        import os

        previous = os.environ.get(router_readiness.READY_TIMEOUT_ENV)
        os.environ[router_readiness.READY_TIMEOUT_ENV] = "0.3"
        try:
            started = time.monotonic()
            with self.assertRaises(router_readiness.ReadinessTimeout):
                router_readiness.wait_for_router_ready(_StubProcess(), url, quiet=True)
            self.assertLess(time.monotonic() - started, 3)
        finally:
            server.shutdown()
            if previous is None:
                os.environ.pop(router_readiness.READY_TIMEOUT_ENV, None)
            else:
                os.environ[router_readiness.READY_TIMEOUT_ENV] = previous


if __name__ == "__main__":
    unittest.main()
