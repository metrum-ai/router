#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Fireworks LoRA upload/deploy REST equivalent. Dry-run unless --execute.

Use a dedicated deployment, not serverless; delete it promptly. No API tokens or
signed upload URLs are printed or persisted. This fallback does not start SFT.
"""
import argparse
import json
import os
import urllib.request
import urllib.error
import urllib.parse
import time
from pathlib import Path


def api(path, payload=None, method='POST'):
    req = urllib.request.Request('https://api.fireworks.ai/v1/' + path,
        data=None if payload is None else json.dumps(payload).encode(), method=method,
        headers={'Authorization': 'Bearer ' + os.environ['FIREWORKS_API_KEY'], 'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=120) as r:
        body = r.read()
        return json.loads(body) if body else {}


def upload(account, model, adapter, resume=False):
    config = json.loads((adapter / 'adapter_config.json').read_text())
    rank = config['r']
    if type(rank) is not int or rank not in (8, 16):
        raise ValueError('expected rank 8 (run2) or 16 (run1)')
    files = {p.name: p for p in adapter.iterdir() if p.name in ('adapter_config.json', 'adapter_model.safetensors')}
    if len(files) != 2:
        raise ValueError('missing adapter artifacts')
    path = f'accounts/{account}/models/{model}'
    if not resume:
        api(f'accounts/{account}/models', {'modelId': model, 'model': {'kind': 'HF_PEFT_ADDON',
            'peftDetails': {'baseModel': 'accounts/fireworks/models/qwen3p8-27b', 'r': rank,
                            'targetModules': config['target_modules']}}})
    else:
        current = api(path, method='GET')
        details = current.get('peftDetails', {})
        if (details.get('baseModel') != 'accounts/fireworks/models/qwen3p8-27b'
                or details.get('r') != rank
                or set(details.get('targetModules', [])) != set(config['target_modules'])):
            raise ValueError('refusing to resume an adapter with different base/rank/target modules')
    endpoints = api(path + ':getUploadEndpoint', {'filenameToSize': {n: p.stat().st_size for n, p in files.items()}})
    for name, url in endpoints['filenameToSignedUrls'].items():
        # Signed URL remains in memory only. Never include it in error logs.
        try:
            size = files[name].stat().st_size
            req = urllib.request.Request(url, data=files[name].read_bytes(), method='PUT',
                headers={'Content-Type': 'application/octet-stream',
                         'x-goog-content-length-range': f'{size},{size}'})
            with urllib.request.urlopen(req, timeout=300) as response:
                response.read()
        except Exception as exc:
            raise RuntimeError(f'adapter upload failed ({type(exc).__name__}, HTTP {getattr(exc, "code", "n/a")}; signed URL omitted)') from None
    validation = api(path + ':validateUpload', method='GET')
    print('upload validation warnings:', len(validation.get('warnings', [])))
    print('adapter uploaded; poll model state until READY before deploying')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('action', choices=['upload', 'deploy', 'status', 'delete'])
    p.add_argument('--account', default=os.environ.get('FIREWORKS_ACCOUNT_ID'))
    p.add_argument('--model', default='qwen3p8-27b-metrum-ft')
    p.add_argument('--adapter', type=Path)
    p.add_argument('--deployment', default='metrum-ft-qwen3p8')
    p.add_argument('--execute', action='store_true')
    p.add_argument('--resume-upload', action='store_true', help='explicitly resume an existing same-base adapter owned by this operator')
    p.add_argument('--ignore-recent-inference', action='store_true', help='delete only: explicitly bypass the recent-inference guard for your owned disposable deployment')
    a = p.parse_args()
    if a.ignore_recent_inference and a.action != 'delete':p.error('--ignore-recent-inference is delete-only')
    if not a.execute:
        print(json.dumps({'dry_run': True, 'action': a.action, 'model': a.model, 'deployment': a.deployment,
            'ignore_recent_inference':a.ignore_recent_inference}))
        return
    if not a.account:
        p.error('set FIREWORKS_ACCOUNT_ID or --account')
    path = f'accounts/{a.account}/deployments'
    if a.action == 'upload':
        if a.adapter is None:
            p.error('--adapter required')
        upload(a.account, a.model, a.adapter, a.resume_upload)
    elif a.action == 'deploy':
        result = api(path + '?' + urllib.parse.urlencode({'deploymentId': a.deployment}),
            {'baseModel': f'accounts/{a.account}/models/{a.model}', 'deploymentShape': 'default',
             'minReplicaCount': 1, 'maxReplicaCount': 1})
        print(json.dumps({k: result.get(k) for k in ['name', 'state']}))
    elif a.action == 'status':
        result = api(path + '/' + a.deployment, method='GET')
        print(json.dumps({k: result.get(k) for k in ['name', 'state']}))
    else:
        resource = path + '/' + a.deployment
        def deleted():
            try:return api(resource,method='GET').get('state') == 'DELETED'
            except urllib.error.HTTPError as exc:
                if exc.code == 404:return True
                raise
        if not deleted():
            delete_path = resource + ('?ignore_checks=true' if a.ignore_recent_inference else '')
            api(delete_path, method='DELETE')
        # A DELETE acknowledgment is not teardown verification; poll the exact ID.
        # Do not repeatedly DELETE: tombstones can reject DELETE yet GET DELETED.
        for _ in range(60):
            if deleted():break
            time.sleep(3)
        else:
            raise RuntimeError('deployment deletion not verified')
        print('deployment deletion verified')

if __name__ == '__main__':
    main()
