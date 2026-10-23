#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Router-only SSE load: scalars, no saved prompts/responses; no fabricated TPOT."""
import argparse
import concurrent.futures
import json
import math
import time
import urllib.request
from pathlib import Path
import sys
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from preflight import preflight


def percentile(values, fraction):
    return sorted(values)[max(0, math.ceil(len(values)*fraction)-1)] if values else None


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--token-file', type=Path, required=True)
    p.add_argument('--api-base', default='http://127.0.0.1:18525/v1')
    p.add_argument('--model', required=True)
    p.add_argument('--preflight-targets', nargs='*', default=[], help='static target aliases for custom weighted routes')
    p.add_argument('--hourly-usd', type=float, required=True)
    p.add_argument('--requests', type=int, default=64)
    p.add_argument('--out', type=Path, required=True)
    a = p.parse_args()
    if a.requests < 32 or not math.isfinite(a.hourly_usd) or a.hourly_usd < 0:
        p.error('at least 32 requests and nonnegative hourly cost required')
    token = a.token_file.read_text().strip()
    preflight(a.api_base,token,[a.model,*a.preflight_targets])
    results = []
    def one(_):
        body = {'model':a.model,'messages':[{'role':'user','content':'List the integers from one through one hundred, separated by spaces.'}],
                'temperature':0,'top_p':.95,'top_k':20,'chat_template_kwargs':{'enable_thinking':False},'max_tokens':128,'stream':True,'stream_options':{'include_usage':True}}
        req = urllib.request.Request(a.api_base+'/chat/completions',data=json.dumps(body).encode(),
                headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
        start = time.monotonic(); first = None; tokens = None
        try:
            with urllib.request.urlopen(req,timeout=180) as response:
                for line in response:
                    if not line.startswith(b'data:'):
                        continue
                    raw = line[5:].strip()
                    if raw == b'[DONE]':
                        break
                    frame = json.loads(raw)
                    if frame.get('usage'):
                        tokens = frame['usage'].get('completion_tokens')
                    for choice in frame.get('choices',[]):
                        delta = choice.get('delta',{})
                        if first is None and (delta.get('content') or delta.get('tool_calls')):
                            first = time.monotonic()
            end = time.monotonic()
            return {'error':0,'latency_ms':(end-start)*1000,'ttft_ms':None if first is None else (first-start)*1000,
                    'tpot_ms':(end-first)*1000/(tokens-1) if first and tokens and tokens>1 else None,
                    'output_tokens':tokens}
        except Exception:
            return {'error':1,'latency_ms':(time.monotonic()-start)*1000,'ttft_ms':None,'tpot_ms':None,'output_tokens':None}
    for concurrency in [1,8,32]:
        start = time.monotonic()
        with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
            rows = list(pool.map(one,range(a.requests)))
        seconds = time.monotonic()-start
        measured = all(r['output_tokens'] is not None for r in rows if not r['error']) and any(not r['error'] for r in rows)
        total = sum(r['output_tokens'] or 0 for r in rows)
        throughput = total/seconds if measured else None
        result = {'concurrency':concurrency,'requests':len(rows),'errors':sum(r['error'] for r in rows),
            'wall_seconds':seconds,'output_tokens':total if measured else None,'output_tokens_per_second':throughput,
            'hourly_usd':a.hourly_usd,'usd_per_million_output_tokens':a.hourly_usd*1e6/(throughput*3600) if throughput else None}
        for metric in ['latency_ms','ttft_ms','tpot_ms']:
            values = [r[metric] for r in rows if r[metric] is not None and not r['error']]
            result[metric] = {'n':len(values),'p50':percentile(values,.5),'p95':percentile(values,.95)}
        results.append(result)
        a.out.write_text(json.dumps({'copyright':'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0',
            'model':a.model,'measurements':results,'caveat':'End-to-end router timings; TPOT requires streamed usage. Hourly economics assume fully utilized measured throughput; not invoices.'},indent=2))
        print('concurrency',concurrency,'errors',result['errors'],flush=True)
    if any(row['errors'] for row in results):
        raise SystemExit(1)
    if any(row['output_tokens'] is None for row in results):
        raise SystemExit(2)

if __name__ == '__main__':
    main()
