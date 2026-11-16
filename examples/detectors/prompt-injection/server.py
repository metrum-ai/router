# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Standalone prompt-injection detector implementing metrum.detector.v1."""

import json
import hashlib
import math
import os
import socket
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Lock

SCHEMA_VERSION = "metrum.detector.v1"
DETECTOR_ID = "prompt_injection"
MAX_REQUEST_BYTES = 64 * 1024
MAX_SEGMENT_BYTES = 8 * 1024
MAX_SEGMENT_BYTES_TOTAL = 48 * 1024
MAX_SEGMENTS = 16
MODEL_REVISION = "d7c8842daf06de3179cc3aca76b7b3a057acc5e7"
MODEL_WEIGHTS_SHA256 = "5f81f709c58b8e8a51d99e8382a152583e17847db3082922bcaf7a7ee80e91d0"
MAX_MODEL_TOKENS = 512
TOKEN_OVERLAP = 64
REQUEST_TIMEOUT_SECONDS = 4.0
MODEL_ID = "protectai/deberta-v3-small-prompt-injection-v2"
ALLOWED_EVENT_FIELDS = {
    "schema_version", "request_id", "caller_id", "kind", "observed_at",
    "source_ip", "key_id", "tiny_request", "denied_group", "tokens", "segments",
}


class ContractError(ValueError):
    pass


def validate_event(event):
    if not isinstance(event, dict) or set(event) - ALLOWED_EVENT_FIELDS:
        raise ContractError("invalid event fields")
    if event.get("schema_version") != SCHEMA_VERSION:
        raise ContractError("invalid schema version")
    if not isinstance(event.get("kind"), str) or not event["kind"]:
        raise ContractError("invalid event kind")
    for key in ("request_id", "caller_id", "observed_at", "source_ip", "key_id", "denied_group"):
        if key in event and not isinstance(event[key], str):
            raise ContractError("invalid event metadata")
    if "tiny_request" in event and not isinstance(event["tiny_request"], bool):
        raise ContractError("invalid event metadata")
    if "tokens" in event and (not isinstance(event["tokens"], int) or isinstance(event["tokens"], bool) or event["tokens"] < 0):
        raise ContractError("invalid event metadata")
    segments = event.get("segments", [])
    if not isinstance(segments, list) or len(segments) > MAX_SEGMENTS:
        raise ContractError("invalid segments")
    total = 0
    for segment in segments:
        if not isinstance(segment, dict) or set(segment) != {"kind", "index", "text"}:
            raise ContractError("invalid segment")
        if segment["kind"] not in ("tool_result", "document"):
            raise ContractError("invalid segment kind")
        if not isinstance(segment["index"], int) or isinstance(segment["index"], bool) or segment["index"] < 0:
            raise ContractError("invalid segment index")
        if not isinstance(segment["text"], str) or not segment["text"]:
            raise ContractError("invalid segment text")
        size = len(segment["text"].encode("utf-8"))
        if size > MAX_SEGMENT_BYTES:
            raise ContractError("segment too large")
        total += size
    if total > MAX_SEGMENT_BYTES_TOTAL:
        raise ContractError("segments too large")
    return segments


def is_injection(result):
    label = str(result.get("label", "")).lower()
    if label in {"injection", "malicious", "label_1"}:
        return True
    if label in {"benign", "safe", "label_0"}:
        return False
    raise ValueError("classifier returned an unknown label")


def overlapping_chunks(text, tokenizer, max_length=MAX_MODEL_TOKENS, overlap=TOKEN_OVERLAP):
    token_ids = tokenizer.encode(text, add_special_tokens=False)
    special_tokens = tokenizer.num_special_tokens_to_add(pair=False)
    window = max_length - special_tokens
    if window <= overlap:
        raise ValueError("model token window is too small")
    step = window - overlap
    chunks = []
    for start in range(0, len(token_ids), step):
        ids = token_ids[start:start + window]
        if not ids:
            break
        chunks.append(tokenizer.decode(ids, skip_special_tokens=True))
        if start + window >= len(token_ids):
            break
    return chunks


class PromptInjectionDetector:
    def __init__(self, classifier, threshold=0.5, tokenizer=None, request_timeout=REQUEST_TIMEOUT_SECONDS):
        self.classifier = classifier
        self.threshold = threshold
        self.tokenizer = tokenizer
        self.request_timeout = request_timeout
        self.lock = Lock()

    def detect(self, event):
        segments = validate_event(event)
        signals = []
        for segment in segments:
            chunks = overlapping_chunks(segment["text"], self.tokenizer) if self.tokenizer else [segment["text"]]
            max_injection_score = 0.0
            for chunk in chunks:
                with self.lock:
                    result = self.classifier(chunk)
                if isinstance(result, list):
                    if len(result) != 1:
                        raise ValueError("classifier returned invalid output")
                    result = result[0]
                if not isinstance(result, dict):
                    raise ValueError("classifier returned invalid output")
                score = float(result.get("score", 0))
                if not math.isfinite(score) or score < 0 or score > 1:
                    raise ValueError("classifier returned invalid score")
                if is_injection(result):
                    max_injection_score = max(max_injection_score, score)
            if max_injection_score >= self.threshold:
                signal = {
                    "schema_version": SCHEMA_VERSION,
                    "detector_id": DETECTOR_ID,
                    "severity": "high",
                    "score": max_injection_score,
                    "segment_kind": segment["kind"],
                    "segment_index": segment["index"],
                }
                if event.get("request_id"):
                    signal["evidence_request_ids"] = [event["request_id"]]
                signals.append(signal)
        return signals


