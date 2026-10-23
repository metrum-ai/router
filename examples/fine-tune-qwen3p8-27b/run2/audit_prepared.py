#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Audit actual Axolotl input/label cache against unchanged base HF rendering.

Remote GPU host only; tokenizer metadata/Arrow rows, no neural weights loaded.
Publish scalars/hashes, never token IDs, prompts or references.
"""
import argparse
from collections import Counter
import hashlib
import importlib.util
import json
from pathlib import Path


def ids_hash(ids):
    return hashlib.sha256(json.dumps(list(ids),separators=(',',':')).encode()).hexdigest()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--remote-gpu-host',action='store_true')
    p.add_argument('--workspace',type=Path,default=Path('/data'))
    p.add_argument('--split',choices=['train','val'],required=True)
    p.add_argument('--cache',type=Path,required=True)
    p.add_argument('--out',type=Path,required=True)
    a = p.parse_args()
    if not a.remote_gpu_host:p.error('run on the remote GPU host')
    registration_path = a.workspace/'run2/preregistration.json'
    registration = json.loads(registration_path.read_text())
    canonical = a.workspace/'canonical'/f'{a.split}.jsonl'
    canonical_sha = hashlib.sha256(canonical.read_bytes()).hexdigest()
    if registration['status']!='frozen' or canonical_sha!=registration['datasets']['sha256'][a.split]:
        p.error('frozen canonical inputs required')
    spec = importlib.util.spec_from_file_location('base_template_contract',Path(__file__).with_name('template_check.py'))
    contract = importlib.util.module_from_spec(spec);spec.loader.exec_module(contract)
    from datasets import load_from_disk
    from transformers import AutoTokenizer
    tokenizer = AutoTokenizer.from_pretrained(str(a.workspace/'base'),local_files_only=True,trust_remote_code=False)
    if hashlib.sha256(tokenizer.chat_template.encode()).hexdigest()!=registration['chat_template']['sha256']:
        p.error('loaded template differs from pinned base')
    expected, native = Counter(),0
    for line in canonical.read_text().splitlines():
        row = json.loads(line)
        ids = tokenizer.apply_chat_template(contract.template_messages(row['messages']),tools=row.get('tools',[]),
            tokenize=True,add_generation_prompt=False,enable_thinking=False)
        expected[ids_hash(ids)]+=1
        native += bool(row['messages'][-1].get('tool_calls'))
    observed = Counter()
    marker = tokenizer.convert_tokens_to_ids('<tool_call>')
    dataset = load_from_disk(str(a.cache))
    max_length = max_supervised = supervised_total = marked_rows = empty_labels = 0
    for row in dataset:
        observed[ids_hash(row['input_ids'])]+=1
        labels = [token for token in row['labels'] if token!=-100]
        max_length = max(max_length,len(row['input_ids']))
        max_supervised = max(max_supervised,len(labels))
        supervised_total += len(labels)
        marked_rows += marker in labels
        empty_labels += not labels
    passed = (expected==observed and len(dataset)==registration['datasets']['counts'][a.split]
              and marked_rows==native and not empty_labels and max_length<=8192)
    result = {'copyright':'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0',
        'protocol':registration['protocol'],'split':a.split,'pass':passed,'rows':len(dataset),
        'all_inputs_equal_base_template':expected==observed,'expected_native_tool_rows':native,
        'supervised_native_tool_rows':marked_rows,'empty_supervised_rows':empty_labels,
        'max_input_tokens':max_length,'supervised_tokens':supervised_total,'max_supervised_tokens':max_supervised,
        'canonical_sha256':canonical_sha,'preregistration_sha256':hashlib.sha256(registration_path.read_bytes()).hexdigest(),
        'input_multiset_sha256':hashlib.sha256(json.dumps(sorted(observed.items())).encode()).hexdigest(),
        'scope':'exact full-input token multiset parity with frozen base template; supervised native marker coverage, not tool accuracy'}
    a.out.write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps({'pass':passed,'split':a.split,'rows':len(dataset),'supervised_native_tool_rows':marked_rows}))
    return 0 if passed else 1


if __name__ == '__main__':
    raise SystemExit(main())
