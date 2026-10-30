#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Select optional CI lanes, conservatively retaining coverage on uncertainty."""
import os
from pathlib import Path
import re
import subprocess


def select(paths):
    if not paths:
        return True, True
    docs_only = all(
        Path(path).suffix.lower() in ('.md', '.mdx', '.rst')
        and (path.startswith(('docs/', 'docs-site/docs/', 'examples/')) or '/' not in path)
        for path in paths
    )
    examples = any(path.startswith('examples/') or path in ('Makefile', 'scripts/ci_changes.py', 'scripts/ci_changes_test.py', '.github/workflows/ci.yml') for path in paths)
    return not docs_only, examples


def main():
    base = os.environ.get('CI_BASE_SHA', '')
    go, examples = True, True
    if re.fullmatch(r'[0-9a-f]{40}', base):
        result = subprocess.run(['git', 'diff', '--no-renames', '--name-only', '-z', base, 'HEAD', '--'], stdout=subprocess.PIPE, check=False)
        if result.returncode == 0:
            go, examples = select([os.fsdecode(p) for p in result.stdout.split(b'\0') if p])
    with open(os.environ['GITHUB_ENV'], 'a', encoding='utf-8') as output:
        output.write(f'CI_RUN_GO={int(go)}\nCI_RUN_EXAMPLES={int(examples)}\n')
    print(f'CI_RUN_GO={int(go)} CI_RUN_EXAMPLES={int(examples)}')


if __name__ == '__main__':
    main()
