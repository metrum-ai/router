# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import unittest
import json
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).parent / 'eval'))
sys.path.insert(0, str(Path(__file__).parent / 'standalone'))
import prepare_data as data

spec = importlib.util.spec_from_file_location('tools', Path(__file__).parent / 'eval/scorers/tools.py')
tools = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tools)


def row(text):
    return {'messages': [{'role': 'user', 'content': text}, {'role': 'assistant', 'content': 'ok'}]}


def call(args, name='lookup'):
    return [{'function': {'name': name, 'arguments': args}}]


class DataTests(unittest.TestCase):
    def test_determinism_dedup(self):
        rows = [row(str(i)) for i in range(20)] + [row('  1 ')]
        for i in range(20):
            rows.append(data.convert({'system':'tools {"name":"lookup"}',
                'chat':f'USER: tool-{i} ASSISTANT: <functioncall> {{"name":"lookup","arguments":{{"id":{i}}}}}<|endoftext|>'}))
        a, _ = data.split(rows, 10, 3, 2)
        b, _ = data.split(reversed(rows), 10, 3, 2)
        # Representative byte choice is deterministic for the same input ordering.
        c, _ = data.split(rows, 10, 3, 2)
        self.assertEqual(a, c)
        self.assertEqual({data.normalized(data.prompt(r)) for r in a['train']},
                         {data.normalized(data.prompt(r)) for r in b['train']})
        data.assert_no_leakage(a['train'] + a['val'], a['test'])

    def test_literal_calls_and_stratification(self):
        result = data.convert({'system':'tools {"name":"lookup"}',
            'chat':'''USER: Find it ASSISTANT: <functioncall> {"name": "lookup", "arguments": '{"id": 1, "enabled": true}'}<|endoftext|>'''})
        args = json.loads(result['messages'][-1]['tool_calls'][0]['function']['arguments'])
        self.assertEqual(args, {'id':1,'enabled':True})
        with self.assertRaises(ValueError):
            data.parse_call("__import__('os').system('false')")
        with self.assertRaises(ValueError):
            data.split([row(str(i)) for i in range(20)],10,3,2)

    def test_leak(self):
        with self.assertRaises(ValueError):
            data.assert_no_leakage([row('Hello WORLD')], [row(' hello   world ')])

    def test_schema(self):
        data.validate(row('valid'))
        with self.assertRaises(Exception):
            data.validate({'messages': [{'role': 'invalid'}]})

    def test_convert(self):
        result = data.convert({'system': 'SYSTEM: tools {"name":"lookup","parameters":{"type":"object"}}',
            'chat': 'USER: Find it ASSISTANT: <functioncall> {"name":"lookup","arguments":{"id":1}}<|endoftext|>'})
        self.assertEqual(result['messages'][1]['tool_calls'][0]['function']['name'], 'lookup')


