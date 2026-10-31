#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Synthetic case-ID policy for bounded experiments, NOT a privacy classifier."""
import json
from pathlib import Path
import re

DATA=Path(__file__).with_name('benchmark_cases.json')


def load_cases(path=DATA):
    data=json.loads(path.read_text());cases=data.get('cases')
    if data.get('version')!=1 or not isinstance(cases,list) or not cases:raise ValueError('invalid dataset')
    seen=set()
    for x in cases:
        if not isinstance(x,dict) or not re.fullmatch(r'[a-z0-9-]{1,32}',str(x.get('id',''))) or x['id'] in seen:
            raise ValueError('invalid or duplicate case id')
        if not isinstance(x.get('prompt'),str) or not x['prompt'] or not re.fullmatch(r'-?\d+',str(x.get('answer',''))):raise ValueError('invalid prompt or answer')
        if x.get('complexity') not in ('simple','medium','complex') or type(x.get('sensitive')) is not bool or type(x.get('latency_budget_ms')) is not int or x['latency_budget_ms']<=0:
            raise ValueError('invalid case labels')
        seen.add(x['id'])
    return cases


def choose(payload,cases):
    # This policy accepts ONLY known synthetic IDs; caller-controlled markers
    # are not a privacy or sensitivity classification mechanism.
    messages=(payload.get('request') or {}).get('messages') or []
    if not messages or not isinstance(messages[-1],dict):return None
    text=messages[-1].get('content')
    if not isinstance(text,str):return None
    marker=re.match(r'^SYNTHETIC427:([a-z0-9-]{1,32})\n',text)
    case=next((c for c in cases if marker and c['id']==marker.group(1)),None)
    if not case or text!=f"SYNTHETIC427:{case['id']}\n{case['prompt']}\nReturn only the number.":return None
    if case['sensitive']:tier,reason='local','synthetic-sensitive-local'
    elif case['latency_budget_ms']<1000:tier,reason='local','synthetic-latency-local'
    elif case['complexity']=='complex':tier,reason='frontier','synthetic-complex-frontier'
    elif case['complexity']=='medium':tier,reason='datacenter','synthetic-medium-datacenter'
    else:tier,reason='local','synthetic-simple-local'
    targets=payload.get('targets') or []
    matches=[i for i,t in enumerate(targets) if t.get('tier')==tier and t.get('keyConfigured') and int(t.get('weight') or 0)>0]
    if len(matches)!=1:return None
    return {'targetIndex':matches[0],'fallbackIndexes':[],'classLabel':reason}
