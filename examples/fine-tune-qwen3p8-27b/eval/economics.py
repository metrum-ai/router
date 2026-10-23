#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Capacity/cost scenario from measured SSE load; not a quality-equivalent ROI claim."""
import argparse
import json
import math
from pathlib import Path


def scenario(profile, reference_input, reference_output, input_output_ratio,
             one_off_cost=0, hours=720):
    numbers = [reference_input,reference_output,input_output_ratio,one_off_cost,hours,
               profile['hourly_usd'],profile['output_tokens_per_second']]
    if not all(isinstance(x,(float,int)) and math.isfinite(x) and x>=0 for x in numbers):
        raise ValueError('finite nonnegative prices, ratios and throughput required')
    throughput = profile['output_tokens_per_second']
    if not throughput or not hours or profile['errors']:
        raise ValueError('complete error-free nonzero-throughput load profile required')
    reference = reference_output + reference_input * input_output_ratio
    if reference <= 0:
        raise ValueError('positive token-priced reference required')
    fixed_cost = profile['hourly_usd'] * hours + one_off_cost
    break_even = fixed_cost / reference * 1e6
    capacity = throughput * 3600 * hours
    return {
        'concurrency':profile['concurrency'],
        'measured_capacity_output_tokens_per_second':throughput,
        'measured_full_utilization_gpu_usd_per_million_outputs':profile['hourly_usd'] / (throughput*3600) * 1e6,
        'scenario_hours':hours,'scenario_one_off_usd':one_off_cost,
        'scenario_fixed_usd':fixed_cost,
        'reference_input_usd_per_million':reference_input,
        'reference_output_usd_per_million':reference_output,
        'assumed_input_output_token_ratio':input_output_ratio,
        'reference_equivalent_usd_per_million_outputs':reference,
        'break_even_output_tokens_in_period':break_even,
        'measured_capacity_output_tokens_in_period':capacity,
        'break_even_capacity_fraction':break_even/capacity,
        'within_measured_capacity':break_even<=capacity,
        'caveat':'Hypothetical capacity/duty-cycle price comparison only. Assumes measured batching efficiency at scaled average load; not validated for continuous low-concurrency traffic or a changed input/output mix. Reference model is not proven quality-equivalent; excludes operator labor, network, storage, downtime and taxes. No retry reduction or business payback measured.',
    }


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--load',type=Path,required=True)
    p.add_argument('--concurrency',type=int,choices=[1,8,32],default=32)
    p.add_argument('--reference-input-usd',type=float,required=True)
    p.add_argument('--reference-output-usd',type=float,required=True)
    p.add_argument('--input-output-ratio',type=float,required=True)
    p.add_argument('--one-off-usd',type=float,default=0)
    p.add_argument('--hours',type=float,default=720)
    p.add_argument('--out',type=Path,required=True)
    a=p.parse_args()
    source=json.loads(a.load.read_text())
    profiles=source['measurements'] if isinstance(source,dict) else source
    selected=[r for r in profiles if r['concurrency']==a.concurrency]
    if len(selected)!=1:p.error('need exactly one requested load profile')
    try:result=scenario(selected[0],a.reference_input_usd,a.reference_output_usd,
                        a.input_output_ratio,a.one_off_usd,a.hours)
    except (ValueError,TypeError,KeyError) as exc:p.error(str(exc))
    result['copyright']='Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0'
    if isinstance(source,dict):result['model']=source.get('model')
    a.out.write_text(json.dumps(result,indent=2,allow_nan=False))

if __name__=='__main__':main()
