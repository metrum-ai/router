# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Download public pinned model weights into a fresh private directory.

Run only on the dedicated approved GPU host. No token is sent; remote Python
code and pickle weights are deliberately excluded. Fails without enough disk.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil

from huggingface_hub import snapshot_download

MODELS = {
    'medical-base': ('Qwen/Qwen3-32B','9216db5781bf21249d130ec9da846c4624c16137'),
    'medical-adapter': ('kingabzpro/Qwen-3-32B-Medical-Reasoning','204d8d82fa421215b4d226b32b137b5accffd87a'),
    'finance': ('ThakiCloud/Qwen3.8-27B-Human-KO-Finance','4cad7eb42230472aef8c1b3a0a5f8eed2f66068b'),
    'math': ('andresnowak/qwen38-27b-math-grpo-monitor-drgrpo-step100','b850e7f05ad7610bb1e00ed20e942f489f2575c1'),
    'coding': ('Qwen/Qwen3-Coder-30B-A3B-Instruct','b2cff646eb4bb1d68355c01b18ae02e7cf42d120'),
    'general': ('Qwen/Qwen3.8-27B','1d4bf0f2ff6012fd82039f2fa52739d0dd7c60c0'),
}
ALLOW = ['*.json','*.safetensors','*.model','*.txt','*.jinja','*.tiktoken','LICENSE*']


def main():
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--out',required=True,type=Path);parser.add_argument('--execute',action='store_true')
    args=parser.parse_args()
    if not args.execute: parser.error('pass --execute after the independent teardown watcher is armed')
    if not args.out.is_absolute():parser.error('output must be absolute')
    out=args.out.resolve()
    if out.exists():parser.error('output must be a new directory')
    if shutil.disk_usage(out.parent).free < 350 * 1024**3:parser.error('at least 350 GiB free space required')
    out.mkdir(mode=0o700,parents=False,exist_ok=False);manifest={}
    for name,(repo,revision) in MODELS.items():
        snapshot_download(repo_id=repo,revision=revision,local_dir=str(out/name),token=False,
                          allow_patterns=ALLOW,max_workers=4)
        files=[]
        for path in sorted((out/name).rglob('*.safetensors')):
            digest=hashlib.sha256()
            with path.open('rb') as stream:
                while chunk:=stream.read(4*1024*1024):digest.update(chunk)
            files.append({'path':str(path.relative_to(out/name)),'sha256':digest.hexdigest(),'bytes':path.stat().st_size})
        if not files:raise RuntimeError('download lacks safe tensor weights')
        manifest[name]={'repo':repo,'revision':revision,'weights':files}
        print(json.dumps({'model':name,'revision':revision,'files':len(files),'status':'downloaded'}),flush=True)
    fd=os.open(out/'manifest.json',os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    with os.fdopen(fd,'w') as stream:json.dump(manifest,stream,sort_keys=True,indent=2)


if __name__=='__main__':main()
