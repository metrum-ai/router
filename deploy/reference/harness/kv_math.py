#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Reproduce a tier's sizing arithmetic (deploy/reference/<tier>/SIZING.md).

Two modes:

  # bytes per token and bytes per request at a context length
  kv_math.py --layers 64 --kv-heads 8 --head-dim 128 --context 8192

  # requests that fit a KV pool derived from a memory budget
  kv_math.py --layers 64 --kv-heads 8 --head-dim 128 --context 8192 \
      --capacity-gb 80 --util 0.88 --weights-gb 33 --overhead-gb 2

The numbers are arithmetic, not measurements; the tier's results/ directory
holds what was measured.
"""

from __future__ import annotations

import argparse

GIB = 1024**3


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--layers", type=int, required=True)
    parser.add_argument("--kv-heads", type=int, required=True)
    parser.add_argument("--head-dim", type=int, required=True)
    parser.add_argument("--bytes", type=int, default=2, help="key/value bytes per element (2 = BF16, 1 = FP8 cache)")
    parser.add_argument("--context", type=int, default=8192)
    parser.add_argument("--capacity-gb", type=float, default=None, help="GPU or usable unified memory in GB")
    parser.add_argument("--util", type=float, default=None, help="--gpu-memory-utilization for this slot")
    parser.add_argument("--weights-gb", type=float, default=None)
    parser.add_argument("--overhead-gb", type=float, default=2.0)
    parser.add_argument("--pool-gb", type=float, default=None, help="KV pool directly, instead of a budget")
    args = parser.parse_args(argv)

    per_token = 2 * args.layers * args.kv_heads * args.head_dim * args.bytes
    per_token_gib = per_token / GIB
    per_request_gib = per_token * args.context / GIB
    print(f"KV bytes/token      : {per_token:,} ({per_token / 1024:.0f} KiB)")
    print(f"KV GiB per request  : {per_request_gib:.2f} at {args.context} context tokens")

    pool_gib = args.pool_gb
    if pool_gib is None:
        if args.capacity_gb is None or args.util is None or args.weights_gb is None:
            return 0
        budget = args.capacity_gb * args.util
        pool_gib = budget - args.weights_gb - args.overhead_gb
        print(f"Budget              : {args.capacity_gb} GB x {args.util} = {budget:.1f} GB")
        print(f"KV pool             : {pool_gib:.1f} GiB after {args.weights_gb} GiB weights and {args.overhead_gb} GiB overhead")
    if pool_gib <= 0:
        print("KV pool is not positive: this slot does not fit the stated budget")
        return 1
    requests = pool_gib / per_request_gib
    print(f"Requests at context : {requests:.1f} (whole requests: {int(requests)})")
    print(f"KV tokens           : {int(pool_gib * GIB / per_token):,}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
