#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Capture one separate 32/128-concurrency profiled load phase; not a latency run."""
import argparse
import concurrent.futures
import hashlib
import http.client
import json
import math
import os
import pathlib
import re
import socket
import threading
import time

import load


def control(path, command):
    if command not in ("start", "status", "stop", "stop29", "stop32", "stop116", "stop128"):
        raise ValueError("invalid command")
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
        sock.settimeout(30)
        sock.connect(path)
        sock.sendall((command + "\n").encode())
        with sock.makefile("rb") as f:
            line = f.readline(4097)
    if not line or len(line) > 4096:
        raise load.ProbeError("invalid control response")
    result = json.loads(line)
    if not isinstance(result, dict) or type(result.get("active")) is not int:
        raise load.ProbeError("invalid control response")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--router-url", required=True)
    parser.add_argument("--router-token-env")
    parser.add_argument("--control-socket", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--path-kind", required=True, choices=("plain", "decision-shadow", "decision-enforce"))
    parser.add_argument("--prompt-file", required=True)
    parser.add_argument("--output-tokens", type=int, required=True)
    parser.add_argument("--concurrency", type=int, required=True, choices=(32, 128))
    parser.add_argument("--requests-per-worker", type=int, default=16)
    parser.add_argument("--warmup-per-worker", type=int, default=1)
    parser.add_argument("--streaming", action="store_true")
    parser.add_argument("--minimum-sampling-seconds", type=float, default=1.0)
    parser.add_argument("--minimum-active-percent", type=int, choices=(90, 100), default=100,
                        help="active-request floor, 100 by default; 90 is opt-in")
    parser.add_argument("--deadline-seconds", type=float, default=180.0)
    parser.add_argument("--socket-timeout", type=float, default=120.0)
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()
    endpoint = load.parse_endpoint(args.router_url)
    if (args.output_tokens < 1 or args.requests_per_worker < 1 or args.warmup_per_worker < 1
            or not args.model or not all(math.isfinite(x) and x > 0 for x in
            (args.minimum_sampling_seconds, args.deadline_seconds, args.socket_timeout))):
        parser.error("positive finite workload and timeout values required")
    token = None
    if args.router_token_env:
        token = os.environ.get(args.router_token_env)
        if not token:
            parser.error("specified credential environment variable is unset or empty")
        if not load.safe_auth_endpoint(endpoint):
            parser.error("credential-bearing endpoints require HTTPS or loopback HTTP")
    raw_prompt = pathlib.Path(args.prompt_file).read_bytes()
    prompt = raw_prompt.decode("utf-8")
    if not prompt:
        parser.error("prompt must be nonempty")
    body = json.dumps({"model": args.model, "messages": [{"role": "user", "content": prompt}],
                       "max_tokens": args.output_tokens, "temperature": 0, "stream": args.streaming,
                       **({"stream_options": {"include_usage": True}} if args.streaming else {})},
                      separators=(",", ":")).encode("utf-8")
    out = pathlib.Path(args.output_dir)
    out.mkdir(mode=0o700)
    started = threading.Event()
    def start_sampling():
        if not control(args.control_socket, "start").get("ok"):
            raise load.ProbeError("profile start failed")
        started.set()

    executor = concurrent.futures.ThreadPoolExecutor(max_workers=1)
    future = executor.submit(load.run_phase, endpoint, token, body, args.streaming, args.output_tokens,
                             args.concurrency, args.warmup_per_worker, args.requests_per_worker,
                             args.socket_timeout, start_sampling)
    deadline = time.monotonic() + args.deadline_seconds
    continuous_since = None
    observed_seconds = None
    floor = math.ceil(args.concurrency * args.minimum_active_percent / 100)
    stopped = False
    try:
        while time.monotonic() < deadline:
            if future.done():
                future.result()  # Preserve a failed workload's safe error classification.
                raise load.ProbeError("load completed before profile capture")
            if not started.wait(.02):
                continue
            status = control(args.control_socket, "status")
            if not status.get("ok"):
                raise load.ProbeError("status failed")
            if status["active"] >= floor:
                if continuous_since is None:
                    continuous_since = time.monotonic()
                if time.monotonic() - continuous_since >= args.minimum_sampling_seconds:
                    observed_seconds = time.monotonic() - continuous_since
                    result = control(args.control_socket, "stop" + str(floor))
                    if result.get("ok"):
                        stopped = True
                        break
                    # Insufficient active requests means profiling was not stopped;
                    # any other failure may have stopped it and invalidated files.
                    if result.get("error") != "insufficient active chat requests":
                        error = result.get("error")
                        # Only emit the bounded diagnostic generated by our wrapper;
                        # never copy arbitrary socket or response data into logs.
                        if isinstance(error, str) and re.fullmatch(r"invalid profile: active chat requests fell to [0-9]{1,3} during capture", error):
                            raise load.ProbeError(error)
                        raise load.ProbeError("profile capture invalid")
                    continuous_since = None
            else:
                continuous_since = None
            time.sleep(.02)
        if not stopped:
            raise load.ProbeError("deadline before profile capture")
        rows, _ = future.result(timeout=max(1.0, deadline - time.monotonic()))
        input_counts = {r["prompt_tokens"] for r in rows}
        if len(input_counts) != 1:
            raise load.ProbeError("prompt token count varied")
        load.exclusive_json(out / "profile-phase.json", {
            "profiled": True, "concurrency": args.concurrency, "streaming": args.streaming,
            "operator_declared_path_kind": args.path_kind,
            "model": args.model, "prompt_sha256": hashlib.sha256(raw_prompt).hexdigest(),
            "prompt_tokens": next(iter(input_counts)), "completion_tokens": args.output_tokens,
            "timed_requests": len(rows), "warmup_per_worker": args.warmup_per_worker,
            "minimum_sampling_seconds": args.minimum_sampling_seconds,
            "poll_observed_sampling_seconds": observed_seconds,
            "minimum_active_percent": args.minimum_active_percent,
            "guard_min_active_during_capture": floor,
            "note": "active floor is polled during the observed window and continuously guarded during CPU stop/heap capture; sampling starts before that window. Do not use for unprofiled latency/throughput statistics",
        })
    except (load.ProbeError, OSError, ValueError, concurrent.futures.TimeoutError,
            threading.BrokenBarrierError, http.client.HTTPException) as exc:
        reason = str(exc) if isinstance(exc, load.ProbeError) else type(exc).__name__
        raise SystemExit("profile phase failed (" + reason + "); profiles and partial files are invalid")
    finally:
        # Running requests cannot be cancelled by Python. An independent
        # watchdog must enforce the real workload and resource deadline.
        executor.shutdown(wait=False, cancel_futures=True)
    print("profile capture completed during guarded in-flight load")


if __name__ == "__main__":
    main()
