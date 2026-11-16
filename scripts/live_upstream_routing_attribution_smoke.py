#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Live upstream + local-router smoke for routing attribution.

Loads provider keys from ignored env.json (tolerant of a trailing comma), runs
direct OpenAI-compatible text smokes against configured providers, then starts a
local router failover group with an intentionally broken primary and a real
fallback. Asserts the parent usage row attributes the serving target after
fallback.

Safe scalar evidence only: never prints provider keys, tokens, or prompts.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import socket
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from caller_token import verifier

ROOT = Path(__file__).resolve().parents[1]


class FlakyPrimaryHandler(BaseHTTPRequestHandler):
    """Always returns a retryable upstream failure so failover can continue."""

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        body = b'{"error":{"message":"synthetic primary unavailable","type":"server_error"}}'
        self.send_response(503)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format: str, *_args: object) -> None:
        return


def load_env_json(path: Path) -> dict[str, str]:
    raw = path.read_text(encoding="utf-8")
    cleaned = re.sub(r",\s*}", "}", raw)
    data = json.loads(cleaned)
    if not isinstance(data, dict):
        raise SystemExit(f"{path}: root must be an object")
    out: dict[str, str] = {}
    for key, value in data.items():
        if not isinstance(key, str) or not isinstance(value, str):
            continue
        out[key] = value
    return out


