# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import copy
import unittest
from run2.grader_route import configure

class GraderRouteTests(unittest.TestCase):
    def fixture(self):
        return {'providers': {'frontier_judge': {'models': {'served': {
            'model': 'gpt-4o-2024-11-20', 'modalities': [], 'input_modalities': []}}}},
            'models': {'frontier-ref': {'targets': [{'provider': 'frontier_judge',
                'model_ref': 'served', 'modalities': []}]}}}

    def test_serialized_empty_legacy_override_repaired_without_model_change(self):
        config = self.fixture()
        configure(config)
        self.assertEqual(config['providers']['frontier_judge']['models']['served']['model'], 'gpt-4o-2024-11-20')
        for target in [config['providers']['frontier_judge']['models']['served'],
                       config['models']['frontier-ref']['targets'][0]]:
            self.assertEqual(target['modalities'], ['text'])
            self.assertEqual(target['input_modalities'], ['text'])

    def test_different_grader_fails_closed_without_mutation(self):
        config = self.fixture()
        config['providers']['frontier_judge']['models']['served']['model'] = 'other'
        original = copy.deepcopy(config)
        with self.assertRaises(ValueError):
            configure(config)
        self.assertEqual(config, original)
