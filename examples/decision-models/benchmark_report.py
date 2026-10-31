# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Summarize labeled synthetic routing decisions without hiding errors."""
import json
import math
from pathlib import Path

TIERS=('cheap','strong')


def dataset(path):
    value=json.loads(Path(path).read_text())
    if value.get('version')!=1 or not isinstance(value.get('provenance'),str):
        raise ValueError('invalid dataset metadata')
    rows=value.get('rows')
    if not isinstance(rows,list) or not rows:raise ValueError('dataset must have rows')
    ids=set()
    for r in rows:
        if not isinstance(r,dict) or any(not isinstance(r.get(k),str) or not r[k].strip() for k in ('id','domain','prompt','expected_tier')):
            raise ValueError('invalid dataset row')
        if r['id'] in ids or r['expected_tier'] not in TIERS:raise ValueError('duplicate id or invalid tier')
        ids.add(r['id'])
    return rows


def summarize(rows):
    groups={}
    for row in rows:
        key=(row['decision'],row['mode'])
        if key[1] not in ('shadow','enforce') or row['expected_tier'] not in TIERS:raise ValueError('invalid benchmark record')
        group=groups.setdefault(key,{'count':0,'successes':0,'errors':0,'correct':0,
            'confusion':{actual:{predicted:0 for predicted in TIERS} for actual in TIERS},'policy_duration_ms':[]})
        group['count']+=1
        pred=row.get('recommended_tier');actual=row['expected_tier']
        expected_outcome='shadow_recommended' if key[1]=='shadow' else 'selected'
        if pred not in TIERS or row.get('outcome')!=expected_outcome:
            group['errors']+=1;continue
        group['successes']+=1;group['correct']+=int(pred==actual);group['confusion'][actual][pred]+=1
        duration=row.get('policy_duration_ms')
        if isinstance(duration,(int,float)) and not isinstance(duration,bool) and math.isfinite(duration) and duration>=0:
            group['policy_duration_ms'].append(duration)
    output=[]
    for (name,mode),g in sorted(groups.items()):
        values=sorted(g.pop('policy_duration_ms'))
        g['accuracy_successes_only']=g['correct']/g['successes'] if g['successes'] else None
        g['correct_fraction_all_requests']=g['correct']/g['count']
        # Nearest-rank percentiles; a high percentile from <100 observations is
        # deliberately omitted rather than presenting one maximum as p99.
        def percentile(q):return values[max(0,math.ceil(q*len(values))-1)] if values else None
        g['policy_p50_ms']=percentile(.5)
        g['policy_p99_ms']=percentile(.99) if len(values)>=100 else None
        g['timed_successes']=len(values)
        output.append({'decision':name,'mode':mode,**g})
    return output
