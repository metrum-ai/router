#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Summarize paired case-study evidence without inventing billed cost.

Input is a local, sanitized JSON document: one workload, two arms, task rows,
router usage rows and optional provider billing receipts. See README.md.
This tool does not collect traffic or establish that a receipt is authentic.
"""
from __future__ import annotations

import argparse
from collections import Counter
from decimal import Decimal, InvalidOperation
import json
import math
from pathlib import Path
import sys

ARMS = ('routed', 'frontier')
WORKLOADS = ('coding-agent', 'support-agent', 'document-qa')


class EvidenceError(ValueError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceError(message)


def usd(value: object, field: str) -> Decimal:
    require(isinstance(value, str), f'{field} must be a decimal string')
    try:
        n = Decimal(value)
    except InvalidOperation as exc:
        raise EvidenceError(f'{field} is not decimal') from exc
    require(n.is_finite() and n >= 0, f'{field} must be finite and nonnegative')
    return n


def wilson(passes: int, n: int) -> list[float]:
    z = 1.959963984540054
    p = passes / n
    d = 1 + z*z/n
    center = (p + z*z/(2*n))/d
    half = z * math.sqrt(p*(1-p)/n + z*z/(4*n*n))/d
    return [max(0.0, center-half), min(1.0, center+half)]


def summarize(document: object) -> dict:
    require(isinstance(document, dict), 'evidence must be an object')
    require(document.get('workload') in WORKLOADS, 'unknown workload')
    require(isinstance(document.get('verifier'), str) and bool(document['verifier'].strip()), 'verifier is required')
    plan = document.get('planned_trials')
    require(isinstance(plan, list) and bool(plan) and all(isinstance(x, str) and x for x in plan) and len(set(plan)) == len(plan), 'unique planned_trials required')
    frontier_model = document.get('frontier_model')
    require(isinstance(frontier_model, str) and bool(frontier_model.strip()), 'frontier_model required')
    arms = document.get('arms')
    require(isinstance(arms, dict) and set(arms) == set(ARMS), 'need exactly routed and frontier arms')
    results = {}
    paired = {}
    all_ids: set[str] = set()
    for arm_name in ARMS:
        arm = arms[arm_name]
        require(isinstance(arm, dict), f'{arm_name}: expected object')
        tasks, usage = arm.get('tasks'), arm.get('usage')
        require(isinstance(tasks, list) and bool(tasks), f'{arm_name}: tasks required')
        require(isinstance(usage, list) and bool(usage), f'{arm_name}: usage required')
        outcomes: dict[str, bool] = {}
        for task in tasks:
            require(isinstance(task, dict), f'{arm_name}: invalid task')
            key, ok = task.get('trial_id'), task.get('passed')
            require(isinstance(key, str) and bool(key) and key not in outcomes, f'{arm_name}: invalid or duplicate trial_id')
            require(type(ok) is bool, f'{arm_name}: passed must be boolean, including failures')
            outcomes[key] = ok
        require(set(outcomes) == set(plan), f'{arm_name}: missing or extra planned trial')
        request_ids: set[str] = set()
        request_trials: set[str] = set()
        mix: Counter[str] = Counter()
        latency: list[float] = []
        row_cost = Decimal(0)
        for row in usage:
            require(isinstance(row, dict), f'{arm_name}: invalid usage row')
            rid, trial, model, ms = row.get('request_id'), row.get('trial_id'), row.get('model'), row.get('latency_ms')
            require(isinstance(rid, str) and bool(rid) and rid not in request_ids and rid not in all_ids, 'duplicate or missing request_id')
            require(trial in outcomes, f'{arm_name}: usage references unknown trial')
            require(isinstance(model, str) and bool(model.strip()), f'{arm_name}: missing model')
            require(isinstance(row.get('price_source'), str) and row['price_source'].startswith('https://'), f'{arm_name}: price source URL required')
            import datetime
            try:
                datetime.date.fromisoformat(row.get('price_date', ''))
            except (TypeError, ValueError) as exc:
                raise EvidenceError(f'{arm_name}: invalid price_date') from exc
            require(type(row.get('status')) is int and 100 <= row['status'] <= 599, f'{arm_name}: invalid HTTP status')
            require(type(ms) in (int, float) and math.isfinite(ms) and ms >= 0, f'{arm_name}: invalid latency')
            request_ids.add(rid)
            all_ids.add(rid)
            request_trials.add(trial)
            mix[model] += 1
            latency.append(float(ms))
            row_cost += usd(row.get('router_cost_usd'), 'router_cost_usd')
        require(request_trials == set(outcomes), f'{arm_name}: every trial needs usage; retain failed attempts')
        receipt = arm.get('billing')
        billed = None
        billing_status = 'unavailable'
        if receipt is not None:
            require(isinstance(receipt, dict), f'{arm_name}: invalid billing receipt')
            require(receipt.get('currency') == 'USD' and receipt.get('scope') == 'exact-request-set', f'{arm_name}: billing scope or currency invalid')
            require(isinstance(receipt.get('source'), str) and bool(receipt['source'].strip()), f'{arm_name}: billing source required')
            ids = receipt.get('request_ids')
            require(isinstance(ids, list) and len(ids) == len(set(ids)) and set(ids) == request_ids, f'{arm_name}: billing must cover exactly the recorded requests')
            billed = str(usd(receipt.get('billed_usd'), 'billed_usd'))
            billing_status = 'receipt-supplied-unverified'
        latency.sort()
        successes = sum(outcomes.values())
        results[arm_name] = {
            'attempts': len(tasks), 'passes': successes, 'pass_rate': successes/len(tasks),
            'wilson_ci95': wilson(successes, len(tasks)), 'request_count': len(usage),
            'routing_mix': {model: {'requests': n, 'share': n/len(usage)} for model, n in sorted(mix.items())},
            'latency_ms': {'p50': latency[math.ceil(.50*len(latency))-1], 'p95': latency[math.ceil(.95*len(latency))-1]},
            'router_recorded_cost_usd': str(row_cost), 'provider_billed_usd': billed, 'billing_status': billing_status,
        }
        paired[arm_name] = outcomes
    require(set(paired['routed']) == set(paired['frontier']), 'arms must have identical paired trial IDs')
    require(set(results['frontier']['routing_mix']) == {frontier_model}, 'frontier arm must use the declared frontier model')
    return {'workload': document['workload'], 'verifier': document['verifier'], 'arms': results,
            'paired': {'wins': sum(paired['routed'][k] and not paired['frontier'][k] for k in paired['routed']),
                       'losses': sum(not paired['routed'][k] and paired['frontier'][k] for k in paired['routed'])},
            'billing_note': 'Receipt authenticity, request attribution and invoice reconciliation require independent verification; missing receipts are not zero cost.'}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('evidence', type=Path)
    args = parser.parse_args()
    try:
        document = json.loads(args.evidence.read_text(encoding='utf-8'))
        result = summarize(document)
    except (OSError, json.JSONDecodeError, EvidenceError, TypeError) as exc:
        print(f'evidence invalid: {exc}', file=sys.stderr)
        return 2
    print(json.dumps(result, indent=2, sort_keys=True, allow_nan=False))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
