#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Expose verified compatibility-tester commit pins for Actions cache keys."""
import re

from compat_mock import TESTERS


if __name__ == '__main__':
    if set(TESTERS) != {'openai', 'anthropic'}:
        raise ValueError('unexpected compatibility tester inventory')
    for name in ('openai', 'anthropic'):
        commit = TESTERS[name][0]
        if not re.fullmatch(r'[0-9a-f]{40}', commit):
            raise ValueError(f'invalid {name} tester commit')
        print(f'{name}={commit}')
