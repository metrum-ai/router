# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Sequence retained servers, preserving partial results after baseline failure."""
EXPERTS=('biomedical','finance','math','coding')

def run_plan(start_and_smoke,run_arm,stop,exists,save):
    status={'baseline':None,'experts':{},'benchmark':{},'notes':'Partial evidence only if any arm or expert fails; never claim parity.'}
    try:
        baseline=start_and_smoke('baseline');status['baseline']={'ready':baseline};save(status)
        if baseline:
            status['benchmark']['baseline_completed']=run_arm('baseline',[]);save(status)
        if exists('baseline'):
            status['baseline']['stopped_and_captured']=stop('baseline')
            if not status['baseline']['stopped_and_captured']:
                status['baseline']['blocked_experts_reason']='baseline stop or evidence capture failed';save(status);return status
        save(status);available=[]
        for name in EXPERTS:
            passed=start_and_smoke(name);status['experts'][name]={'ready':passed}
            if passed:available.append(name)
            save(status)
        if available:
            status['benchmark']['experts_completed']=run_arm('experts',available);save(status)
    finally:
        for name in ('baseline',*EXPERTS):
            if exists(name):status.setdefault('final_capture',{})[name]=stop(name)
        save(status)
    return status
