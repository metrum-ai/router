# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Generate a fresh synthetic source pool; no model calls or training answers.

The public, deterministic generator is NOT an independent natural-traffic sample.
Source assistant messages are checker-only gold and must never be used for SFT.
"""
import argparse
import hashlib
import json
from pathlib import Path
import sys

LOOP = Path(__file__).resolve().parents[1] / 'router-loop'
sys.path.insert(0, str(LOOP))
import full

COUNTS = {'native_tool': 1500, 'reviewed_general': 2000, 'computed_letter': 700}
AGENTS = (
    ('support', 'lookup_ticket', 'ticket', 'Find the support ticket'),
    ('operations', 'inspect_service', 'service', 'Inspect the service'),
    ('inventory', 'lookup_item', 'item', 'Find the inventory item'),
)
GENERAL = (
    ('support', 'Write a brief customer update for a delayed mock ticket.'),
    ('operations', 'Write a concise handoff for a synthetic service incident.'),
    ('inventory', 'Write a short inventory discrepancy note for a mock item.'),
    ('documentation', 'Explain how to review a draft runbook without running commands.'),
    ('analysis', 'Summarize a hypothetical project status and one next step.'),
)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def generate():
    rows = []
    for i in range(COUNTS['native_tool']):
        persona, tool, parameter, instruction = AGENTS[i % len(AGENTS)]
        value = f'case-{i:04d}'
        schema = {'type': 'function', 'function': {'name': tool, 'description': 'Read a synthetic record.',
            'parameters': {'type': 'object', 'properties': {parameter: {'type': 'string'}},
                           'required': [parameter], 'additionalProperties': False}}}
        row = {'messages': [{'role': 'system', 'content': f'You are the {persona} agent. Use the available mock tool when needed.'},
                {'role': 'user', 'content': f'{instruction} identified as {value}; then report completion.'},
                {'role': 'assistant', 'tool_calls': [{'id': 'source-gold', 'type': 'function', 'function': {
                    'name': tool, 'arguments': json.dumps({parameter: value}, separators=(',', ':'))}}]}], 'tools': [schema]}
        rows.append(('native_tool', row))
    for i in range(COUNTS['reviewed_general']):
        persona, instruction = GENERAL[i % len(GENERAL)]
        row = {'messages': [{'role': 'system', 'content': f'You are a {persona} agent handling synthetic work.'},
                {'role': 'user', 'content': f'{instruction} The reference is note-{i:04d}; the status is pending review.'},
                {'role': 'assistant', 'content': f'Reference note-{i:04d} is pending review. Next step: review it.'}], 'tools': []}
        rows.append(('reviewed_general', row))
    for i in range(COUNTS['computed_letter']):
        target = i % 10
        options = list(range(10)); shift = (i // 10) % 10
        options = options[shift:] + options[:shift]
        answer = 'ABCDEFGHIJ'[options.index(target)]
        choices = '; '.join(f'{letter}: {n}' for letter, n in zip('ABCDEFGHIJ', options))
        row = {'messages': [{'role': 'system', 'content': 'Return only the letter of the correct option.'},
                {'role': 'user', 'content': f'Synthetic quiz item quiz-{i:04d}: what is {target} + 0? {choices}'},
                {'role': 'assistant', 'content': answer}], 'tools': []}
        rows.append(('computed_letter', row))
    return rows


def build(out):
    out.mkdir(mode=0o700, parents=False, exist_ok=False)
    rows = generate(); classes = {}; path = out / 'seed-canonical.jsonl'
    with path.open('w', encoding='utf-8') as stream:
        for kind, row in rows:
            key = full.user_hash(row)
            if key in classes: raise ValueError('duplicate normalized prompt')
            classes[key] = kind
            stream.write(json.dumps(row, ensure_ascii=False, separators=(',', ':')) + '\n')
    (out / 'class-by-normalized-user-sha256.json').write_text(json.dumps(classes, sort_keys=True, separators=(',', ':')) + '\n')
    manifest = {'license': 'Apache-2.0', 'source': 'run3/build_pool.py synthetic task generator',
                'counts': COUNTS, 'seed_file_sha256': digest(path),
                'classes_sha256': digest(out / 'class-by-normalized-user-sha256.json'),
                'training_answer_policy': 'source assistant messages are checker-only; training answers are new governed router captures',
                'limitations': 'synthetic templated tasks, not independent natural agent traffic'}
    (out / 'manifest.json').write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')
    for p in out.iterdir(): p.chmod(0o600)
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__); parser.add_argument('--out', required=True, type=Path)
    args = parser.parse_args(); print(json.dumps(build(args.out), sort_keys=True))


if __name__ == '__main__': main()
