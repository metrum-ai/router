# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Iteration-1 seed corpus construction tooling for issue #157.

Operator artifacts stay outside the repository. Paid replay is opt-in and is
not invoked by unit tests.
"""

from __future__ import annotations

SOURCE_DATASET = "nebius/SWE-rebench-openhands-trajectories"
SOURCE_REVISION = "35455389ab51bf5e2306bfd436ef72d0f98bf882"
AUDIT_DATE = "2026-09-12"
# Example corpus budget: at most 100 source traces, then at most 100 sampled turns.
# Not statistical sufficiency. Do not load the full upstream parquet into RAM.
EXAMPLE_MAX_TRACES = 100
SAMPLE_N = 100
DEFAULT_SAMPLE_SEED = 42
SPEND_ABORT_USD = 100.0

__all__ = [
    "AUDIT_DATE",
    "DEFAULT_SAMPLE_SEED",
    "EXAMPLE_MAX_TRACES",
    "SAMPLE_N",
    "SOURCE_DATASET",
    "SOURCE_REVISION",
    "SPEND_ABORT_USD",
]
