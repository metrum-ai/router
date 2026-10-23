# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Explicit seed355 full-cohort producer. Never train public seed answers.
Counts/sampling/gate mapping agreed in reports/ft-amd-freeze.md. Secrets env only.
"""
import argparse
import concurrent.futures
import hashlib
import io
import json
import os
from pathlib import Path
import random
import re
import sqlite3
import subprocess
import threading
import time
import unicodedata
import urllib.error
import urllib.request
import zipfile
from jsonschema import Draft202012Validator
import backend
import collect
import local

MATRIX = {'train': {'native_tool':1000,'reviewed_general':1500,'computed_letter':500},
          'val': {'native_tool':167,'reviewed_general':250,'computed_letter':83},
          'test': {'native_tool':200,'reviewed_general':75,'computed_letter':25}}
SEED_SHA = 'e670ccdf15663a38264c673bda38e09e2c9f239f643317740553d25374ae8b81'
PHONE = re.compile(r'\+?[0-9][0-9 ()-]{7,}[0-9]')
EMAIL = re.compile(r'[a-z0-9.!#$%&\'*+/=?^_`{|}~-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)+', re.I)
SECRET = re.compile(r'\b(?:sk-[A-Za-z0-9_-]{8,}|rtr_[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{8,}|[a-f0-9]{64})\b', re.I)


def sha(path): return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def user_hash(row):
    text='\n'.join(m.get('content','') for m in row['messages'] if m['role']=='user')
    return hashlib.sha256(' '.join(unicodedata.normalize('NFKC',text).casefold().split()).encode()).hexdigest()
def private_json(path, value):
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
    with os.fdopen(fd,'w') as f: json.dump(value,f,indent=2)


def prepare(pool, out):
    assert sha(pool/'seed-canonical.jsonl')==SEED_SHA
    classes=json.loads((pool/'class-by-normalized-user-sha256.json').read_text())
    source=json.loads((pool/'manifest.json').read_text())
    buckets={k:[] for k in MATRIX['train']}; rejected={k:0 for k in buckets};seen=set()
    for line in (pool/'seed-canonical.jsonl').read_text().splitlines():
        row=json.loads(line);key=user_hash(row);kind=classes[key]
        try:
            assert key not in seen;seen.add(key)
            initial=[m for m in row['messages'] if m['role'] in ('system','user')]
            tools=row.get('tools',[])
            text=json.dumps({'messages':initial,'tools':tools},ensure_ascii=False)
            assert len(text.encode())<=16000 and not PHONE.search(text) and not EMAIL.search(text) and not SECRET.search(text)
            backend.local_refs(tools)
            expected=[{'name':c['function']['name'],'arguments':json.loads(c['function']['arguments'])}
                      for m in row['messages'] for c in m.get('tool_calls',[])]
            for call in expected:
                schema=next(t['function']['parameters'] for t in tools if t['function']['name']==call['name'])
                Draft202012Validator.check_schema(schema);Draft202012Validator(schema).validate(call['arguments'])
            final=row['messages'][-1].get('content') or ''
            anchor=final.strip() if kind=='computed_letter' else ''
            if kind=='computed_letter': assert len(anchor)==1 and anchor in 'ABCDEFGHIJ'
            task={'source_user_sha256':key,'class':kind,'initial_messages':initial,'tools':tools,
                  'expected_calls':expected,'expected_final':anchor,'reference':final,
                  'task_hash':collect.digest({'initial':initial,'tools':tools})}
            buckets[kind].append(task)
        except Exception: rejected[kind]+=1
    print(json.dumps({'eligible_before_selection':{k:len(v) for k,v in buckets.items()},'rejected_before_selection':rejected}),flush=True)
    rng=random.Random(355)
    for rows in buckets.values(): rows.sort(key=lambda r:r['source_user_sha256']);rng.shuffle(rows)
    selected={};offsets={k:0 for k in buckets};letters={a:[r for r in buckets['computed_letter'] if r['expected_final']==a] for a in 'ABCDEFGHIJ'};letter_offsets={a:0 for a in letters}
    for split,counts in MATRIX.items():
        selected[split]=[]
        for kind,n in counts.items():
            if kind=='computed_letter':
                for index,a in enumerate(letters):
                    count=n//10+(index<n%10);start=letter_offsets[a];chosen=letters[a][start:start+count]
                    assert len(chosen)==count;letter_offsets[a]+=count;selected[split].extend(chosen)
            else:
                start=offsets[kind];chosen=buckets[kind][start:start+n];assert len(chosen)==n,(kind,len(chosen),n)
                offsets[kind]+=n;selected[split].extend(chosen)
        rng.shuffle(selected[split])
        for index,task in enumerate(selected[split]): task['seed']=3550000+sum(sum(x.values()) for s,x in MATRIX.items() if list(MATRIX).index(s)<list(MATRIX).index(split))+index
    private_json(out/'selected-plan.json',selected)
    private_json(out/'heldout-gold.json',{t['task_hash']:t for t in selected['test']})
    private_json(out/'precollection-freeze.json',{'matrix':MATRIX,'source_manifest':source,'seed':355,'seed_file_sha256':SEED_SHA,
        'selected_plan_sha256':sha(out/'selected-plan.json'),'heldout_gold_sha256':sha(out/'heldout-gold.json'),
        'max_turns':8,'max_tokens':2048,'concurrency':8,'temperature':0,'top_p':.95,'top_k':20,
        'teacher_thinking':'gpt-oss disable-thinking unsupported; native final content only in training, no reasoning_content labels',
        'no_model_retries_or_score_refills':True,'time_split_schedule':'fixed train -> barrier -> val -> barrier -> test; freeze actual UTC phase boundaries, no outcome-dependent reassignment',
        'budget_usd':20,'previous_collection_estimate_usd':.03170595,
        'code_sha256':{p.name:sha(p) for p in Path(__file__).parent.glob('*.py')}})
    return selected


class Ledger:
    def __init__(self): self.lock=threading.Lock();self.spent=.03170595;self.reserved=0;self.calls=0
    def reserve(self,payload):
        amount=(len(json.dumps(payload,ensure_ascii=False).encode())+4096)*.15/1e6+2048*.6/1e6
        with self.lock:
            if self.spent+self.reserved+amount>19.50 or self.calls>=9400: raise RuntimeError('predispatch budget/usage-row bound reached')
            self.reserved+=amount;self.calls+=1
        return amount
    def settle(self,amount,response):
        usage=response.get('usage') or {};cost=usage.get('prompt_tokens',0)*.15/1e6+usage.get('completion_tokens',0)*.6/1e6
        if not usage: cost=amount # conservative reservation when error accounting unavailable
        with self.lock: self.reserved-=amount;self.spent+=max(cost,0)


def routed(base,key,payload,ledger):
    reservation=ledger.reserve(payload)
    try:
        status,headers,raw=collect.request(base,'/v1/chat/completions',payload,key)
        response=json.loads(raw);ledger.settle(reservation,response)
        return status,headers,response
    except urllib.error.HTTPError as error:
        ledger.settle(reservation,{})
        return error.code,error.headers,{}
    except Exception:
        ledger.settle(reservation,{})
        raise


def episode(base,key,task,ledger):
    messages=json.loads(json.dumps(task['initial_messages']));actual=[];valid=True;ids=[];success=False;finish='turn_limit'
    for turn in range(8):
        payload={'model':'support-agent','messages':messages,'tools':task['tools'],'max_tokens':2048,'temperature':0,'top_p':.95,'top_k':20,'stream':False}
        status,headers,response=routed(base,key,payload,ledger)
        identifier=headers.get('X-Request-Id')
        if not identifier: raise RuntimeError('router response missing request ID; cannot label capture')
        ids.append(identifier)
        if status!=200: finish='http_'+str(status);valid=False;break
        choice=response['choices'][0];message=choice['message'];calls=message.get('tool_calls') or []
        messages.append(message);finish=choice.get('finish_reason','unknown')
        if not calls:
            content=message.get('content') or ''
            success=valid and actual==task['expected_calls'] and finish=='stop' and bool(content) and (not task['expected_final'] or content.strip()==task['expected_final'])
            break
        for call in calls:
            try:
                result,checked=collect.execute(call,task['tools'],task['seed']);actual.append(checked)
            except Exception: valid=False;result={'ok':False,'mock':True,'error':'invalid_tool_call'}
            messages.append({'role':'tool','tool_call_id':call['id'],'content':json.dumps(result)})
    label={'version':'router-outcome-v1','task_hash':task['task_hash'],'task_success':success,'schema_valid':valid,'quality':float(success),
           'backend':'acme-support-v1','seed':task['seed'],'expected_calls':task['expected_calls'],'expected_final':task['expected_final']}
    for attempt in range(120):
        try:
            collect.request(base,'/admin/training/outcomes/'+ids[-1],label);captured=True;break
        except urllib.error.HTTPError as error:
            if error.code!=404: raise
            captured=False
            if key==os.environ.get('TRACE_IT_KEY'): break
            time.sleep(.1)
    return {'task_hash':task['task_hash'],'class':task['class'],'seed':task['seed'],'success':success,'schema_valid':valid,
            'finish_reason':finish,'request_ids':ids,'captured':captured,'turns':len(ids)}


def run(args,env,selected):
    os.environ.update({k:v for k,v in env.items() if k.startswith('TRACE_')})
    ledger=Ledger();boundaries=[collect.now()];all_results=[];base='http://127.0.0.1:18410'
    with (args.out/'episode-ledger.jsonl').open('a') as log:
        for split,tasks in selected.items():
            with concurrent.futures.ThreadPoolExecutor(max_workers=8) as executor:
                futures={executor.submit(episode,base,env['TRACE_SUPPORT_KEY'],t,ledger):t for t in tasks}
                for future in concurrent.futures.as_completed(futures):
                    result=future.result();assert result['captured'];result.update(split=split,caller='acme-support');all_results.append(result)
                    log.write(json.dumps(result)+'\n');log.flush()
                    if len(all_results)%50==0:
                        private_json(args.out/'progress.json',{'completed':len(all_results),'last_split':split,'calls':ledger.calls,'conservative_estimated_spend_usd':ledger.spent})
                        print(json.dumps({'completed':len(all_results),'phase':split,'spend_upper_estimate':round(ledger.spent,4)}),flush=True)
            # Fixed three controls per phase, selected before model outcomes.
            for kind in MATRIX[split]:
                task=next(t for t in tasks if t['class']==kind)
                result=episode(base,env['TRACE_IT_KEY'],task,ledger);assert not result['captured']
                result.update(split=split,caller='acme-it');all_results.append(result);log.write(json.dumps(result)+'\n');log.flush()
            time.sleep(.5);boundaries.append(collect.now())
    source={'license':'Apache-2.0','seed_file_sha256':SEED_SHA,'glaive_revision':collect.SOURCE['revision'],
            'general_revision':'fdf72ae0827c1cda404aff25b6603abec9e3399b','selected_plan_sha256':sha(args.out/'selected-plan.json'),
            'heldout_gold_sha256':sha(args.out/'heldout-gold.json'),'purpose':'source answers checker/eval gold ONLY; training answers NEW router captures'}
    filters={'project':'acme-support','group':'support-agent','from':boundaries[0],'validation_from':boundaries[1],'test_from':boundaries[2],'to':boundaries[3],
             'success_only':True,'min_quality':1,'teacher_terms_decision':collect.DECISION,'task_source':source}
    private_json(args.out/'export-request.json',filters)
    _,_,raw=collect.request(base,'/admin/training/export',filters)
    dataset=args.out/'dataset';dataset.mkdir(mode=0o700)
    (dataset/'dataset.zip').write_bytes(raw)
    manifest=collect.validate_archive(raw)
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        for name in ['manifest.json','train.jsonl','val.jsonl','test.jsonl']: (dataset/name).write_bytes(archive.read(name))
    rows={s:[json.loads(line) for line in (dataset/(s+'.jsonl')).read_text().splitlines()] for s in MATRIX}
    assert len(rows['test'])==300 and sum(bool(r['tools']) for r in rows['test'])==200
    assert {r['provenance']['task_hash'] for r in rows['test']}=={t['task_hash'] for t in selected['test']}
    it_ids={hashlib.sha256(i.encode()).hexdigest() for r in all_results if r['caller']=='acme-it' for i in r['request_ids']}
    assert not any(r['provenance']['trace_hash'] in it_ids for group in rows.values() for r in group)
    with sqlite3.connect(args.out/'usage.sqlite') as db:
        assert db.execute("SELECT COUNT(*) FROM request_content_captures WHERE caller_id='acme-it-prod'").fetchone()[0]==0
    private_json(args.out/'delivery.json',{'dataset_hash':manifest['dataset_hash'],'manifest_sha256':sha(dataset/'manifest.json'),
        'counts':{s:len(r) for s,r in rows.items()},'test_teacher_failures':sum(not r['outcome']['task_success'] for r in rows['test']),
        'all_300_test_groups_retained':True,'acme_it_absent_capture_and_export':True,'conservative_estimated_total_spend_usd':ledger.spent,
        'heldout_gold_sha256':sha(args.out/'heldout-gold.json'),'matrix':MATRIX})
    print((args.out/'delivery.json').read_text(),flush=True)


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--pool',type=Path,required=True);p.add_argument('--out',type=Path,required=True)
    p.add_argument('--binary',type=Path);p.add_argument('--plan-only',action='store_true');args=p.parse_args()
    args.out=args.out.resolve();args.out.mkdir(parents=True,mode=0o700,exist_ok=True)
    selected=prepare(args.pool,args.out)
    if args.plan_only:return
    assert args.binary and os.environ.get('FIREWORKS_API_KEY')
    env=dict(os.environ);cfg=local.config(args.out,env)
    for caller in cfg['callers']:
        caller['rate']={'rpm':2000,'tpm':2000000,'concurrent':8}
        caller['quota']={'day':{'requests':10000,'tokens':30000000},'month':{'requests':10000,'tokens':30000000}}
    private_json(args.out/'runtime-local-env.json',{k:v for k,v in env.items() if k.startswith('TRACE_') or k=='CONTENT_CAPTURE_LOCAL_KEY'})
    private_json(args.out/'config.json',cfg)
    with (args.out/'router.log').open('w') as log:
        router=subprocess.Popen([str(args.binary.resolve()),'-config',str(args.out/'config.json')],env=env,stdout=log,stderr=log)
        try:
            for _ in range(120):
                if router.poll() is not None:raise RuntimeError('router startup failed')
                try:urllib.request.urlopen('http://127.0.0.1:18410/healthz',timeout=1).close();break
                except Exception:time.sleep(.25)
            run(args,env,selected)
        finally:
            router.terminate()
            try:router.wait(timeout=30)
            except subprocess.TimeoutExpired:router.kill();router.wait()

if __name__=='__main__':main()
