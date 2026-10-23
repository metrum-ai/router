#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Credential-safe marketplace API adapter. Mutations require --execute.

Payloads contain public SSH keys only. Keep returned runtime state outside Git.
"""
import argparse
import json
import os
import urllib.parse
import urllib.request


def request(cloud, path, method='GET', payload=None, execute=False):
    if isinstance(payload, dict):
        payload = {k:v for k,v in payload.items() if k != '_comment'}
    if method != 'GET' and not execute:
        return {'dry_run': True, 'cloud': cloud, 'path': path, 'method': method, 'payload': payload}
    if cloud == 'vast':
        base = 'https://console.vast.ai/api/v0'
        key = os.environ.get('VAST_AI_API_KEY') or os.environ['VAST_API_KEY']
        headers = {'Authorization': 'Bearer ' + key}
    else:
        base = 'https://api.shadeform.ai/v1'
        headers = {'X-API-KEY': os.environ['SHADEFORM_API_KEY']}
    headers['Content-Type'] = 'application/json'
    req = urllib.request.Request(base + path, headers=headers, method=method,
                                 data=None if payload is None else json.dumps(payload).encode())
    with urllib.request.urlopen(req, timeout=90) as response:
        body = response.read()
        return json.loads(body) if body else {}


def redact(value):
    if isinstance(value, dict):
        return {k: ('[REDACTED]' if any(s in k.lower() for s in ('key', 'token', 'password', 'secret')) else redact(v)) for k, v in value.items()}
    if isinstance(value, list):
        return [redact(v) for v in value]
    return value


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('cloud', choices=['vast', 'shadeform'])
    p.add_argument('action', choices=['offers', 'create', 'info', 'delete'])
    p.add_argument('--id')
    p.add_argument('--payload', help='JSON path; no credentials')
    p.add_argument('--execute', action='store_true')
    a = p.parse_args()
    payload = json.load(open(a.payload)) if a.payload else None
    if a.action in ('info', 'delete') and not a.id:
        p.error('--id required')
    if a.action == 'create' and payload is None:
        p.error('--payload required')
    if a.cloud == 'vast':
        if a.action == 'offers':
            q = {'gpu_name': {'eq': 'B200'}, 'num_gpus': {'eq': 1}, 'rentable': {'eq': True},
                 'rented': {'eq': False}, 'disk_space': {'gte': 500}, 'allocated_storage': 500,
                 'inet_down': {'gte': 1000}, 'inet_up': {'gte': 1000}, 'cuda_max_good': {'gte': 12.8},
                 'order': [['dph_total', 'asc']], 'limit': 20, 'type': 'on-demand'}
            path, method = '/bundles/?' + urllib.parse.urlencode({'q': json.dumps(q)}), 'GET'
        elif a.action == 'create':
            if not a.id:
                p.error('--id offer required')
            path, method = f'/asks/{a.id}/', 'PUT'
        else:
            path, method = f'/instances/{a.id}/', 'DELETE' if a.action == 'delete' else 'GET'
    else:
        path, method = {'offers': ('/instances/types?available=true', 'GET'),
                        'create': ('/instances/create', 'POST'),
                        'info': (f'/instances/{a.id}/info', 'GET'),
                        'delete': (f'/instances/{a.id}/delete', 'POST')}[a.action]
    print(json.dumps(redact(request(a.cloud, path, method, payload, a.execute)), indent=2))


if __name__ == '__main__':
    main()
