#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Run all top-level router tests, examples and fuzz seeds in balanced shards."""
import argparse
import json
import math
from pathlib import Path
import re
import subprocess
import sys

TIMINGS = Path(__file__).with_name('ci_router_timings.json')
NAME = re.compile(r'(?:Test|Example|Fuzz)[A-Za-z0-9_]*\Z')


def partition(names, durations, count):
    """Greedy longest-processing-time scheduling, deterministic for every job."""
    if count < 1 or not names or len(names) != len(set(names)) or any(not NAME.fullmatch(n) for n in names):
        raise ValueError('invalid shard count or test discovery')
    if any(not isinstance(v, (int, float)) or isinstance(v, bool) or not math.isfinite(v) or v < 0 for v in durations.values()):
        raise ValueError('invalid measured test duration')
    # Go JSON rounds elapsed time to 0.01s; distribute reported zeros rather
    # than concentrating them on one shard. Unknown new tests default to 1s.
    def weight(name):
        return max(durations.get(name, 1.0), 0.01)
    bins = [[] for _ in range(count)]
    totals = [0.0] * count
    for name in sorted(names, key=lambda n: (-weight(n), n)):
        index = min(range(count), key=lambda i: (totals[i], i))
        bins[index].append(name)
        totals[index] += weight(name)
    return bins


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--shard-count', type=int, default=4)
    parser.add_argument('--shard-index', type=int, help='zero-based; omitted runs all shards locally')
    args = parser.parse_args(argv)
    if args.shard_count < 1 or (args.shard_index is not None and not 0 <= args.shard_index < args.shard_count):
        parser.error('invalid shard index/count')
    durations = json.loads(TIMINGS.read_text())['seconds']
    listing = subprocess.run(['go', 'test', './internal/router', '-list', '^(Test|Example|Fuzz)'], check=True, text=True, stdout=subprocess.PIPE)
    names = [line for line in listing.stdout.splitlines() if NAME.fullmatch(line)]
    shards = partition(names, durations, args.shard_count)
    indices = range(args.shard_count) if args.shard_index is None else [args.shard_index]
    processes = []
    for i in indices:
        shard = shards[i]
        print(f'router shard {i + 1}/{args.shard_count}: {len(shard)} top-level tests; measured estimate {sum(max(durations.get(n, 1.0), 0.01) for n in shard):.2f}s', flush=True)
        if not shard:
            continue
        pattern = '^(' + '|'.join(map(re.escape, shard)) + ')$'
        processes.append(subprocess.Popen(['go', 'test', './internal/router', '-timeout=30m', '-run', pattern]))
    results = [p.wait() for p in processes]
    if any(results):
        sys.exit(f'router shards failed: {results}')


if __name__ == '__main__':
    main()
