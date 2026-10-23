#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Refuse run-2 training until frozen inputs and base-format checks agree."""
import argparse
import hashlib
import json
from pathlib import Path


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def verify(workspace, require_prepared=False):
    registration_path = workspace/'run2/preregistration.json'
    registration = json.loads(registration_path.read_text())
    if registration['status'] != 'frozen':
        raise ValueError('preregistration is not frozen')
    for name, expected in registration['code_sha256'].items():
        path = (workspace/name).resolve()
        if not path.is_relative_to(workspace.resolve()) or sha(path) != expected:
            raise ValueError('frozen source checksum mismatch')
    for name in ('train','val'):
        if sha(workspace/'canonical'/f'{name}.jsonl') != registration['datasets']['sha256'][name]:
            raise ValueError('frozen training/validation checksum mismatch')
        if sha(workspace/'canonical'/f'{name}-axolotl.jsonl') != registration['datasets']['axolotl_view_sha256'][name]:
            raise ValueError('frozen Axolotl-view checksum mismatch')
    if sha(workspace/'canonical/manifest.json') != registration['dataset_manifest_sha256']:
        raise ValueError('dataset manifest mismatch')
    if sha(workspace/'base/chat_template.jinja') != registration['chat_template']['sha256']:
        raise ValueError('base chat template changed after pre-check')
    identity = sha(registration_path)
    template = json.loads((workspace/'base-template-check.json').read_text())
    generation = json.loads((workspace/'base-format-precheck.json').read_text())
    if (template.get('pass') is not True or template.get('preregistration_sha256') != identity
            or template.get('base_template_sha256') != registration['chat_template']['sha256']):
        raise ValueError('fresh base-template/token-prefix check required')
    if (generation.get('complete') is not True or generation.get('preregistration_sha256') != identity
            or generation.get('model') != 'qwen3p8-27b-base'
            or generation.get('case_count') != 12 or generation.get('strict_letter_format') != 12
            or generation.get('synthetic_cases_sha256') != registration['precheck_cases_sha256']):
        raise ValueError('fresh base single-letter-format pre-check required')
    if require_prepared:
        for name in ('train','val'):
            audit = json.loads((workspace/f'prepared-check-{name}.json').read_text())
            if (audit.get('pass') is not True or audit.get('preregistration_sha256') != identity
                    or audit.get('canonical_sha256') != registration['datasets']['sha256'][name]):
                raise ValueError('matching actual Axolotl cache audit required before training')
    return {'ready':True,'protocol':registration['protocol'],'preregistration_sha256':identity}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--workspace',type=Path,default=Path('/workspace'))
    p.add_argument('--require-prepared',action='store_true')
    a = p.parse_args()
    print(json.dumps(verify(a.workspace,a.require_prepared)))


if __name__ == '__main__':
    main()
