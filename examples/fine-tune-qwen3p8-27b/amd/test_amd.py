# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest
import json
import hashlib
from pathlib import Path
import yaml
import plan
import render
import train_peft
import sdk
import localhost_proxy
import qualification
from unittest.mock import patch, MagicMock


class AMDTests(unittest.TestCase):
    def test_fallback_supervises_all_assistant_turns(self):
        class Tokenizer:
            def apply_chat_template(self, messages, **kwargs):
                values = []
                for message in messages:
                    values += [1 if message['role'] == 'user' else 2, 10, 11]
                if kwargs['add_generation_prompt']:
                    values += [2]
                return {'input_ids': values}
        row = {'messages': [{'role': 'user'}, {'role': 'assistant'}, {'role': 'user'}, {'role': 'assistant'}], 'tools': '[]'}
        encoded = train_peft.encode(row, Tokenizer())
        self.assertEqual(encoded['labels'], [-100]*4+[10]+[-100]*5+[10,-100])
        self.assertEqual(train_peft.token_ids({'input_ids': [3,4]}), [3,4])
        class Broken(Tokenizer):
            def apply_chat_template(self, messages, **kwargs):
                return [99] if kwargs['add_generation_prompt'] else [1,2]
        with self.assertRaises(ValueError):
            train_peft.encode(row, Broken())

    def test_fixed_relay_preserves_bytes_and_request_id_no_redirect(self):
        client = MagicMock()
        response = client.getresponse.return_value
        response.status = 200
        response.read.return_value = b'{"unchanged":true}'
        response.getheader.side_effect = lambda name: {'Content-Type':'application/json','X-Request-Id':'proof'}.get(name)
        with patch.object(localhost_proxy.http.client, 'HTTPConnection', return_value=client) as create:
            result = localhost_proxy.forward_request('POST','/v1/chat/completions',b'{"model":"support-agent"}',{'Authorization':'Bearer synthetic','X-Untrusted':'drop'})
            self.assertEqual(result, (200, {'Content-Type':'application/json','X-Request-Id':'proof'}, b'{"unchanged":true}'))
            create.assert_called_once_with('127.0.0.1',18592,timeout=180)
            self.assertEqual(client.request.call_args.kwargs['body'], b'{"model":"support-agent"}')
            self.assertNotIn('X-Untrusted',client.request.call_args.kwargs['headers'])
            response.status = 302
            with self.assertRaises(ValueError):
                localhost_proxy.forward_request('GET','/v1/models',b'',{})
            self.assertEqual(client.request.call_count,2)  # one each, no redirect/retry
        with self.assertRaises(ValueError):
            localhost_proxy.forward_request('POST','http://upstream.invalid/',b'',{})
        self.assertEqual(sdk.REGISTRATION, 'e411fc9f7c6216eb87e659d3b120561bc0e1b3f45ef2ef6c623637796e0ee1ea')
        self.assertEqual(localhost_proxy.LISTEN,('127.0.0.1',18596))

    def test_predispatch_duplicate_remains_failure_denominator_not_fake_join(self):
        first = {'provider':{'id':'openai:chat:synthetic'},'testIdx':0,'response':{'output':'synthetic'}}
        duplicate = {'provider':{'id':'openai:chat:synthetic'},'testIdx':1,'response':{},
            'vars':{'declared':['lookup','lookup']},'error':'Error: schema is invalid: data/properties/function/properties/name/enum must NOT have duplicate items'}
        doc = {'results':{'results':[first,duplicate]}}
        journal = [{'provider':'openai:chat:synthetic','case_index':0,'router_request_id':'req_proof'}]
        lookup = lambda _: {'status':200,'target_provider':'synthetic','request_id':'req_proof'}
        result = qualification.reconcile(doc,journal,lookup)
        self.assertEqual(result['complete_result_denominator'],2)
        self.assertEqual(result['joined_http_requests'],1)
        self.assertEqual(result['known_duplicate_declaration_no_dispatch'],1)
        self.assertTrue(result['coverage_complete'])
        duplicate['error'] = 'HTTP timeout, unknown dispatch'
        result = qualification.reconcile(doc,journal,lookup)
        self.assertFalse(result['coverage_complete'])
        self.assertEqual(result['known_duplicate_declaration_no_dispatch'],0)
        self.assertEqual(result['unknown_missing_dispatch_or_ids'],1)
        with self.assertRaises(ValueError):
            qualification.reconcile(doc,journal+journal,lookup)

    def test_official_digest_only(self):
        for image in ['rocm/vllm:latest', 'vllm/vllm-openai@sha256:'+'a'*64,
                      'rocm/vllm@sha256:'+'A'*64, 'rocm/vllm@sha256:bad']:
            with self.assertRaises(ValueError):
                plan.command('serve', image)
        with self.assertRaises(ValueError):
            plan.command('train', 'rocm/vllm@sha256:'+'a'*64)

    def test_loopback_and_no_privileged(self):
        args = plan.command('serve', 'rocm/vllm@sha256:'+'a'*64)
        self.assertIn('127.0.0.1:18590:8000', args)
        self.assertNotIn('--privileged', args)
        self.assertNotIn('--network=host', args)
        self.assertIn('--device=/dev/kfd', args)
        self.assertEqual(args[args.index('--max-num-seqs')+1], '128')

    def test_axolotl_training_commands_retired(self):
        for mode in ('smoke', 'train'):
            with self.assertRaisesRegex(ValueError, 'retired'):
                plan.command(mode, 'rocm/pytorch@sha256:'+'b'*64)

    def test_current_qualification_envelope_is_not_training_parent(self):
        root = Path(__file__).resolve().parents[3]/'docs/evidence/finetune-amd/2026-10-23'
        raw = (root/'qualification-preregistration.json').read_bytes()
        self.assertEqual(hashlib.sha256(raw).hexdigest(), 'e411fc9f7c6216eb87e659d3b120561bc0e1b3f45ef2ef6c623637796e0ee1ea')
        reg = json.loads(raw)
        frozen = json.loads((root/'shared-freeze.json').read_text())
        self.assertEqual(reg['datasets']['dataset_hash'], frozen['dataset']['dataset_hash'])
        self.assertEqual(reg['datasets']['export_sha256'], frozen['dataset']['export_sha256'])
        self.assertEqual(reg['promotion_rules']['tool_exact_gain_pp_at_least'], 10)
        self.assertEqual(reg['promotion_rules']['required_tool_denominator'], 200)

    def test_final_freeze_keeps_endpoints_and_failure_denominators(self):
        root = Path(__file__).resolve().parents[3]
        freeze = json.loads((root/'docs/evidence/finetune-amd/2026-10-23/shared-freeze.json').read_text())
        self.assertTrue(freeze['status'].startswith('frozen_'))
        self.assertEqual(freeze['dataset']['counts'], {'train': 1186, 'val': 221, 'test': 300})
        self.assertEqual(freeze['dataset']['historical_teacher_failures_in_test'], 98)
        evaluation = freeze['evaluation']
        self.assertEqual(evaluation['native']['tool_cases'], 200)
        self.assertEqual(evaluation['gates']['original_json_text_exact_gain_pp_minimum'], 10)
        self.assertEqual(evaluation['gates']['additional_native_task_regression_maximum'], 0)
        self.assertIn('2 policy-mapped cases', evaluation['oracle_mapping'])
        self.assertFalse(freeze['shared_recipe']['tf32'])
        self.assertEqual(freeze['shared_recipe']['seed'], 355)
        for value in freeze['dataset']['export_sha256'].values():
            self.assertRegex(value, r'^[a-f0-9]{64}$')

    def test_shared_recipe_proposal_parity(self):
        root = Path(__file__).parent
        proposal = json.loads((root/'freeze-proposal.json').read_text())
        recipe = yaml.safe_load((root.parent/'standalone/axolotl.yaml').read_text())
        recipe.update(proposal['shared_recipe'])
        config = render.render(recipe, 'a'*64)
        for key, expected in proposal['shared_recipe'].items():
            self.assertEqual(config[key], expected, key)
        self.assertFalse(config['tf32'])
        self.assertIn('6366186842', proposal['teacher_terms_decision']['reference'])
        self.assertEqual(proposal['status'], 'coordination_proposal_not_training_authorization')

    def test_config_and_cache_identity(self):
        recipe = yaml.safe_load((Path(__file__).parents[1]/'standalone/axolotl.yaml').read_text())
        smoke = render.render(recipe, 'a'*64, True)
        train = render.render(recipe, 'a'*64)
        other = render.render(recipe, 'b'*64)
        self.assertEqual(smoke['max_steps'], 2)
        self.assertFalse(smoke['tf32'])
        self.assertEqual(smoke['attn_implementation'], 'sdpa')
        self.assertEqual(train['lora_r'], 8)
        self.assertEqual(train['num_epochs'], 1)
        self.assertNotIn('test_datasets', smoke)
        self.assertNotEqual(train['dataset_prepared_path'], smoke['dataset_prepared_path'])
        self.assertNotEqual(train['dataset_prepared_path'], other['dataset_prepared_path'])
        self.assertEqual(recipe['lora_r'], 16)
        with self.assertRaises(ValueError):
            render.render(recipe, 'not-a-manifest')


if __name__ == '__main__':
    unittest.main()
