#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""AMD operator-port binding for unchanged shared e411 SDK configuration.

Uses the shared config_for/child_environment functions, prompts and scorers.
Only the fixed no-redirect relay/router ports differ from the NVIDIA operator.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import yaml

REGISTRATION = 'e411fc9f7c6216eb87e659d3b120561bc0e1b3f45ef2ef6c623637796e0ee1ea'
RELAY = 'http://127.0.0.1:18596/v1'


def config(shared, tests, arm, mode, seed):
    # The frozen shared function validates localhost origin and resolves SAME scorers.
    result = shared.config_for(tests, arm, mode, seed, RELAY)
    for provider in result['providers']:
        if provider['config']['apiBaseUrl'] != RELAY:
            raise ValueError('AMD fixed relay required')
    return result


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--shared-example', type=Path, required=True)
    p.add_argument('--shared-runner-sha256', required=True)
    p.add_argument('--tests', type=Path, required=True)
    p.add_argument('--preregistration', type=Path, required=True)
    p.add_argument('--token-file', type=Path, required=True)
    p.add_argument('--out', type=Path, required=True)
    p.add_argument('--arm', choices=['base', 'teacher', 'amd_onprem'], required=True)
    p.add_argument('--mode', choices=['primary', 'regressions', 'sampling'], required=True)
    p.add_argument('--seed', type=int, choices=[326,327,328], default=326)
    p.add_argument('--execute', action='store_true')
    a = p.parse_args()
    sha = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
    if sha(a.preregistration) != REGISTRATION:
        p.error('same current e411 registration bytes required; never substitute training parent')
    source = a.shared_example/'run2/run_auxiliary.py'
    if sha(source) != a.shared_runner_sha256:
        p.error('shared SDK source differs from signed port-binding envelope')
    reg = json.loads(a.preregistration.read_text())
    expected = reg['fresh_regressions']['sha256'] if a.mode=='regressions' else reg['heldout_projection_sha256']
    if sha(a.tests) != expected or (a.mode!='sampling' and a.seed!=326):
        p.error('frozen probe hash/seed differs')
    if stat.S_IMODE(a.token_file.stat().st_mode)!=0o600:
        p.error('private caller token required')
    if a.out.resolve().is_relative_to(Path(__file__).resolve().parents[3]):
        p.error('private results outside Git required')
    spec = importlib.util.spec_from_file_location('shared_sdk', source)
    shared = importlib.util.module_from_spec(spec); spec.loader.exec_module(shared)
    tests = yaml.safe_load(a.tests.read_text())
    rendered = config(shared, tests, a.arm, a.mode, a.seed)
    if not a.execute:
        print(json.dumps({'dry_run':True,'arm':a.arm,'tests':len(rendered['tests']),
            'registration_sha256':REGISTRATION,'fixed_relay':RELAY,'no_inference':True})); return 0
    a.out.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if a.out.exists():
        p.error('refuse replacing a qualification outcome')
    cfg = a.out.with_suffix('.private-config.yaml'); cfg.write_text(yaml.safe_dump(rendered,sort_keys=False)); cfg.chmod(0o600)
    journal = a.out.with_suffix('.router-ids.jsonl'); journal.write_text(''); journal.chmod(0o600)
    env = shared.child_environment(os.environ, a.token_file.read_text().strip(), journal)
    cmd = [str(a.shared_example/'.cache/promptfoo/node_modules/.bin/promptfoo'),'eval','-c',str(cfg),
           '--no-cache','--max-concurrency','16','-o',str(a.out)]
    result = subprocess.run(cmd,env=env,umask=0o077)
    if a.out.exists() and a.out.stat().st_size:
        rows = json.loads(a.out.read_text())['results']['results']
        if len(journal.read_text().splitlines()) != len(rows):
            p.error('incomplete request journal; costs unknown')
    return result.returncode


if __name__=='__main__':
    raise SystemExit(main())
