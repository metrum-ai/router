#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Synthetic-only tests; no capture/consent/model-quality claim."""
import copy
import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).parent


def module(name,path):
    spec = importlib.util.spec_from_file_location(name,ROOT/path)
    value = importlib.util.module_from_spec(spec);spec.loader.exec_module(value)
    return value


export = module('export_contract','run2/router_export.py')
replay = module('replay_contract','run2/replay.py')


class ExportTests(unittest.TestCase):
    def fixture(self,directory):
        manifest = {'format':export.FORMAT,'source':{'kind':'router_capture'},'export_id':'synthetic',
            'filters':{'project':'synthetic-only'},'split_boundaries':{'method':'time'},
            'audit_event_ref':'synthetic-audit','teacher_terms':{'training_outputs_allowed':True},
            'teacher_terms_decision':{'scope':'synthetic fixture only','reference':'synthetic','terms_sha256':'0'*64},
            'privacy':{k:True for k in ('capture_opt_in','redaction_applied','pii_filter_applied')},'splits':{}}
        for n,split in enumerate(('train','val','test'),1):
            row = {'messages':[{'role':'user','content':split},{'role':'assistant','content':'done'}],
                'tools':[],'provenance':{'trace_sha256':str(n)*64,'task_sha256':str(n+3)*64,
                'captured_at':f'2026-10-2{n}T00:00:00Z'},
                'outcome':{'task_success':True,'tool_schema_valid':True},
                'replay':{'initial_messages':[{'role':'user','content':split}]}}
            p = directory/f'{split}.jsonl';p.write_text(json.dumps(row)+'\n')
            manifest['splits'][split] = {'count':1,'sha256':export.sha(p)}
        (directory/'manifest.json').write_text(json.dumps(manifest))
        return manifest

    def test_canonical_projection_keeps_envelope_out_of_training(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d);source=path/'source';source.mkdir();self.fixture(source)
            result = export.prepare(source,path/'output')
            self.assertFalse(result['training_authorized'])
            self.assertEqual(result['teacher_terms_decision']['scope'],'synthetic fixture only')
            row = json.loads((path/'output/train.jsonl').read_text())
            self.assertEqual(set(row),{'messages','tools'})
            self.assertIn('provenance',json.loads((path/'output/replay-test.jsonl').read_text()))
            view = json.loads((path/'output/train-axolotl.jsonl').read_text())
            self.assertEqual(json.loads(view['tools']),row['tools'])

    def test_privacy_rights_checksum_and_direct_source_fail_closed(self):
        for change in ('privacy','rights','hash','source'):
            with self.subTest(change=change),tempfile.TemporaryDirectory() as d:
                p=Path(d);m=self.fixture(p)
                if change=='privacy':m['privacy']['capture_opt_in']=False
                if change=='rights':m['teacher_terms']['training_outputs_allowed']=False
                if change=='hash':m['splits']['train']['sha256']='0'*64
                if change=='source':m['source']['kind']='direct_download'
                (p/'manifest.json').write_text(json.dumps(m))
                with self.assertRaises(ValueError):export.load_export(p)

    def test_orphan_tool_continuation_rejected(self):
        with self.assertRaises(ValueError):export.chat({'messages':[
            {'role':'user','content':'task'},{'role':'tool','tool_call_id':'orphan','content':'ok'}]})

    def test_failed_teacher_tool_arguments_retained_only_for_heldout_provenance(self):
        row={'messages':[{'role':'user','content':'task'},{'role':'assistant','content':None,
            'tool_calls':[{'id':'bad','type':'function','function':{'name':'lookup','arguments':'not-json'}}]}]}
        self.assertEqual(export.chat(row,require_complete=False),row)
        with self.assertRaises(ValueError):export.chat(row)

    def test_time_and_group_leakage_rejected(self):
        for change in ('time','group'):
            with self.subTest(change=change),tempfile.TemporaryDirectory() as d:
                p=Path(d);m=self.fixture(p);row=json.loads((p/'test.jsonl').read_text())
                if change=='time':row['provenance']['captured_at']='2026-10-20T00:00:00Z'
                else:row['provenance']['task_sha256']='4'*64
                (p/'test.jsonl').write_text(json.dumps(row)+'\n');m['splits']['test']['sha256']=export.sha(p/'test.jsonl')
                (p/'manifest.json').write_text(json.dumps(m))
                with self.assertRaises(ValueError):export.load_export(p)