def export_env(env: dict[str, str]) -> None:
    for key, value in env.items():
        if key not in os.environ:
            os.environ[key] = value


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def post_json(url: str, payload: dict, *, headers: dict[str, str] | None = None, timeout: float = 60.0) -> tuple[int, dict | str]:
    body = json.dumps(payload).encode()
    req_headers = {"Content-Type": "application/json", "Accept": "application/json"}
    if headers:
        req_headers.update(headers)
    req = urllib.request.Request(url, data=body, headers=req_headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            try:
                return resp.status, json.loads(raw)
            except json.JSONDecodeError:
                return resp.status, raw.decode("utf-8", errors="replace")[:200]
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        try:
            return exc.code, json.loads(raw)
        except Exception:
            return exc.code, raw.decode("utf-8", errors="replace")[:200]
    except Exception as exc:  # noqa: BLE001
        return 0, f"{type(exc).__name__}"


def assistant_text(body: dict | str) -> str:
    if not isinstance(body, dict):
        return ""
    choices = body.get("choices") or []
    if not choices:
        return ""
    message = choices[0].get("message") or {}
    content = message.get("content")
    return content if isinstance(content, str) else ""


def direct_chat_smoke(name: str, base_url: str, api_key: str, model: str, *, max_tokens: int = 16) -> dict:
    status, body = post_json(
        f"{base_url.rstrip('/')}/chat/completions",
        {
            "model": model,
            "messages": [{"role": "user", "content": "Reply OK only."}],
            "max_tokens": max_tokens,
            "stream": False,
        },
        headers={"Authorization": f"Bearer {api_key}"},
        timeout=90.0,
    )
    text = assistant_text(body)
    usage = body.get("usage") if isinstance(body, dict) else {}
    # Reasoning-heavy models can return HTTP 200 with empty final content when the
    # completion budget is spent on reasoning. Count those as limited, not hard fail.
    ok = status == 200 and (bool(text.strip()) or int((usage or {}).get("completion_tokens") or 0) > 0)
    return {
        "check": f"direct:{name}",
        "passed": ok,
        "http_status": status,
        "model": model,
        "has_assistant_text": bool(text.strip()),
        "prompt_tokens": (usage or {}).get("prompt_tokens"),
        "completion_tokens": (usage or {}).get("completion_tokens"),
        "error_class": None if ok else ("http_error" if status else "transport_error"),
        "limited_empty_content": bool(status == 200 and not text.strip()),
    }


def wait_ready(base: str, process: subprocess.Popen, stderr_path: Path, timeout_s: float = 30.0) -> None:
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        if process.poll() is not None:
            diagnostic = stderr_path.read_text(encoding="utf-8", errors="replace")[-1500:]
            raise RuntimeError(f"router exited before readiness: {diagnostic}")
        try:
            req = urllib.request.Request(base + "/readyz", method="GET")
            with urllib.request.urlopen(req, timeout=1.0) as resp:
                if resp.status == 200:
                    return
        except Exception:
            time.sleep(0.1)
            continue
        time.sleep(0.1)
    raise RuntimeError("router did not become ready")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-json", type=Path, default=ROOT / "env.json")
    parser.add_argument("--router-bin", type=Path, default=None, help="Optional prebuilt router binary; default uses go run")
    parser.add_argument("--out", type=Path, default=ROOT / "tmp" / "live-upstream-routing-attribution.json")
    args = parser.parse_args()

    if not args.env_json.is_file():
        raise SystemExit(f"missing {args.env_json}")

    env = load_env_json(args.env_json)
    export_env(env)

    results: list[dict] = []
    required = {
        "baseten": ("BASETEN_API_KEY", "https://inference.baseten.co/v1", "openai/gpt-oss-120b", 256),
        "minimax": ("MINIMAX_API_KEY", "https://api.minimax.io/v1", "MiniMax-M3", 32),
        "openai": ("OPENAI_API_KEY", "https://api.openai.com/v1", "gpt-4o-mini", 16),
    }
    for name, (key_name, base_url, model, max_tokens) in required.items():
        api_key = env.get(key_name, "").strip()
        if not api_key:
            results.append({"check": f"direct:{name}", "passed": False, "skipped": True, "reason": f"{key_name} empty"})
            continue
        results.append(direct_chat_smoke(name, base_url, api_key, model, max_tokens=max_tokens))

    direct_pass = [r for r in results if r.get("check", "").startswith("direct:") and r.get("passed")]
    if len(direct_pass) < 1:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps({"passed": False, "checks": results}, indent=2) + "\n", encoding="utf-8")
        print(f"FAIL no direct upstream smokes passed; wrote {args.out}")
        return 1

    # Prefer a provider that returned visible assistant text for the live fallback.
    text_ok = [r for r in results if r.get("check", "").startswith("direct:") and r.get("passed") and r.get("has_assistant_text")]
    fallback_provider = None
    for preferred in ("baseten", "minimax", "openai"):
        if any(r.get("check") == f"direct:{preferred}" and r.get("passed") and r.get("has_assistant_text") for r in text_ok):
            fallback_provider = preferred
            break
    if fallback_provider is None:
        for preferred in ("baseten", "minimax", "openai"):
            if any(r.get("check") == f"direct:{preferred}" and r.get("passed") for r in results):
                fallback_provider = preferred
                break
    if fallback_provider is None:
        print("FAIL no usable fallback provider after direct smokes")
        return 1

    token = "rtr_metrum_live_attr_local_k1_attributionsecret"
    caller_hash, caller_token_id = verifier(token)
    router_port = free_port()
    primary = ThreadingHTTPServer(("127.0.0.1", 0), FlakyPrimaryHandler)
    threading.Thread(target=primary.serve_forever, daemon=True).start()
    with tempfile.TemporaryDirectory(prefix="live-upstream-attr-") as temp_raw:
        temp = Path(temp_raw)
        usage_db = temp / "usage.sqlite"
        # Clean env beside config so Go's strict JSON loader succeeds.
        (temp / "env.json").write_text(json.dumps(env, indent=2) + "\n", encoding="utf-8")

        providers: dict = {
            "broken_primary": {
                "base_url": f"http://127.0.0.1:{primary.server_port}/v1",
                "dialect": "openai-chat",
                "api_key": "synthetic-primary-key",
            }
        }
        targets = [
            {
                "provider": "broken_primary",
                "model": "primary-unavailable",
                "input_price_per_million_usd": 9.0,
                "output_price_per_million_usd": 18.0,
                "pricing_source": "test-primary",
                "pricing_updated_at": "2026-09-08",
            }
        ]

        if fallback_provider == "baseten":
            providers["baseten"] = {
                "base_url": "https://inference.baseten.co/v1",
                "dialect": "openai-chat",
                "api_key": "${BASETEN_API_KEY}",
                "api_key_env": "BASETEN_API_KEY",
            }
            targets.append(
                {
                    "provider": "baseten",
                    "model": "openai/gpt-oss-120b",
                    "input_price_per_million_usd": 0.1,
                    "output_price_per_million_usd": 0.5,
                    "pricing_source": "test-fallback-baseten",
                    "pricing_updated_at": "2026-09-08",
                }
            )
            expected_model = "openai/gpt-oss-120b"
            expected_provider = "baseten"
            expected_pricing = "test-fallback-baseten"
        elif fallback_provider == "minimax":
            providers["minimax"] = {
                "base_url": "https://api.minimax.io/v1",
                "dialect": "openai-chat",
                "auth_scheme": "bearer",
                "api_key": "${MINIMAX_API_KEY}",
                "api_key_env": "MINIMAX_API_KEY",
            }
            targets.append(
                {
                    "provider": "minimax",
                    "model": "MiniMax-M3",
                    "input_price_per_million_usd": 0.3,
                    "output_price_per_million_usd": 1.2,
                    "pricing_source": "test-fallback-minimax",
                    "pricing_updated_at": "2026-09-08",
                }
            )
            expected_model = "MiniMax-M3"
            expected_provider = "minimax"
            expected_pricing = "test-fallback-minimax"
        else:
            providers["openai"] = {
                "base_url": "https://api.openai.com/v1",
                "dialect": "openai-chat",
                "api_key": "${OPENAI_API_KEY}",
                "api_key_env": "OPENAI_API_KEY",
            }
            targets.append(
                {
                    "provider": "openai",
                    "model": "gpt-4o-mini",
                    "input_price_per_million_usd": 0.15,
                    "output_price_per_million_usd": 0.6,
                    "pricing_source": "test-fallback-openai",
                    "pricing_updated_at": "2026-09-08",
                }
            )
            expected_model = "gpt-4o-mini"
            expected_provider = "openai"
            expected_pricing = "test-fallback-openai"

        config = {
            "server": {
                "listen": f"127.0.0.1:{router_port}",
                "default_model_group": "attr-failover",
                "license": {"enabled": False, "recheck_interval": "1h"},
                "cache": {"enabled": True, "max_bytes": 1048576, "default_ttl": "5m"},
                "diagnostics": {"enabled": True},
                "logging": {"path": str(temp / "requests.jsonl")},
                "usage_db": {
                    "enabled": True,
                    "driver": "sqlite",
                    "path": str(usage_db),
                    "migration_policy": "auto-safe",
                },
            },
            "state_path": str(temp / "state.json"),
            "providers": providers,
            "models": {
                "attr-failover": {
                    "strategy": "failover",
                    "targets": targets,
                }
            },
            "callers": [
                {
                    "id": "live-attr",
                    "user": "live-attr",
                    "project": "live-attr",
                    "environment": "local",
                    "status": "active",
                    "token_hash": caller_hash,
                    "token_id": caller_token_id,
                    "allow": ["attr-failover"],
                    "rate": {"rpm": 60, "tpm": 200000, "concurrent": 4},
                    "quota": {
                        "day": {"requests": 100, "tokens": 200000},
                        "month": {"tokens": 2000000},
                        "soft_pct": 80,
                    },
                    "key": {"lifetime_tokens": 2000000, "soft_pct": 90, "on_exhaust": "disable"},
                }
            ],
        }
        config_path = temp / "config.yaml"
        import yaml  # type: ignore

        config_path.write_text(yaml.safe_dump(config, sort_keys=False), encoding="utf-8")

        stderr_path = temp / "router.stderr"
        # Normal release builds require a license; this smoke uses the internal
        # dev_no_license tag only for local evidence, matching api-compat tests.
        if args.router_bin:
            cmd = [str(args.router_bin.resolve()), "--config", str(config_path)]
        else:
            bin_path = temp / "router-dev"
            build = subprocess.run(
                ["go", "build", "-tags", "dev_no_license", "-o", str(bin_path), "./cmd/metrum-ai-router"],
                cwd=str(ROOT),
                capture_output=True,
                text=True,
                check=False,
            )
            if build.returncode != 0:
                raise RuntimeError(f"go build failed: {build.stderr[-1500:]}")
            cmd = [str(bin_path), "--config", str(config_path)]
        with stderr_path.open("wb") as stderr:
            process = subprocess.Popen(cmd, cwd=str(ROOT), stdout=subprocess.DEVNULL, stderr=stderr, env=os.environ.copy())
        try:
            base = f"http://127.0.0.1:{router_port}"
            wait_ready(base, process, stderr_path, timeout_s=90.0)
            router_max_tokens = 256 if expected_provider == "baseten" else 32
            status, body = post_json(
                base + "/v1/chat/completions",
                {
                    "model": "attr-failover",
                    "messages": [{"role": "user", "content": "Reply OK only."}],
                    "max_tokens": router_max_tokens,
                    "stream": False,
                },
                headers={"Authorization": f"Bearer {token}"},
                timeout=120.0,
            )
            text = assistant_text(body)
            usage = body.get("usage") if isinstance(body, dict) else {}
            chat_ok = status == 200 and (
                bool(text.strip()) or int((usage or {}).get("completion_tokens") or (usage or {}).get("output_tokens") or 0) > 0
            )
            results.append(
                {
                    "check": "router:failover-chat",
                    "passed": chat_ok,
                    "http_status": status,
                    "has_assistant_text": bool(text.strip()),
                    "fallback_provider": expected_provider,
                    "max_tokens": router_max_tokens,
                }
            )
            # Give usage writer a moment.
            time.sleep(0.5)
            if not usage_db.is_file():
                results.append({"check": "router:usage-attribution", "passed": False, "reason": "usage db missing"})
            else:
                conn = sqlite3.connect(usage_db)
                try:
                    row = conn.execute(
                        "SELECT target_provider, target_model, pricing_source, fallback_used, attempts, total_cost_usd, status FROM request_usage ORDER BY ts DESC LIMIT 1"
                    ).fetchone()
                    attempts = conn.execute(
                        "SELECT provider, model, selected, status_code FROM request_attempts ORDER BY attempt_index ASC"
                    ).fetchall()
                finally:
                    conn.close()
                if not row:
                    results.append({"check": "router:usage-attribution", "passed": False, "reason": "no usage row"})
                else:
                    provider, model, pricing_source, fallback_used, attempt_count, total_cost, row_status = row
                    attr_ok = (
                        provider == expected_provider
                        and model == expected_model
                        and pricing_source == expected_pricing
                        and bool(fallback_used)
                        and int(attempt_count) >= 2
                        and int(row_status) == 200
                        and float(total_cost or 0) >= 0
                    )
                    selected = [a for a in attempts if a[2]]
                    results.append(
                        {
                            "check": "router:usage-attribution",
                            "passed": attr_ok,
                            "target_provider": provider,
                            "target_model": model,
                            "pricing_source": pricing_source,
                            "fallback_used": bool(fallback_used),
                            "attempts": attempt_count,
                            "selected_attempts": [
                                {"provider": a[0], "model": a[1], "status_code": a[3]} for a in selected
                            ],
                            "attempt_count_rows": len(attempts),
                        }
                    )
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
            primary.shutdown()

    passed = all(r.get("passed") for r in results if not r.get("skipped"))
    # Require the attribution check specifically.
    attribution = next((r for r in results if r.get("check") == "router:usage-attribution"), None)
    if attribution is None or not attribution.get("passed"):
        passed = False

    payload = {
        "passed": passed,
        "fallback_provider": fallback_provider,
        "checks": results,
    }
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    for row in results:
        mark = "PASS" if row.get("passed") else ("SKIP" if row.get("skipped") else "FAIL")
        print(f"{mark}  {row.get('check')}  http={row.get('http_status')}  { {k:v for k,v in row.items() if k not in ('check','passed','skipped')} }")
    print()
    print(f"{'PASS' if passed else 'FAIL'} wrote {args.out}")
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
