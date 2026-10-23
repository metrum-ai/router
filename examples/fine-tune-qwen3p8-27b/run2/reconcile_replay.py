#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Join post-run authenticated router-cost ledger, emit aggregate task economics.

Ledger JSON maps request ID to {cost_known:bool, router_cost_usd:number}.
Normalize from an authenticated router usage export; never invent zero prices.
No prompts, answers or per-task IDs are emitted in the aggregate output.
"""
import argparse
import json
from pathlib import Path

from replay import attach_cost, summarize
from router_export import load_export, sha


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--export',type=Path,required=True)
    p.add_argument('--records',type=Path,nargs='+',required=True)
    p.add_argument('--ledger',type=Path,required=True)
    p.add_argument('--out',type=Path,required=True)
    a=p.parse_args()
    ledger=json.loads(a.ledger.read_text())
    records=[json.loads(line) for path in a.records for line in path.read_text().splitlines()]
    for field in ('export_manifest_sha256','preregistration_sha256'):
        values={r[field] for r in records}
        if len(values)!=1:p.error('cannot mix exports/protocols in a paired replay')
    _,captured=load_export(a.export)
    expected={r['provenance']['task_sha256'] for r in captured['test']}
    if any(r['export_manifest_sha256']!=sha(a.export/'manifest.json') for r in records):
        p.error('records do not match export')
    for arm in ('candidate','teacher','base'):
        if {r['task_sha256'] for r in records if r['arm']==arm}!=expected:
            p.error('incomplete/wrong heldout task cohort')
    result=summarize([attach_cost(r,ledger) for r in records])
    result['copyright']='Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0'
    result['export_manifest_sha256']=records[0]['export_manifest_sha256']
    result['preregistration_sha256']=records[0]['preregistration_sha256']
    a.out.write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps({'arms':list(result['arms']),'promotion_decision':None}))


if __name__=='__main__':main()
