#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline regression for the direct-provider differential canary (#330, ARCH-05).

Runs two local HTTP servers as the direct provider and the pinned router target,
so the whole layer (probes, checks, classification, report, gates, exit codes)
is exercised with no network and no spend.
"""

from __future__ import annotations

import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def load(name: str):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / f"{name}.py")
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    # Register before execution so dataclasses can resolve the module namespace.
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


C = load("direct_provider_canary")


def require(ok: bool, message: str) -> None:
    if not ok:
        raise AssertionError(message)


def probe_checks() -> None:
    nonce = "abc123"
    require(C.check_probe({"kind": "exact_text"}, nonce, "CANARY-abc123")[0], "exact text must pass")
    require(not C.check_probe({"kind": "exact_text"}, nonce, "CANARY-abc123.")[0], "punctuation must fail exact text")
    require(C.check_probe({"kind": "contains_text"}, nonce, "here is CANARY-abc123 in a sentence")[0], "contains must pass")
    require(not C.check_probe({"kind": "contains_text"}, nonce, "no token here")[0], "missing token must fail")
    require(C.check_probe({"kind": "max_words", "max_words": 3}, nonce, "it is four")[0], "three words pass")
    require(not C.check_probe({"kind": "max_words", "max_words": 3}, nonce, "the answer is four")[0], "four words fail")
    require(C.check_probe({"kind": "max_words", "max_words": 3}, nonce, "four.")[0], "one word passes")
    require(C.check_probe({"kind": "json_int_field", "field": "answer", "a": 17, "b": 25}, nonce, '{"answer": 42}')[0], "json sum passes")
    require(not C.check_probe({"kind": "json_int_field", "field": "answer", "a": 17, "b": 25}, nonce, '{"answer": 41}')[0], "wrong sum fails")
    require(C.check_probe({"kind": "json_int_field", "field": "answer", "a": 1, "b": 1}, nonce, 'Sure: {"answer": 2} done')[0], "json extracted from prose")
    require(not C.check_probe({"kind": "json_int_field", "field": "answer"}, nonce, "no json")[0], "missing json fails")


def classify_cases() -> None:
    def side(pass_ids: set[str], fail_ids: set[str]) -> dict[str, C.ProbeResult]:
        out = {}
        for probe_id in pass_ids | fail_ids:
            outcome = "pass" if probe_id in pass_ids else "fail"
            out[probe_id] = C.ProbeResult(probe_id, outcome, 1.0, 0.0)
        return out

    counts = C.classify(side({"a", "b"}, {"c"}), side({"a", "b"}, {"c"}))
    require(counts == {"both_pass": 2, "both_fail": 1, "router_regression": 0, "provider_only_fail": 0}, f"both pass: {counts}")
    counts = C.classify(side({"a", "b"}, {"c"}), side({"a"}, {"b", "c"}))
    require(counts["router_regression"] == 1 and counts["both_fail"] == 1, f"router regression: {counts}")
    counts = C.classify(side({"a"}, {"b"}), side({"a", "b"}, set()))
    require(counts["provider_only_fail"] == 1, f"provider-only fail: {counts}")


class _ProbeHandler(BaseHTTPRequestHandler):
    """Scripted OpenAI-compatible Chat endpoint."""

    answers: dict[str, str] = {}

    def do_POST(self) -> None:  # noqa: N802 - stdlib handler name
        length = int(self.headers.get("content-length") or 0)
        body = json.loads(self.rfile.read(length) or b"{}")
        prompt = body["messages"][0]["content"]
        token = _nonce_in(prompt)
        text = ""
        for key, value in type(self).answers.items():
            if key in prompt:
                text = value.replace("{nonce}", token)
                break
        payload = json.dumps(
            {
                "id": "chatcmpl_canary",
                "object": "chat.completion",
                "choices": [{"index": 0, "message": {"role": "assistant", "content": text}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 12, "completion_tokens": 4, "total_tokens": 16},
            }
        ).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *_args) -> None:
        pass


def _nonce_in(prompt: str) -> str:
    import re

    match = re.search(r"CANARY-([A-Za-z0-9-]+)", prompt)
    return match.group(1) if match else "none"


GOOD = {
    "Reply with exactly this token": "CANARY-{nonce}",
    "compact JSON object": '{"answer": 42}',
    "at most three words": "four",
    "contains the token": "This sentence contains CANARY-{nonce} verbatim.",
}


def _serve(answers: dict) -> tuple[HTTPServer, threading.Thread]:
    handler = type("Handler", (_ProbeHandler,), {"answers": answers})
    server = HTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, thread


def _run_script(direct_url: str, router_url: str, report_path: Path) -> subprocess.CompletedProcess:
    env = dict(os.environ)
    env.update(
        {
            "CANARY_DIRECT_BASE_URL": direct_url,
            "CANARY_DIRECT_API_KEY": "test-key",
            "CANARY_DIRECT_MODEL": "test-model",
            "CANARY_ROUTER_BASE_URL": router_url,
            "CANARY_ROUTER_API_KEY": "test-key",
            "CANARY_ROUTER_MODEL_GROUP": "test-group",
            "CANARY_MAX_COST_USD": "0.50",
        }
    )
    return subprocess.run(
        [sys.executable, str(ROOT / "scripts" / "direct_provider_canary.py"), "run", "--report-out", str(report_path)],
        capture_output=True,
        text=True,
        env=env,
    )


def end_to_end(tmp: Path) -> None:
    # One direct server answers every probe correctly for the whole test.
    direct, _ = _serve(GOOD)
    try:
        # 1. Both sides correct: pass, report validates, validate subcommand accepts.
        router_ok, _ = _serve(GOOD)
        try:
            report_path = tmp / "canary-ok.json"
            run = _run_script(
                f"http://127.0.0.1:{direct.server_port}/v1",
                f"http://127.0.0.1:{router_ok.server_port}/v1",
                report_path,
            )
            require(run.returncode == 0, f"both-pass canary must exit 0: {run.stdout}\n{run.stderr}")
            report = json.loads(report_path.read_text())
            require(report["canary"]["classification"]["both_pass"] == 4, f"both pass count: {report['canary']}")
            require([c["cell_id"] for c in report["cells"]] == ["canary-direct", "canary-router-pinned"], "cell ids")
            violations = C.validate_report(report)
            require(not violations, f"report must validate: {violations}")
            validate = subprocess.run(
                [sys.executable, str(ROOT / "scripts" / "direct_provider_canary.py"), "validate", str(report_path)],
                capture_output=True,
                text=True,
            )
            require(validate.returncode == 0, f"validate must pass: {validate.stdout}\n{validate.stderr}")
        finally:
            router_ok.shutdown()

        # 2. Router misses a probe the direct call passes: router regression, exit 1.
        broken = {key: value for key, value in GOOD.items() if "JSON object" not in key}
        router_bad, _ = _serve(broken)
        try:
            report_path = tmp / "canary-regression.json"
            run = _run_script(
                f"http://127.0.0.1:{direct.server_port}/v1",
                f"http://127.0.0.1:{router_bad.server_port}/v1",
                report_path,
            )
            require(run.returncode == 1, f"router regression must exit 1: {run.stdout}\n{run.stderr}")
            report = json.loads(report_path.read_text())
            require(report["canary"]["classification"]["router_regression"] == 1, f"regression count: {report['canary']}")
            validate = subprocess.run(
                [sys.executable, str(ROOT / "scripts" / "direct_provider_canary.py"), "validate", str(report_path)],
                capture_output=True,
                text=True,
            )
            require(validate.returncode == 1, "recorded regression report must fail validation")
        finally:
            router_bad.shutdown()
    finally:
        direct.shutdown()

    # 3. Direct side misses a probe the router passes: provider-only noise, exit 0.
    direct_bad, _ = _serve({key: value for key, value in GOOD.items() if "JSON object" not in key})
    router_ok2, _ = _serve(GOOD)
    try:
        report_path = tmp / "canary-provider.json"
        run = _run_script(
            f"http://127.0.0.1:{direct_bad.server_port}/v1", f"http://127.0.0.1:{router_ok2.server_port}/v1", report_path
        )
        require(run.returncode == 0, f"provider-only failure must not fail the run: {run.stdout}\n{run.stderr}")
        report = json.loads(report_path.read_text())
        require(report["canary"]["classification"]["provider_only_fail"] == 1, f"provider-only: {report['canary']}")
    finally:
        direct_bad.shutdown()
        router_ok2.shutdown()


def config_errors() -> None:
    env = {k: v for k, v in os.environ.items() if not k.startswith("CANARY_")}
    run = subprocess.run(
        [sys.executable, str(ROOT / "scripts" / "direct_provider_canary.py"), "run"],
        capture_output=True,
        text=True,
        env=env,
    )
    require(run.returncode == 2, f"missing configuration must exit 2, got {run.returncode}")


def main() -> int:
    probe_checks()
    classify_cases()
    with tempfile.TemporaryDirectory() as tmp:
        end_to_end(Path(tmp))
    config_errors()
    print("direct-provider differential canary tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
