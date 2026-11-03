#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Concurrency sweep for a deploy/reference tier.

Runs the repository's load driver (benchmarks/overhead/load.py) against the
routed lane and the direct vLLM lane with an identical request body, then
reduces its per-request samples to the numbers the tier README reports:
p50/p95/p99 end-to-end latency, failures, and the highest worker count that
stayed inside the tier's latency target with zero failures.

The tier's model group name is also the vLLM served name, so `--group` is valid
on both lanes and the comparison is like for like.

Environment:
  ROUTER_TOKEN        required; caller token, read by load.py from this variable
  LOCAL_VLLM_API_KEY  required when the direct lane enforces a key

Usage:
  concurrency_sweep.py <vendor>-<tier> [--group local-chat] [--direct-port 8001]
      [--concurrency 1,8,32] [--output-tokens 256] [--out-dir results/<run>]
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import statistics
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
LOAD_DRIVER = ROOT / "benchmarks" / "overhead" / "load.py"

# Fixed synthetic prompt: repeated filler keeps the prefill cost comparable
# between lanes without carrying content that must not be shared. The trailing
# instruction matters: the driver requires every response to report exactly
# `--output-tokens`, so the model has to run to the length limit instead of
# stopping at an end-of-sequence token within the first few dozen tokens.
FILLER = "The quick brown fox jumps over the lazy dog. "
INSTRUCTION = (
    "\n\nCount upward from 1, one integer per line. Do not stop, do not summarize, "
    "do not add commentary: keep producing the next integer until you are cut off."
)


