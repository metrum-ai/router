# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline CPU-only validation using the pinned Axolotl image source schema."""
import hashlib
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import yaml
from pydantic import ValidationError
from axolotl.utils.schemas.config import AxolotlConfigWCapabilities

HERE=Path(__file__).resolve().parent
RECIPE_SHA='c546981d61015d3ff06f7d47455ae9c335997d60cf3c4ad90e38cf92d68c679d'
CAPS={'bf16':True,'fp8':False,'tf32':True,'n_gpu':1,'n_node':1,'compute_capability':'sm_90'}


def validate(cfg):
    # Schema only: H100 capabilities are declared inputs, not hardware proof.
    return AxolotlConfigWCapabilities.model_validate({**cfg,'capabilities':CAPS,'env_capabilities':{'torch_version':'2.14.1'}})


class SchemaChecks(unittest.TestCase):
    def test_frozen_and_rendered_smoke_config(self):
        recipe=(HERE/'axolotl.yaml').read_bytes()
        self.assertEqual(hashlib.sha256(recipe).hexdigest(),RECIPE_SHA)
        full=yaml.safe_load(recipe)
        with tempfile.TemporaryDirectory() as tmp:
            rendered=Path(tmp)/'smoke.yaml'
            subprocess.run([sys.executable,str(HERE/'smoke_config.py'),'--recipe',str(HERE/'axolotl.yaml'),'--out',str(rendered)],check=True)
            smoke=yaml.safe_load(rendered.read_text())
        with patch.object(socket.socket,'connect',side_effect=AssertionError('network forbidden')):
            validate(full);validate(smoke)
            bad=dict(smoke);bad['test_datasets']=[]
            with self.assertRaises(ValidationError):validate(bad)
        self.assertEqual(smoke['test_datasets'],full['test_datasets'])
        self.assertEqual(smoke['max_steps'],1)
        self.assertEqual(smoke['datasets'][0]['path'],'/data/canonical/smoke.jsonl')
        expected={'datasets','evals_per_epoch','max_steps','output_dir','dataset_prepared_path'}
        self.assertEqual({k for k in full.keys()|smoke.keys() if full.get(k)!=smoke.get(k)},expected)

if __name__=='__main__':unittest.main()
