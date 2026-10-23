#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Merge trained LoRA into BF16 on the remote host only (never workstation ML)."""
import argparse
from pathlib import Path


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--base', default='/data/base')
    p.add_argument('--adapter', default='/data/adapter')
    p.add_argument('--out', default='/data/merged')
    p.add_argument('--remote-gpu-host', action='store_true', required=True)
    a = p.parse_args()
    import torch
    from transformers import AutoModelForImageTextToText, AutoProcessor
    from peft import PeftModel
    if not torch.cuda.is_available():
        raise RuntimeError('merge must run on remote GPU host, not workstation')
    if not Path(a.adapter, 'adapter_model.safetensors').is_file():
        raise ValueError('trained adapter missing')
    model = AutoModelForImageTextToText.from_pretrained(a.base, torch_dtype=torch.bfloat16,
        device_map='cpu', local_files_only=True)
    model = PeftModel.from_pretrained(model, a.adapter, local_files_only=True)
    model = model.merge_and_unload(safe_merge=True)
    model.save_pretrained(a.out, safe_serialization=True, max_shard_size='5GB')
    AutoProcessor.from_pretrained(a.base, local_files_only=True).save_pretrained(a.out)
    print('merged BF16 snapshot saved')

if __name__ == '__main__':
    main()
