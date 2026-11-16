#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Run the bounded, non-Docker nightly canary and emit a sanitized eval report."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.request
from datetime import datetime, timezone
from decimal import Decimal
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import direct_provider_canary as canary  # noqa: E402


PINNED_MODEL = "gpt-4o-mini-2024-07-18"
CURRENT_MODEL = "gpt-4o-mini"
PRICE = (0.15, 0.60)
MAX_NIGHTLY_USD = Decimal("1.00")
MAX_CALL_USD = Decimal("0.50")
SAFETY_FACTOR = Decimal("8")


class NightlyError(ValueError):
    pass


def preflight_upper_bound(probes: list[dict[str, Any]]) -> Decimal:
    """Reserve worst-case input/output cost for both sides and both model rows."""
    one_side = Decimal("0")
    for probe in probes:
        prompt = canary.probe_prompt(probe, "nightly-budget-probe")
        input_bound = 4 * len(prompt.encode("utf-8")) + 4096
        output_bound = int(probe["max_tokens"])
        one_side += (Decimal(input_bound) * Decimal(str(PRICE[0]))
                     + Decimal(output_bound) * Decimal(str(PRICE[1]))) / Decimal(1_000_000)
    return (one_side * 4 * SAFETY_FACTOR).quantize(Decimal("0.000001"))


def history_reserved(reports: list[Path], today: str) -> Decimal:
    total = Decimal("0")
    for path in reports:
        try:
            report = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise NightlyError("a prior same-day evaluation report is unreadable") from exc
        if not isinstance(report, dict) or not isinstance(report.get("nightly_cost"), dict):
            raise NightlyError("a prior same-day evaluation report has invalid cost metadata")
        cost = report["nightly_cost"]
        if cost.get("date") == today:
            try:
                reserved = Decimal(str(cost["reserved_usd"]))
                charged = Decimal(str(cost["charged_usd"]))
            except (KeyError, ArithmeticError, ValueError) as exc:
                raise NightlyError("a prior same-day evaluation report has invalid reservation metadata") from exc
            if (not reserved.is_finite() or reserved < 0 or not charged.is_finite() or charged < 0):
                raise NightlyError("a prior same-day evaluation report has invalid reservation metadata")
            total += max(reserved, charged)
    return total


def router_config(temp: Path, port: int, model: str, token: str) -> Path:
    import yaml

    cfg = {
        "server": {
            "listen": f"127.0.0.1:{port}",
            "identifiers": {"mode": "passthrough"},
            "default_model_group": "nightly-pinned",
            "cache": {"enabled": False},
            "usage_db": {"enabled": True, "driver": "sqlite", "path": str(temp / "usage.sqlite"), "migration_policy": "auto-safe"},
            "logging": {"path": str(temp / "requests.jsonl")},
        },
        "state_path": str(temp / "state.json"),
        "providers": {"openai": {"base_url": "https://api.openai.com/v1", "dialect": "openai-chat",
                                    "api_key": "${OPENAI_API_KEY}", "api_key_env": "OPENAI_API_KEY"}},
        "models": {"nightly-pinned": {"strategy": "static", "targets": [{
            "provider": "openai", "model": model,
            "input_price_per_million_usd": PRICE[0], "output_price_per_million_usd": PRICE[1],
            "pricing_source": "https://platform.openai.com/docs/pricing", "pricing_updated_at": "2026-11-16",
            "request_shape_support": {"supported_inbound_dialects": ["openai-chat"], "validation_status": "passed"},
        }]}},
        "callers": [{"id": "nightly-eval", "token_sha256": hashlib.sha256(token.encode()).hexdigest(),
                      "allow": ["nightly-pinned"], "rate": {"rpm": 60, "tpm": 100000, "concurrent": 2},
                      "quota": {"day": {"requests": 100, "tokens": 500000}, "month": {"tokens": 2000000}},
                      "key": {"lifetime_tokens": 2000000}}],
    }
    path = temp / "config.yaml"
    path.write_text(yaml.safe_dump(cfg, sort_keys=False), encoding="utf-8")
    return path


def free_port() -> int:
    import socket

    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def wait_ready(process: subprocess.Popen, port: int, timeout_s: int = 60) -> None:
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise NightlyError("router exited before readiness")
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/readyz", timeout=1) as response:
                if response.status == 200:
                    return
        except OSError:
            time.sleep(0.1)
    raise NightlyError("router readiness timed out")


