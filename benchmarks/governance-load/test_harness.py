#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline tests for benchmarks/governance-load/harness.py.

Covers the pure parts: DSN parsing that keeps credentials off command lines,
budget/cost arithmetic, percentile selection, quota state reading, config
rendering (including that a Postgres DSN is referenced by environment variable,
never inlined), and the markdown renderer. It builds nothing and runs no load.
"""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "harness.py"
_spec = importlib.util.spec_from_file_location("governance_load_harness", SCRIPT)
_mod = importlib.util.module_from_spec(_spec)
# dataclasses resolves annotations through sys.modules, so register first.
sys.modules[_spec.name] = _mod
_spec.loader.exec_module(_mod)


def expect(name: str, condition: bool, detail: str = "") -> int:
    if condition:
        return 0
    print(f"FAIL {name}{': ' + detail if detail else ''}")
    return 1


def main() -> int:
    failures = 0

    env = _mod.parse_dsn("postgres://user%40corp:p%40ss%3Aword@db.internal:5433/router?sslmode=disable")
    failures += expect("dsn host", env.get("PGHOST") == "db.internal", str(env))
    failures += expect("dsn port", env.get("PGPORT") == "5433", str(env))
    failures += expect("dsn user decoded once", env.get("PGUSER") == "user@corp", str(env))
    failures += expect("dsn password decoded once", env.get("PGPASSWORD") == "p@ss:word", str(env))
    failures += expect("dsn database", env.get("PGDATABASE") == "router", str(env))
    failures += expect("dsn never lands in a generic variable", set(env) <= {
        "PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE"
    }, str(env))
    for bad in ("https://example.com/db", "sqlite:///tmp/x.db", ""):
        try:
            _mod.parse_dsn(bad)
        except ValueError:
            continue
        failures += expect(f"dsn scheme rejected: {bad!r}", False)

    cost = _mod.request_cost_usd(1_000_000, 0, 0.15, 0.60)
    failures += expect("cost input side", abs(cost - 0.15) < 1e-12, str(cost))
    cost = _mod.request_cost_usd(0, 1_000_000, 0.15, 0.60)
    failures += expect("cost output side", abs(cost - 0.60) < 1e-12, str(cost))
    tokens = _mod.tokens_for_budget(0.01, 0.15, 0.60)
    failures += expect("token budget is a whole number of requests", tokens % 20 == 0, str(tokens))
    failures += expect("token budget is positive", tokens > 0, str(tokens))
    failures += expect("zero budget yields no tokens", _mod.tokens_for_budget(0.0, 0.15, 0.60) == 0)

    failures += expect("percentile of empty set", _mod.percentile([], 0.99) is None)
    failures += expect("percentile p50", _mod.percentile([1, 2, 3, 4], 0.5) == 2, str(_mod.percentile([1, 2, 3, 4], 0.5)))
    failures += expect("percentile p99 upper bound", _mod.percentile([5], 0.99) == 5)

    with tempfile.TemporaryDirectory() as tmp:
        work = Path(tmp)
        state = work / "state.json"
        state.write_text(json.dumps({"schema_version": 1, "state": {"callers": {"a": {"day_tokens": 41}}}}))
        failures += expect("state reader", _mod.read_state_day_tokens(state, "a") == 41)
        failures += expect("state reader unknown caller", _mod.read_state_day_tokens(state, "b") is None)
        broken = work / "broken.json"
        broken.write_text("{not json")
        failures += expect("state reader corrupt file", _mod.read_state_day_tokens(broken, "a") is None)
        failures += expect("state reader missing file", _mod.read_state_day_tokens(work / "nope.json", "a") is None)

        caller = _mod.caller_entry("harness-caller", "pbkdf2-sha3-512:i=1:s:k", "rtr_x", day_tokens=100)
        sqlite_config = _mod.render_config(
            port=1234,
            log_path=work / "requests.jsonl",
            state_path=work / "router-state.json",
            backend="sqlite",
            sqlite_path=work / "usage.sqlite",
            dsn_env="ROUTER_USAGE_DB_DSN",
            providers=[{"name": "mock", "base_url": "http://127.0.0.1:9/v1", "model": "m"}],
            targets="      - {provider: mock, model_ref: m}\n",
            group_extra="",
            callers=[caller],
        )
        failures += expect("sqlite config names the path", str(work / "usage.sqlite") in sqlite_config)
        failures += expect("sqlite config does not read a DSN", "ROUTER_USAGE_DB_DSN" not in sqlite_config)
        postgres_config = _mod.render_config(
            port=1234,
            log_path=work / "requests.jsonl",
            state_path=work / "router-state.json",
            backend="postgres",
            sqlite_path=None,
            dsn_env="ROUTER_USAGE_DB_DSN",
            providers=[{"name": "mock", "base_url": "http://127.0.0.1:9/v1", "model": "m"}],
            targets="      - {provider: mock, model_ref: m}\n",
            group_extra="",
            callers=[caller],
        )
        failures += expect("postgres config references the DSN environment variable",
                           "${ROUTER_USAGE_DB_DSN}" in postgres_config, postgres_config[-300:])
        failures += expect("postgres config carries no DSN", "postgres://" not in postgres_config)
        failures += expect("postgres config uses the deployment-job migration gate",
                           "deployment-job" in postgres_config)
        failures += expect("config carries the caller hash", "pbkdf2-sha3-512:i=1:s:k" in sqlite_config)
        failures += expect("config keeps the admin hash behind an env var",
                           "HARNESS_ADMIN_PASSWORD_HASH" in sqlite_config and "$2a$" not in sqlite_config)

        report = {
            "schema": _mod.SCHEMA,
            "generated_at": "2026-11-02T00:00:00Z",
            "environment": {
                "cpu_model": "test",
                "visible_cpus": 2,
                "kernel": "test",
                "machine": "x86_64",
                "python": "3.12",
                "router_commit": "deadbeef",
                "backend": "sqlite",
                "loopback_only": True,
                "hosted_model_calls": 0,
            },
            "scenarios": [{"scenario": "auth", "statuses": {"valid": 200}, "pipe": "a|b"}],
            "checks": [{"name": "no raw pipes", "ok": False, "detail": "x|y"}],
            "checks_failed": 1,
            "limits": ["bounded"],
        }
        markdown = _mod.render_markdown(report)
        failures += expect("markdown shows the failed check", "| no raw pipes | FAIL |" in markdown)
        failures += expect("markdown escapes pipes in detail", "x\\|y" in markdown)
        failures += expect("markdown preserves scenario values", "- `pipe`: a|b" in markdown)
        failures += expect("markdown carries the commit", "deadbeef" in markdown)

    failures += expect("scenario inventory", set(_mod.SCENARIOS) == set(_mod.SCENARIO_FUNCS))

    if failures:
        print(f"{failures} governance-load harness test(s) failed")
        return 1
    print("ok: governance-load harness pure functions and renderers behave")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
