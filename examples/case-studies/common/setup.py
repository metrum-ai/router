#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Create a private loopback router config for the case-study workloads."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import secrets
import subprocess
import yaml


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--config', required=True, type=Path)
    p.add_argument('--state-dir', required=True, type=Path, help='new private directory, preferably tmpfs')
    args = p.parse_args()
    if not args.state_dir.is_absolute(): p.error('state-dir must be absolute')
    settings = json.loads(args.config.read_text())
    if settings['router_listen'] != '127.0.0.1:18123': p.error('only the example loopback listen is supported')
    if settings['routed'] != {'strategy': 'weighted', 'cheap_model': 'deepseek-v4p1-flash-1017', 'cheap_weight': 70, 'frontier_model': 'gpt-6.1-sol', 'frontier_weight': 30} or settings['frontier'] != {'strategy': 'static', 'model': 'gpt-6.1-sol'}:
        p.error('unsupported model configuration; update the budget and verification first')
    args.state_dir.mkdir(mode=0o700, parents=False, exist_ok=False)
    repo = Path(__file__).resolve().parents[3]
    spec = importlib.util.spec_from_file_location('local_pi_instance', repo / 'scripts/local_pi_instance.py')
    m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
    token = 'rtr_metrum_eval_support_dev_k423_' + secrets.token_urlsafe(32)
    token_path=args.state_dir/'caller'; token_path.write_text(token+'\n'); token_path.chmod(0o600)
    cfg = m.build_config(out_dir=args.state_dir, listen=settings['router_listen'], id_key_hex=secrets.token_hex(64), token=token)
    # Use the router's CLI and a private token file; never pass secrets in argv.
    env={k:os.environ[k] for k in ('PATH','HOME','TMPDIR','SYSTEMROOT','GOCACHE','GOMODCACHE','GOROOT','GOPATH','GOPROXY','GOTOOLCHAIN') if k in os.environ}
    reply=subprocess.run(['go','run','./cmd/metrum-ai-routerctl','callers','hash','--token-file',str(token_path)],cwd=repo,env=env,capture_output=True,check=True,timeout=300)
    fields=json.loads(reply.stdout); token_id=fields.get('token_id'); token_hash=fields.get('token_hash')
    if not isinstance(token_id,str) or not token.startswith(token_id+'_') or not isinstance(token_hash,str) or not token_hash.startswith('pbkdf2-sha3-512:'): raise RuntimeError('invalid routerctl hash')
    cfg['callers'][0].pop('token_sha256',None); cfg['callers'][0].update(token_id=token_id,token_hash=token_hash)
    cfg['server']['identifiers'] = {'mode': 'passthrough'}
    cfg['server']['admin_auth'] = {'basic': {'enabled': False, 'users': []}}
    cfg['server']['admin_reports'] = {'enabled': False}
    cfg['server']['default_model_group'] = 'routed'
    for provider, model in [('fireworks', 'deepseek-v4p1-flash-1017'), ('openai', 'gpt-6.1-sol')]:
        metadata = dict(m.TIER_MODELS[model][1]); price = settings['prices'][model]
        for src, dst in [('input_per_million_usd', 'input_price_per_million_usd'), ('cached_input_per_million_usd', 'cached_input_price_per_million_usd'), ('output_per_million_usd', 'output_price_per_million_usd'), ('source', 'pricing_source'), ('date', 'pricing_updated_at')]:
            metadata[dst] = price[src]
        cfg['providers'][provider]['models'] = {model: metadata}
    cfg['models'] = {'routed': {'strategy': 'weighted', 'targets': [m.tier_target('deepseek-v4p1-flash-1017', weight=70), m.tier_target('gpt-6.1-sol', weight=30)]}, 'frontier': {'strategy': 'static', 'targets': [m.tier_target('gpt-6.1-sol')]}}
    cfg['callers'][0]['allow'] = ['routed', 'frontier']
    (args.state_dir/'config.yaml').write_text(yaml.safe_dump(cfg, sort_keys=False))
    print('Created private router configuration and caller token; do not publish this directory.')


if __name__ == '__main__': main()
