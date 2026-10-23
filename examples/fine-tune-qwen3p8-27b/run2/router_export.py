#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Consume proposed #358 canonical trace export; never fetch training data.

Contract v1 is a coordination proposal, NOT yet confirmed by the exporter agent.
Hashes/attestations validate the handoff, not independent proof of consent or PII
absence. The exporter remains responsible for authorization/redaction/auditing.
"""
import argparse
import hashlib
import json
import re
import unicodedata
from pathlib import Path

import jsonschema

FORMAT = 'metrum-router-ft-export-v1'
ROOT = Path(__file__).resolve().parents[1]


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def chat(row, require_complete=True):
    value = {key: row[key] for key in ('messages','tools') if key in row}
    jsonschema.Draft202012Validator(json.loads((ROOT/'schema.json').read_text())).validate(value)
    if not require_complete:
        return value  # Preserve historical teacher failures; never replay their answers.
    pending = set()
    for message in value['messages']:
        if message['role'] == 'tool':
            identifier = message.get('tool_call_id')
            if identifier not in pending:
                raise ValueError('orphan tool result')
            pending.remove(identifier)
        for call in message.get('tool_calls',[]):
            if call['id'] in pending or not isinstance(json.loads(call['function']['arguments']),dict):
                raise ValueError('duplicate pending call or non-object arguments')
            pending.add(call['id'])
    if pending:
        raise ValueError('incomplete captured tool continuation')
    return value


def load_export(directory):
    manifest_path = directory/'manifest.json'
    manifest = json.loads(manifest_path.read_text())
    if manifest.get('format') != FORMAT or manifest.get('source',{}).get('kind') != 'router_capture':
        raise ValueError('versioned router-capture export required; direct-download inputs refused')
    for key in ('export_id','filters','split_boundaries','audit_event_ref','teacher_terms'):
        if not manifest.get(key):
            raise ValueError('export provenance/policy metadata incomplete')
    if manifest['teacher_terms'].get('training_outputs_allowed') is not True:
        raise ValueError('teacher output training rights not approved')
    if any(manifest.get('privacy',{}).get(k) is not True for k in ('capture_opt_in','redaction_applied','pii_filter_applied')):
        raise ValueError('export privacy attestations missing')
    rows, groups, prompts, times = {}, set(), set(), {}
    for split in ('train','val','test'):
        meta = manifest['splits'][split]
        path = directory/f'{split}.jsonl'
        if sha(path) != meta['sha256']:
            raise ValueError('export file checksum mismatch')
        rows[split] = [json.loads(line) for line in path.read_text().splitlines()]
        if not rows[split] or len(rows[split]) != meta['count']:
            raise ValueError('empty export split or row-count mismatch')
        local_groups, local_prompts, local_times = set(), set(), []
        for row in rows[split]:
            canonical = chat(row,require_complete=split!='test')
            if split!='test' and any(re.search(r'<\|[^<>]*\|>|</?(?:think|tool_call)>',json.dumps(m))
                    for m in canonical['messages'] if m['role']=='assistant'):
                raise ValueError('training labels contain foreign control/thinking tags; collect nonthinking final outputs')
            provenance = row['provenance']
            for k in ('trace_sha256','task_sha256'):
                h = provenance[k]
                if len(h)!=64 or any(c not in '0123456789abcdef' for c in h):
                    raise ValueError('trace/task hashes required, not raw identifiers')
            local_groups.update((provenance['trace_sha256'],provenance['task_sha256']))
            initial = [m.get('content','') for m in canonical['messages'] if m['role']=='user']
            local_prompts.add(unicodedata.normalize('NFKC',json.dumps(initial,ensure_ascii=False)).casefold())
            from datetime import datetime
            t = datetime.fromisoformat(provenance['captured_at'].replace('Z','+00:00'))
            if t.tzinfo is None:raise ValueError('UTC-aware capture time required')
            local_times.append(t)
            if split in ('train','val') and (row['outcome'].get('task_success') is not True
                    or row['outcome'].get('tool_schema_valid') is not True):
                raise ValueError('unsuccessful/invalid training trace')
            if split=='test' and not row.get('replay'):
                raise ValueError('heldout deterministic replay descriptor required')
        if groups & local_groups or prompts & local_prompts:
            raise ValueError('source-task/trace/prompt crosses splits')
        groups.update(local_groups);prompts.update(local_prompts)
        times[split] = (min(local_times),max(local_times))
    if times['train'][1]>=times['val'][0] or times['val'][1]>=times['test'][0]:
        raise ValueError('strict time-based train/val/test separation required')
    return manifest,rows


def prepare(directory, out):
    if out.resolve().is_relative_to(ROOT.parents[1]):
        raise ValueError('raw canonical outputs must remain outside Git')
    manifest,rows = load_export(directory)
    out.mkdir(parents=True,exist_ok=False,mode=0o700)
    hashes, views = {}, {}
    def write(name, values):
        path = out/name
        path.write_text(''.join(json.dumps(r,sort_keys=True,ensure_ascii=False)+'\n' for r in values))
        path.chmod(0o600)
        return sha(path)
    for split, items in rows.items():
        canonical = [chat(r,require_complete=split!='test') for r in items]
        hashes[split] = write(split+'.jsonl',canonical)
        if split!='test':
            views[split] = write(split+'-axolotl.jsonl',[
                {**r,'tools':json.dumps(r.get('tools',[]),sort_keys=True,ensure_ascii=False)} for r in canonical])
    # Keep complete captured envelopes for CPU replay, never train on provenance.
    write('replay-test.jsonl',rows['test'])
    (out/'router-export-manifest.json').write_bytes((directory/'manifest.json').read_bytes())
    result = {'source':{'kind':'router_capture','export_id':manifest['export_id']},
        'router_export_manifest_sha256':sha(directory/'manifest.json'),
        'sha256':hashes,'axolotl_view_sha256':views,'counts':{k:len(v) for k,v in rows.items()},
        'contract_status':'proposed; exporter-agent confirmation and new preregistration required',
        'training_authorized':False}
    (out/'manifest.json').write_text(json.dumps(result,indent=2)+'\n')
    return result


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--export',type=Path,required=True)
    p.add_argument('--out',type=Path,required=True)
    a = p.parse_args()
    try:
        result = prepare(a.export,a.out)
    except (ValueError,KeyError,TypeError,jsonschema.ValidationError):
        p.exit(2,'export validation failed; inspect private inputs (raw values not logged)\n')
    print(json.dumps({'validated':True,'counts':result['counts'],'training_authorized':False}))


if __name__=='__main__':main()
