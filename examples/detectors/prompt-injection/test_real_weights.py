# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Opt-in HTTP smoke with the pinned, anonymously downloaded classifier."""

import json
import os
import threading
import time
import unittest
from unittest.mock import patch
from urllib.request import Request, urlopen

from server import DetectorHTTPServer, Handler, PromptInjectionDetector, load_classifier
from smoke_fixtures import BENIGN, POSITIVE


@unittest.skipUnless(os.environ.get("PROMPT_INJECTION_REAL_WEIGHTS") == "1", "opt-in real weights")
class RealWeightsSmokeTest(unittest.TestCase):
    def test_benign_and_positive_over_http(self):
        # Never substitute the synthetic test classifier in this smoke.
        with patch.dict(os.environ, {"PROMPT_INJECTION_TEST_MODE": "0"}):
            classifier, tokenizer = load_classifier()
        Handler.detector = PromptInjectionDetector(classifier, tokenizer=tokenizer)
        server = DetectorHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            for name, text, expected in [("benign", BENIGN, 0), ("positive", POSITIVE, 1)]:
                with self.subTest(fixture=name):
                    event = {"schema_version": "metrum.detector.v1", "kind": "tool_result", "request_id": name,
                             "segments": [{"kind": "tool_result", "index": 0, "text": text}]}
                    request = Request(f"http://127.0.0.1:{server.server_port}/v1/detect", data=json.dumps(event).encode(),
                                      headers={"Content-Type": "application/json"})
                    started = time.monotonic()
                    with urlopen(request, timeout=6) as response:
                        signals = json.load(response)
                    print(json.dumps({"fixture": name, "elapsed_seconds": round(time.monotonic()-started, 3), "signals": signals}))
                    self.assertEqual(len(signals), expected)
                    if expected:
                        self.assertEqual(signals[0]["detector_id"], "prompt_injection")
                        self.assertGreaterEqual(signals[0]["score"], 0.5)
                        self.assertEqual(signals[0]["evidence_request_ids"], [name])
                    self.assertNotIn(text, json.dumps(signals))
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)


if __name__ == "__main__":
    unittest.main()
