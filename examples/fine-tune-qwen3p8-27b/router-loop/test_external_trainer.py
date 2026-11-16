# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import zipfile

import external_trainer


class ExternalTrainerTests(unittest.TestCase):
    def test_external_candidate_passes_frozen_gate_without_copying_text(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            train = (json.dumps({'messages': [{'role': 'user', 'content': 'private prompt'}],
                                 'outcome': {'task_success': True}}) + '\n').encode()
            manifest = {'dataset_hash': 'dataset-hash',
                        'files': {'train.jsonl': {'sha256': hashlib.sha256(train).hexdigest()}}}
            export = root / 'export.zip'
            with zipfile.ZipFile(export, 'w') as archive:
                archive.writestr('manifest.json', json.dumps(manifest))
                archive.writestr('train.jsonl', train)
            gate = root / 'gate.json'
            gate.write_text(json.dumps({'id': 'gate-v1', 'minimum_train_rows': 1,
                                        'minimum_train_success_rate': 1.0}))
            output = root / 'candidate.json'
            self.assertTrue(external_trainer.run(export, gate, output, 'mock/m-two'))
            result = json.loads(output.read_text())
            self.assertEqual(result['candidate_target'], 'mock/m-two')
            self.assertEqual(result['gate'], {'id': 'gate-v1', 'passed': True})
            self.assertNotIn('private prompt', output.read_text())

    def test_frozen_gate_rejects_insufficient_training_quality(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            train = (json.dumps({'messages': [], 'outcome': {'task_success': False}}) + '\n').encode()
            manifest = {'dataset_hash': 'dataset-hash',
                        'files': {'train.jsonl': {'sha256': hashlib.sha256(train).hexdigest()}}}
            export = root / 'export.zip'
            with zipfile.ZipFile(export, 'w') as archive:
                archive.writestr('manifest.json', json.dumps(manifest))
                archive.writestr('train.jsonl', train)
            gate = root / 'gate.json'
            gate.write_text(json.dumps({'id': 'gate-v1', 'minimum_train_rows': 1,
                                        'minimum_train_success_rate': 1.0}))
            self.assertFalse(external_trainer.run(export, gate, root / 'candidate.json', 'mock/m-two'))


if __name__ == '__main__':
    unittest.main()
