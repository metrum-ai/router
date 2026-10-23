# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Synthetic-only checks: no dataset content, credentials, weights or GPU."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch
from collections import Counter

ROOT = Path(__file__).parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


data = load('run2_data_tests', 'run2/data.py')
render = load('run2_render_tests', 'standalone/render.py')
fw = load('run2_fw_tests', 'fireworks/run.py')
contract = load('run2_contract_tests', 'run2/template_check.py')
readiness = load('run2_readiness_tests', 'run2/readiness.py')
precheck = load('run2_generation_tests', 'run2/precheck.py')


def text_row(text):
    return {'messages': [{'role':'user','content':text}, {'role':'assistant','content':'B'}], 'tools':[]}


def root_pair():
    common = {'lang':'en','review_result':True,'deleted':False,'synthetic':False,'labels':{}}
    return [dict(common, message_id='root', role='prompter', text='Synthetic root question', parent_id=None),
            dict(common, message_id='reply', role='assistant', text='Synthetic answer', parent_id='root', rank=0)]


class Run2DataTests(unittest.TestCase):
    def test_arithmetic_correct_deterministic_balanced(self):
        rows = data.format_rehearsal(20)
        self.assertEqual(rows, data.format_rehearsal(20))
        self.assertEqual(len({data.key(r) for r in rows}), 20)
        self.assertEqual(Counter(r['messages'][-1]['content'] for r in rows), dict.fromkeys('ABCDEFGHIJ', 2))
        for row in rows:
            lines = row['messages'][0]['content'].splitlines()
            numbers = lines[0].removeprefix('What is ').removesuffix('?').split(' + ')
            index = ord(row['messages'][-1]['content']) - 65
            self.assertEqual(int(lines[1+index][3:]), sum(map(int,numbers)))
            data.legacy.validate(row)

    def test_opaque_tools_view_preserves_order_and_explicit_nulls(self):
        row = text_row('synthetic')
        row['tools'] = [{'type':'function','function':{'name':'probe','parameters':{
            'properties':{'choice':{'default':None,'enum':['a',None]}}}}}]
        original = json.dumps(row,sort_keys=True)
        view = data.axolotl_view(row)
        self.assertIsInstance(view['tools'],str)
        self.assertEqual(json.loads(view['tools']),row['tools'])
        self.assertEqual(json.dumps(row,sort_keys=True),original)

    def test_normalized_source_exclusion(self):
        rows = [text_row('ALPHA'), text_row('ＡＬＰＨＡ'), text_row(' Beta '), text_row('Gamma')]
        result = data.fresh_unique(rows, data.legacy.prompt, {'alpha'}, 2, 355)
        self.assertEqual({data.key(r) for r in result}, {'beta','gamma'})
        with self.assertRaises(ValueError):
            data.fresh_unique(rows, data.legacy.prompt, {'alpha'}, 3, 355)

    def test_source_groups_cannot_cross_splits(self):
        with self.assertRaises(ValueError):
            data.assert_disjoint({'train':[text_row('hello  world')], 'test':[text_row('ＨＥＬＬＯ world')]})
        data.assert_disjoint({'train':[text_row('one'),text_row('one')], 'test':[text_row('two')]})

    def test_json_rehearsal_is_frozen_contract_not_native_tools(self):
        row = {'tools':[{'type':'function','function':{'name':'weather','description':'Synthetic weather',
                'parameters':{'type':'object','properties':{'city':{'type':'string'}},'required':['city']}}}],
            'messages':[{'role':'user','content':'Synthetic weather in Kyoto'},
                {'role':'assistant','content':None,'tool_calls':[{'id':'call_0','type':'function',
                    'function':{'name':'weather','arguments':'{"city":"Kyoto"}'}}]}]}
        original = json.dumps(row,sort_keys=True)
        target = data.json_rehearsal(row)
        expected = data.builder.build([row])[0]
        self.assertEqual(target['messages'][:-1], json.loads(expected['vars']['messages']))
        self.assertEqual(target['tools'], [])
        self.assertEqual(json.loads(target['messages'][-1]['content']),
            {'tool_calls':[{'function':{'name':'weather','arguments':{'city':'Kyoto'}}}]})
        self.assertEqual(json.dumps(row,sort_keys=True), original)
        with self.assertRaises(ValueError):
            data.json_rehearsal(text_row('not a tool'))

    def test_general_filter_drops_metadata_flagged_and_old_cases(self):
        rows = root_pair()
        pairs = data.general_pairs(rows,set())
        self.assertEqual(len(pairs),1)
        self.assertEqual(set(pairs[0]), {'messages','tools'})
        self.assertEqual(data.general_pairs(rows,{data.key(pairs[0])}),[])
        for field, value in [('synthetic',True),('review_result',False),('rank',1),('lang','de'),('deleted',True)]:
            altered = root_pair();altered[1][field] = value
            self.assertEqual(data.general_pairs(altered,set()),[])
        for labels in ({'pii':{'value':.5}}, [{'name':'spam','value':.5}]):
            altered = root_pair();altered[1]['labels'] = labels
            self.assertEqual(data.general_pairs(altered,set()),[])
        for marker in ('<|im_start|>','<|vision_start|>','<think>','<tool_call>'):
            altered = root_pair();altered[1]['text'] += marker
            self.assertEqual(data.general_pairs(altered,set()),[])


