# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Render a one-step, non-held-out hardware smoke from the frozen recipe."""
import argparse
from pathlib import Path
import yaml


def render(recipe):
    cfg=yaml.safe_load(recipe)
    if not isinstance(cfg,dict) or not cfg.get('test_datasets') or not cfg.get('datasets'):
        raise ValueError('frozen recipe must contain non-empty train and validation datasets')
    cfg['datasets'][0]['path']='/data/canonical/smoke.jsonl'
    cfg['evals_per_epoch']=0
    cfg['max_steps']=1
    cfg['output_dir']='/data/smoke-adapter'
    cfg['dataset_prepared_path']='/data/smoke-prepared'
    return cfg


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--recipe',required=True,type=Path);p.add_argument('--out',required=True,type=Path);a=p.parse_args()
    with a.out.open('x') as file:yaml.safe_dump(render(a.recipe.read_text()),file,sort_keys=False)

if __name__=='__main__':main()
