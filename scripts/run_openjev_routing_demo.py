#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Run fake OpenJev + OpenJev policy + synthetic harness."""
from __future__ import annotations

import argparse
import subprocess
import sys
import time
import urllib.request
from pathlib import Path


def wait_url(url: str, timeout_s: float = 8.0) -> None:
    deadline = time.time() + timeout_s
    last = None
    while time.time() < deadline:
        try:
            urllib.request.urlopen(url, timeout=0.5).read()
            return
        except Exception as err:  # noqa: BLE001
            last = err
            time.sleep(0.05)
    raise RuntimeError(f"not ready: {url}: {last}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--openjev-port", type=int, default=3000)
    ap.add_argument("--policy-port", type=int, default=18093)
    args = ap.parse_args()
    root = Path(__file__).resolve().parents[1]
    fake = root / "examples" / "external-routing-policy" / "fake_openjev.py"
    policy = root / "examples" / "external-routing-policy" / "openjev_policy.py"
    harness = root / "examples" / "external-routing-policy" / "openjev_routing_harness.py"

    procs: list[subprocess.Popen] = [
        subprocess.Popen(
            [sys.executable, str(fake), "--host", "127.0.0.1", "--port", str(args.openjev_port)],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        ),
        subprocess.Popen(
            [
                sys.executable,
                str(policy),
                "--host",
                "127.0.0.1",
                "--port",
                str(args.policy_port),
                "--openjev-url",
                f"http://127.0.0.1:{args.openjev_port}",
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        ),
    ]
    try:
        # Fake OpenJev only serves POST; wait on the policy health endpoint.
        time.sleep(0.15)
        wait_url(f"http://127.0.0.1:{args.policy_port}/health")
        result = subprocess.run(
            [sys.executable, str(harness), f"http://127.0.0.1:{args.policy_port}"],
            check=False,
        )
        return result.returncode
    finally:
        for p in procs:
            p.terminate()
        for p in procs:
            try:
                p.wait(timeout=2)
            except subprocess.TimeoutExpired:
                p.kill()


if __name__ == "__main__":
    raise SystemExit(main())
