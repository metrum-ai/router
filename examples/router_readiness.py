#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Shared router readiness wait for the example harnesses (issue #787).

Every example that starts a local router and polls `/readyz` used to carry its
own inline loop with a hard-coded 15 s budget, which flaked on shared-CPU CI
runners. This module owns the budget, the polling and the diagnostics.

Default budget rule (derived from measured data, not a guess): the shared-CPU
runner class measured on 2026-11-05 with METRUM_ROUTER_READY_TIMEOUT_S=120, 40
starts of the governance and tokenomics demo configs (n=40, no failures):
p50 11.87 s, p95 13.31 s, max 24.65 s. Twice the maximum, rounded up to the next
5 s, is 50 s. Earlier CI samples were censored at the old fixed 15 s loop (a
failure only proved "longer than 15 s"); the uncensored local numbers for the
same config shape are p50 2.47 s / max 2.93 s unrestricted and p50 2.75 s / max
4.05 s pinned to one CPU, which is why the shared runners, not the startup work,
set the budget. Override with METRUM_ROUTER_READY_TIMEOUT_S when a deployment
starts the router on slower storage or a busier host.

A timeout is still a failure: the elapsed time and the tail of the router log are
printed first, so a real startup regression stays visible.
"""

from __future__ import annotations

import os
import sys
import time
from pathlib import Path
from urllib.error import URLError
from urllib.request import urlopen

# Environment override for the budget, in seconds.
READY_TIMEOUT_ENV = "METRUM_ROUTER_READY_TIMEOUT_S"

# See the module docstring for the measured rule that produced this value.
DEFAULT_READY_TIMEOUT_S = 50.0

# Poll interval between readiness attempts, and the per-attempt request timeout.
POLL_INTERVAL_S = 0.1
REQUEST_TIMEOUT_S = 1.0

# How many trailing router-log lines to print on failure.
LOG_TAIL_LINES = 20


class ReadinessTimeout(RuntimeError):
    """The router did not answer /readyz within the configured budget."""


def ready_timeout_s(environ: dict | None = None) -> float:
    """Return the readiness budget in seconds, honouring the env override."""
    env = os.environ if environ is None else environ
    raw = str(env.get(READY_TIMEOUT_ENV, "")).strip()
    if not raw:
        return DEFAULT_READY_TIMEOUT_S
    try:
        value = float(raw)
    except ValueError:
        raise ValueError(f"{READY_TIMEOUT_ENV} must be a number of seconds, got {raw!r}") from None
    if value <= 0:
        raise ValueError(f"{READY_TIMEOUT_ENV} must be positive, got {raw!r}")
    return value


def _log_tail(log_path: Path | None, lines: int = LOG_TAIL_LINES) -> str:
    if log_path is None:
        return "(router log path not provided)"
    try:
        text = Path(log_path).read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        return f"(router log unreadable: {exc.__class__.__name__})"
    tail = [line for line in text.splitlines() if line.strip()][-lines:]
    return "\n".join(tail) if tail else "(router log empty)"


def wait_for_router_ready(
    process,
    url: str,
    *,
    log_path: Path | str | None = None,
    timeout_s: float | None = None,
    poll_interval_s: float = POLL_INTERVAL_S,
    request_timeout_s: float = REQUEST_TIMEOUT_S,
    quiet: bool = False,
) -> float:
    """Poll ``url`` until it answers 200 and return the elapsed seconds.

    Raises ``RuntimeError`` if the router process exits first and
    ``ReadinessTimeout`` when the budget runs out; both paths print the elapsed
    time and the router log tail before raising, so a startup problem is visible.
    """
    budget = ready_timeout_s() if timeout_s is None else float(timeout_s)
    if budget <= 0:
        raise ValueError("readiness timeout must be positive")
    path = Path(log_path) if log_path is not None else None
    started = time.monotonic()
    while True:
        if process is not None and process.poll() is not None:
            elapsed = time.monotonic() - started
            raise RuntimeError(
                f"router startup failed: process exited after {elapsed:.2f}s (budget {budget:.1f}s)\n"
                f"router log tail:\n{_log_tail(path)}"
            )
        try:
            with urlopen(url, timeout=request_timeout_s) as response:
                if response.status == 200:
                    elapsed = time.monotonic() - started
                    if not quiet:
                        print(f"router ready in {elapsed:.2f}s ({url})", file=sys.stderr)
                    return elapsed
        except (URLError, TimeoutError, OSError):
            pass
        elapsed = time.monotonic() - started
        if elapsed >= budget:
            print(f"router readiness timeout after {elapsed:.2f}s (budget {budget:.1f}s, {url})", file=sys.stderr)
            raise ReadinessTimeout(
                f"router readiness timeout after {elapsed:.2f}s (budget {budget:.1f}s, {url})\n"
                f"router log tail:\n{_log_tail(path)}"
            )
        time.sleep(poll_interval_s)
