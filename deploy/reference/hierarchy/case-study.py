#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Run a small synthetic CPU-tier case study against the hierarchy reference."""
from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
from pathlib import Path
import re
import secrets
import socket
import sqlite3
import subprocess
import tempfile
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import Request, urlopen

import yaml

ROOT = Path(__file__).resolve().parents[3]
REFERENCE_CONFIG = Path(__file__).with_name("config") / "dc.yaml"
CPU_MODEL = "Qwen/Qwen2.5-0.5B-Instruct"
ANTHROPIC_MODEL = "claude-haiku-5-5"
ANTHROPIC_INPUT_PER_MILLION_USD = 0.10
ANTHROPIC_OUTPUT_PER_MILLION_USD = 0.50


def percentile(values: list[float], fraction: float) -> float | None:
    if not values:
        return None
    values = sorted(values)
    return round(values[min(len(values) - 1, int((len(values) - 1) * fraction))], 2)


def make_task(index: int) -> tuple[str, str, str]:
    label = "invoice" if index % 2 == 0 else "receipt"
    if label == "invoice":
        content = f"Invoice INV-{9000 + index}. Northwind Supplies. Amount due ${17 + index}. Due date 2026-12-01."
    else:
        content = f"Point-of-sale receipt RC-{9000 + index}. Paid at Harbor Market with card. Total ${17 + index}."
    prompt = "Classify this synthetic accounting record. Reply with exactly invoice or receipt. Record: " + content
    return f"case-{index:04d}", label, prompt


def classify(text: str) -> str:
    match = re.search(r"\b(invoice|receipt)\b", text.lower())
    return match.group(1) if match else "unknown"


def request_json(url: str, payload: dict, token: str | None = None, timeout: float = 180) -> tuple[dict, int, int, str | None]:
    body = json.dumps(payload).encode()
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = Request(url, data=body, headers=headers)
    started = time.perf_counter()
    first_byte_ms: int | None = None
    try:
        with urlopen(request, timeout=timeout) as response:
            first_byte_ms = round((time.perf_counter() - started) * 1000)
            result = json.loads(response.read())
            total_ms = round((time.perf_counter() - started) * 1000)
            return result, first_byte_ms, total_ms, response.headers.get("X-Request-Id")
    except HTTPError as exc:
        raise RuntimeError(f"request returned HTTP {exc.code}") from None
    except (URLError, TimeoutError) as exc:
        raise RuntimeError(f"request failed ({type(exc).__name__})") from None


def usage_row(db_path: Path, request_id: str) -> tuple | None:
    with sqlite3.connect(db_path) as db:
        return db.execute(
            "select target_model,status,latency_ms,ttfb_ms,input_tokens,output_tokens,total_cost_usd "
            "from request_usage where request_id=?", (request_id,)
        ).fetchone()


def wait_usage(db_path: Path, request_id: str) -> tuple:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        row = usage_row(db_path, request_id)
        if row is not None:
            return row
        time.sleep(0.05)
    raise RuntimeError("router usage row did not arrive")


def summarize(rows: list[dict]) -> dict:
    targets = {}
    for name in ("cpu", "anthropic", "other"):
        sample = [row for row in rows if row["target"] == name]
        if sample:
            targets[name] = {
                "requests": len(sample),
                "quality_accuracy": round(sum(r["correct"] for r in sample) / len(sample), 4),
                "client_latency_ms_p50": percentile([r["client_latency_ms"] for r in sample], 0.50),
                "client_latency_ms_p95": percentile([r["client_latency_ms"] for r in sample], 0.95),
                "client_ttfb_ms_p50": percentile([r["client_ttfb_ms"] for r in sample], 0.50),
                "client_ttfb_ms_p95": percentile([r["client_ttfb_ms"] for r in sample], 0.95),
            }
    count = len(rows)
    return {
        "requests": count,
        "cpu_traffic_share": round(sum(r["target"] == "cpu" for r in rows) / count, 4) if count else 0,
        "quality_accuracy": round(sum(r["correct"] for r in rows) / count, 4) if count else 0,
        "client_latency_ms_p50": percentile([r["client_latency_ms"] for r in rows], 0.50),
        "client_latency_ms_p95": percentile([r["client_latency_ms"] for r in rows], 0.95),
        "client_ttfb_ms_p50": percentile([r["client_ttfb_ms"] for r in rows], 0.50),
        "client_ttfb_ms_p95": percentile([r["client_ttfb_ms"] for r in rows], 0.95),
        "by_target": targets,
    }


