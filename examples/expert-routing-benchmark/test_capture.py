# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json,os,subprocess,sys,tempfile,unittest
from pathlib import Path

HERE=Path(__file__).resolve().parent
class CaptureTests(unittest.TestCase):
    def test_retains_logs_and_inspect_without_env_secrets(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);state=root/'state';state.mkdir();evidence=root/'evidence';binpath=root/'bin';binpath.mkdir()
            for name,data in {'workspace':{'docker_sudo':False,'root':'/tmp/proof426-test'},'connection':{'user':'test','ip':'127.0.0.1','port':22},'lease':{'id':'test-id'},'ssh-proof':{'success':True,'instance_id':'test-id'}}.items():(state/(name+'.json')).write_text(json.dumps(data))
            ssh=binpath/'ssh';ssh.write_text('''#!/usr/bin/env bash
if [[ "$*" == *"docker inspect"* ]]; then
 printf '%s\\n' '[{"Name":"/proof426-baseline","Config":{"Labels":{"metrum.task":"issue-426"},"Env":["HF_TOKEN=private-placeholder"]},"State":{"Running":false,"ExitCode":1}}]'
elif [[ "$*" == *"docker logs"* ]]; then
 printf '%s\\n' 'startup failure sample'
elif [[ "$*" == *".host.log"* ]]; then
 printf '%s\\n' 'tee startup failure sample'
else exit 1; fi
''');ssh.chmod(0o755)
            env={'PATH':str(binpath)+':'+os.environ['PATH'],'HOME':str(root)}
            if 'LD_LIBRARY_PATH' in os.environ:env['LD_LIBRARY_PATH']=os.environ['LD_LIBRARY_PATH']
            subprocess.run([sys.executable,str(HERE/'capture.py'),str(state),str(evidence),'once','baseline'],env=env,check=True,stdout=subprocess.DEVNULL)
            inspect=(evidence/'baseline.inspect.json').read_text();self.assertNotIn('private-placeholder',inspect)
            self.assertEqual(json.loads(inspect)[0]['State']['ExitCode'],1)
            self.assertEqual((evidence/'baseline.log').read_text().strip(),'startup failure sample')
            self.assertEqual((evidence/'baseline.host.log').read_text().strip(),'tee startup failure sample')
            self.assertEqual((evidence/'baseline.log').stat().st_mode&0o777,0o600)

    def test_missing_host_tee_still_saves_docker_evidence_and_fails_closed(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);state=root/'state';state.mkdir();evidence=root/'evidence';binpath=root/'bin';binpath.mkdir()
            for name,data in {'workspace':{'docker_sudo':False,'root':'/tmp/proof426-test'},'connection':{'user':'test','ip':'127.0.0.1','port':22},'lease':{'id':'test-id'},'ssh-proof':{'success':True,'instance_id':'test-id'}}.items():(state/(name+'.json')).write_text(json.dumps(data))
            ssh=binpath/'ssh';ssh.write_text('''#!/usr/bin/env bash
if [[ "$*" == *"docker inspect"* ]]; then echo '[{"Name":"/proof426-baseline","Config":{"Labels":{"metrum.task":"issue-426"}},"State":{"Running":false}}]'
elif [[ "$*" == *"docker logs"* ]]; then echo saved-log
else exit 1; fi
''');ssh.chmod(0o755)
            env={'PATH':str(binpath)+':'+os.environ['PATH'],'HOME':str(root)}
            if 'LD_LIBRARY_PATH' in os.environ:env['LD_LIBRARY_PATH']=os.environ['LD_LIBRARY_PATH']
            result=subprocess.run([sys.executable,str(HERE/'capture.py'),str(state),str(evidence),'once','baseline'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            self.assertNotEqual(result.returncode,0);self.assertTrue((evidence/'baseline.inspect.json').exists());self.assertEqual((evidence/'baseline.log').read_text().strip(),'saved-log');self.assertFalse((state/'done.txt').exists())

    def test_stop_preserves_container_and_captures_twice(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);state=root/'state';state.mkdir();evidence=root/'evidence';binpath=root/'bin';binpath.mkdir();trace=root/'ssh-calls'
            for name,data in {'workspace':{'docker_sudo':False,'root':'/tmp/proof426-test'},'connection':{'user':'test','ip':'127.0.0.1','port':22},'lease':{'id':'test-id'},'ssh-proof':{'success':True,'instance_id':'test-id'}}.items():(state/(name+'.json')).write_text(json.dumps(data))
            ssh=binpath/'ssh';ssh.write_text('''#!/usr/bin/env bash
printf '%s\\n' "$*" >> "TRACE_PLACEHOLDER"
if [[ "$*" == *"docker inspect proof426-baseline"* ]]; then
 printf '%s\\n' '[{"Name":"/proof426-baseline","Config":{"Labels":{"metrum.task":"issue-426"}},"State":{"Running":false}}]'
elif [[ "$*" == *"docker logs"* ]]; then echo saved-log
elif [[ "$*" == *".host.log"* ]]; then echo saved-host-log
elif [[ "$*" == *"docker stop"* ]]; then exit 0
else exit 1; fi
'''.replace('TRACE_PLACEHOLDER',str(trace)));ssh.chmod(0o755)
            env={'PATH':str(binpath)+':'+os.environ['PATH'],'HOME':str(root)}
            if 'LD_LIBRARY_PATH' in os.environ:env['LD_LIBRARY_PATH']=os.environ['LD_LIBRARY_PATH']
            subprocess.run([sys.executable,str(HERE/'stop_one.py'),str(state),str(evidence),'baseline'],env=env,check=True,stdout=subprocess.DEVNULL)
            commands=trace.read_text();self.assertEqual(commands.count('docker logs proof426-baseline'),2)
            self.assertIn('{{ index .Config.Labels "metrum.task" }}',commands)
            self.assertIn('docker stop proof426-baseline',commands);self.assertNotIn('docker rm',commands)
            self.assertEqual((evidence/'baseline.log').read_text().strip(),'saved-log')

if __name__=='__main__':unittest.main()
