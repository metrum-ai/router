# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Assert serving launch arguments using a fake sudo/docker, never a GPU."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE=Path(__file__).resolve().parent

class ServeTests(unittest.TestCase):
    def test_qwen_launch_bounds_mamba_sequences(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);sudo=root/'sudo';log=root/'calls'
            sudo.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$MOCK_LOG"\n');sudo.chmod(0o700)
            for mode in ('base','lora','merged'):
                log.write_text('')
                env={'PATH':str(root)+':'+os.environ['PATH'],'MODEL_DIR':str(root),
                     'ADAPTER_DIR':str(root),'DRY_RUN':'0','MODE':mode,'MOCK_LOG':str(log)}
                subprocess.run(['bash',str(HERE/'run.sh')],env=env,check=True,timeout=10)
                args=log.read_text().split()
                self.assertEqual(args[args.index('--max-num-seqs')+1],'128')
                self.assertEqual(args[args.index('--max-model-len')+1],'8192')
                self.assertEqual('--enable-lora' in args,mode=='lora')

if __name__=='__main__':unittest.main()
