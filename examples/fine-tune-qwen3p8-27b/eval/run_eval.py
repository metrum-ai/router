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
    p.add_argument('--groups', nargs='+', default=['qwen3p8-27b-base','qwen3p8-27b-ft-onprem','qwen3p8-27b-ft-fireworks'])
    p.add_argument('--temperature', type=float, default=0)
    p.add_argument('--seed', type=int, default=326)
    p.add_argument('--non-tool-only', action='store_true')
    a = p.parse_args()
    out = a.out.resolve()
    if ROOT/'docs' in out.parents or ROOT/'examples' in out.parents:
        p.error('raw outputs/configs must remain outside public docs/examples')
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
    prototype = config['providers'][0]['config']
    config['providers'] = [{'id':'openai:chat:'+group,'config':{**prototype,'temperature':a.temperature,
        'seed':a.seed,'max_tokens':2048}} for group in a.groups]
    out.parent.mkdir(parents=True,exist_ok=True)
    cfg = out.with_suffix('.private-config.yaml')
    cfg.write_text(yaml.safe_dump(config,sort_keys=False));cfg.chmod(0o600)
    env = os.environ.copy()
    env['ROUTER_CALLER_KEY'] = a.token_file.read_text().strip()
    env['PROMPTFOO_PYTHON'] = sys.executable
    env['PROMPTFOO_DISABLE_TELEMETRY'] = '1'
    binary = HERE.parent/'.cache/promptfoo/node_modules/.bin/promptfoo'
    process = subprocess.run([str(binary),'eval','-c',str(cfg),'--no-cache','--max-concurrency','8','-o',str(out)],env=env,umask=0o077)
    # promptfoo nonzero may represent genuine failed quality assertions, not an infrastructure fault.
    return process.returncode

if __name__ == '__main__':
    raise SystemExit(main())
