#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Prepare provisional AMD Axolotl config, not authority to start training."""
import argparse
import hashlib
import json
import re
from pathlib import Path
import yaml


def render(recipe, data_id, smoke=False):
    if not re.fullmatch(r'[0-9a-f]{64}', data_id):
        raise ValueError('data_id must be the validated export manifest SHA256')
    config = dict(recipe)
    config.update(tf32=False, bf16=True, attn_implementation='sdpa',
                  optimizer='adamw_torch', load_in_4bit=False, load_in_8bit=False, seed=355,
                  lora_r=8, lora_alpha=16, learning_rate=1e-5, num_epochs=1,
                  lora_target_modules=['q_proj', 'v_proj', 'linear_attn.in_proj_qkv',
                                       'linear_attn.out_proj'])
    for key in ('flash_attention', 'flash_attn', 'deepspeed', 'fsdp', 'fsdp_config'):
        config.pop(key, None)
    if smoke:
        config.update(max_steps=2, sequence_len=512, gradient_accumulation_steps=1,
                      output_dir='/data/smoke-adapter', evals_per_epoch=0,
                      saves_per_epoch=0, save_strategy='no')
        config['datasets'] = [dict(config['datasets'][0], path='/data/canonical/smoke.jsonl')]
        config.pop('test_datasets', None)
    # Cache identity includes recipe: no reuse after a recipe/data amendment.
    identity = hashlib.sha256((data_id + json.dumps(config, sort_keys=True)).encode()).hexdigest()
    config['dataset_prepared_path'] = '/data/prepared/' + identity
    return config


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--recipe', type=Path, required=True)
    p.add_argument('--data-id', required=True)
    p.add_argument('--smoke', action='store_true')
    p.add_argument('--out', type=Path, required=True)
    a = p.parse_args()
    a.out.write_text(yaml.safe_dump(render(yaml.safe_load(a.recipe.read_text()), a.data_id, a.smoke), sort_keys=False))


if __name__ == '__main__':
    main()