class Run2TemplateTests(unittest.TestCase):
    def test_prefix_and_token_parity(self):
        _, messages, tools = contract.fixtures()[0]
        prefix = 'synthetic context' + contract.GENERATION_SUFFIX
        tokenizer = Mock()
        tokenizer.apply_chat_template.side_effect = [prefix, prefix+'B'+contract.END, [1,2], [1,2,3]]
        result = contract.check_case(tokenizer,messages,tools)
        self.assertEqual(result['generation_prefix_tokens'],2)
        for call in tokenizer.apply_chat_template.call_args_list:
            self.assertIs(call.kwargs['enable_thinking'],False)
        for responses in ([prefix,prefix+'B'+contract.END,[1,2],[1,9,3]],
                          ['synthetic context<think>\\n',prefix+'B'+contract.END]):
            tokenizer.apply_chat_template.side_effect = responses
            with self.assertRaises(ValueError):contract.check_case(tokenizer,messages,tools)

    def test_wire_argument_normalization_is_not_template_editing(self):
        _, messages, _ = contract.fixtures()[3]
        before = json.dumps(messages,sort_keys=True)
        normalized = contract.template_messages(messages)
        self.assertEqual(normalized[-1]['tool_calls'][0]['function']['arguments'],{'value':'synthetic'})
        self.assertEqual(json.dumps(messages,sort_keys=True),before)


class Run2ReadinessTests(unittest.TestCase):
    def test_frozen_inputs_and_base_proofs_required(self):
        with tempfile.TemporaryDirectory() as td:
            w = Path(td)
            for directory in ('run2','canonical','base'):(w/directory).mkdir()
            for name in ('canonical/train.jsonl','canonical/val.jsonl','canonical/train-axolotl.jsonl','canonical/val-axolotl.jsonl','canonical/manifest.json','base/chat_template.jinja','source.txt'):
                (w/name).write_text('synthetic fixture')
            registration = {'status':'frozen','protocol':'synthetic-only','code_sha256':{'source.txt':readiness.sha(w/'source.txt')},
                'datasets':{'sha256':{n:readiness.sha(w/'canonical'/f'{n}.jsonl') for n in ('train','val')},
                            'axolotl_view_sha256':{n:readiness.sha(w/'canonical'/f'{n}-axolotl.jsonl') for n in ('train','val')}},
                'dataset_manifest_sha256':readiness.sha(w/'canonical/manifest.json'),
                'chat_template':{'sha256':readiness.sha(w/'base/chat_template.jinja')},'precheck_cases_sha256':'synthetic-cases'}
            (w/'run2/preregistration.json').write_text(json.dumps(registration))
            identity = readiness.sha(w/'run2/preregistration.json')
            template = {'pass':True,'preregistration_sha256':identity,'base_template_sha256':registration['chat_template']['sha256']}
            generation = {'complete':True,'preregistration_sha256':identity,'model':'qwen3p8-27b-base',
                'case_count':12,'strict_letter_format':12,'synthetic_cases_sha256':'synthetic-cases'}
            (w/'base-template-check.json').write_text(json.dumps(template))
            (w/'base-format-precheck.json').write_text(json.dumps(generation))
            self.assertTrue(readiness.verify(w)['ready'])
            for split in ('train','val'):
                (w/f'prepared-check-{split}.json').write_text(json.dumps({'pass':True,
                    'preregistration_sha256':identity,'canonical_sha256':registration['datasets']['sha256'][split]}))
            self.assertTrue(readiness.verify(w,require_prepared=True)['ready'])
            generation['strict_letter_format'] = 11
            (w/'base-format-precheck.json').write_text(json.dumps(generation))
            with self.assertRaisesRegex(ValueError,'single-letter'):readiness.verify(w)
            generation['strict_letter_format'] = 12
            (w/'base-format-precheck.json').write_text(json.dumps(generation))
            (w/'source.txt').write_text('source drift')
            with self.assertRaisesRegex(ValueError,'source checksum'):readiness.verify(w)

    def test_development_cases_disjoint_from_training_generator(self):
        training = {data.key(r) for r in data.format_rehearsal()}
        development = {data.legacy.normalized(c['messages'][0]['content']) for c in precheck.cases()}
        self.assertEqual(len(development),12)
        self.assertFalse(training & development)

    def test_hardware_neutral_provider_and_references(self):
        import yaml
        config = yaml.safe_load((ROOT/'config.qwen3p8-ft.example.yaml').read_text())
        self.assertIn('vllm_onprem_ft',config['providers'])
        for group in config['models'].values():
            for target in group.get('targets',[]):self.assertIn(target['provider'],config['providers'])
        self.assertEqual(config['models']['qwen3p8-27b-base']['targets'][0]['provider'],'vllm_onprem_ft')
        self.assertEqual(config['models']['qwen3p8-27b-ft-onprem']['targets'][0]['provider'],'vllm_onprem_ft')