def usage_rows_cost(db_path: Path) -> Decimal:
    if not db_path.exists():
        return Decimal("0")
    with sqlite3.connect(f"file:{db_path}?mode=ro", uri=True) as db:
        return sum((Decimal(str(row[0] or 0)) for row in db.execute(
            "SELECT total_cost_usd FROM request_usage WHERE total_cost_usd IS NOT NULL")), Decimal("0"))


def _run_one(model: str, kind: str, router_url: str, token: str, cap: Decimal, out: Path) -> None:
    env = dict(os.environ)
    env.update({
        "CANARY_DIRECT_BASE_URL": "https://api.openai.com/v1",
        "CANARY_DIRECT_API_KEY": os.environ["OPENAI_API_KEY"],
        "CANARY_DIRECT_MODEL": model,
        "CANARY_DIRECT_INPUT_PRICE_PER_MILLION_USD": str(PRICE[0]),
        "CANARY_DIRECT_OUTPUT_PRICE_PER_MILLION_USD": str(PRICE[1]),
        "CANARY_ROUTER_BASE_URL": router_url,
        "CANARY_ROUTER_API_KEY": token,
        "CANARY_ROUTER_MODEL_GROUP": "nightly-pinned",
        "CANARY_ROUTER_INPUT_PRICE_PER_MILLION_USD": str(PRICE[0]),
        "CANARY_ROUTER_OUTPUT_PRICE_PER_MILLION_USD": str(PRICE[1]),
        "CANARY_MAX_COST_USD": str(cap),
    })
    completed = subprocess.run([sys.executable, str(ROOT / "scripts/direct_provider_canary.py"), "run",
                                "--report-out", str(out), "--baseline-kind", kind],
                               cwd=ROOT, env=env, check=False, timeout=600,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if completed.returncode not in (0, 1) or not out.is_file():
        raise NightlyError("canary runner did not produce a report")


def run_nightly(args: argparse.Namespace) -> dict[str, Any]:
    key = os.environ.get("OPENAI_API_KEY", "").strip()
    token = os.environ.get("PARALLEL_LIVE_TOKEN", "").strip()
    if not key or not token:
        raise NightlyError("evaluation credentials are unavailable")
    probes = canary.load_probes()
    today = datetime.now(timezone.utc).strftime("%Y-%m-%d")
    reserved_today = history_reserved(args.history, today)
    reserved_this_run = preflight_upper_bound(probes)
    if reserved_this_run > MAX_NIGHTLY_USD or reserved_today + reserved_this_run > MAX_NIGHTLY_USD:
        raise NightlyError("preflight spend reservation exceeds the $1.00 per-run or per-night ceiling")

    with tempfile.TemporaryDirectory(prefix="router-eval-nightly-") as temp_text:
        temp = Path(temp_text)
        binary = temp / "router"
        subprocess.run(["go", "build", "-tags", "dev_no_license", "-o", str(binary), "./cmd/metrum-ai-router"],
                       cwd=ROOT, check=True, timeout=600, stdout=subprocess.DEVNULL)
        port = free_port()
        pinned_cfg = router_config(temp, port, PINNED_MODEL, token)
        log = (temp / "router.log").open("w", encoding="utf-8")
        process = subprocess.Popen([str(binary), "-config", str(pinned_cfg)], cwd=temp,
                                   env=dict(os.environ), stdout=log, stderr=log)
        try:
            wait_ready(process, port)
            router_url = f"http://127.0.0.1:{port}/v1"
            pinned_path, stable_path = temp / "pinned.json", temp / "stable.json"
            _run_one(PINNED_MODEL, "pinned", router_url, token, MAX_CALL_USD, pinned_path)
            # Reconfigure to the current-stable alias while retaining the same
            # pinned baseline report; each side is still bounded by preflight.
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)
            pinned_cfg = router_config(temp, port, CURRENT_MODEL, token)
            process = subprocess.Popen([str(binary), "-config", str(pinned_cfg)], cwd=temp,
                                       env=dict(os.environ), stdout=log, stderr=log)
            wait_ready(process, port)
            _run_one(CURRENT_MODEL, "current-stable", router_url, token, MAX_CALL_USD, stable_path)
            merged = temp / "merged.json"
            merge_result = subprocess.run([sys.executable, str(ROOT / "scripts/direct_provider_canary.py"), "merge",
                                           "--pinned", str(pinned_path), "--current-stable", str(stable_path),
                                           "--report-out", str(merged)], cwd=ROOT, check=False, timeout=60,
                                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if merge_result.returncode not in (0, 1) or not merged.is_file():
                raise NightlyError("canary merge did not produce a report")
            report = json.loads(merged.read_text(encoding="utf-8"))
            router_cost = usage_rows_cost(temp / "usage.sqlite")
            direct_cost = sum(Decimal(str(task["cost_usd"]))
                              for cell in report["cells"] if "direct" in cell["cell_id"]
                              for task in cell["task_results"])
            report["nightly_cost"] = {
                "date": today,
                "ceiling_usd": float(MAX_NIGHTLY_USD),
                "reserved_usd": float(reserved_this_run),
                "reserved_before_today_usd": float(reserved_today),
                "router_usage_rows_usd": float(router_cost),
                "direct_provider_usage_estimate_usd": float(direct_cost),
                "charged_usd": float(router_cost + direct_cost),
                "source": "router usage rows plus direct provider response usage at pinned list rates",
                "direct_cost_reconciled": False,
            }
            if report["nightly_cost"]["charged_usd"] > float(MAX_NIGHTLY_USD):
                report["gate_violations"] = ["observed charges exceed the $1.00 per-run/night ceiling"]
            return report
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
            log.close()


def failure_report(reason: str, reserved_usd: Decimal = Decimal("0")) -> dict[str, Any]:
    probes = canary.load_probes()
    now = datetime.now(timezone.utc)
    started = now.strftime("%Y-%m-%dT%H:%M:%SZ")
    common = {"client_versions": {"direct_provider_canary": "v1", "python": sys.version.split()[0]},
              "seed": int(now.timestamp())}
    specifications = [
        ("canary-direct", "direct", {**common, "baseline_kind": "pinned"}),
        ("canary-router-pinned", "fixed-router", {**common, "baseline_kind": "pinned"}),
        ("canary-direct-current-stable", "direct", {**common, "baseline_kind": "current-stable", "baseline_of": "canary-direct"}),
        ("canary-router-pinned-current-stable", "fixed-router", {**common, "baseline_kind": "current-stable", "baseline_of": "canary-router-pinned"}),
    ]
    cells = [{
        "cell_id": cell_id, "mode": mode, "attempts": len(probes), "passes": 0,
        "observed_error_rate": 1.0, "provenance": provenance,
        "task_results": [{"task_id": probe["id"], "outcome": "error", "time_to_first_event_ms": 0,
                          "completion_latency_ms": 0, "tool_turns": 0, "cost_usd": 0, "detail": reason}
                         for probe in probes],
    } for cell_id, mode, provenance in specifications]
    now_date = now.strftime("%Y-%m-%d")
    return {
        "schema_version": 1, "run_id": "canary-failed-" + now.strftime("%Y%m%dT%H%M%SZ"),
        "started_utc": started, "cells": cells,
        "canary": {"probes": len(probes), "classification": {"both_pass": 0, "both_fail": 0, "unavailable": len(probes),
                    "router_regression": 0, "provider_only_fail": 0}, "allowed_router_regressions": 0,
                    "allowed_error_rate": canary.ALLOWED_ERROR_RATE, "cost_usd_total": 0,
                    "cost_basis": "no provider response was recorded"},
        "nightly_status": {"status": "failed", "reason": reason},
        "nightly_cost": {"date": now_date, "ceiling_usd": float(MAX_NIGHTLY_USD), "reserved_usd": float(reserved_usd),
                         "reserved_before_today_usd": 0, "router_usage_rows_usd": 0,
                         "direct_provider_usage_estimate_usd": 0, "charged_usd": 0,
                         "charge_status": "unknown_on_failure",
                         "source": "failure-path accounting unavailable; full preflight reservation retained",
                         "direct_cost_reconciled": False},
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--history", type=Path, nargs="*", default=[])
    args = parser.parse_args()
    try:
        report = run_nightly(args)
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    except (NightlyError, canary.CanaryError, subprocess.SubprocessError, OSError, sqlite3.Error, ImportError, TimeoutError):
        report = failure_report("nightly-runner-failed", preflight_upper_bound(canary.load_probes()))
        report["gate_violations"] = canary.validate_report(report)
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print("nightly evaluation failed; sanitized failure report written", file=sys.stderr)
        return 1
    errors = canary.validate_report(report)
    if errors:
        report["gate_violations"] = sorted(set(report.get("gate_violations", []) + errors))
        args.out.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(f"nightly report has {len(errors)} gate violation(s)", file=sys.stderr)
        return 1
    if report.get("gate_violations"):
        args.out.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(f"nightly report has {len(report['gate_violations'])} gate violation(s)", file=sys.stderr)
        return 1
    print(f"report={args.out} cells={len(report['cells'])} reserved_usd={report['nightly_cost']['reserved_usd']:.6f} "
          f"charged_usd={report['nightly_cost']['charged_usd']:.6f}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
