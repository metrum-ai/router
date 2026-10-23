#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Disjoint synthetic answer-format diagnostic through a localhost router.

Not the promotion gate. No answer normalization or held-out selection occurs.
Defaults to dry-run. Raw outputs must remain outside the repository.
"""
import argparse
import hashlib
import json
from pathlib import Path
import random
import re
import urllib.error
import urllib.parse
import urllib.request


def cases():
    rng = random.Random(355999)
    result = []
    for i in range(12):
        a,b = rng.randrange(201,1000),rng.randrange(201,1000)
        total = a+b
        options = rng.sample([n for n in range(total-30,total+31) if n!=total],9)
        options.insert(i%10,total)
        question = f'What is {a} + {b}?\n' + '\n'.join(f'{chr(65+j)}. {v}' for j,v in enumerate(options))
        question += '\nReturn only the letter of the correct option.'
        result.append({'messages':[{'role':'user','content':question}],'expected':chr(65+i%10)})
    return result


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--model',required=True)
    p.add_argument('--api-base',default='http://127.0.0.1:18525/v1')
    p.add_argument('--token-file',type=Path)
    p.add_argument('--preregistration',type=Path,required=True)
    p.add_argument('--out',type=Path,required=True)
    p.add_argument('--raw-out',type=Path)
    p.add_argument('--execute',action='store_true')
    a = p.parse_args()
    if not a.execute:
        print(json.dumps({'dry_run':True,'model':a.model,'synthetic_letter_cases':len(cases())}))
        return
    if urllib.parse.urlparse(a.api_base).hostname not in ('127.0.0.1','localhost','::1'):
        p.error('use the localhost router, not a direct provider endpoint')
    if not a.token_file or not a.raw_out:p.error('--token-file and --raw-out required')
    if a.raw_out.resolve().is_relative_to(Path(__file__).resolve().parents[3]):
        p.error('raw responses must remain outside Git/worktree')
    registration = json.loads(a.preregistration.read_text())
    if registration['status']!='frozen':p.error('freeze preregistration before model calls')
    records = []
    for case in cases():
        body = {'model':a.model,'messages':case['messages'],'temperature':0,'max_tokens':2048,
                'top_p':.95,'top_k':20,'seed':326,'chat_template_kwargs':{'enable_thinking':False}}
        request = urllib.request.Request(a.api_base+'/chat/completions',data=json.dumps(body).encode(),
            headers={'Authorization':'Bearer '+a.token_file.read_text().strip(),'Content-Type':'application/json'})
        try:
            with urllib.request.urlopen(request,timeout=180) as response:result=json.load(response)
        except urllib.error.HTTPError as exc:
            if exc.code in (401,403):raise RuntimeError('authenticate first, then retry') from None
            raise RuntimeError(f'pre-check HTTP {exc.code}; no response body logged') from None
        output = result['choices'][0]['message'].get('content') or ''
        records.append({'output':output,'expected':case['expected'],'response_id':result['id'],
                        'format':bool(re.fullmatch(r'\s*[A-J][.)]?\s*',output)),
                        'correct':bool(re.fullmatch(r'\s*'+case['expected']+r'[.)]?\s*',output))})
    a.raw_out.write_text(json.dumps(records));a.raw_out.chmod(0o600)
    summary = {'copyright':'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0',
        'protocol':registration['protocol'],'model':a.model,'complete':len(records)==12,'case_count':len(records),
        'strict_letter_format':sum(r['format'] for r in records),'correct_letter':sum(r['correct'] for r in records),
        'synthetic_cases_sha256':hashlib.sha256(json.dumps(cases(),sort_keys=True).encode()).hexdigest(),
        'preregistration_sha256':hashlib.sha256(a.preregistration.read_bytes()).hexdigest(),
        'scope':'synthetic development diagnostic; not held-out or promotion evidence; no retuning on these results'}
    a.out.write_text(json.dumps(summary,indent=2)+'\n')
    print(json.dumps({k:summary[k] for k in ('model','complete','case_count','strict_letter_format','correct_letter')}))


if __name__ == '__main__':
    main()