class Run2RecipeTests(unittest.TestCase):
    def test_default_run1_preserved(self):
        old, new = render.render(), render.render(recipe='run2')
        self.assertEqual(old['learning_rate'], .0001)
        self.assertEqual(old['num_epochs'],2)
        self.assertEqual(old['lora_r'],16)
        for field in ('base_model','chat_template','chat_template_kwargs','sequence_len','load_in_4bit','load_in_8bit'):
            self.assertEqual(old[field],new[field])
        self.assertEqual(new['learning_rate'],.00001)
        self.assertEqual(new['num_epochs'],1)
        self.assertEqual(new['lora_r'],8)
        self.assertEqual(set(new['lora_target_modules']),
            {'q_proj','v_proj','linear_attn.in_proj_qkv','linear_attn.out_proj'})
        self.assertTrue(set(new['lora_target_modules']) < set(old['lora_target_modules']))
        with self.assertRaises(ValueError):
            render.render(recipe='arbitrary-path')

    def test_same_scalar_criteria_new_protocol_only(self):
        old = json.loads((ROOT/'eval/preregistration.json').read_text())
        new = json.loads((ROOT/'run2/scalar-preregistration.json').read_text())
        self.assertNotEqual(old.pop('preregistration_id'),new.pop('preregistration_id'))
        self.assertEqual(old,new)

    def test_smoke_and_multi_gpu(self):
        for gpus in (1,2,4,8):
            cfg = render.render(gpus,recipe='run2')
            self.assertEqual(cfg['gradient_accumulation_steps'] * gpus,16)
        cfg = render.render(smoke=True,recipe='run2')
        self.assertEqual(cfg['max_steps'],2)
        self.assertEqual(cfg['lora_r'],8)
        self.assertNotIn('test_datasets',cfg)

    def test_rank8_upload_metadata_and_resume_guard(self):
        with tempfile.TemporaryDirectory() as td:
            path = Path(td)
            (path/'adapter_model.safetensors').write_bytes(b'synthetic-not-weights')
            (path/'adapter_config.json').write_text(json.dumps({'r':8,'target_modules':['q_proj']}))
            calls = []
            def api(url,payload=None,method='POST'):
                calls.append((url,payload,method))
                if ':getUploadEndpoint' in url:return {'filenameToSignedUrls':{}}
                if method=='GET' and ':validateUpload' not in url:
                    return {'peftDetails':{'baseModel':'accounts/fireworks/models/qwen3p8-27b','r':16,'targetModules':['q_proj']}}
                return {}
            with patch.object(fw,'api',side_effect=api):
                fw.upload('synthetic-account','synthetic-model',path)
                self.assertEqual(calls[0][1]['model']['peftDetails']['r'],8)
                with self.assertRaisesRegex(ValueError,'base/rank/target'):
                    fw.upload('synthetic-account','synthetic-model',path,resume=True)
            (path/'adapter_config.json').write_text(json.dumps({'r':8.0,'target_modules':['q_proj']}))
            with self.assertRaises(ValueError):fw.upload('synthetic-account','synthetic-model',path)


if __name__ == '__main__':
    unittest.main()
