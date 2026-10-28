# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import setup


class SetupTests(unittest.TestCase):
    def test_hash_keys_tmpfs_and_validate_both_configs(self):
        with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryDirectory(dir='/dev/shm') as tmp:
            root=Path(directory);tokens=Path(tmp)/'tokens';state=root/'state';setup.build(state,tokens)
            values=[(tokens/name).read_text().strip() for name in setup.PERSONAS]
            self.assertEqual(len(set(values)),4)
            for arm in ('experts','baseline'):
                config=json.loads((state/(arm+'.json')).read_text())
                self.assertNotIn('token_sha256',json.dumps(config));self.assertTrue(all(v not in json.dumps(config) for v in values))
                self.assertEqual(len(config['callers']),4)
                subprocess.run(['go','run','./cmd/metrum-ai-routerctl','config','validate','--config',str(state/(arm+'.json'))],cwd=setup.ROOT,
                               check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=180)
            for name in setup.PERSONAS:self.assertEqual((tokens/name).stat().st_mode & 0o777,0o600)

    def test_server_launch_uses_official_image_and_no_env_flags(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);models=root/'models';models.mkdir()
            for name in ('medical-base','medical-adapter','finance','math','coding','general'):(models/name).mkdir()
            log=root/'docker.log';docker=root/'docker'
            docker.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$DOCKER_TEST_LOG"\ncase "$1 $2" in "container inspect") exit 1;; esac\nexit 0\n');docker.chmod(0o700)
            env={'PATH':str(root)+':'+os.environ['PATH'],'HOME':str(root),'DOCKER_TEST_LOG':str(log)}
            result=subprocess.run(['bash',str(setup.HERE/'serve.sh'),'experts',str(models)],env=env,text=True,capture_output=True,timeout=30)
            self.assertEqual(result.returncode,0,result.stderr);lines=log.read_text().splitlines();launch=[s for s in lines if s.startswith('run ')]
            self.assertEqual(len(launch),4)
            self.assertTrue(all('vllm/vllm-openai@sha256:' in s and 'readonly' in s and '--network host' in s for s in launch))
            self.assertTrue(all(' --env' not in s and ' -e ' not in s for s in launch))

    def test_download_manifest_is_pinned_and_avoids_remote_code(self):
        from unittest.mock import patch
        import sys
        from types import SimpleNamespace
        with patch.dict(sys.modules, {'huggingface_hub': SimpleNamespace(snapshot_download=lambda **kwargs: None)}):
            spec=importlib.util.spec_from_file_location('download_models',setup.HERE/'download_models.py')
            module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
        self.assertEqual(len(module.MODELS),6)
        self.assertTrue(all(len(revision)==40 for _,revision in module.MODELS.values()))
        self.assertNotIn('*.py',module.ALLOW);self.assertNotIn('*.bin',module.ALLOW)

    def test_in_container_servers_use_disjoint_gpus_and_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);models=root/'models';models.mkdir()
            for name in ('medical-base','medical-adapter','finance','math','coding','general'):(models/name).mkdir()
            log=root/'vllm.log';binary=root/'vllm'
            binary.write_text('#!/bin/sh\nprintf "%s %s\\n" "$CUDA_VISIBLE_DEVICES" "$*" >> "$VLLM_TEST_LOG"\nsleep .2\n');binary.chmod(0o700)
            env={'PATH':str(root)+':'+os.environ['PATH'],'HOME':str(root),'VLLM_TEST_LOG':str(log)}
            result=subprocess.run(['bash',str(setup.HERE/'serve-in-container.sh'),'experts',str(models)],env=env,text=True,capture_output=True,timeout=30)
            self.assertEqual(result.returncode,1);self.assertIn('server exited unexpectedly',result.stderr)
            lines=log.read_text().splitlines();self.assertEqual({s.split()[0] for s in lines},{'0','1','2','3'})
            self.assertTrue(all('--host 127.0.0.1' in s for s in lines))


if __name__=='__main__':unittest.main()
