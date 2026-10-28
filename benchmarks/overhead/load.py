#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Partial #422 load driver. Does not provision or validate a backend."""

import argparse
import concurrent.futures
import hashlib
import http.client
import json
import math
import os
import pathlib
import socket
import statistics
import threading
import time
import urllib.parse

MAX_BODY = 4 * 1024 * 1024


class ProbeError(Exception):
    pass


def parse_endpoint(url):
    p = urllib.parse.urlsplit(url)
    if (p.scheme not in ("http", "https") or not p.hostname or p.username or
            p.password or p.query or p.fragment or not p.path.startswith("/")):
        raise ValueError("endpoint must be an http(s) URL without credentials, query, or fragment")
    # Access validates a malformed port before any requests start.
    _ = p.port
    return p


def connection(endpoint, timeout):
    cls = http.client.HTTPSConnection if endpoint.scheme == "https" else http.client.HTTPConnection
    return cls(endpoint.hostname, endpoint.port, timeout=timeout)


def percentile(values, p):
    if not values:
        raise ValueError("no observations")
    ordered = sorted(values)
    return ordered[max(0, math.ceil(p * len(ordered)) - 1)]


def stream_response(resp, start):
    """Return first nonempty content time and inter-content-event intervals in ms.

    SSE event boundaries are not guaranteed to correspond to tokenizer tokens.
    """
    content_times = []
    usage = None
    data_lines = []
    saw_done = False
    total = 0
    while True:
        line = resp.readline(MAX_BODY + 1)
        total += len(line)
        if len(line) > MAX_BODY or total > MAX_BODY:
            raise ProbeError("stream response too large")
        if not line:
            break
        if line in (b"\n", b"\r\n"):
            if not data_lines:
                continue
            payload = b"\n".join(data_lines)
            data_lines.clear()
            if payload == b"[DONE]":
                saw_done = True
                break
            try:
                event = json.loads(payload)
            except (ValueError, UnicodeDecodeError) as exc:
                raise ProbeError("invalid SSE JSON") from exc
            if not isinstance(event, dict) or event.get("error"):
                raise ProbeError("invalid SSE event")
            if isinstance(event.get("usage"), dict):
                usage = event["usage"]
            choices = event.get("choices", [])
            if not isinstance(choices, list):
                raise ProbeError("invalid choices")
            if any(isinstance(c, dict) and isinstance(c.get("delta"), dict)
                   and isinstance(c["delta"].get("content"), str)
                   and c["delta"]["content"] for c in choices):
                content_times.append(time.perf_counter_ns())
            continue
        if line.startswith(b"data:"):
            value = line[5:].rstrip(b"\r\n")
            if value.startswith(b" "):
                value = value[1:]
            data_lines.append(value)
    # Consume HTTP chunk terminators/trailers, or the next request may not be reusable.
    if saw_done:
        remainder = resp.read(MAX_BODY + 1)
        if len(remainder) + total > MAX_BODY:
            raise ProbeError("stream response too large")
    if not saw_done or not content_times:
        raise ProbeError("missing stream terminator or content")
    gaps = [(b - a) / 1e6 for a, b in zip(content_times, content_times[1:])]
    return (content_times[0] - start) / 1e6, gaps, usage


