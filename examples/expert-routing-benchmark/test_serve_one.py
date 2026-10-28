# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE=Path(__file__).resolve().parent

class ServeOneTests(unittest.TestCase):
    def test_one_pinned_container_retained_without_secret_flags(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);models=root/'models';models.mkdir();bindir=root/'bin';bindir.mkdir();log=root/'calls'
            for n in ('medical-base','medical-adapter','finance','math','coding','general'):(models/n).mkdir()
            docker=bindir/'docker';docker.write_text('''#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$MOCK_LOG"
if [[ $1 == container && $2 == inspect ]]; then exit 1; fi
exit 0
''');docker.chmod(0o755)
            for mode,gpu in (('baseline','all'),('biomedical','device=0'),('finance','device=1'),('math','device=2'),('coding','device=3')):
                log.write_text('');env={'PATH':str(bindir)+':'+os.environ['PATH'],'MOCK_LOG':str(log)}
                subprocess.run(['bash',str(HERE/'serve-one.sh'),mode,str(models)],env=env,check=True,stdout=subprocess.DEVNULL)
                calls=log.read_text().splitlines();runs=[x for x in calls if x.startswith('run ')]
                self.assertEqual(len(runs),1);run=runs[0].split()
                self.assertNotIn('--rm',run);self.assertNotIn('-e',run);self.assertNotIn('--env-file',run)
                self.assertEqual(run[run.index('--gpus')+1],gpu)
                self.assertIn('proof426-'+mode,run)
                if mode in ('baseline','finance','math'):
                    self.assertEqual(run[run.index('--max-num-seqs')+1],'128')
                else:
                    self.assertNotIn('--max-num-seqs',run)
                if mode == 'baseline': self.assertEqual(run[run.index('--tensor-parallel-size')+1],'4')
                self.assertTrue(any('sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34' in x for x in run))

    def test_stop_retains_labeled_containers(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);log=root/'calls';docker=root/'docker'
            docker.write_text('''#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$MOCK_LOG"
if [[ $1 == inspect && "$*" == *Config.Labels* ]]; then echo issue-426; fi
if [[ $1 == inspect && "$*" == *State.Running* ]]; then echo false; fi
exit 0
''');docker.chmod(0o755)
            subprocess.run(['bash',str(HERE/'stop.sh')],env={'PATH':str(root)+':'+os.environ['PATH'],'MOCK_LOG':str(log)},check=True,stdout=subprocess.DEVNULL)
            calls=log.read_text().splitlines()
            self.assertEqual(sum(x.startswith('stop ') for x in calls),5)
            self.assertFalse(any(x.startswith('rm ') or x.startswith('container rm ') for x in calls))

if __name__=='__main__':unittest.main()
