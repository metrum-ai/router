# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""IFEval strict instruction checks using pinned external Apache-2.0 verifiers."""
import os
import sys


def get_assert(output, context):
    root = os.environ.get('IFEVAL_VERIFIER_ROOT')
    if not root:
        raise RuntimeError('set IFEVAL_VERIFIER_ROOT to prepare_regressions.py output')
    if root not in sys.path:
        sys.path.insert(0, root)
    from lm_eval.tasks.ifeval import instructions_registry
    variables = context['vars']
    if not isinstance(output, str) or not output.strip():
        return {'pass': False, 'score': 0, 'reason': 'empty instruction response'}
    results = []
    for identifier, kwargs in zip(variables['instruction_ids'], variables['instruction_kwargs'], strict=True):
        instruction = instructions_registry.INSTRUCTION_DICT[identifier](identifier)
        instruction.build_description(**{k:v for k,v in kwargs.items() if v is not None})
        args = instruction.get_instruction_args()
        if args and 'prompt' in args:
            instruction.build_description(prompt=variables['instruction_prompt'])
        results.append(bool(instruction.check_following(output)))
    if not results:
        raise ValueError('no instructions')
    return {'pass': all(results), 'score': sum(results)/len(results), 'reason': 'strict IFEval instruction checks'}
