# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Read provisional Vast per-instance charges; missing rows are unknown, not zero."""
import argparse
import datetime as dt
from decimal import Decimal
import json
import time
import urllib.parse
import watcher


def collect(identifier,start,end,request=watcher.cloud.request):
    if not isinstance(identifier,int) or identifier<=0 or not (0<start<=end):raise ValueError('invalid charge query')
    filters={'day':{'gte':int(start)-86400,'lte':int(end)+86400},'type':{'in':['instance']}}
    token=None;seen=set();amounts=[]
    for _ in range(100):
        params={'select_filters':json.dumps(filters,separators=(',',':'))}
        if token is not None:params['next_token']=token
        result=request('vast','/charges?'+urllib.parse.urlencode(params))
        if not isinstance(result,dict) or result.get('success') is not True or not isinstance(result.get('results'),list):raise RuntimeError('invalid billing response')
        for item in result['results']:
            if item.get('source') == f'instance-{identifier}':
                value=Decimal(str(item['amount']))
                if not value.is_finite() or value<0:raise RuntimeError('invalid charge amount')
                amounts.append(value)
        token=result.get('next_token')
        if not token:break
        if token in seen:raise RuntimeError('billing pagination loop')
        seen.add(token)
    else:raise RuntimeError('billing page limit exceeded')
    return {'instance_id':identifier,'rows':len(amounts),'observed_billed_usd':str(sum(amounts,Decimal(0))) if amounts else None,
            'status':'provisional_observed_charges' if amounts else 'unknown_no_charge_rows',
            'checked_at':dt.datetime.now(dt.timezone.utc).isoformat()}


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--id',required=True,type=int);p.add_argument('--start-unix',required=True,type=float)
    a=p.parse_args();print(json.dumps(collect(a.id,a.start_unix,time.time()),sort_keys=True))


if __name__=='__main__':main()
