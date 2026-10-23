#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Render Axolotl's single/multi-GPU config; --smoke is two tiny hardware steps."""
import argparse
import re
from pathlib import Path
import yaml


def render(gpus=1, smoke=False):
    if gpus not in (1, 2, 4, 8):
        raise ValueError('GPU count must divide effective batch 16, maximum 8')
    root = Path(__file__).parent
    config = yaml.safe_load((root / 'axolotl.yaml').read_text())
    if gpus > 1:
        config.update(yaml.safe_load((root / 'fsdp2.yaml').read_text()))
    config['gradient_accumulation_steps'] = 16 // gpus
    if smoke:
        config.update({'max_steps': 2, 'sequence_len': 512, 'gradient_accumulation_steps': 1,
            'output_dir': '/data/smoke-adapter', 'dataset_prepared_path': '/data/smoke-prepared',
            'evals_per_epoch': 0, 'saves_per_epoch': 0, 'save_strategy': 'no'})
        config['datasets'][0]['path'] = '/data/canonical/smoke.jsonl'
        config.pop('test_datasets', None)
    return config


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--gpus', type=int, default=1)
    p.add_argument('--smoke', action='store_true')
    p.add_argument('--out', type=Path, required=True)
    p.add_argument('--data-id', help='launcher-computed mode plus dataset digest; prevents stale prepared-cache reuse')
    a = p.parse_args()
    config = render(a.gpus, a.smoke)
    if a.data_id:
        if not re.fullmatch(r'(smoke|train)-[a-f0-9]{64}',a.data_id):
            p.error('invalid dataset cache identity')
        config['dataset_prepared_path'] = '/data/prepared/' + a.data_id
    a.out.write_text(yaml.safe_dump(config, sort_keys=False))

if __name__ == '__main__':
    main()
