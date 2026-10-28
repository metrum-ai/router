# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest
import experiment_plan

class ExperimentPlanTests(unittest.TestCase):
    def test_baseline_failure_continues_experts_in_order(self):
        calls=[];present=set()
        def start(name):calls.append(('start',name));present.add(name);return name not in ('baseline','math')
        def arm(name,available):calls.append(('arm',name,tuple(available)));return True
        def stop(name):calls.append(('capture-stop',name));return True
        status=experiment_plan.run_plan(start,arm,stop,lambda n:n in present,lambda _:None)
        self.assertEqual([v for k,v,*_ in calls if k=='start'],['baseline','biomedical','finance','math','coding'])
        self.assertNotIn(('arm','baseline',()),calls)
        self.assertIn(('arm','experts',('biomedical','finance','coding')),calls)
        self.assertFalse(status['baseline']['ready']);self.assertFalse(status['experts']['math']['ready'])
        self.assertTrue(all(status['final_capture'].values()))
        self.assertLess(calls.index(('capture-stop','baseline')),calls.index(('start','biomedical')))

    def test_unverified_baseline_stop_blocks_gpu_contention(self):
        started=[]
        def start(name):started.append(name);return False
        status=experiment_plan.run_plan(start,lambda *_:True,lambda _:False,lambda n:n=='baseline',lambda _:None)
        self.assertEqual(started,['baseline'])
        self.assertEqual(status['baseline']['blocked_experts_reason'],'baseline stop or evidence capture failed')

if __name__=='__main__':unittest.main()