class RuntimeTests(unittest.TestCase):
    def load(self, name, path):
        spec = importlib.util.spec_from_file_location(name, Path(__file__).parent / path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def test_fireworks_signed_upload_headers_and_validation(self):
        import tempfile
        from unittest.mock import patch
        fw = self.load('fw_upload_test', 'fireworks/run.py')
        calls = []
        class Response:
            def __enter__(self): return self
            def __exit__(self,*args): return False
            def read(self): return b''
        with tempfile.TemporaryDirectory() as directory:
            adapter = Path(directory)
            (adapter/'adapter_config.json').write_text(json.dumps({'r':16,'target_modules':['q_proj']}))
            (adapter/'adapter_model.safetensors').write_bytes(b'synthetic-unit-fixture')
            def api(path,payload=None,method='POST'):
                calls.append((path,method))
                if path.endswith(':getUploadEndpoint'):
                    return {'filenameToSignedUrls':{name:'https://example.invalid/'+name for name in payload['filenameToSize']}}
                return {}
            def put(request,**kwargs):
                headers = {k.lower():v for k,v in request.header_items()}
                self.assertEqual(headers['content-type'],'application/octet-stream')
                size = len(request.data)
                self.assertEqual(headers['x-goog-content-length-range'],f'{size},{size}')
                self.assertEqual(request.method,'PUT')
                return Response()
            with patch.object(fw,'api',api),patch.object(fw.urllib.request,'urlopen',put):
                fw.upload('synthetic-account','synthetic-model',adapter)
            self.assertEqual(calls[-1],('accounts/synthetic-account/models/synthetic-model:validateUpload','GET'))

    def test_rollout_and_economics(self):
        import yaml
        config = yaml.safe_load((Path(__file__).parent/'config.qwen3p8-ft.example.yaml').read_text())
        self.assertEqual(config['server']['default_model_group'],'qwen3p8-27b-base')
        self.assertEqual([t['weight'] for t in config['models']['qwen3p8-27b-canary']['targets']],[90,5,5])
        shadow = config['models']['qwen3p8-27b-ft-shadow']
        self.assertEqual([t['model_ref'] for t in shadow['targets']],['base','adapter'])
        self.assertFalse(shadow['external_policy']['include_request'])
        economics = self.load('economics_test','eval/economics.py')
        profile={'concurrency':32,'hourly_usd':3.6,'output_tokens_per_second':1000,'errors':0}
        result=economics.scenario(profile,2,2,1,hours=1)
        self.assertAlmostEqual(result['measured_full_utilization_gpu_usd_per_million_outputs'],1)
        self.assertAlmostEqual(result['break_even_output_tokens_in_period'],900000)
        self.assertAlmostEqual(result['break_even_capacity_fraction'],.25)
        with self.assertRaises(ValueError):economics.scenario(profile,float('nan'),2,1)
        with self.assertRaises(ValueError):economics.scenario({**profile,'output_tokens_per_second':0},2,2,1)

    def test_redaction_and_dryrun(self):
        cloud = self.load('cloud', 'standalone/cloud.py')
        self.assertEqual(cloud.redact({'instance_api_key': 'not-a-real-key', 'nested': {'token': 'x'}, 'id': 1}),
                         {'instance_api_key': '[REDACTED]', 'nested': {'token': '[REDACTED]'}, 'id': 1})
        result = cloud.request('vast', '/asks/1/', 'PUT', {'image': 'test'})
        self.assertTrue(result['dry_run'])

    def test_axolotl_smoke_and_fsdp(self):
        render = self.load('render', 'standalone/render.py').render
        config = render(1, True)
        self.assertEqual(config['max_steps'], 2)
        self.assertEqual(config['sequence_len'], 512)
        self.assertEqual(config['adapter'], 'lora')
        self.assertFalse(config['load_in_4bit'])
        config = render(2, False)
        self.assertEqual(config['fsdp_version'], 2)
        self.assertEqual(config['gradient_accumulation_steps'] * 2, 16)
        self.assertNotIn('lm_head', config['lora_target_modules'])
        with self.assertRaises(ValueError):
            render(3)

    def test_synthetic_and_eval_builder(self):
        rows = json.loads((Path(__file__).parent / 'synthetic.json').read_text())['rows']
        for row in rows:
            data.validate(row)
        build = self.load('build_tests', 'eval/build_tests.py').build
        tests = build(rows)
        self.assertEqual(tests[0]['assert'][0]['type'], 'is-json')
        self.assertEqual(tests[1]['assert'][0]['type'], 'llm-rubric')
        self.assertNotIn('tool_calls', json.loads(tests[0]['vars']['messages'])[-1])

    def test_summary_pairs_tool_subset(self):
        summarize = self.load('summarize', 'eval/summarize.py').summarize
        rows = []
        for provider in ['base', 'ft']:
            for index in range(2):
                grade = {'componentResults': []}
                if index == 0:
                    grade['componentResults'].append({'reason': json.dumps({'exact': int(provider == 'ft')})})
                rows.append({'provider': {'id': provider}, 'testIdx': index, 'success': True,
                    'latencyMs': 10, 'response': {}, 'gradingResult': grade})
        report, _ = summarize({'results': {'results': rows}})
        self.assertEqual(report['paired']['base -> ft']['exact']['n'], 1)
        self.assertEqual(report['paired']['base -> ft']['exact']['right_minus_left'], 1)
        with self.assertRaises(ValueError):
            summarize({'results': {'results': rows + rows[:1]}})

    def test_promotion_gate_thresholds(self):
        gate = self.load('outcome', 'eval/outcome.py').additional_failures
        quality = {'providers':{'base':{'count':300,'metrics':{'exact':{'n':100,'mean':.5}}},
            'ft':{'count':300,'metrics':{'exact':{'n':100,'mean':.6}}}},
            'paired':{'base -> ft':{'passed':{'n':300},'exact':{'n':100}}}}
        regression = {'providers':{'base':{'count':200,'metrics':{'passed':{'mean':.8}}},
            'ft':{'count':200,'metrics':{'passed':{'mean':.78}}}},
            'paired':{'base -> ft':{'passed':{'n':200}}}}
        self.assertEqual(gate(quality,regression,regression,'base','ft'), [])
        quality['providers']['ft']['metrics']['exact']['mean'] = .59
        self.assertIn('tool_exact_improvement_below_10pt', gate(quality,regression,regression,'base','ft'))
        regression['providers']['ft']['metrics']['passed']['mean'] = .77
        self.assertIn('ifeval_regression_above_2pt', gate(quality,regression,regression,'base','ft'))
        quality['paired'] = {}
        with self.assertRaises(ValueError):
            gate(quality,regression,regression,'base','ft')

    def test_load_percentiles(self):
        percentile = self.load('load', 'eval/load.py').percentile
        self.assertIsNone(percentile([], .95))
        self.assertEqual(percentile([3,1,2], .5), 2)
        self.assertEqual(percentile([3,1,2], .95), 3)

    def test_fireworks_missing_artifacts_no_api(self):
        import tempfile
        from unittest.mock import patch
        fw = self.load('fw', 'fireworks/run.py')
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            (path/'adapter_config.json').write_text(json.dumps({'r':16,'target_modules':['q_proj']}))
            with patch.object(fw, 'api') as api:
                with self.assertRaises(ValueError):
                    fw.upload('account','model',path)
                api.assert_not_called()

    def test_lease_budget_and_teardown(self):
        import argparse
        import datetime
        import tempfile
        from unittest.mock import patch
        lease = self.load('lease', 'standalone/lease.py')
        for rate, hours, spent in [(float('nan'),1,0),(1,4,0),(10,1,795),(1,1,-1)]:
            with self.assertRaises(ValueError):
                lease.create(argparse.Namespace(hourly_usd=rate,hours=hours,spent_usd=spent))
        for provider in ['vast','shadeform']:
            with tempfile.TemporaryDirectory() as directory:
                path = Path(directory)
                for name in ['instance-key','instance-key.pub']:
                    (path/name).write_text('synthetic-not-a-credential')
                (path/'lease.json').write_text(json.dumps({'id':'our-instance','provider':provider,
                    'ssh_key_id':'our-key','hourly_usd':1,'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat()}))
                def response(cloud, endpoint, *args, **kwargs):
                    if endpoint.startswith('/instances/?') or endpoint=='/instances':
                        return {'instances':[{'id':'another-track'}]}
                    if endpoint=='/sshkeys':
                        return {'ssh_keys':[]}
                    return {}
                with patch.object(lease,'request',side_effect=response):
                    lease.delete(path)
                self.assertTrue(json.loads((path/'lease.json').read_text())['deletion_verified'])
                self.assertFalse((path/'instance-key').exists())

    def test_shadow_policy(self):
        recommend = self.load('shadow','eval/shadow_policy.py').recommend
        self.assertEqual(recommend({'targets':[{},{}]})['targetIndex'], 1)
        with self.assertRaises(ValueError):
            recommend({'targets':[]})

    def test_bootstrap(self):
        bootstrap = self.load('calibrate', 'eval/calibrate.py').bootstrap
        self.assertEqual(bootstrap([1] * 50, repeats=100), [1, 1])
        self.assertEqual(bootstrap([0] * 50, repeats=100), [0, 0])
        self.assertEqual(bootstrap([0, 1], repeats=100), bootstrap([0, 1], repeats=100))
        with self.assertRaises(ValueError):
            bootstrap([])


class ScorerTests(unittest.TestCase):
    def score(self, a, b):
        return tools.score(call(a), call(b), ['lookup'])

    def test_exact_nested(self):
        self.assertEqual(self.score({'b': [1, {'c': True}], 'a': 2}, {'a': 2, 'b': [1, {'c': True}]})['exact'], 1)

    def test_types(self):
        self.assertEqual(self.score({'a': '1'}, {'a': 1})['argument_f1'], 0)
        self.assertEqual(self.score({'a': True}, {'a': 1})['exact'], 0)

    def test_missing_extra(self):
        self.assertAlmostEqual(self.score({'a': 1}, {'a': 1, 'b': 2})['argument_f1'], 2/3)
        self.assertAlmostEqual(self.score({'a': 1, 'b': 2}, {'a': 1})['argument_f1'], 2/3)

    def test_multiple(self):
        a = call({'x': 1}) + call({'x': 2})
        self.assertEqual(tools.score(a, list(reversed(a)), ['lookup'])['exact'], 1)
        self.assertEqual(tools.score(a, a[:1], ['lookup'])['exact'], 0)

    def test_invalid(self):
        for value in ['not json', '{', None, 3, '[null]', '{}']:
            result = tools.score(value, call({'x': 1}), ['lookup'])
            self.assertEqual(result['exact'], 0)

    def test_hallucination_overcall_refusal(self):
        self.assertEqual(tools.score(call({}), [], [])['over_call'], 1)
        self.assertEqual(tools.score(call({}, 'fake'), [], ['lookup'])['no_hallucination'], 0)
        self.assertEqual(tools.score([], call({}), ['lookup'])['refusal'], 1)


if __name__ == '__main__':
    unittest.main()