def request(conn, endpoint, token, body, streaming, expected_output):
    headers = {"Content-Type": "application/json", "Accept": "text/event-stream" if streaming else "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    started = time.perf_counter_ns()
    conn.request("POST", endpoint.path, body=body, headers=headers)
    resp = conn.getresponse()
    if resp.status != 200:
        resp.close()
        raise ProbeError("HTTP status " + str(resp.status))
    if streaming:
        ttft, gaps, usage = stream_response(resp, started)
    else:
        data = resp.read(MAX_BODY + 1)
        if len(data) > MAX_BODY:
            raise ProbeError("response too large")
        try:
            obj = json.loads(data)
            if not isinstance(obj, dict) or obj.get("error"):
                raise ProbeError("invalid response")
            choices = obj.get("choices", [])
            if not isinstance(choices, list) or not any(
                isinstance(c, dict) and isinstance(c.get("message"), dict)
                and isinstance(c["message"].get("content"), str)
                and c["message"]["content"] for c in choices
            ):
                raise ProbeError("missing response content")
            usage = obj.get("usage")
        except (ValueError, UnicodeDecodeError) as exc:
            raise ProbeError("invalid response JSON") from exc
        ttft, gaps = None, []
    elapsed = (time.perf_counter_ns() - started) / 1e6
    if not isinstance(usage, dict) or type(usage.get("prompt_tokens")) is not int or type(usage.get("completion_tokens")) is not int:
        raise ProbeError("missing token usage")
    if usage["prompt_tokens"] <= 0 or usage["completion_tokens"] != expected_output:
        raise ProbeError("unexpected token count")
    return {"latency_ms": elapsed, "ttft_content_ms": ttft, "content_event_gaps_ms": gaps,
            "prompt_tokens": usage["prompt_tokens"], "completion_tokens": usage["completion_tokens"]}


def run_phase(endpoint, token, body, streaming, output_tokens, workers, warmup, per_worker, timeout):
    """Warm each persistent client, then start all clients together.

    Warmup failures abort the barrier so other workers cannot deadlock.
    """
    phase_start = []
    barrier = threading.Barrier(workers, timeout=timeout * max(2, warmup + 1),
                                action=lambda: phase_start.append(time.perf_counter_ns()))
    def client(index):
        conn = connection(endpoint, timeout)
        try:
            for _ in range(warmup):
                request(conn, endpoint, token, body, streaming, output_tokens)
            barrier.wait()
            rows = []
            for j in range(per_worker):
                row = request(conn, endpoint, token, body, streaming, output_tokens)
                row.update(worker=index, request=j)
                rows.append(row)
            return rows
        except BaseException:
            barrier.abort()
            raise
        finally:
            conn.close()
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        futures = [pool.submit(client, i) for i in range(workers)]
        rows = []
        for f in futures:
            rows.extend(f.result())
        elapsed_s = (time.perf_counter_ns() - phase_start[0]) / 1e9
    return rows, elapsed_s


def summarize(rows, streaming, elapsed_s):
    metric = "ttft_content_ms" if streaming else "latency_ms"
    vals = [r[metric] for r in rows]
    gaps = [gap for r in rows for gap in r["content_event_gaps_ms"]]
    return {"requests": len(rows), "phase_seconds": elapsed_s,
            "achieved_requests_per_second": len(rows) / elapsed_s, "metric": metric,
            "p50_ms": percentile(vals, .50), "p99_ms": percentile(vals, .99),
            "content_event_gap_p50_ms": percentile(gaps, .50) if gaps else None,
            "content_event_gap_p99_ms": percentile(gaps, .99) if gaps else None,
            "content_event_gap_count": len(gaps),
            "prompt_tokens": sorted(set(r["prompt_tokens"] for r in rows))}


def exclusive_json(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        json.dump(value, f, indent=2, sort_keys=True, allow_nan=False)
        f.write("\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--direct-url", required=True, help="complete OpenAI-compatible chat completion URL")
    parser.add_argument("--router-url", required=True, help="complete router chat completion URL")
    parser.add_argument("--direct-token-env", help="name of env var holding backend token")
    parser.add_argument("--router-token-env", help="name of env var holding router token")
    parser.add_argument("--model", required=True, help="same served model/group name for both routes")
    parser.add_argument("--prompt-file", required=True, help="fixed UTF-8 synthetic prompt; content is not stored")
    parser.add_argument("--output-dir", required=True, help="new directory, created private; never overwritten")
    parser.add_argument("--output-tokens", type=int, required=True, help="each response must report exactly this completion count")
    parser.add_argument("--concurrency", default="1,8,32,128")
    parser.add_argument("--repetitions", type=int, default=3)
    parser.add_argument("--requests-per-worker", type=int, default=8)
    parser.add_argument("--warmup-per-worker", type=int, default=1)
    parser.add_argument("--timeout", type=float, default=120)
    args = parser.parse_args()
    direct, routed = parse_endpoint(args.direct_url), parse_endpoint(args.router_url)
    if args.direct_url == args.router_url:
        parser.error("direct and router endpoints must differ")
    if args.repetitions < 3 or args.requests_per_worker < 1 or args.warmup_per_worker < 1 or args.output_tokens < 1 or not math.isfinite(args.timeout) or args.timeout <= 0:
        parser.error("repetitions >= 3; positive warmup, requests, output tokens and timeout required")
    try:
        concurrency = [int(x) for x in args.concurrency.split(",")]
        if not concurrency or len(set(concurrency)) != len(concurrency) or any(x < 1 or x > 1024 for x in concurrency):
            raise ValueError()
    except ValueError:
        parser.error("concurrency must be distinct positive integers <= 1024")
    def token_from(name):
        if name is None:
            return None
        if not os.environ.get(name):
            parser.error("specified credential environment variable is unset or empty")
        return os.environ[name]
    tokens = {"direct": token_from(args.direct_token_env), "router": token_from(args.router_token_env)}
    prompt_bytes = pathlib.Path(args.prompt_file).read_bytes()
    prompt = prompt_bytes.decode("utf-8")
    if not prompt or not args.model:
        parser.error("prompt and model must be nonempty")
    out = pathlib.Path(args.output_dir)
    out.mkdir(mode=0o700)  # fails if the directory exists
    manifest = {"schema": 1, "prompt_sha256": hashlib.sha256(prompt_bytes).hexdigest(),
                "model": args.model, "output_tokens": args.output_tokens, "concurrency": concurrency,
                "repetitions": args.repetitions, "requests_per_worker": args.requests_per_worker,
                "warmup_per_worker": args.warmup_per_worker,
                "note": "partial load driver; backend identity, hardware, profile and host-resource verification are external"}
    exclusive_json(out / "manifest.json", manifest)
    endpoints = {"direct": direct, "router": routed}
    summaries = []
    prompt_token_count = None
    fd = os.open(out / "samples.jsonl", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as raw:
            for workers in concurrency:
                for streaming in (False, True):
                    body = json.dumps({"model": args.model, "messages": [{"role": "user", "content": prompt}],
                                       "max_tokens": args.output_tokens, "temperature": 0,
                                       "stream": streaming, **({"stream_options": {"include_usage": True}} if streaming else {})},
                                      separators=(",", ":")).encode("utf-8")
                    for repeat in range(args.repetitions):
                        order = ("direct", "router") if repeat % 2 == 0 else ("router", "direct")
                        pair = {}
                        for route in order:
                            rows, phase_seconds = run_phase(endpoints[route], tokens[route], body, streaming, args.output_tokens,
                                             workers, args.warmup_per_worker, args.requests_per_worker, args.timeout)
                            for row in rows:
                                row.update(route=route, concurrency=workers, streaming=streaming, repetition=repeat)
                                raw.write(json.dumps(row, allow_nan=False) + "\n")
                            raw.flush()
                            pair[route] = summarize(rows, streaming, phase_seconds)
                        if len(pair["direct"]["prompt_tokens"]) != 1 or pair["direct"]["prompt_tokens"] != pair["router"]["prompt_tokens"]:
                            raise ProbeError("direct/router prompt token counts differ or vary")
                        current_prompt_tokens = pair["direct"]["prompt_tokens"][0]
                        if prompt_token_count is None:
                            prompt_token_count = current_prompt_tokens
                        elif prompt_token_count != current_prompt_tokens:
                            raise ProbeError("prompt token count varies between phases")
                        summaries.append({"concurrency": workers, "streaming": streaming, "repetition": repeat,
                                          "direct": pair["direct"], "router": pair["router"],
                                          "p50_delta_ms": pair["router"]["p50_ms"] - pair["direct"]["p50_ms"],
                                          "p99_delta_ms": pair["router"]["p99_ms"] - pair["direct"]["p99_ms"]})
        variance = []
        for workers in concurrency:
            for streaming in (False, True):
                pairs = [p for p in summaries if p["concurrency"] == workers and p["streaming"] == streaming]
                record = {"concurrency": workers, "streaming": streaming}
                for key in ("p50_delta_ms", "p99_delta_ms"):
                    values = [p[key] for p in pairs]
                    record[key] = {"mean": statistics.mean(values), "sample_stdev": statistics.stdev(values),
                                   "min": min(values), "max": max(values)}
                variance.append(record)
        exclusive_json(out / "summary.json", {"pairs": summaries, "delta_variance": variance})
    except (ProbeError, OSError, socket.timeout, threading.BrokenBarrierError, http.client.HTTPException, ValueError):
        # Do not expose provider response bodies, URLs or credentials. Raw output
        # remains partial and is never accompanied by a complete summary.
        raise SystemExit("load failed; partial samples retained, no complete summary")
    print("completed; inspect private output directory")


if __name__ == "__main__":
    main()
