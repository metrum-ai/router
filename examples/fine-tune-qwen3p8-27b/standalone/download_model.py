#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""GPU-host one-command download: read HF token on stdin, never persist it."""
import sys
from huggingface_hub import snapshot_download

MODEL = 'Qwen/Qwen3.8-27B'
REVISION = '1d4bf0f2ff6012fd82039f2fa52739d0dd7c60c0'


def main():
    token = sys.stdin.read().strip()
    if not token:
        raise ValueError('HF token required on encrypted stdin')
    snapshot_download(MODEL, revision=REVISION, local_dir='/data/base', token=token)
    print('pinned snapshot downloaded')

if __name__ == '__main__':
    main()
