#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Synthetic live canary/shadow routing and prompt-absence proof, not quality."""
import argparse
import collections
import concurrent.futures
import json
import re
import sqlite3
import time
import urllib.request
from pathlib import Path
import sys
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from preflight import preflight


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--token-file',type=Path,required=True)
    p.add_argument('--usage-db',type=Path,required=True)
    p.add_argument('--router-log',type=Path,required=True)
    p.add_argument('--out',type=Path,required=True)
    p.add_argument('--preflight-targets',nargs='*',default=[],help='static target aliases for custom route profiles')
    p.add_argument('--requests',type=int,default=100)
    a=p.parse_args()
    if not 32<=a.requests<=1000:p.error('bounded synthetic request count32..1000 required')
    token=a.token_file.read_text().strip()
    preflight('http://127.0.0.1:18525/v1',token,['qwen3p8-27b-canary','qwen3p8-27b-ft-shadow',*a.preflight_targets])
    marker='FT_GOVERNANCE_PROMPT_ABSENCE_326'
    def call(model):
        body={'model':model,'messages':[{'role':'user','content':'Reply only OK. '+marker}],
              'temperature':0,'max_tokens':32,'chat_template_kwargs':{'enable_thinking':False}}
        req=urllib.request.Request('http://127.0.0.1:18525/v1/chat/completions',data=json.dumps(body).encode(),
            headers={'Content-Type':'application/json','Authorization':'Bearer '+token})
        with urllib.request.urlopen(req,timeout=180) as response:
            assert json.load(response).get('choices')
            request_id=response.headers.get('X-Request-Id')
        assert request_id and re.fullmatch(r'req_[A-Za-z0-9_-]{1,100}',request_id)
        return request_id
    with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
        ids=list(pool.map(call,['qwen3p8-27b-canary']*a.requests))
    shadow_id=call('qwen3p8-27b-ft-shadow')
    connection=sqlite3.connect(a.usage_db.resolve().as_uri()+'?mode=ro',uri=True)
    counts=collections.Counter()
    for request_id in ids+[shadow_id]:
        row=None
        for _ in range(30):
            row=connection.execute('SELECT target_provider,target_model,status FROM request_usage WHERE request_id=?',(request_id,)).fetchone()
            if row:break
            time.sleep(.1)
        assert row and row[2]==200, 'missing or unsuccessful telemetry'
        if request_id==shadow_id:
            assert row[0]=='vllm_b200_ft' and row[1]=='qwen3p8-27b-base', 'shadow must serve base'
        else:
            public_model=re.sub(r'accounts/(?!fireworks/)[^/]+/','accounts/OPERATOR/',row[1])
            counts[row[0]+'/'+public_model]+=1
    connection.close()
    artifacts=[a.router_log,a.usage_db,Path(str(a.usage_db)+'-wal')]
    assert all(marker.encode() not in path.read_bytes() for path in artifacts if path.exists())
    assert any(b'shadow_recommended_candidate' in path.read_bytes() for path in artifacts if path.exists())
    a.out.write_text(json.dumps({'copyright':'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0',
        'synthetic_only':True,'requests':len(ids),'configured_weights':'base90/onpremFT5/FireworksFT5',
        'observed_targets':dict(counts),'shadow_baseline':'qwen3p8-27b-base','shadow_recommendation_recorded':True,
        'prompt_marker_absent':True,'router_request_ids':ids,'shadow_request_id':shadow_id,
        'caveat':'Finite observed routing sample, not an exact10% promise or model-quality evidence. Provider account IDs redacted.'},indent=2))

if __name__=='__main__':main()
