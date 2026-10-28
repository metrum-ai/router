# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE=Path(__file__).resolve().parent
class Run3ServeTests(unittest.TestCase):
    def test_smoke_and_final_bound_model_sequences(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);model=root/'models';model.mkdir();bindir=root/'bin';bindir.mkdir();log=root/'calls'
            (model/'base').mkdir()
            for name in ('adapter','smoke-adapter'):
                (model/name).mkdir();(model/name/'adapter_model.safetensors').touch()
            docker=bindir/'docker';docker.write_text('''#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$MOCK_LOG"
if [[ $1 == container && $2 == inspect ]]; then exit 1; fi
if [[ $1 == info && $2 == --format ]]; then echo /tmp; fi
exit 0
''');docker.chmod(0o700)
            df=bindir/'df';df.write_text('#!/bin/sh\nprintf "Filesystem 1024-blocks Used Available Capacity Mounted\\nfake 100000000 1 90000000 1%% /\\n"\n');df.chmod(0o700)
            env={'PATH':str(bindir)+':'+os.environ['PATH'],'MOCK_LOG':str(log)}
            for mode in ('base','smoke','smoke-eager','final','final-eager'):
                log.write_text('');subprocess.run(['bash',str(HERE/'serve_eval.sh'),str(model),mode],env=env,check=True,timeout=10)
                run=next(line for line in log.read_text().splitlines() if line.startswith('run ')).split()
                self.assertEqual(run[run.index('--max-num-seqs')+1],'128')
                self.assertEqual('--enable-lora' in run,mode!='base')
                self.assertEqual('--enforce-eager' in run,mode.endswith('-eager'))
                self.assertNotIn('--rm',run)
                self.assertNotIn('-e',run)

if __name__=='__main__':unittest.main()
