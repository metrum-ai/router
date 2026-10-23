# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Allowlisted, isolated deterministic replay backend for FT consumers.
Freeze this file and the jsonschema runtime. No row-provided code, network,
shell, remote schema refs, or recorded teacher answers are executed.
"""
import json
from jsonschema import Draft202012Validator


def local_refs(value):
    if isinstance(value, dict):
        for key in ('$ref', '$dynamicRef', '$recursiveRef'):
            if key in value and not value[key].startswith('#'):
                raise ValueError('remote schema references forbidden')
        for child in value.values():
            local_refs(child)
    elif isinstance(value, list):
        for child in value:
            local_refs(child)


def initialize(descriptor):
    if descriptor.get('version') != 'router-replay-v1' or descriptor.get('backend') != 'acme-support-v1' or descriptor.get('checker') != 'expected-tool-calls-v1':
        raise ValueError('unknown replay backend/checker')
    fixture = json.loads(json.dumps(descriptor))
    if not isinstance(fixture.get('tools'), list) or not isinstance(fixture.get('expected_calls'), list) or not isinstance(fixture.get('seed'), int):
        raise ValueError('invalid replay fixture')
    if fixture.get('expected_final', '') not in ('', 'A', 'B', 'C', 'D'):
        raise ValueError('unknown exact-final anchor')
    local_refs(fixture['tools'])
    return {'fixture': fixture, 'actual': [], 'schema_valid': True}


def execute(state, name, args):
    fixture = state['fixture']
    try:
        if not isinstance(args, dict):
            raise ValueError('function arguments must be an object')
        schema = next(t['function']['parameters'] for t in fixture['tools'] if t['function']['name'] == name)
        local_refs(schema)
        Draft202012Validator(schema).validate(args)
        state['actual'].append(json.loads(json.dumps({'name': name, 'arguments': args})))
        return {'ok': True, 'mock': True, 'status': 'completed', 'seed': fixture['seed']}
    except Exception:
        state['schema_valid'] = False
        return {'ok': False, 'mock': True, 'error': 'invalid_tool_call'}


def success(state, final_message):
    content = final_message.get('content')
    return (state['schema_valid'] and state['actual'] == state['fixture']['expected_calls']
            and isinstance(content, str) and bool(content) and not final_message.get('tool_calls')
            and final_message.get('finish_reason', 'stop') == 'stop'
            and (not state['fixture'].get('expected_final') or content.strip() == state['fixture']['expected_final']))