def measure_sizing(cpu_url: str) -> list[dict]:
    """Measure concurrent synthetic agent requests per physical CPU core."""
    prompt = "Classify this synthetic accounting record. Reply with exactly invoice or receipt. Record: Invoice INV-9012. Northwind Supplies. Amount due $29. Due date 2026-12-01."
    result = []
    for agents in (1, 2, 4, 8, 16):
        latencies: list[float] = []
        started = time.perf_counter()
        def call(_: int) -> float:
            _, _, total_ms, _ = request_json(
                cpu_url.rstrip("/") + "/chat/completions",
                {"model": CPU_MODEL, "max_tokens": 16, "messages": [{"role": "user", "content": prompt}]}, timeout=180,
            )
            return float(total_ms)
        with concurrent.futures.ThreadPoolExecutor(max_workers=agents) as pool:
            latencies = list(pool.map(call, range(agents * 2)))
        elapsed = time.perf_counter() - started
        result.append({
            "concurrent_agents": agents, "physical_cores": 8,
            "agents_per_core": round(agents / 8, 2),
            "requests": len(latencies), "requests_per_second": round(len(latencies) / elapsed, 3),
            "latency_ms_p50": percentile(latencies, 0.5), "latency_ms_p95": percentile(latencies, 0.95),
        })
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--router", type=Path, required=True)
    parser.add_argument("--routerctl", type=Path, required=True)
    parser.add_argument("--cpu-url", default="http://127.0.0.1:18002/v1")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--samples", type=int, default=100)
    args = parser.parse_args()
    if not os.environ.get("ANTHROPIC_API_KEY"):
        parser.error("ANTHROPIC_API_KEY must be available in the process environment")
    parsed = urlparse(args.cpu_url)
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path != "/v1"
        or parsed.query
        or parsed.fragment
    ):
        parser.error("--cpu-url must be an HTTP(S) OpenAI-compatible /v1 endpoint")
    if not 20 <= args.samples <= 120:
        parser.error("--samples must be between 20 and 120 to bound the case study")

    source = yaml.safe_load(REFERENCE_CONFIG.read_text())
    group = source["models"]["agent-small"]
    group["targets"][0]["provider"] = "cpu_small"
    group["targets"][0]["model_ref"] = "cpu-small"
    group["targets"][0]["timeout_ms"] = 15000
    group["targets"][1]["provider"] = "anthropic"
    group["targets"][1]["model_ref"] = "haiku"

    work_ctx = tempfile.TemporaryDirectory(prefix="router-v5-439-case-")
    try:
        state = Path(work_ctx.name)
        os.chmod(state, 0o700)
        token_path = state / "caller.token"
        generated = subprocess.run(
            [str(args.routerctl.resolve()), "callers", "generate", "--owner-user", "ops",
             "--project", "router-v5-439", "--env", "case-study", "--key", "cpu-study",
             "--allow", "agent-small", "--token-out", str(token_path)],
            cwd=ROOT, capture_output=True, text=True, check=False, timeout=30,
        )
        if generated.returncode != 0:
            raise RuntimeError("routerctl could not create a private case-study caller")
        os.chmod(token_path, 0o600)
        hashed = subprocess.run(
            [str(args.routerctl.resolve()), "callers", "hash", "--token-file", str(token_path)],
            cwd=ROOT, capture_output=True, text=True, check=False, timeout=30,
        )
        if hashed.returncode != 0:
            raise RuntimeError("routerctl could not hash the private case-study caller")
        token_info = json.loads(hashed.stdout)
        token = token_path.read_text().strip()
        token_path.unlink()

        port_socket = socket.socket()
        port_socket.bind(("127.0.0.1", 0))
        port = port_socket.getsockname()[1]
        port_socket.close()
        config = {
            "server": {
                "listen": f"127.0.0.1:{port}",
                "default_model_group": "agent-small",
                "identifiers": {"mode": "passthrough"},
                "cache": {"enabled": False},
                "logging": {"path": str(state / "router.jsonl"), "rotate_mb": 16, "keep": 1},
                "usage_db": {"enabled": True, "driver": "sqlite", "path": str(state / "usage.sqlite"), "migration_policy": "auto-safe"},
                "diagnostics": {"enabled": True, "retention_days": 1, "store_sanitized_upstream_errors": True, "max_error_bytes": 512},
                "upstream": {"timeout_ms": 60000, "max_response_bytes": 1048576},
                "admin_reports": {"enabled": False},
                "admin_auth": {"basic": {"enabled": False, "users": []}},
            },
            "admin": {"enabled": False},
            "state_path": str(state / "state.json"),
            "providers": {
                "cpu_small": {
                    "base_url": args.cpu_url, "dialect": "openai-chat", "auth_scheme": "bearer",
                    "api_key": "cpu-case-study-local", "key_id": "cpu-case-study",
                    "models": {"cpu-small": {"model": CPU_MODEL, "tier": "cpu", "input_modalities": ["text"], "output_modalities": ["text"]}},
                },
                "anthropic": {
                    "base_url": "https://api.anthropic.com", "dialect": "anthropic",
                    "api_key": "${ANTHROPIC_API_KEY}", "api_key_env": "ANTHROPIC_API_KEY", "key_id": "anthropic-case-study",
                    "models": {"haiku": {
                        "model": ANTHROPIC_MODEL, "tier": "frontier", "input_modalities": ["text"], "output_modalities": ["text"],
                        "input_price_per_million_usd": ANTHROPIC_INPUT_PER_MILLION_USD,
                        "output_price_per_million_usd": ANTHROPIC_OUTPUT_PER_MILLION_USD,
                        "pricing_source": "https://www.anthropic.com/claude-haiku-5-5", "pricing_updated_at": "2026-11-16",
                    }},
                },
            },
            "models": {"agent-small": group},
            "callers": [{
                "id": "cpu-case-study", "owner_user": "ops", "project": "router-v5-439", "environment": "case-study", "status": "active",
                "token_hash": token_info["token_hash"], "token_id": token_info["token_id"], "allow": ["agent-small"],
                "rate": {"rpm": 600, "tpm": 200000, "concurrent": 16},
                "quota": {"day": {"requests": 0, "tokens": 0}, "month": {"requests": 0, "tokens": 0}, "soft_pct": 80},
            }],
        }
        config_path = state / "config.yaml"
        config_path.write_text(yaml.safe_dump(config, sort_keys=False))
        os.chmod(config_path, 0o600)
        log = (state / "router.log").open("ab")
        process = subprocess.Popen([str(args.router.resolve()), "--config", str(config_path)], cwd=state, env=os.environ.copy(), stdout=log, stderr=subprocess.STDOUT)
        try:
            base = f"http://127.0.0.1:{port}"
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline:
                try:
                    with urlopen(base + "/readyz", timeout=1):
                        break
                except Exception:
                    if process.poll() is not None:
                        raise RuntimeError("router exited during case-study startup")
                    time.sleep(0.2)
            else:
                raise RuntimeError("router readiness timed out")

            rows: list[dict] = []
            for index in range(args.samples):
                task_id, expected, prompt = make_task(index)
                max_tokens = 160 if index % 5 == 0 else 16
                response, client_ttfb_ms, client_latency_ms, request_id = request_json(
                    base + "/v1/chat/completions",
                    {"model": "agent-small", "max_tokens": max_tokens,
                     "messages": [{"role": "user", "content": prompt}]}, token,
                )
                if client_ttfb_ms is not None and client_ttfb_ms > client_latency_ms:
                    raise RuntimeError("client TTFB exceeded total response time")
                if not request_id:
                    raise RuntimeError("router response omitted X-Request-Id")
                result_text = response.get("choices", [{}])[0].get("message", {}).get("content", "")
                usage = wait_usage(state / "usage.sqlite", request_id)
                model = str(usage[0] or "")
                target = "cpu" if model == CPU_MODEL else ("anthropic" if model == ANTHROPIC_MODEL else "other")
                rows.append({
                    "task_id": task_id, "expected": expected, "predicted": classify(result_text),
                    "correct": classify(result_text) == expected, "target": target, "status": int(usage[1]),
                    "client_latency_ms": client_latency_ms, "client_ttfb_ms": client_ttfb_ms,
                })

            report = {
                "date": "2026-11-16", "cpu_model": CPU_MODEL, "frontier_model": ANTHROPIC_MODEL,
                "sample_count": len(rows), "synthetic_workload": "balanced invoice/receipt classification; 20% request caps above CPU eligibility",
                "router_summary": summarize(rows), "router_rows": rows,
                "cpu_sizing": measure_sizing(args.cpu_url),
            }
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(json.dumps(report, indent=2) + "\n")
            # Costs come from usage rows at the configured Anthropic rates.
            with sqlite3.connect(state / "usage.sqlite") as db:
                cost = db.execute("select coalesce(sum(total_cost_usd),0),count(*) from request_usage").fetchone()
            print(json.dumps({"summary": report["router_summary"], "usage_rows": cost[1], "actual_usage_cost_usd": round(cost[0], 8)}, indent=2))
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            log.close()
    finally:
        work_ctx.cleanup()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
