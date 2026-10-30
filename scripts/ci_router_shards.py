#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Run every top-level router test, example and fuzz seed in four processes."""
import re
import subprocess
import sys

listing = subprocess.run(['go', 'test', './internal/router', '-list', '^(Test|Example|Fuzz)'], check=True, text=True, stdout=subprocess.PIPE)
names = [line for line in listing.stdout.splitlines() if re.fullmatch(r'(?:Test|Example|Fuzz)[A-Za-z0-9_]*', line)]
if len(names) < 4 or len(names) != len(set(names)):
    sys.exit('router test discovery failed or returned duplicate names')
processes = []
for i in range(4):
    shard = names[i::4]
    pattern = '^(' + '|'.join(map(re.escape, shard)) + ')$'
    print(f'router shard {i + 1}: {len(shard)} top-level tests', flush=True)
    processes.append(subprocess.Popen(['go', 'test', './internal/router', '-timeout=30m', '-run', pattern]))
results = [p.wait() for p in processes]
if any(results):
    sys.exit(f'router shards failed: {results}')
