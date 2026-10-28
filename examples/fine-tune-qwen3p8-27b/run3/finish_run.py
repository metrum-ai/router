# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Signal teardown only after complete gate results and verified adapter files."""
import hashlib,json,os
from pathlib import Path


def sha(path):
    h=hashlib.sha256()
    with path.open('rb') as f:
        for chunk in iter(lambda:f.read(1024*1024),b''):h.update(chunk)
    return h.hexdigest()


def finish(state,instance_id):
    state=Path(state);receipt=json.loads((state/'gate-finalization.json').read_text());status=receipt['status']
    if receipt.get('instance_id')!=instance_id or not receipt.get('attempted') or status.get('errors') or status.get('gate') not in ('pass','fail'):
        raise ValueError('complete matching gate results are required')
    names={'finalization.json','quality.summary.json','ifeval.summary.json','mmlu-pro.summary.json','sample1.summary.json','sample2.summary.json','sample3.summary.json','outcome.json'}
    if set(receipt['verified_sha256'])!=names:raise ValueError('complete gate evidence files required')
    for name,expected in receipt['verified_sha256'].items():
        p=state/'gate-results'/name
        if p.is_symlink() or not p.is_file() or sha(p)!=expected:raise ValueError('gate result checksum mismatch')
    if json.loads((state/'gate-results/outcome.json').read_text()).get('status')!=status['gate']:raise ValueError('gate outcome mismatch')
    adapter=state/'final-adapter';manifest=json.loads((adapter/'manifest.json').read_text())
    if set(manifest)!={'adapter_config.json','adapter_model.safetensors'}:raise ValueError('complete adapter required')
    for name,metadata in manifest.items():
        p=adapter/name
        if p.is_symlink() or not p.is_file() or p.stat().st_size!=metadata['bytes'] or sha(p)!=metadata['sha256']:raise ValueError('adapter checksum mismatch')
    # The operator keeps the watcher active while saving durable evidence. A
    # snapshot receipt is mandatory before requesting early teardown.
    saved=json.loads((state/'artifact-receipt.json').read_text())
    if saved.get('instance_id')!=instance_id or not isinstance(saved.get('snapshot'),str) or not saved['snapshot']:raise ValueError('durable evidence receipt required')
    fd=os.open(state/'done.txt',os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    with os.fdopen(fd,'w') as f:f.write('GPU-DONE #425\n')
