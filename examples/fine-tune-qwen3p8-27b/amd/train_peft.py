#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""ROCm Transformers/PEFT fallback; no download, no teacher gold, remote GPU only.

Use only after documenting Axolotl bootstrap failure and freezing this amendment.
Assistant-only supervision is derived from exact unchanged template prefixes.
"""
import argparse
from copy import deepcopy
import hashlib
import json
import math
import time
from pathlib import Path


def token_ids(value):
    if hasattr(value, 'keys'):
        value = value['input_ids']
    return list(value)


def encode(row, tokenizer):
    tools = row.get('tools') or []
    if isinstance(tools, str):
        tools = json.loads(tools)
    messages = deepcopy(row['messages'])
    for message in messages:
        for call in message.get('tool_calls', []):
            function = call['function']
            if isinstance(function['arguments'], str):
                function['arguments'] = json.loads(function['arguments'])
    def template(part, generation=False):
        return token_ids(tokenizer.apply_chat_template(part, tools=tools or None,
            tokenize=True, add_generation_prompt=generation, enable_thinking=False))
    ids = template(messages)
    labels = [-100] * len(ids)
    for index, message in enumerate(messages):
        if message['role'] != 'assistant':
            continue
        prefix = template(messages[:index], True)
        end = template(messages[:index+1])
        if ids[:len(prefix)] != prefix or ids[:len(end)] != end:
            raise ValueError('unchanged base template prefix mismatch')
        # Axolotl supervises through <|im_end|>, not the following newline.
        # Keep full input bytes/tokens unchanged; mask that one delimiter token.
        labels[len(prefix):len(end)-1] = ids[len(prefix):len(end)-1]
    if len(ids) > 8192 or not any(x != -100 for x in labels):
        raise ValueError('invalid training length/supervision')
    return {'input_ids': ids, 'attention_mask': [1]*len(ids), 'labels': labels}


def main():
    import torch
    from transformers import AutoTokenizer, AutoModelForImageTextToText, Trainer, TrainingArguments, set_seed
    from peft import LoraConfig, get_peft_model
    from datasets import Dataset
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--workspace', type=Path, default=Path('/data'))
    p.add_argument('--smoke', action='store_true')
    p.add_argument('--audit-only', action='store_true')
    a = p.parse_args()
    w = a.workspace
    mode = 'peft-smoke' if a.smoke else 'peft-train'
    if not torch.version.hip or not torch.cuda.is_available():
        raise ValueError('actual ROCm GPU required')
    set_seed(355)
    tokenizer = AutoTokenizer.from_pretrained(w/'base', local_files_only=True)
    if tokenizer.pad_token_id is None:
        tokenizer.pad_token = tokenizer.eos_token
    data = {}
    audit = {}
    for split in ['train', 'val']:
        path = w/'canonical'/f'{split}.jsonl'
        rows = [json.loads(line) for line in path.read_text().splitlines()]
        encoded = [encode(row, tokenizer) for row in rows]
        audit[split] = {'rows': len(rows), 'file_sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
            'max_tokens': max(len(row['input_ids']) for row in encoded),
            'supervised_tokens': sum(sum(v != -100 for v in row['labels']) for row in encoded),
            'input_tokens': sum(len(row['input_ids']) for row in encoded),
            'all_template_prefixes_equal': True,
            'native_rows': sum(any(m.get('tool_calls') for m in row['messages']) for row in rows)}
        data[split] = Dataset.from_list(encoded)
    (w/f'{mode}-token-audit.json').write_text(json.dumps(audit, indent=2))
    if a.audit_only:
        print(json.dumps(audit))
        return
    if a.smoke:
        # No heldout or validation rows selected for gradients; full template audit above.
        short = [row for row in data['train'] if len(row['input_ids']) <= 512]
        if len(short) < 2:
            raise ValueError('need two exported training rows <=512 tokens for smoke')
        data['train'] = Dataset.from_list(short[:2])
    model = AutoModelForImageTextToText.from_pretrained(w/'base', local_files_only=True,
        torch_dtype=torch.bfloat16, attn_implementation='sdpa')
    model.config.use_cache = False
    model = get_peft_model(model, LoraConfig(r=8, lora_alpha=16, lora_dropout=0,
        target_modules=['q_proj', 'v_proj', 'linear_attn.in_proj_qkv', 'linear_attn.out_proj'],
        bias='none', task_type='CAUSAL_LM'))
    model.enable_input_require_grads()
    model.print_trainable_parameters()
    def collate(rows):
        length = max(len(row['input_ids']) for row in rows)
        result = {}
        for key, pad in [('input_ids', tokenizer.pad_token_id), ('attention_mask', 0), ('labels', -100)]:
            result[key] = torch.tensor([row[key]+[pad]*(length-len(row[key])) for row in rows])
        return result
    args = TrainingArguments(output_dir=str(w/mode), num_train_epochs=1,
        max_steps=2 if a.smoke else -1, per_device_train_batch_size=1,
        per_device_eval_batch_size=1, gradient_accumulation_steps=1 if a.smoke else 16,
        learning_rate=1e-5, lr_scheduler_type='cosine', warmup_ratio=.03,
        bf16=True, tf32=False, optim='adamw_torch', seed=355, data_seed=355,
        gradient_checkpointing=True, gradient_checkpointing_kwargs={'use_reentrant': False},
        logging_steps=1, save_strategy='no', eval_strategy='no' if a.smoke else 'steps',
        eval_steps=None if a.smoke else .5, report_to='none', dataloader_num_workers=0,
        remove_unused_columns=False)
    trainer = Trainer(model=model, args=args, train_dataset=data['train'],
        eval_dataset=None if a.smoke else data['val'], data_collator=collate,
        processing_class=tokenizer)
    torch.cuda.reset_peak_memory_stats()
    start = time.monotonic()
    result = trainer.train()
    seconds = time.monotonic()-start
    expected = 2 if a.smoke else math.ceil(len(data['train'])/16)
    if trainer.state.global_step != expected:
        raise ValueError('optimizer-step count mismatch')
    output = w/('peft-smoke-adapter' if a.smoke else 'adapter')
    model.save_pretrained(output)
    runtime = {'copyright': 'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0',
        'mode': mode, 'wall_seconds': seconds, 'optimizer_steps': trainer.state.global_step,
        'peak_allocated_bytes': torch.cuda.max_memory_allocated(),
        'peak_reserved_bytes': torch.cuda.max_memory_reserved(),
        'torch': torch.__version__, 'hip': torch.version.hip, 'metrics': result.metrics,
        'training_input_tokens': None if a.smoke else audit['train']['input_tokens'],
        'input_tokens_per_second': None if a.smoke else audit['train']['input_tokens']/seconds}
    (w/f'{mode}-runtime.json').write_text(json.dumps(runtime, indent=2))
    print(json.dumps(runtime))


if __name__ == '__main__':
    main()
