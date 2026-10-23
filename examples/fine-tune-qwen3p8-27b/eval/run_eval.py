#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Run pinned promptfoo with private config/results and env-only caller key."""
import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
import yaml

ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).parent


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--tests', type=Path, required=True)
    p.add_argument('--token-file', type=Path, required=True)
    p.add_argument('--out', type=Path, required=True, help='PRIVATE output, never public evidence')
    p.add_argument('--groups', nargs='+', default=['qwen3p8-27b-base','qwen3p8-27b-ft','qwen3p8-27b-ft-onprem','qwen3p8-27b-ft-fireworks'])
    p.add_argument('--temperature', type=float, default=0)
    p.add_argument('--seed', type=int, default=326)
    p.add_argument('--non-tool-only', action='store_true')
    p.add_argument('--concurrency', type=int, choices=[1,8,16,32], default=16)
    a = p.parse_args()
    out = a.out.resolve()
    if (out==ROOT or ROOT in out.parents) and ROOT/'tmp' not in out.parents:
        p.error('raw outputs/configs must remain outside the worktree or in its ignored tmp directory')
    config = yaml.safe_load((HERE/'promptfooconfig.yaml').read_text())
    tests = yaml.safe_load(a.tests.read_text())
    if a.non_tool_only:
        tests = [t for t in tests if any(item['type']=='llm-rubric' for item in t.get('assert',[]))]
    if not tests:
        p.error('no tests')
    for test in tests:
        for assertion in test.get('assert',[]):
            if isinstance(assertion.get('value'),str) and assertion['value'].startswith('file://scorers/'):
                assertion['value'] = 'file://' + str((HERE/assertion['value'][7:]).resolve())
    config['tests'] = tests
    config['extensions'] = ['file://' + str((HERE/'telemetry.cjs').resolve())]
    prototype = config['providers'][0]['config']
    config['providers'] = [{'id':'openai:chat:'+group,'config':{**prototype,'temperature':a.temperature,
        'seed':a.seed,'max_tokens':2048,'top_p':.95,
        **({'passthrough':{'top_k':20,'chat_template_kwargs':{'enable_thinking':False}}} if group.startswith('qwen3p8-27b-') else {})}} for group in a.groups]
    out.parent.mkdir(parents=True,exist_ok=True)
    cfg = out.with_suffix('.private-config.yaml')
    cfg.write_text(yaml.safe_dump(config,sort_keys=False));cfg.chmod(0o600)
    # Provider/GitHub/cloud credentials are unnecessary in an untrusted-data evaluator.
    env = {key:value for key,value in os.environ.items()
        if not any(part in key.upper() for part in ('TOKEN','API_KEY','SECRET','PASSWORD'))}
    env['ROUTER_CALLER_KEY'] = a.token_file.read_text().strip()
    env['PROMPTFOO_PYTHON'] = sys.executable
    env['PROMPTFOO_DISABLE_TELEMETRY'] = '1'
    env['PROMPTFOO_DISABLE_TEMPLATE_ENV_VARS'] = '1'
    # These arrays are scorer inputs, NOT a Cartesian test-variable sweep.
    env['PROMPTFOO_DISABLE_VAR_EXPANSION'] = '1'
    journal = out.with_suffix('.router-ids.jsonl')
    journal.write_text('');journal.chmod(0o600)
    env['FT_ROUTER_JOURNAL'] = str(journal)
    binary = HERE.parent/'.cache/promptfoo/node_modules/.bin/promptfoo'
    process = subprocess.run([str(binary),'eval','-c',str(cfg),'--no-cache','--max-concurrency',str(a.concurrency),'-o',str(out)],env=env,umask=0o077)
    if out.exists() and out.stat().st_size:
        rows = json.loads(out.read_text())['results']['results']
        if len(journal.read_text().splitlines()) != len(rows):
            p.error('telemetry journal incomplete; do not claim router join')
    # promptfoo nonzero may represent genuine failed quality assertions, not an infrastructure fault.
    return process.returncode

if __name__ == '__main__':
    raise SystemExit(main())
