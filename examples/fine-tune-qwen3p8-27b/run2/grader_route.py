# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Declare text capability explicitly after CLI config serialization.

An explicit legacy modalities: [] overrides input_modalities/default text in
router admission. Do not change model, price, rubric, or evaluated-arm targets.
"""
def configure(config):
    provider = config['providers']['frontier_judge']
    model = provider['models']['served']
    if model['model'] != 'gpt-4o-2024-11-20':
        raise ValueError('frozen grader model mismatch')
    for target in [model, *config['models']['frontier-ref']['targets']]:
        target['modalities'] = ['text']
        target['input_modalities'] = ['text']
        target['output_modalities'] = ['text']
