# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline checks: the public model downloader uses the host CLI without secrets."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import download_models

class DownloadTests(unittest.TestCase):
    def test_host_cli_uses_revision_and_no_credentials(self):
        with patch.dict(os.environ, {'HF_TOKEN':'private-placeholder','OPENAI_API_KEY':'private-placeholder'}), patch('download_models.subprocess.run') as run:
            download_models.download_one('/tmp/hf','example/public','a'*40,Path('/tmp/weights'),Path('/tmp/hf-cache'))
        args=run.call_args.args[0];env=run.call_args.kwargs['env']
        self.assertEqual(args[:5], ['/tmp/hf','download','example/public','--revision','a'*40])
        self.assertEqual(args.count('--include'),len(download_models.ALLOW))
        self.assertEqual(args[args.index('--include'):], [item for pattern in download_models.ALLOW for item in ('--include',pattern)])
        self.assertEqual(env['HF_HUB_DISABLE_IMPLICIT_TOKEN'],'1')
        self.assertNotIn('HF_TOKEN',env);self.assertNotIn('OPENAI_API_KEY',env)
        self.assertTrue(run.call_args.kwargs['check'])

    def test_hf_cli_help_offline(self):
        executable=shutil.which('hf')
        if not executable:self.skipTest('pinned hf CLI dependency not installed')
        with tempfile.TemporaryDirectory() as home:
            env={'PATH':os.environ['PATH'],'HOME':home,'HF_HOME':home,'HF_HUB_OFFLINE':'1','HF_HUB_DISABLE_TELEMETRY':'1'}
            subprocess.run([executable,'download','--help'],env=env,check=True,stdout=subprocess.DEVNULL)

    def test_offline_host_wrapper_uses_uv_and_no_container(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);log=root/'calls';uv=root/'uv';(root/'download_models.py').write_text('# fake')
            uv.write_text('''#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$MOCK_LOG"
if [[ $1 == venv ]]; then
  mkdir -p "$4/bin"
  printf '%s\\n' '#!/usr/bin/env bash' 'printf "python %s\\\\n" "$*" >> "$MOCK_LOG"' > "$4/bin/python"
  chmod +x "$4/bin/python"
fi
''');uv.chmod(0o755)
            script=Path(__file__).with_name('download_host.sh')
            subprocess.run(['bash',str(script),str(root)],env={'PATH':os.environ['PATH'],'MOCK_LOG':str(log)},check=True)
            calls=log.read_text();self.assertIn('huggingface_hub==1.14.0 click==8.3.1',calls)
            self.assertIn('download_models.py --hf',calls);self.assertIn('--out',calls);self.assertNotIn('docker',calls)

if __name__=='__main__':unittest.main()
