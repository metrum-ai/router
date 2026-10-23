#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Small synthetic base-template check on the remote GPU host; no weights loaded.

This proves rendering/token-prefix parity, not generated-answer correctness.
Actual base/candidate answer-format and native tool continuation are separate.
"""
import argparse
from copy import deepcopy
import hashlib
import json
from pathlib import Path

GENERATION_SUFFIX = '<|im_start|>assistant\n<think>\n\n</think>\n\n'
END = '<|im_end|>\n'


def template_messages(messages):
    """Direct HF templates expect object arguments, unlike OpenAI wire JSON."""
    result = deepcopy(messages)
    for m in result:
        for call in m.get('tool_calls', []):
            fn = call['function']
            if isinstance(fn['arguments'], str):
                fn['arguments'] = json.loads(fn['arguments'])
    return result


def fixtures():
    user = {'role':'user','content':'Synthetic format probe: reply with B only.'}
    call = {'role':'assistant','content':None,'tool_calls':[{'id':'probe','type':'function',
        'function':{'name':'echo','arguments':'{"value":"synthetic"}'}}]}
    tool = {'type':'function','function':{'name':'echo','description':'Synthetic echo',
        'parameters':{'type':'object','properties':{'value':{'type':'string'}},'required':['value']}}}
    return [
        ('letter', [user,{'role':'assistant','content':'B'}], []),
        ('plain', [user,{'role':'assistant','content':'Synthetic plain answer.'}], []),
        ('json', [user,{'role':'assistant','content':'{"tool_calls":[]}'}], []),
        ('native', [user,call], [tool]),
        ('continuation', [user,call,{'role':'tool','tool_call_id':'probe','content':'synthetic'},
                          {'role':'assistant','content':'Synthetic continuation.'}], [tool]),
    ]


def check_case(tokenizer, messages, tools):
    messages = template_messages(messages)
    kwargs = {'tools':tools,'enable_thinking':False}
    prefix = tokenizer.apply_chat_template(messages[:-1],tokenize=False,add_generation_prompt=True,**kwargs)
    full = tokenizer.apply_chat_template(messages,tokenize=False,add_generation_prompt=False,**kwargs)
    if not prefix.endswith(GENERATION_SUFFIX) or not full.startswith(prefix) or not full.endswith(END):
        raise ValueError('base non-thinking prefix/answer/end-of-turn mismatch')
    content = messages[-1].get('content')
    if content and full != prefix + content.strip() + END:
        raise ValueError('plain/JSON answer format was altered')
    prefix_ids = tokenizer.apply_chat_template(messages[:-1],tokenize=True,add_generation_prompt=True,**kwargs)
    full_ids = tokenizer.apply_chat_template(messages,tokenize=True,add_generation_prompt=False,**kwargs)
    if full_ids[:len(prefix_ids)] != prefix_ids:
        raise ValueError('training and generation token prefixes differ')
    return {'generation_prefix_tokens':len(prefix_ids),'full_tokens':len(full_ids),
            'generation_prefix_sha256':hashlib.sha256(prefix.encode()).hexdigest()}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--remote-gpu-host',action='store_true')
    p.add_argument('--base',type=Path,required=True)
    p.add_argument('--preregistration',type=Path,required=True)
    p.add_argument('--out',type=Path,required=True)
    a = p.parse_args()
    if not a.remote_gpu_host:p.error('run on the remote GPU host, not the coding-agent machine')
    registration = json.loads(a.preregistration.read_text())
    if registration['status'] != 'frozen':p.error('publish/freeze preregistration first')
    expected = registration['chat_template']['sha256']
    if hashlib.sha256((a.base/'chat_template.jinja').read_bytes()).hexdigest() != expected:
        p.error('base template bytes differ from pinned source')
    from transformers import AutoTokenizer
    tokenizer = AutoTokenizer.from_pretrained(str(a.base),local_files_only=True,trust_remote_code=False)
    if hashlib.sha256(tokenizer.chat_template.encode()).hexdigest() != expected:
        p.error('loaded tokenizer template differs from pinned base template')
    checks = {name:check_case(tokenizer,messages,tools) for name,messages,tools in fixtures()}
    result = {'copyright':'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0',
        'protocol':registration['protocol'],'pass':True,'synthetic_cases':len(checks),'checks':checks,
        'base_template_sha256':expected,'preregistration_sha256':hashlib.sha256(a.preregistration.read_bytes()).hexdigest(),
        'scope':'HF rendering/token-prefix parity only; JSON wire arguments decoded to objects; actual Axolotl cache and model continuations checked separately'}
    a.out.write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps({'pass':True,'synthetic_cases':len(checks),'base_template_sha256':expected}))


if __name__ == '__main__':
    main()
