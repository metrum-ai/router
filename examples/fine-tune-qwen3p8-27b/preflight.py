# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Authenticated, synthetic admission probes before any measurement batch.
No retries, dataset prompts, returned model text or credentials in receipts.
Static aliases cover each target in the example's weighted routes.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import urllib.error
import urllib.request

TARGET_ROUTES = {
    'qwen3p8-27b-ft': ['qwen3p8-27b-ft-onprem','qwen3p8-27b-ft-fireworks'],
    'qwen3p8-27b-canary': ['qwen3p8-27b-base','qwen3p8-27b-ft-onprem','qwen3p8-27b-ft-fireworks'],
    'qwen3p8-27b-ft-shadow': ['qwen3p8-27b-base','qwen3p8-27b-ft-onprem'],
}


class PreflightError(RuntimeError):
    pass


def preflight(api_base, token, models, *, caller='configured-caller', request=None):
    if not token or not token.strip():
        raise PreflightError('authenticated preflight requires a caller key')
    routes=[]
    for model in models:
        for route in [model,*TARGET_ROUTES.get(model,[])]:
            if route not in routes: routes.append(route)
    if not routes: raise PreflightError('preflight requires at least one route')
    receipt=[]
    for route in routes:
        payload={'model':route,'messages':[{'role':'user','content':'PREFLIGHT_ONLY: reply OK.'}],
                 'max_tokens':32,'temperature':0,'stream':False}
        try:
            if request is None:
                req=urllib.request.Request(api_base.rstrip('/')+'/chat/completions',
                    data=json.dumps(payload).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
                with urllib.request.urlopen(req,timeout=180) as response:
                    status=response.status;headers=response.headers
                    response.read() # Never persist or expose returned model text.
            else:
                status,headers,_=request(payload)
        except urllib.error.HTTPError as error:
            raise PreflightError(f'preflight HTTP{error.code} caller={caller} route={route}: abort whole run before batch; no selective retries') from None
        except (urllib.error.URLError,TimeoutError,OSError):
            raise PreflightError('preflight transport failure: abort whole run before batch') from None
        if not 200<=status<300:
            raise PreflightError(f'preflight HTTP{status} caller={caller} route={route}: abort whole run before batch; no selective retries')
        request_id=headers.get('X-Request-Id','')
        receipt.append({'caller':caller,'route':route,'status':status,
            'request_id_sha256':hashlib.sha256(request_id.encode()).hexdigest() if request_id else None,
            'purpose':'synthetic-admission-preflight-not-evaluation'})
    return receipt


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--api-base',required=True);p.add_argument('--token-file',type=Path,required=True)
    p.add_argument('--models',nargs='+',required=True);p.add_argument('--out',type=Path,required=True)
    a=p.parse_args()
    result=preflight(a.api_base,a.token_file.read_text().strip(),a.models)
    a.out.parent.mkdir(parents=True,exist_ok=True)
    fd=os.open(a.out,os.O_CREAT|os.O_WRONLY|os.O_TRUNC,0o600)
    with os.fdopen(fd,'w') as stream:json.dump(result,stream,indent=2)

if __name__=='__main__':main()
