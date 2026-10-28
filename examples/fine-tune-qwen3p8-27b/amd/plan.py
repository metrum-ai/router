#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Print (never execute) official ROCm container commands; no cloud mutations."""
import argparse
import json
import re
import shlex


def image_ref(value, repository):
    if not re.fullmatch(re.escape(repository) + r'@sha256:[0-9a-f]{64}', value):
        raise ValueError('require an immutable official ' + repository + ' digest')
    return value


def command(mode, image):
    image_ref(image, 'rocm/vllm' if mode == 'serve' else 'rocm/pytorch')
    if mode not in ('smoke', 'train', 'serve'):
        raise ValueError('unknown mode')
    args = ['docker', 'run', '--rm', '--name', 'amd359-' + mode,
            '--device=/dev/kfd', '--device=/dev/dri', '--group-add=video',
            '--shm-size=32g', '-v', '/workspace/amd359:/data']
    if mode == 'serve':
        args += ['-p', '127.0.0.1:18590:8000', '--entrypoint', 'python', image,
                 '-m', 'vllm.entrypoints.openai.api_server', '--host', '0.0.0.0',
                 '--port', '8000', '--model', '/data/base', '--served-model-name',
                 'qwen-base', '--dtype', 'bfloat16', '--max-model-len', '8192',
                 '--max-num-seqs', '128',
                 '--enable-lora', '--lora-modules', 'qwen-amd-ft=/data/adapter',
                 '--max-lora-rank', '8', '--gpu-memory-utilization', '0.90']
    else:
        # No --privileged, host networking, credentials, or writable host-root mount.
        args += ['--network=none', '--entrypoint', 'axolotl', image,
                 'train', '/data/' + mode + '.yaml']
    return args


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=['smoke', 'train', 'serve'])
    parser.add_argument('--image', required=True)
    parser.add_argument('--json', action='store_true')
    a = parser.parse_args()
    try:
        args = command(a.mode, a.image)
    except ValueError as error:
        parser.error(str(error))
    print(json.dumps(args) if a.json else shlex.join(args))


if __name__ == '__main__':
    main()
