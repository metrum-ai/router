#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Convert held-out canonical data to promptfoo JSON-extraction tests outside Git.

The task requests JSON tool calls in text. Native tool plumbing is tested by the
separate tool-result continuation e2e. Do not conflate these measurements.
"""
import argparse
import hashlib
import json
from pathlib import Path
import yaml

TEST_HASH = 'b46d1c6cd90b67e212fe9c2ec55d46a49201a37892f221f5ac087b795f2ef0d2'


def build(rows):
    tests = []
    for index, row in enumerate(rows):
        answer = row['messages'][-1]
        declared = [t['function']['name'] for t in row.get('tools', [])]
        calls = answer.get('tool_calls', [])
        messages = row['messages'][:-1]
        if calls:
            messages = [{'role': 'system', 'content': 'Select tools from the following definitions. Return ONLY a JSON object with a tool_calls array of objects with function.name and function.arguments; arguments must be a JSON object or JSON-encoded object. Tool definitions: ' + json.dumps(row.get('tools', []))}] + messages
            assertions = [{'type': 'is-json'}, {'type': 'contains-json', 'value': {
                'type': 'object', 'required': ['tool_calls'], 'properties': {'tool_calls': {
                    'type': 'array', 'items': {'type': 'object', 'required': ['function'],
                        'properties': {'function': {'type': 'object', 'required': ['name', 'arguments'],
                            'properties': {'name': {'enum': declared}, 'arguments': {'type': ['object', 'string']}}}}}}}}},
                {'type': 'python', 'value': 'file://scorers/tools.py'}]
        else:
            assertions = [{'type': 'llm-rubric', 'value': 'Evaluate correctness, relevance, instruction following and completeness. Do not favor verbosity. The answer must address the user without inventing tool results. Reference answer: ' + str(answer.get('content', ''))}]
        tests.append({'description': f'held-out-{index:03d}', 'vars': {'messages': json.dumps(messages),
            'expected': calls, 'declared': declared}, 'assert': assertions})
    return tests


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--test', type=Path, required=True)
    p.add_argument('--out', type=Path, required=True)
    a = p.parse_args()
    content = a.test.read_bytes()
    if hashlib.sha256(content).hexdigest() != TEST_HASH:
        p.error('held-out hash differs from preregistration')
    rows = [json.loads(line) for line in content.splitlines()]
    if len(rows) != 300:
        p.error('expected 300 held-out cases')
    a.out.write_text(yaml.safe_dump(build(rows), sort_keys=False))

if __name__ == '__main__':
    main()
