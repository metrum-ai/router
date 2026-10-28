# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("overhead_load", HERE / "load.py")
load = importlib.util.module_from_spec(spec)
spec.loader.exec_module(load)


class Upstream(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args):
        pass

    def do_POST(self):
        raw = self.rfile.read(int(self.headers["Content-Length"]))
        req = json.loads(raw)
        with self.server.lock:
            self.server.seen.append((self.path, self.headers.get("Authorization"), raw))
        usage = {"prompt_tokens": 8 if self.server.mismatch_prompt and self.path == "/routed" else 7,
                 "completion_tokens": 3}
        if self.server.bad_usage and req["stream"]:
            usage = {"prompt_tokens": 7, "completion_tokens": 2}
        if req["stream"]:
            events = [
                {"choices": [{"delta": {"role": "assistant"}}]},
                {"choices": [{"delta": {"content": "a"}}]},
                {"choices": [{"delta": {"content": "bc"}}]},
                {"choices": [], "usage": usage},
            ]
            data = b"".join(b"data: " + json.dumps(e).encode() + b"\n\n" for e in events) + b"data: [DONE]\n\n"
            content_type = "text/event-stream"
        else:
            data = json.dumps({"choices": [{"message": {"content": "abc"}}], "usage": usage}).encode()
            content_type = "application/json"
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class LoadTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)
        self.prompt = self.root / "prompt.txt"
        self.prompt.write_text("synthetic prompt for load driver", encoding="utf-8")
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        self.server.daemon_threads = True
        self.server.lock = threading.Lock()
        self.server.seen = []
        self.server.bad_usage = False
        self.server.mismatch_prompt = False
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.stop_server)

    def stop_server(self):
        self.server.shutdown()
        self.thread.join()
        self.server.server_close()

    def run_driver(self):
        host = f"http://127.0.0.1:{self.server.server_port}"
        env = dict(os.environ, LOAD_TEST_DIRECT="synthetic-direct-key", LOAD_TEST_ROUTER="synthetic-router-key")
        return subprocess.run([
            sys.executable, str(HERE / "load.py"), "--direct-url", host + "/direct", "--router-url", host + "/routed",
            "--direct-token-env", "LOAD_TEST_DIRECT", "--router-token-env", "LOAD_TEST_ROUTER",
            "--prompt-file", str(self.prompt), "--model", "mock-model", "--output-dir", str(self.root / "results"),
            "--output-tokens", "3", "--concurrency", "2", "--repetitions", "3", "--requests-per-worker", "2",
            "--timeout", "10",
        ], env=env, text=True, capture_output=True, timeout=40)

    def test_pairing_streaming_and_private_results(self):
        result = self.run_driver()
        self.assertEqual(result.returncode, 0, result.stderr)
        out = self.root / "results"
        samples = [json.loads(line) for line in (out / "samples.jsonl").read_text().splitlines()]
        summary = json.loads((out / "summary.json").read_text())
        manifest = json.loads((out / "manifest.json").read_text())
        self.assertEqual(len(samples), 48)
        self.assertEqual(len(summary["pairs"]), 6)
        self.assertEqual(len(summary["delta_variance"]), 2)
        self.assertEqual(manifest["concurrency"], [2])
        self.assertTrue(all(r["prompt_tokens"] == 7 and r["completion_tokens"] == 3 for r in samples))
        self.assertTrue(all(r["ttft_content_ms"] is not None and len(r["content_event_gaps_ms"]) == 1 for r in samples if r["streaming"]))
        self.assertTrue(all(r["ttft_content_ms"] is None for r in samples if not r["streaming"]))
        self.assertEqual(out.stat().st_mode & 0o777, 0o700)
        for path in out.iterdir():
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            text = path.read_text()
            self.assertNotIn("synthetic-direct-key", text)
            self.assertNotIn("synthetic-router-key", text)
            self.assertNotIn("synthetic prompt for load driver", text)
        with self.server.lock:
            seen = self.server.seen.copy()
        self.assertEqual(len(seen), 72)  # includes one warmup per worker per phase
        self.assertTrue(all(auth == ("Bearer synthetic-direct-key" if path == "/direct" else "Bearer synthetic-router-key") for path, auth, _ in seen))
        self.assertEqual({body for path, _, body in seen if path == "/direct"}, {body for path, _, body in seen if path == "/routed"})

    def test_wrong_output_tokens_aborts_without_summary(self):
        self.server.bad_usage = True
        result = self.run_driver()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no complete summary", result.stderr)
        self.assertFalse((self.root / "results" / "summary.json").exists())
        self.assertNotIn("synthetic-direct-key", result.stderr)
        self.assertNotIn("synthetic-router-key", result.stderr)

    def test_mismatched_prompt_tokens_aborts_without_summary(self):
        self.server.mismatch_prompt = True
        result = self.run_driver()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no complete summary", result.stderr)
        self.assertFalse((self.root / "results" / "summary.json").exists())

    def test_url_and_percentiles(self):
        self.assertEqual(load.percentile([1, 2, 3, 4], .5), 2)
        self.assertEqual(load.percentile([1, 2, 3, 4], .99), 4)
        for url in ["http://user:secret@localhost/path", "http://localhost/path?key=secret", "file:///tmp/foo", "http://localhost/path#fragment"]:
            with self.assertRaises(ValueError):
                load.parse_endpoint(url)


if __name__ == "__main__":
    unittest.main()