class RentalBlockTests(unittest.TestCase):
    def test_unfrozen_registration_blocks_training_before_inputs(self):
        readiness = module('readiness_export','run2/readiness.py')
        with tempfile.TemporaryDirectory() as d:
            p=Path(d);(p/'run2').mkdir()
            (p/'run2/preregistration.json').write_text(json.dumps({'status':'blocked'}))
            with self.assertRaisesRegex(ValueError,'not frozen'):readiness.verify(p)

    def test_projection_uses_source_gold_not_recorded_teacher(self):
        projector=module('source_projection','run2/project_eval.py')
        rows=[{'provenance':{'task_sha256':'task'},'messages':[{'content':'TEACHER_SENTINEL'}]}]
        gold={'task':{'initial_messages':[{'role':'user','content':'question'}],
            'tools':[],'expected_calls':[],'reference':'SOURCE_REFERENCE'}}
        tests=projector.project(rows,gold)
        self.assertNotIn('TEACHER_SENTINEL',json.dumps(tests))
        self.assertNotIn('SOURCE_REFERENCE',tests[0]['vars']['messages'])
        self.assertIn('SOURCE_REFERENCE',tests[0]['assert'][0]['value'])
        with self.assertRaises(ValueError):projector.project(rows,{})

    def test_replay_execute_blocked_before_backend_or_network(self):
        with tempfile.TemporaryDirectory() as d:
            out=Path(d)/'private.jsonl'
            registration=Path(d)/'blocked.json'
            registration.write_text(json.dumps({'status':'blocked'}))
            result=subprocess.run([sys.executable,str(ROOT/'run2/run_replay.py'),
                '--export',str(Path(d)/'absent-export'),'--preregistration',str(registration),
                '--backend',str(Path(d)/'absent-backend'),'--arm','candidate','--out',str(out),'--execute'],
                capture_output=True,text=True)
            self.assertNotEqual(result.returncode,0)
            self.assertIn('blocked until #358',result.stderr)
            self.assertFalse(out.exists())

    def test_replay_child_environment_redacts_credentials_without_mutating_parent(self):
        sys.path.insert(0,str(ROOT/'run2'))
        try:runner=module('replay_runner_privacy','run2/run_replay.py')
        finally:sys.path.pop(0)
        original={'PATH':'/bin','OPENAI_API_KEY':'private','HF_TOKEN':'private',
                  'AWS_SESSION_TOKEN':'private','GH_TOKEN':'private','LANG':'C.UTF-8'}
        safe=runner.evaluation_environment(original)
        self.assertEqual(safe,{'PATH':'/bin','LANG':'C.UTF-8'})
        self.assertEqual(original['GH_TOKEN'],'private')

    def test_run2_budget_profile_dryrun_and_total_reservation(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d);payload=p/'payload.json';payload.write_text('{}')
            args=[sys.executable,str(ROOT/'standalone/lease.py'),'create','--state-dir',str(p/'lease'),
                '--payload',str(payload),'--hourly-usd','2.19','--hours','4',
                '--budget-profile','run2','--reserved-other-usd','70']
            result=subprocess.run(args,capture_output=True,text=True,check=True)
            value=json.loads(result.stdout)
            self.assertEqual((value['stop_usd'],value['hard_cap_usd']),(120,150))
            self.assertTrue(value['dry_run']);self.assertFalse((p/'lease').exists())
            denied=subprocess.run(args+['--spent-usd','50'],capture_output=True,text=True)
            self.assertNotEqual(denied.returncode,0)
            self.assertIn('$120',denied.stderr)
            blocked=subprocess.run(args+['--execute'],capture_output=True,text=True)
            self.assertNotEqual(blocked.returncode,0)
            self.assertIn('blocked until #358',blocked.stderr)
            self.assertFalse((p/'lease').exists())


