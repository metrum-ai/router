#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Render the frozen run-3 held-out gold into the existing first-turn gate.

This uses checker-only source references, never teacher outputs or outcomes.
It must run and be hashed before any candidate/base held-out inference.
"""
import argparse
from collections import Counter
import hashlib
import importlib.util
import json
from pathlib import Path
import yaml

HERE=Path(__file__).resolve().parent
PLAN_SHA='3310f9cb2d983f72e20cd6a9b3c5489b36758468b30ac00f3b7a0f8411bb1a55'
GOLD_SHA='00e09198e5572487764a768619d48c6b7083e48a69e98d992501d861b4362483'
spec=importlib.util.spec_from_file_location('gate_builder',HERE.parent/'eval/build_tests.py')
builder=importlib.util.module_from_spec(spec);spec.loader.exec_module(builder)


def sha(path):return hashlib.sha256(path.read_bytes()).hexdigest()


def render(rows):
    if len(rows)!=300 or Counter(r['class'] for r in rows)!={'native_tool':200,'reviewed_general':75,'computed_letter':25}:
        raise ValueError('frozen 200/75/25 held-out class mix required')
    if len({r['task_hash'] for r in rows})!=300:raise ValueError('duplicate task hash')
    converted=[]
    for row in rows:
        native=row['class']=='native_tool';calls=row['expected_calls']
        if len(calls)!=(1 if native else 0):raise ValueError('unexpected reference call count')
        if native:
            answer={'role':'assistant','tool_calls':[{'function':{'name':c['name'],'arguments':json.dumps(c['arguments'],sort_keys=True)}} for c in calls]}
        else:
            if not isinstance(row['reference'],str) or not row['reference']:raise ValueError('missing checker reference')
            answer={'role':'assistant','content':row['reference']}
        converted.append({'messages':row['initial_messages']+[answer],'tools':row['tools']})
    return builder.build(converted)


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--plan',required=True,type=Path);p.add_argument('--gold',required=True,type=Path);p.add_argument('--out',required=True,type=Path);a=p.parse_args()
    if a.out.resolve().is_relative_to(HERE.parents[2]):raise ValueError('gate data must stay outside the worktree')
    if sha(a.plan)!=PLAN_SHA or sha(a.gold)!=GOLD_SHA:raise ValueError('source hashes differ from prior freeze')
    selected=json.loads(a.plan.read_text())['test'];gold=json.loads(a.gold.read_text())
    if len(gold)!=300 or any(gold.get(row['task_hash'])!=row for row in selected):raise ValueError('selected test and gold mismatch')
    tests=render(selected);payload=yaml.safe_dump(tests,sort_keys=False,allow_unicode=True).encode()
    with a.out.open('xb') as f:f.write(payload)
    a.out.chmod(0o600)
    print(json.dumps({'cases':len(tests),'sha256':hashlib.sha256(payload).hexdigest(),'teacher_outputs_used':False}))


if __name__=='__main__':main()