def synthetic_prompt(tokens: int) -> str:
    # ~12 tokens per repetition is close enough for a fixed, reproducible body.
    return FILLER * max(1, tokens // 12) + INSTRUCTION


def percentile(values: list[float], p: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    index = min(len(ordered) - 1, max(0, int(round(p / 100 * (len(ordered) - 1)))))
    return ordered[index]


def read_samples(raw_dir: Path) -> list[dict]:
    samples: list[dict] = []
    path = raw_dir / "samples.jsonl"
    if not path.is_file():
        return samples
    with path.open(encoding="utf-8") as handle:
        for line in handle:
            line = line.strip()
            if line:
                samples.append(json.loads(line))
    return samples


def reduce_phase(samples: list[dict], lane: str, workers: int, streaming: bool) -> dict:
    """Reduce one lane/concurrency/streaming cell to latency percentiles.

    load.py sample rows carry route, concurrency, streaming, repetition and the
    two timing columns; a failed request aborts the driver, so a completed run
    has no per-row error column and `requests` must match the plan.
    """
    rows = [
        row
        for row in samples
        if row.get("route") == lane
        and int(row.get("concurrency", -1)) == workers
        and bool(row.get("streaming")) == streaming
    ]
    metric = "ttft_content_ms" if streaming else "latency_ms"
    values = [float(row[metric]) for row in rows if row.get(metric) is not None]
    return {
        "lane": lane,
        "workers": workers,
        "streaming": streaming,
        "requests": len(rows),
        "metric": metric,
        "p50_ms": percentile(values, 50),
        "p95_ms": percentile(values, 95),
        "p99_ms": percentile(values, 99),
        "mean_ms": round(statistics.fmean(values), 1) if values else None,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tier")
    parser.add_argument("--group", default="local-chat", help="model group; also the vLLM served name")
    parser.add_argument("--base-url", default=os.environ.get("ROUTER_BASE_URL", "http://127.0.0.1:8080"))
    parser.add_argument("--direct-port", type=int, default=int(os.environ.get("DIRECT_VLLM_PORT", "8001")))
    parser.add_argument("--concurrency", default=os.environ.get("SWEEP_CONCURRENCY", "1,8,32"))
    parser.add_argument("--repetitions", type=int, default=3)
    parser.add_argument("--requests-per-worker", type=int, default=8)
    parser.add_argument("--output-tokens", type=int, default=256)
    parser.add_argument("--prompt-tokens", type=int, default=2000)
    parser.add_argument("--p95-target-ms", type=float, default=float(os.environ.get("SWEEP_P95_TARGET_MS", "8000")))
    parser.add_argument("--out-dir", default="", help="run directory; raw driver output goes to <out>/raw")
    args = parser.parse_args(argv)

    if not os.environ.get("ROUTER_TOKEN"):
        print("ROUTER_TOKEN is required", file=sys.stderr)
        return 2

    out_dir = Path(args.out_dir) if args.out_dir else Path(tempfile.mkdtemp(prefix=f"{args.tier}-sweep-"))
    raw_dir = out_dir / "raw"
    if raw_dir.exists():
        print(f"{raw_dir} already exists; the load driver never overwrites a run", file=sys.stderr)
        return 2
    raw_dir.parent.mkdir(parents=True, exist_ok=True)

    prompt_dir = Path(tempfile.mkdtemp(prefix="reference-sweep-prompt-"))
    prompt_path = prompt_dir / "prompt.txt"
    prompt_path.write_text(synthetic_prompt(args.prompt_tokens), encoding="utf-8")

    command = [
        sys.executable,
        str(LOAD_DRIVER),
        "--direct-url",
        f"http://127.0.0.1:{args.direct_port}/v1/chat/completions",
        "--router-url",
        f"{args.base_url.rstrip('/')}/v1/chat/completions",
        "--direct-token-env",
        "LOCAL_VLLM_API_KEY",
        "--router-token-env",
        "ROUTER_TOKEN",
        "--model",
        args.group,
        "--prompt-file",
        str(prompt_path),
        "--output-tokens",
        str(args.output_tokens),
        "--concurrency",
        args.concurrency,
        "--repetitions",
        str(args.repetitions),
        "--requests-per-worker",
        str(args.requests_per_worker),
        "--output-dir",
        str(raw_dir),
    ]
    print("running:", " ".join(command[:4]), "...", file=sys.stderr)
    completed = subprocess.run(command, check=False)
    if completed.returncode != 0:
        print(
            "load driver failed; partial samples stay under results/ and no summary is written",
            file=sys.stderr,
        )
        return completed.returncode

    samples = read_samples(raw_dir)
    workers = sorted({int(row["concurrency"]) for row in samples if row.get("concurrency") is not None})
    phases = []
    for count in workers:
        for streaming in (False, True):
            for lane in ("direct", "router"):
                phases.append(reduce_phase(samples, lane, count, streaming))

    expected = args.repetitions * args.requests_per_worker
    router_rows = [row for row in phases if row["lane"] == "router" and not row["streaming"]]
    sustained = 0
    for row in sorted(router_rows, key=lambda item: item["workers"]):
        complete = row["requests"] == expected
        within = row["p95_ms"] is not None and row["p95_ms"] <= args.p95_target_ms
        if complete and within:
            sustained = max(sustained, row["workers"])
        else:
            break

    result = {
        "schema": "metrum.ai/reference-concurrency-sweep/v1",
        "tier": args.tier,
        "group": args.group,
        "commit": os.environ.get("RUN_COMMIT", ""),
        "output_tokens": args.output_tokens,
        "prompt_tokens": args.prompt_tokens,
        "concurrency": args.concurrency,
        "repetitions": args.repetitions,
        "requests_per_worker": args.requests_per_worker,
        "p95_target_ms": args.p95_target_ms,
        "sustained_workers": sustained,
        "requests_planned_per_cell": expected,
        "sustained_definition": "highest non-streaming router worker count whose p95 end-to-end latency is inside "
        "p95_target_ms with every planned request completed; streaming TTFT is reported separately",
        "failures": "a failed request aborts the load driver and leaves no summary, so a written sweep has none",
        "prompt_sha256": hashlib.sha256(prompt_path.read_bytes()).hexdigest(),
        "phases": phases,
        "raw": str(raw_dir),
        "limits": "One group on one box. Latency is end-to-end on a fixed synthetic body; "
        "quality is covered by the functional suite, not by this sweep.",
    }
    document = json.dumps(result, indent=2) + "\n"
    target = out_dir / "sweep.json"
    target.write_text(document, encoding="utf-8")
    print(f"wrote {target}: sustained {sustained} worker(s) at p95 <= {args.p95_target_ms:.0f} ms")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
