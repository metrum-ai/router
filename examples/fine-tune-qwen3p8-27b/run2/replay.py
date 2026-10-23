#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Backend-independent router replay engine; #358 backend contract pending.

call_router(messages, tools) must dispatch through the authenticated localhost
router, NOT directly upstream, and return (assistant_message, request_id).
A trusted, frozen harness backend supplies initialize/execute/success methods.
Never import/execute code or fetch URLs named by a captured trace.
"""
import copy
import json
import math
from collections import defaultdict

import jsonschema


def run_case(row, call_router, backend, max_turns=8):
    # Initial fixture contains no recorded teacher assistant/tool answers.
    messages = copy.deepcopy(row['replay']['initial_messages'])
    if not messages or any(m['role'] not in ('system','user') for m in messages):
        raise ValueError('replay must start from initial task, never teacher continuations')
    tools = copy.deepcopy(row.get('tools',[]))
    schemas = {t['function']['name']:t['function'].get('parameters',{}) for t in tools}
    if len(schemas)!=len(tools):raise ValueError('duplicate tool definition')
    def no_references(value):
        if isinstance(value,dict):
            if any(k in value for k in ('$ref','$dynamicRef')):
                raise ValueError('inline schemas only; no row-driven reference/URL resolution')
            for child in value.values():no_references(child)
        elif isinstance(value,list):
            for child in value:no_references(child)
    for schema in schemas.values():no_references(schema)
    state = backend.initialize(copy.deepcopy(row['replay']))
    request_ids = []
    result = {'task_sha256':row['provenance']['task_sha256'],'task_success':False,
              'tool_schema_valid':True,'request_ids':request_ids,'transport_error':False}
    for _ in range(max_turns):
        # No retry/arm failover: failures remain in the task denominator.
        try:
            message, identifier = call_router(copy.deepcopy(messages),tools)
        except Exception as exc:
            if getattr(exc,'code',None) in (401,403):
                raise PermissionError('authenticate first, then retry') from None
            result['transport_error'] = True
            return result
        if not identifier or identifier in request_ids:
            raise ValueError('unique router request identifiers required for cost joins')
        request_ids.append(identifier)
        if message.get('role')!='assistant':raise ValueError('assistant response required')
        calls = message.get('tool_calls',[])
        if not calls:
            if not isinstance(message.get('content'),str):return result
            result['task_success'] = backend.success(state,copy.deepcopy(message)) is True
            return result
        messages.append(copy.deepcopy(message))
        for call in calls:
            try:
                if call.get('type')!='function' or not isinstance(call.get('id'),str) or not call['id']:
                    raise ValueError('identified function call required')
                name = call['function']['name']
                arguments = json.loads(call['function']['arguments'])
                if name not in schemas or not isinstance(arguments,dict):raise ValueError('unknown tool/arguments')
                jsonschema.Draft202012Validator(schemas[name]).validate(arguments)
            except (KeyError,ValueError,TypeError,jsonschema.ValidationError):
                result['tool_schema_valid'] = False
                return result
            value = backend.execute(state,name,arguments)
            messages.append({'role':'tool','tool_call_id':call['id'],
                             'content':json.dumps(value,sort_keys=True,ensure_ascii=False)})
    return result


def attach_cost(record, ledger):
    """Ledger from authenticated router usage export, not model-reported tokens.

    Missing/unknown prices or timeout costs are NOT treated as zero/free.
    """
    ids = record['request_ids']
    if len(ids)!=len(set(ids)):raise ValueError('duplicate request within task')
    known = not record['transport_error'] and bool(ids) and all(
        i in ledger and ledger[i].get('cost_known') is True for i in ids)
    costs = [ledger[i]['router_cost_usd'] for i in ids] if known else []
    if any(isinstance(c,bool) or not isinstance(c,(int,float)) or not math.isfinite(c) or c<0 for c in costs):
        raise ValueError('invalid router cost')
    return {**record,'cost_complete':known,'router_cost_usd':sum(costs) if known else None}


def summarize(records):
    groups, seen_requests = defaultdict(list),set()
    for r in records:
        if r['arm'] not in ('candidate','teacher','base'):raise ValueError('unknown arm')
        if type(r['task_success']) is not bool:raise ValueError('boolean outcome required')
        if len(r['request_ids'])!=len(set(r['request_ids'])) or seen_requests.intersection(r['request_ids']):
            raise ValueError('reused router request ID')
        seen_requests.update(r['request_ids']);groups[r['arm']].append(r)
    cohorts = []
    result = {}
    for arm in ('candidate','teacher','base'):
        rows = groups[arm]
        tasks = {r['task_sha256'] for r in rows}
        if not rows or len(tasks)!=len(rows):raise ValueError('missing/duplicate replay task')
        cohorts.append(tasks)
        successes = sum(r['task_success'] for r in rows)
        complete = all(r['cost_complete'] for r in rows)
        if any(r['cost_complete'] and (isinstance(r['router_cost_usd'],bool)
                or not isinstance(r['router_cost_usd'],(int,float))
                or not math.isfinite(r['router_cost_usd']) or r['router_cost_usd']<0) for r in rows):
            raise ValueError('invalid task cost')
        total = sum(r['router_cost_usd'] for r in rows) if complete else None
        result[arm] = {'tasks':len(rows),'successful_tasks':successes,
            'task_success_rate':successes/len(rows),'router_cost_complete':complete,
            'router_cost_usd':total,
            'router_cost_per_successful_task_usd':total/successes if complete and successes else None}
    if not cohorts[0]==cohorts[1]==cohorts[2]:raise ValueError('arms must replay same frozen tasks')
    return {'arms':result,'scope':'router-recorded model cost estimates, includes failed attempts; not provider invoices or total lifecycle ROI',
            'promotion_decision':None,'gate_status':'new replay protocol/endpoint must be preregistered; this is not the original JSON-text accuracy gate'}
