# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import json
import io
import sys
import socket
import threading
import time
import unittest
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from server import DetectorHTTPServer, Handler, MAX_SEGMENTS, PromptInjectionDetector, validate_event


class CharacterTokenizer:
    def encode(self, text, add_special_tokens=False):
        return [ord(char) for char in text]

    def num_special_tokens_to_add(self, pair=False):
        return 2

    def decode(self, token_ids, skip_special_tokens=True):
        return "".join(chr(token) for token in token_ids)


class PromptInjectionDetectorTest(unittest.TestCase):
    def setUp(self):
        self.server = DetectorHTTPServer(("127.0.0.1", 0), Handler, request_timeout=0.5)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.endpoint = f"http://127.0.0.1:{self.server.server_port}/v1/detect"

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)

    def post(self, body, endpoint=None, method="POST"):
        request = Request(endpoint or self.endpoint, data=body if method == "POST" else None, headers={"Content-Type": "application/json"}, method=method)
        try:
            with urlopen(request, timeout=2) as response:
                return response.status, response.read()
        except HTTPError as error:
            return error.code, error.read()

    def test_injection_signal_contains_only_metadata(self):
        marker = "PRIVATE_SEGMENT_MARKER"
        Handler.detector = PromptInjectionDetector(lambda text: {"label": "LABEL_1", "score": 0.91})
        event = {
            "schema_version": "metrum.detector.v1",
            "request_id": "request-1",
            "kind": "tool_result",
            "segments": [{"kind": "tool_result", "index": 2, "text": marker}],
        }
        stderr = io.StringIO()
        previous_stderr = sys.stderr
        sys.stderr = stderr
        try:
            status, body = self.post(json.dumps(event).encode())
        finally:
            sys.stderr = previous_stderr
        self.assertEqual(status, 200)
        signal = json.loads(body)[0]
        self.assertEqual(signal["detector_id"], "prompt_injection")
        self.assertEqual(signal["segment_kind"], "tool_result")
        self.assertEqual(signal["segment_index"], 2)
        self.assertEqual(signal["evidence_request_ids"], ["request-1"])
        self.assertNotIn(marker.encode(), body)
        self.assertNotIn(marker, stderr.getvalue())
        self.assertNotIn("key", signal)

    def test_benign_and_unselected_segments_return_empty_list(self):
        calls = []
        Handler.detector = PromptInjectionDetector(lambda text: calls.append(text) or {"label": "LABEL_0", "score": 0.99})
        event = {
            "schema_version": "metrum.detector.v1",
            "kind": "benign",
            "segments": [{"kind": "document", "index": 0, "text": "ordinary"}],
        }
        status, body = self.post(json.dumps(event).encode())
        self.assertEqual((status, json.loads(body)), (200, []))
        self.assertEqual(calls, ["ordinary"])

    def test_bad_fields_segment_kind_size_and_classifier_error(self):
        Handler.detector = PromptInjectionDetector(lambda _text: {"label": "LABEL_0", "score": 1.0})
        bad = {"schema_version": "metrum.detector.v1", "kind": "benign", "prompt": "private"}
        status, body = self.post(json.dumps(bad).encode())
        self.assertEqual(status, 400)
        self.assertNotIn(b"private", body)

        valid_segments = [{"kind": "document", "index": i, "text": "ok"} for i in range(MAX_SEGMENTS)]
        self.assertEqual(MAX_SEGMENTS, 16)
        validate_event({"schema_version": "metrum.detector.v1", "kind": "test", "segments": valid_segments})
        too_many = dict(schema_version="metrum.detector.v1", kind="test", segments=valid_segments + [valid_segments[0]])
        with self.assertRaises(ValueError):
            validate_event(too_many)

        invalid_segment = {
            "schema_version": "metrum.detector.v1", "kind": "benign",
            "segments": [{"kind": "user_message", "index": 0, "text": "private"}],
        }
        self.assertEqual(self.post(json.dumps(invalid_segment).encode())[0], 400)
        oversized = b" " * (64 * 1024 + 1)
        self.assertEqual(self.post(oversized)[0], 413)

        Handler.detector = PromptInjectionDetector(lambda _text: (_ for _ in ()).throw(RuntimeError("private")))
        event = {
            "schema_version": "metrum.detector.v1", "kind": "tool_result",
            "segments": [{"kind": "tool_result", "index": 0, "text": "private"}],
        }
        status, body = self.post(json.dumps(event).encode())
        self.assertEqual(status, 500)
        self.assertNotIn(b"private", body)

    def test_injection_after_benign_padding_is_found_in_later_chunk(self):
        marker = "[injection-appended-after-padding]"
        text = ("ordinary benign padding " * 35) + marker
        calls = []

        def classify(chunk):
            calls.append(chunk)
            return {"label": "injection" if marker in chunk else "benign", "score": 0.81 if marker in chunk else 0.99}

        detector = PromptInjectionDetector(classify, tokenizer=CharacterTokenizer())
        signals = detector.detect({
            "schema_version": "metrum.detector.v1", "request_id": "chunked", "kind": "tool_result",
            "segments": [{"kind": "tool_result", "index": 4, "text": text}],
        })
        self.assertGreater(len(calls), 1)
        self.assertIn(marker, "".join(calls))
        self.assertEqual(len(signals), 1)
        self.assertEqual(signals[0]["score"], 0.81)

    def test_stalled_client_does_not_block_another_detection(self):
        Handler.detector = PromptInjectionDetector(lambda _text: {"label": "benign", "score": 1.0}, request_timeout=0.5)
        stalled = socket.create_connection(("127.0.0.1", self.server.server_port), timeout=1)
        stalled.sendall(b"POST /v1/detect HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\n{}")
        time.sleep(0.05)
        event = {
            "schema_version": "metrum.detector.v1", "kind": "benign",
            "segments": [{"kind": "document", "index": 0, "text": "ordinary"}],
        }
        status, body = self.post(json.dumps(event).encode())
        self.assertEqual((status, json.loads(body)), (200, []))
        stalled.settimeout(2)
        response = stalled.recv(1024)
        stalled.close()
        self.assertIn(b"408", response)

    def test_invalid_path_and_method_do_not_echo_request_line(self):
        marker = "PRIVATE_PATH_MARKER"
        endpoint = f"http://127.0.0.1:{self.server.server_port}/{marker}"
        status, body = self.post(b"{}", endpoint=endpoint)
        self.assertEqual(status, 404)
        self.assertNotIn(marker.encode(), body)
        status, body = self.post(b"", endpoint=endpoint, method="GET")
        self.assertEqual(status, 501)
        self.assertNotIn(marker.encode(), body)


if __name__ == "__main__":
    unittest.main()
