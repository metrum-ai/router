# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Strict objective scoring and honest aggregate metrics; never execute output."""
from decimal import Decimal, InvalidOperation
import json
import math
import re


def score(case, message):
    if not isinstance(message, dict): return False
    if case['domain'] == 'biomedical' and case.get('tools'):
        calls = message.get('tool_calls') or []
        if len(calls) != 1: return False
        try:
            function = calls[0]['function']; arguments = json.loads(function['arguments'])
            expected = case['expected']
            return function['name'] == expected['name'] and arguments == expected['arguments']
        except (KeyError, TypeError, ValueError): return False
    content = message.get('content')
    if not isinstance(content, str): return False
    if case['domain'] == 'math':
        value = content.strip()
        if not re.fullmatch(r'[+-]?(?:\d+(?:,\d{3})*|\d+)(?:\.\d+)?', value): return False
        try: return Decimal(value.replace(',','')) == Decimal(str(case['expected']))
        except InvalidOperation: return False
    return content.strip().casefold() == str(case['expected']).strip().casefold()


def percentile(values, pct):
    if not values: return None
    xs = sorted(values); index = (len(xs)-1)*pct; lo = math.floor(index); hi = math.ceil(index)
    return xs[lo] + (xs[hi]-xs[lo])*(index-lo)


def summarize(records, elapsed_seconds, actual_gpu_cost_usd=None):
    if elapsed_seconds <= 0: raise ValueError('elapsed time must be positive')
    if actual_gpu_cost_usd is not None and actual_gpu_cost_usd < 0: raise ValueError('negative GPU cost')
    passed = sum(r.get('status') == 200 and bool(r.get('passed')) for r in records)
    responses = sum(r.get('status') == 200 and not r.get('error') for r in records)
    latencies = [r['latency_ms'] for r in records if isinstance(r.get('latency_ms'), (int,float))]
    ttft = [r['ttft_ms'] for r in records if isinstance(r.get('ttft_ms'), (int,float))]
    domains = {}
    for domain in sorted({r['domain'] for r in records}):
        group = [r for r in records if r['domain'] == domain]
        domains[domain] = {'attempted': len(group), 'passed': sum(r.get('status') == 200 and bool(r.get('passed')) for r in group)}
    return {'attempted':len(records), 'completed_responses':responses, 'quality_passed':passed, 'by_domain':domains,
            'responses_per_second':responses/elapsed_seconds, 'quality_passes_per_second':passed/elapsed_seconds,
            'latency_ms':{'p50':percentile(latencies,.5),'p99':percentile(latencies,.99),'count':len(latencies)},
            'ttft_ms':{'p50':percentile(ttft,.5),'p99':percentile(ttft,.99),'count':len(ttft)},
            'actual_gpu_cost_usd':actual_gpu_cost_usd,
            'actual_gpu_cost_per_quality_pass_usd':actual_gpu_cost_usd/passed if actual_gpu_cost_usd is not None and passed else None}
