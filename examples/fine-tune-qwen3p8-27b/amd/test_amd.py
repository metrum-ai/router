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

    def test_training_offline(self):
        args = plan.command('smoke', 'rocm/pytorch@sha256:'+'b'*64)
        self.assertIn('--network=none', args)
        self.assertEqual(args[-1], '/data/smoke.yaml')
        self.assertNotIn('-p', args)

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