class Handler(BaseHTTPRequestHandler):
    detector = None

    def setup(self):
        super().setup()
        timeout = self.server.request_timeout
        self.request_deadline = time.monotonic() + timeout
        self.connection.settimeout(timeout)

    def log_message(self, _format, *_args):
        # Request paths and bodies are intentionally never logged.
        return

    def send_error(self, code, _message=None, _explain=None):
        # BaseHTTPRequestHandler's default error page includes the request line.
        self._respond(code, {"error": "request rejected"})

    def do_POST(self):
        deadline = self.request_deadline
        if self.path != "/v1/detect":
            self._respond(404, {"error": "request rejected"})
            return
        try:
            length = int(self.headers.get("Content-Length", "-1"))
        except ValueError:
            self._respond(400, {"error": "invalid request"})
            return
        if length < 0 or length > MAX_REQUEST_BYTES:
            self._respond(413 if length > MAX_REQUEST_BYTES else 400, {"error": "invalid request"})
            return
        try:
            raw = bytearray()
            while len(raw) < length:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise socket.timeout("request deadline exceeded")
                self.connection.settimeout(remaining)
                chunk = self.rfile.read1(min(8192, length - len(raw)))
                if not chunk:
                    raise ContractError("truncated request body")
                raw.extend(chunk)
            event = json.loads(raw)
            signals = self.detector.detect(event)
            if time.monotonic() > deadline:
                self._respond(408, {"error": "request deadline exceeded"})
                return
        except (UnicodeDecodeError, json.JSONDecodeError, ContractError):
            self._respond(400, {"error": "invalid detector event"})
            return
        except (socket.timeout, TimeoutError):
            self._respond(408, {"error": "request deadline exceeded"})
            return
        except Exception:
            self._respond(500, {"error": "detector unavailable"})
            return
        self._respond(200, signals)

    def _respond(self, status, body):
        data = json.dumps(body, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(data)


class DetectorHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, address, handler, request_timeout=REQUEST_TIMEOUT_SECONDS):
        self.request_timeout = request_timeout
        super().__init__(address, handler)

    def get_request(self):
        connection, address = super().get_request()
        connection.settimeout(self.request_timeout)
        return connection, address


def load_classifier():
    if os.environ.get("PROMPT_INJECTION_TEST_MODE") == "1":
        def test_classifier(text):
            if "[detector-test-injection]" in text:
                return {"label": "injection", "score": 0.99}
            return {"label": "benign", "score": 0.99}
        return test_classifier, None
    import torch
    from huggingface_hub import snapshot_download
    from transformers import AutoModelForSequenceClassification, AutoTokenizer, pipeline

    model_path = snapshot_download(
        repo_id=MODEL_ID,
        revision=MODEL_REVISION,
        allow_patterns=[
            "added_tokens.json",
            "config.json",
            "model.safetensors",
            "special_tokens_map.json",
            "spm.model",
            "tokenizer.json",
            "tokenizer_config.json",
        ],
    )
    weights_path = os.path.join(model_path, "model.safetensors")
    digest = hashlib.sha256()
    with open(weights_path, "rb") as weights:
        for block in iter(lambda: weights.read(1024 * 1024), b""):
            digest.update(block)
    if digest.hexdigest() != MODEL_WEIGHTS_SHA256:
        raise RuntimeError("downloaded model weights failed SHA-256 verification")
    tokenizer = AutoTokenizer.from_pretrained(model_path, use_fast=False, local_files_only=True)
    classifier_model = AutoModelForSequenceClassification.from_pretrained(model_path, local_files_only=True)
    classifier = pipeline(
        "text-classification",
        model=classifier_model,
        tokenizer=tokenizer,
        truncation=True,
        max_length=MAX_MODEL_TOKENS,
        device=torch.device("cuda" if torch.cuda.is_available() else "cpu"),
    )
    return classifier, tokenizer


def main():
    threshold = float(os.environ.get("PROMPT_INJECTION_THRESHOLD", "0.5"))
    if not math.isfinite(threshold) or threshold < 0 or threshold > 1:
        raise ValueError("PROMPT_INJECTION_THRESHOLD must be between 0 and 1")
    classifier, tokenizer = load_classifier()
    Handler.detector = PromptInjectionDetector(classifier, threshold=threshold, tokenizer=tokenizer)
    host = os.environ.get("LISTEN_HOST", "127.0.0.1")
    port = int(os.environ.get("LISTEN_PORT", "8082"))
    server = DetectorHTTPServer((host, port), Handler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
