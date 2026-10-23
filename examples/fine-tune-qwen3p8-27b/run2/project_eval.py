#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Auxiliary original JSON-text/semantic probe, NEVER native replay or training.

Use licensed private SOURCE gold, not recorded teacher calls/answers. Native
replay separately uses exported, privacy-masked descriptors, including two
known oracle-value differences. No output normalization or oracle substitution.
"""
import importlib.util
import json
from pathlib import Path


def project(rows, gold):
    tasks = [r['provenance']['task_sha256'] for r in rows]
    if len(set(tasks)) != len(tasks) or set(tasks) != set(gold):
        raise ValueError('complete identical gold/export cohort required')
    spec = importlib.util.spec_from_file_location('original_json_probe', Path(__file__).resolve().parents[1]/'eval/build_tests.py')
    builder = importlib.util.module_from_spec(spec); spec.loader.exec_module(builder)
    projected = []
    for task in tasks:
        source = gold[task]
        initial = source['initial_messages']
        if not initial or any(m['role'] not in ('system','user') for m in initial):
            raise ValueError('source initial messages must not contain answers')
        calls = source['expected_calls']
        answer = {'role':'assistant','content':source.get('reference','')}
        if calls:
            answer = {'role':'assistant','content':None,'tool_calls':[
                {'id':f'oracle_{i}','type':'function','function':{
                    'name':c['name'],'arguments':json.dumps(c['arguments'],sort_keys=True)}}
                for i,c in enumerate(calls)]}
        projected.append({'messages':initial+[answer],'tools':source['tools']})
    return builder.build(projected)
