#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Run a packaged router against a synthetic localhost upstream.

The emitted JSON contains safe scalar local-process evidence only. It is never
credential-backed deployment, publication, promotion, or human approval evidence.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import base64
import json
import os
import secrets
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


class UpstreamHandler(BaseHTTPRequestHandler):
    calls = 0

    def do_POST(self) -> None:  # noqa: N802
        type(self).calls += 1
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        body = json.dumps(
            {
                "id": "synthetic-upstream-response",
                "choices": [{"message": {"role": "assistant", "content": "LOCAL_SMOKE_OK"}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
            }
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format: str, *_args: object) -> None:
        return


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def request(base_url: str, path: str, *, token: str | None = None, payload: dict[str, object] | None = None) -> tuple[int, bytes, str]:
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    data = None
    if payload is not None:
        data = json.dumps(payload).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(base_url + path, data=data, headers=headers, method="POST" if data is not None else "GET")
    try:
        with urllib.request.urlopen(req, timeout=5) as response:
            return response.status, response.read(), response.headers.get("X-Request-Id", "")
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read(), exc.headers.get("X-Request-Id", "")


def token_identity(label: str) -> tuple[str, str]:
    token_id = f"rtr_metrum_local_localproject_local_{label}"
    return token_id, f"{token_id}_{hashlib.sha256(label.encode()).hexdigest()[:32]}"


def token_hash(token: str) -> str:
    salt = secrets.token_bytes(16)
    key = hashlib.pbkdf2_hmac("sha3_512", token.encode(), salt, 20000, 64)
    enc = lambda value: base64.urlsafe_b64encode(value).decode().rstrip("=")
    return f"pbkdf2-sha3-512:i=20000:{enc(salt)}:{enc(key)}"


def check_json(body: bytes, key: str, expected: object) -> bool:
    parsed = json.loads(body)
    return parsed.get(key) == expected


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--router-binary", required=True, type=Path)
    parser.add_argument("--usage-report-binary", required=True, type=Path)
    parser.add_argument("--expected-version", required=True)
    parser.add_argument("--expected-commit", required=True)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--usage-db", type=Path, help="use an existing migrated usage database")
    args = parser.parse_args()

    allowed_id, allowed_token = token_identity("allowed")
    quota_id, quota_token = token_identity("quota")
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), UpstreamHandler)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    router_port = free_port()
    checks: list[dict[str, object]] = []

    with tempfile.TemporaryDirectory(prefix="smart-router-launch-smoke-") as temp_raw:
        temp = Path(temp_raw)
        config = {
            "server": {
                "listen": f"127.0.0.1:{router_port}",
                "default_model_group": "local-smoke",
                "identifiers": {"mode": "passthrough"},
                "logging": {"path": str(temp / "requests.jsonl")},
                "usage_db": {"enabled": True, "driver": "sqlite", "path": str(args.usage_db or temp / "usage.sqlite"), "migration_policy": "deployment-job" if args.usage_db else "auto-safe"},
            },
            "state_path": str(temp / "state.json"),
            "providers": {
                "synthetic": {
                    "base_url": f"http://127.0.0.1:{upstream.server_port}/v1",
                    "dialect": "openai",
                    "api_key": "synthetic-local-provider-key",
                }
            },
            "models": {
                "local-smoke": {"strategy": "static", "targets": [{"provider": "synthetic", "model": "synthetic-model"}]},
                "denied-group": {"strategy": "static", "targets": [{"provider": "synthetic", "model": "denied-model"}]},
            },
            "callers": [
                {
                    "id": "local-allowed", "user": "local-user", "project": "local-project", "environment": "local",
                    "status": "active", "token_hash": token_hash(allowed_token), "token_id": allowed_id,
                    "allow": ["local-smoke"], "rate": {"rpm": 100, "tpm": 100000, "concurrent": 4},
                    "quota": {"day": {"requests": 100, "tokens": 100000}, "month": {"tokens": 1000000}, "soft_pct": 80},
                    "key": {"lifetime_tokens": 1000000, "soft_pct": 90, "on_exhaust": "disable"},
                },
                {
                    "id": "local-quota", "user": "local-user", "project": "local-project", "environment": "local",
                    "status": "active", "token_hash": token_hash(quota_token), "token_id": quota_id,
                    "allow": ["local-smoke"], "rate": {"rpm": 100, "tpm": 100000, "concurrent": 4},
                    "quota": {"day": {"requests": 100, "tokens": 20}, "month": {"tokens": 1000000}, "soft_pct": 80},
                    "key": {"lifetime_tokens": 1000000, "soft_pct": 90, "on_exhaust": "disable"},
                },
            ],
        }
        config_path = temp / "config.json"
        config_path.write_text(json.dumps(config), encoding="utf-8")
        stderr_path = temp / "router.stderr"
        with stderr_path.open("wb") as stderr:
            process = subprocess.Popen([str(args.router_binary.resolve()), "-config", str(config_path)], stdout=subprocess.DEVNULL, stderr=stderr)
        try:
            base = f"http://127.0.0.1:{router_port}"
            for _ in range(100):
                try:
                    status, _, _ = request(base, "/readyz")
                    if status == 200:
                        break
                except OSError:
                    pass
                if process.poll() is not None:
                    diagnostic = stderr_path.read_text(encoding="utf-8", errors="replace")[-1000:]
                    for sensitive in (allowed_token, quota_token, "synthetic-local-provider-key"):
                        diagnostic = diagnostic.replace(sensitive, "[REDACTED]")
                    raise RuntimeError(f"router exited before readiness: {diagnostic.strip()}")
                time.sleep(0.05)
            else:
                raise RuntimeError("router did not become ready")

            def record(name: str, status: int, passed: bool, request_id: str = "") -> None:
                checks.append({"check": name, "http_status": status, "passed": passed, "request_id_present": bool(request_id)})

            status, body, rid = request(base, "/version")
            version = json.loads(body)
            record("version", status, status == 200 and version.get("version") == args.expected_version and version.get("commit") == args.expected_commit, rid)
            status, body, rid = request(base, "/readyz")
            record("ready", status, status == 200 and check_json(body, "ok", True), rid)
            status, body, rid = request(base, "/docs/")
            record("hosted-docs", status, status == 200 and b"Metrum AI Router" in body, rid)
            status, body, rid = request(base, "/v1/models", token=allowed_token)
            model_ids = [item.get("id") for item in json.loads(body).get("data", [])]
            record("authenticated-models", status, status == 200 and model_ids == ["local-smoke"], rid)
            prompt = "synthetic local launch path sentinel"
            chat = {"model": "local-smoke", "messages": [{"role": "user", "content": prompt}], "max_tokens": 16}
            status, body, rid = request(base, "/v1/chat/completions", token=allowed_token, payload=chat)
            record("authenticated-text", status, status == 200 and b"LOCAL_SMOKE_OK" in body and bool(rid), rid)
            status, body, rid = request(base, "/v1/chat/completions", payload=chat)
            record("unauthenticated-rejection", status, status == 401 and b"unauthorized" in body and bool(rid), rid)
            denied = {**chat, "model": "denied-group"}
            status, body, rid = request(base, "/v1/chat/completions", token=allowed_token, payload=denied)
            rejected_model = (status == 403 and b"model-not-allowed" in body) or status == 404
            record("model-access-rejection", status, rejected_model and bool(rid), rid)
            over_quota = {**chat, "max_tokens": 100}
            status, body, rid = request(base, "/v1/chat/completions", token=quota_token, payload=over_quota)
            record("quota-rejection", status, status == 429 and b"quota-exhausted" in body and bool(rid), rid)
            status, body, rid = request(base, "/metrics", token=allowed_token)
            record("metrics-admin-isolation", status, status == 403 and b"metrics-forbidden" in body and bool(rid), rid)

            report_path = temp / "usage-report.md"
            completed = subprocess.run(
                [str(args.usage_report_binary.resolve()), "-db", str(args.usage_db or temp / "usage.sqlite"), "-since", "1h", "-out", str(report_path)],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False,
            )
            report = report_path.read_text(encoding="utf-8") if report_path.exists() else ""
            safe_report = prompt not in report and allowed_token not in report and quota_token not in report and "synthetic-local-provider-key" not in report
            checks.append({"check": "usage-report", "exit_code": completed.returncode, "passed": completed.returncode == 0 and safe_report, "sensitive_values_absent": safe_report})
            checks.append({"check": "negative-cases-skipped-upstream", "upstream_calls": UpstreamHandler.calls, "passed": UpstreamHandler.calls == 1})
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
            upstream.shutdown()
            upstream.server_close()

    passed = all(bool(item["passed"]) for item in checks)
    payload = {
        "schema": "smart-router.local-launch-path-smoke/v1",
        "evidence_scope": "local-process-with-synthetic-upstream-only",
        "not_evidence_of": ["credential-backed deployment", "public artifact promotion", "protected-environment settings", "human coverage or approval"],
        "generated_at": dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z"),
        "router_version": args.expected_version,
        "router_commit": args.expected_commit,
        "result": "passed" if passed else "failed",
        "checks": checks,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"local launch-path smoke {'passed' if passed else 'failed'}; safe evidence: {args.output}")
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
