# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Type-strict, nested argument scoring; malformed outputs score zero, never crash."""
import json
from collections import Counter


def leaves(value, path=()):
    if isinstance(value, dict):
        if not value:
            return [(path, 'object', '{}')]
        return [v for key in sorted(value) for v in leaves(value[key], path + (key,))]
    if isinstance(value, list):
        if not value:
            return [(path, 'array', '[]')]
        return [v for i, item in enumerate(value) for v in leaves(item, path + (i,))]
    return [(path, type(value).__name__, json.dumps(value, sort_keys=True))]


def parse_calls(output):
    if isinstance(output, str):
        output = json.loads(output)
    if isinstance(output, dict):
        if 'choices' in output:
            output = output['choices'][0]['message']
        output = output.get('tool_calls', [])
    if not isinstance(output, list):
        raise ValueError('calls must be a list')
    calls = []
    for call in output:
        fn = call.get('function', call)
        args = fn.get('arguments', {})
        if isinstance(args, str):
            args = json.loads(args)
        if not isinstance(args, dict) or not isinstance(fn['name'], str):
            raise ValueError('invalid function')
        calls.append((fn['name'], args))
    return calls


def score(output, expected, declared):
    try:
        actual = parse_calls(output)
        target = parse_calls(expected)
        names = [n for n, _ in actual]
        def features(calls):
            return Counter((name, path, typ, val) for name, args in calls for path, typ, val in leaves(args))
        a, b = features(actual), features(target)
        common = sum((a & b).values())
        f1 = 2 * common / (sum(a.values()) + sum(b.values())) if a or b else 1.0
        canonical = lambda calls: sorted((n, json.dumps(args, sort_keys=True)) for n, args in calls)
        return {'valid': 1, 'exact': int(canonical(actual) == canonical(target)),
                'tool_name_exact': int(Counter(names) == Counter(n for n, _ in target)),
                'argument_f1': f1, 'no_hallucination': int(all(n in declared for n in names)),
                'over_call': int(bool(actual) and not target), 'refusal': int(bool(target) and not actual)}
    except (ValueError, KeyError, TypeError, IndexError, AttributeError):
        return {'valid': 0, 'exact': 0, 'tool_name_exact': 0, 'argument_f1': 0,
                'no_hallucination': 0, 'over_call': 0, 'refusal': 0}


def get_assert(output, context):
    variables = context['vars']
    metrics = score(output, variables['expected'], variables['declared'])
    return {'pass': bool(metrics['exact'] and metrics['no_hallucination']),
            'score': metrics['argument_f1'], 'reason': json.dumps(metrics)}