class ReplayCostTests(unittest.TestCase):
    def records(self):
        return [{'arm':arm,'task_sha256':str(n)*64,'task_success':n==1,'request_ids':[f'{arm}-{n}'],
                 'cost_complete':True,'router_cost_usd':2.0} for arm in ('candidate','teacher','base') for n in (1,2)]

    def test_failed_attempt_cost_included(self):
        result=replay.summarize(self.records())['arms']
        self.assertEqual(result['candidate']['router_cost_per_successful_task_usd'],4)

    def test_unknown_or_zero_success_is_not_free(self):
        rows=self.records();rows[0]['cost_complete']=False;rows[0]['router_cost_usd']=None
        self.assertIsNone(replay.summarize(rows)['arms']['candidate']['router_cost_per_successful_task_usd'])
        rows=self.records()
        for r in rows:r['task_success']=False
        self.assertIsNone(replay.summarize(rows)['arms']['candidate']['router_cost_per_successful_task_usd'])

    def test_cost_join_requires_known_actual_router_requests(self):
        record={'request_ids':['r1'],'transport_error':False}
        self.assertFalse(replay.attach_cost(record,{})['cost_complete'])
        self.assertEqual(replay.attach_cost(record,{'r1':{'cost_known':True,'router_cost_usd':.2}})['router_cost_usd'],.2)
        with self.assertRaises(ValueError):replay.attach_cost(record,{'r1':{'cost_known':True,'router_cost_usd':float('nan')}})

    def test_same_cohort_and_unique_requests_required(self):
        rows=self.records();rows[0]['task_sha256']='different'
        with self.assertRaises(ValueError):replay.summarize(rows)
        rows=self.records();rows[0]['request_ids']=rows[1]['request_ids']
        with self.assertRaises(ValueError):replay.summarize(rows)

    def test_truncated_or_unknown_final_reason_is_not_success(self):
        class Backend:
            def initialize(self,descriptor):return {}
            def success(self,state,message):return message.get('finish_reason')=='stop'
        row={'tools':[],'provenance':{'task_sha256':'0'*64},
             'replay':{'initial_messages':[{'role':'user','content':'fixture'}]}}
        for reason,expected in (('length',False),(None,False),('stop',True)):
            def call(messages,tools):
                return {'role':'assistant','content':'ok','finish_reason':reason},'request'
            self.assertEqual(replay.run_case(row,call,Backend())['task_success'],expected)

    def test_duplicate_or_invalid_declaration_is_retained_failure_not_batch_abort(self):
        row={'tools':[{'type':'function','function':{'name':'echo','parameters':{'type':'object'}}}]*2,
             'provenance':{'task_sha256':'0'*64},'replay':{'initial_messages':[{'role':'user','content':'fixture'}]}}
        def forbidden(*args):raise AssertionError('invalid declarations must never dispatch')
        result=replay.run_case(row,forbidden,None)
        self.assertFalse(result['task_success']);self.assertFalse(result['tool_schema_valid'])
        self.assertEqual(result['input_schema_failure'],'duplicate_tool_definition')
        self.assertEqual(replay.attach_cost(result,{})['router_cost_usd'],0)
        row['tools']=[{'type':'function','function':{'name':'echo','parameters':{'type':'invalid'}}}]
        self.assertEqual(replay.run_case(row,forbidden,None)['input_schema_failure'],'invalid_declared_tool_schema')

    def test_failed_http_attempt_retains_router_request_id_and_unknown_cost(self):
        import urllib.error
        class Backend:
            def initialize(self,descriptor):return {}
        row={'tools':[],'provenance':{'task_sha256':'0'*64},'replay':{'initial_messages':[{'role':'user','content':'fixture'}]}}
        def failed(*args):raise urllib.error.HTTPError('http://127.0.0.1',503,'unavailable',{'X-Request-Id':'req_failed'},None)
        result=replay.run_case(row,failed,Backend())
        self.assertEqual(result['request_ids'],['req_failed']);self.assertTrue(result['transport_error'])
        self.assertIsNone(replay.attach_cost(result,{})['router_cost_usd'])

    def test_teacher_continuations_never_initial_inputs(self):
        with self.assertRaises(ValueError):replay.run_case({'replay':{'initial_messages':[
            {'role':'assistant','content':'teacher oracle'}]}},None,None)


if __name__=='__main__':unittest.main()
