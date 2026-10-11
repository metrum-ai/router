#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Live per-turn view of external-policy (d3) routing decisions for demos (issue #278).

Tails a Metrum AI Router SQLite usage DB (read-only) and prints one line per
routed request in a group, joining `request_usage` with
`request_policy_executions`:

    turn 3 │ d3 hard·code·r0.82      │ → strong  gpt-6-luna          │  1.9 s │ $0.00041

Only scalar columns are read (class label, served model, latency, cost). The
class label is built by the d3 adapter from config keys, so the panel never
sees request content.

Usage:
    python3 scripts/d3_demo_panel.py --db tmp/test-demo/usage.sqlite --since now \
        --tier deepseek=cheap --tier gpt-6.1-sol=strong
    python3 scripts/d3_demo_panel.py --db ... --since 2026-10-11T15:00:00Z --summary

`--summary` prints the turn count, tier split, total cost and an all-strong
estimate: turns that went strong keep their recorded cost, and every other
turn's tokens are priced at the strong target's list price (input, cached
input and output, as recorded on a strong usage row or given with
--strong-price IN/CACHED/OUT). The second line compares the two (issue #287).

Copyright 2026 Metrum AI.
"""
from __future__ import annotations

import argparse
import datetime as dt
import sqlite3
import statistics
import sys
import time
from pathlib import Path

QUERY = """
SELECT u.request_id, u.ts, u.target_provider, u.target_model, u.status, u.latency_ms,
       u.total_cost_usd, u.input_tokens, u.output_tokens,
       u.input_price_per_million_usd, u.output_price_per_million_usd,
       u.cached_input_tokens, u.cached_input_price_per_million_usd,
       p.class_label, p.outcome, p.duration_ms
FROM request_usage u
LEFT JOIN request_policy_executions p ON p.request_id = u.request_id
WHERE u.resolved_group = ? AND u.ts > ?
ORDER BY u.ts, u.request_id
"""

COLORS = {"cheap": "\x1b[1;32m", "strong": "\x1b[1;35m"}
DIM, BOLD, RESET = "\x1b[2m", "\x1b[1m", "\x1b[0m"
LABEL_WAIT_S = 3.0


def utc_now_ts() -> str:
    """Router usage timestamps are RFC 3339 UTC with nanoseconds; match their shape."""
    return dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f000Z")


def parse_tiers(pairs: list[str]) -> list[tuple[str, str]]:
    out = []
    for pair in pairs:
        needle, sep, tier = pair.partition("=")
        if not sep or not needle or not tier:
            raise SystemExit(f"--tier wants SUBSTRING=TIER, got {pair!r}")
        out.append((needle, tier))
    return out


def tier_for(model: str, tiers: list[tuple[str, str]]) -> str:
    for needle, tier in tiers:
        if needle in (model or ""):
            return tier
    return "?"


def short_model(model: str) -> str:
    return (model or "-").rsplit("/", 1)[-1]


def label_parts(label: str | None) -> str:
    """`d3:hard:code:r0.82` -> `hard·code·r0.82`; other labels pass through."""
    if not label:
        return "-"
    parts = label.split(":")
    if parts[0] == "d3" and len(parts) > 1:
        return "·".join(parts[1:])
    return label


def fetch(db: Path, group: str, since: str) -> list[sqlite3.Row]:
    conn = sqlite3.connect(f"file:{db}?mode=ro", uri=True, timeout=2)
    conn.row_factory = sqlite3.Row
    try:
        return conn.execute(QUERY, (group, since)).fetchall()
    finally:
        conn.close()


def format_row(n: int, row: sqlite3.Row, tiers: list[tuple[str, str]], color: bool = True) -> str:
    tier = tier_for(row["target_model"], tiers)
    c = COLORS.get(tier, "") if color else ""
    r = RESET if color and c else ""
    d = DIM if color else ""
    dr = RESET if color else ""
    status = "" if row["status"] == 200 else f" │ HTTP {row['status']}"
    latency = (row["latency_ms"] or 0) / 1000.0
    cost = row["total_cost_usd"] or 0.0
    return (
        f"turn {n:<2}{d} │{dr} d3 {label_parts(row['class_label']):<22}{d} │{dr} "
        f"→ {c}{tier:<6}{r}  {short_model(row['target_model']):<20}{d} │{dr} "
        f"{latency:4.1f} s{d} │{dr} ${cost:.5f}{status}"
    )


def parse_price(text: str | None) -> tuple[float, float, float] | None:
    """`2/0.1/10` -> (input, cached input, output) USD per 1M tokens."""
    if not text:
        return None
    parts = text.split("/")
    try:
        values = tuple(float(p) for p in parts)
    except ValueError:
        values = ()
    if len(values) != 3:
        raise SystemExit(f"--strong-price wants IN/CACHED/OUT, got {text!r}")
    return values  # type: ignore[return-value]


def strong_estimate(row: sqlite3.Row, price: tuple[float, float, float]) -> float:
    """This turn's tokens at the strong list price; cached input at the cached rate."""
    cached = min(row["cached_input_tokens"] or 0, row["input_tokens"] or 0)
    fresh = (row["input_tokens"] or 0) - cached
    return (fresh * price[0] + cached * price[1] + (row["output_tokens"] or 0) * price[2]) / 1e6


def summarize(
    rows: list[sqlite3.Row],
    tiers: list[tuple[str, str]],
    strong_price: tuple[float, float, float] | None = None,
) -> dict:
    by_tier: dict[str, int] = {}
    for row in rows:
        tier = tier_for(row["target_model"], tiers)
        by_tier[tier] = by_tier.get(tier, 0) + 1
        if tier == "strong" and row["input_price_per_million_usd"] is not None:
            cached_price = row["cached_input_price_per_million_usd"]
            strong_price = (
                row["input_price_per_million_usd"],
                row["input_price_per_million_usd"] if cached_price is None else cached_price,
                row["output_price_per_million_usd"] or 0.0,
            )
    total = sum((r["total_cost_usd"] or 0.0) for r in rows)
    all_strong = None
    if strong_price is not None:
        all_strong = sum(
            (r["total_cost_usd"] or 0.0)
            if tier_for(r["target_model"], tiers) == "strong"
            else strong_estimate(r, strong_price)
            for r in rows
        )
    durations = [r["duration_ms"] for r in rows if r["duration_ms"] is not None]
    return {
        "turns": len(rows),
        "by_tier": by_tier,
        "cheap_share": (by_tier.get("cheap", 0) / len(rows)) if rows else 0.0,
        "total_cost_usd": total,
        "all_strong_cost_usd_estimate": all_strong,
        "policy_ms_p50": statistics.median(durations) if durations else None,
    }


def format_summary(s: dict) -> str:
    """Two lines, each under 100 columns: routing split, then cost and policy latency."""
    line1 = (
        f"{s['turns']} turns · {s['by_tier'].get('cheap', 0)} cheap / "
        f"{s['by_tier'].get('strong', 0)} strong ({s['cheap_share'] * 100:.0f}% cheap)"
    )
    parts = [f"cost ${s['total_cost_usd']:.4f}"]
    est = s["all_strong_cost_usd_estimate"]
    if est is not None:
        part = f"all-strong est. ${est:.4f}"
        if est > 0:
            part += f" ({(1 - s['total_cost_usd'] / est) * 100:.0f}% less)" if s["total_cost_usd"] <= est else (
                f" ({(s['total_cost_usd'] / est - 1) * 100:.0f}% more)")
        parts.append(part)
    if s["policy_ms_p50"] is not None:
        parts.append(f"router→policy hop p50 {s['policy_ms_p50']:.0f} ms")
    return line1 + "\n" + " · ".join(parts)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--db", type=Path, required=True, help="router SQLite usage DB")
    ap.add_argument("--group", default="d3-auto")
    ap.add_argument("--since", default="now", help="'now' or an RFC 3339 UTC timestamp")
    ap.add_argument("--tier", action="append", default=[], help="SUBSTRING=TIER for served models")
    ap.add_argument("--interval", type=float, default=0.3)
    ap.add_argument("--once", action="store_true", help="print current rows and exit")
    ap.add_argument("--summary", action="store_true", help="print a one-line summary and exit")
    ap.add_argument("--no-color", action="store_true")
    ap.add_argument("--strong-price", help="IN/CACHED/OUT USD per 1M tokens when no strong row exists")
    args = ap.parse_args(argv)
    tiers = parse_tiers(args.tier)
    since = utc_now_ts() if args.since == "now" else args.since
    if args.summary:
        print(format_summary(summarize(fetch(args.db, args.group, since), tiers, parse_price(args.strong_price))))
        return 0
    seen: set[str] = set()
    first_seen: dict[str, float] = {}
    n = 0
    while True:
        try:
            rows = fetch(args.db, args.group, since)
        except sqlite3.Error as err:  # DB busy or not created yet
            rows = []
            if args.once:
                print(f"usage DB not readable: {err.__class__.__name__}", file=sys.stderr)
                return 1
        for row in rows:
            if row["request_id"] in seen:
                continue
            # usage and policy rows are written separately; wait briefly for the label
            if row["class_label"] is None and not args.once:
                first = first_seen.setdefault(row["request_id"], time.monotonic())
                if time.monotonic() - first < LABEL_WAIT_S:
                    continue
            seen.add(row["request_id"])
            n += 1
            print(format_row(n, row, tiers, color=not args.no_color), flush=True)
        if args.once:
            return 0
        time.sleep(args.interval)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(0)
