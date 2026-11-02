#!/usr/bin/env python3
"""Generate five fixed synthetic Harbor tasks; offline-safe and deterministic."""
from pathlib import Path

TASKS={
 'hello':('Create /app/hello.txt containing exactly Hello, Harbor! followed by one newline.', 'hello.txt', 'Hello, Harbor!\n'),
 'uppercase':('Create /app/result.txt containing the uppercase form of the word harbor, followed by one newline.', 'result.txt', 'HARBOR\n'),
 'json':('Create /app/result.json containing exactly the JSON object {"ready": true, "count": 3}.', 'result.json', '{"ready": true, "count": 3}\n'),
 'sum':('Create /app/result.txt containing the sum of 17 and 25 followed by one newline.', 'result.txt', '42\n'),
 'edit':('Fix /app/calc.py so add(a, b) returns the sum. Run python3 -m unittest test_calc.py.', 'calc.py', None),
}
DOCKER='FROM node:22.16.0-bookworm-slim\nRUN apt-get update -qq && apt-get install -y -qq python3 && npm install -g --ignore-scripts @earendil-works/pi-coding-agent@1.4.0 && pi --version\nWORKDIR /app\nCOPY workspace/ /app/\nCMD ["sleep", "infinity"]\n'
CONFIG='''version = "1.0"
[metadata]
author_name = "Metrum AI"
category = "synthetic-e2e"
[verifier]
timeout_sec = 60.0
[agent]
timeout_sec = 240.0
[environment]
build_timeout_sec = 180.0
cpus = 1
memory_mb = 1024
'''

def generate(root):
 for name,(instruction,filename,expected) in TASKS.items():
  task=root/name;(task/'environment/workspace').mkdir(parents=True,exist_ok=False);(task/'tests').mkdir()
  (task/'task.toml').write_text(CONFIG);(task/'instruction.md').write_text(instruction+'\n')
  (task/'environment/Dockerfile').write_text(DOCKER)
  if name=='edit':
   (task/'environment/workspace/calc.py').write_text('def add(a, b):\n    return a - b\n')
   (task/'environment/workspace/test_calc.py').write_text('import unittest\nfrom calc import add\nclass Check(unittest.TestCase):\n    def test_add(self): self.assertEqual(add(2,3),5)\n    def test_neg(self): self.assertEqual(add(-2,3),1)\n')
   check="r=subprocess.run([sys.executable,'-m','unittest','test_calc.py'],cwd=work,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL); ok=r.returncode==0"
  elif name=='json':
   check="\ntry: ok=json.loads((work/'result.json').read_text())=={'ready':True,'count':3}\nexcept (OSError,ValueError): ok=False"
  else:
   check=f"\ntry: ok=(work/{filename!r}).read_text()=={expected!r}\nexcept OSError: ok=False"
  verify="""#!/usr/bin/env python3
import json,os,pathlib,subprocess,sys
work=pathlib.Path(os.environ.get('HARBOR_WORKSPACE','/app'))
"""+check+"""
reward=pathlib.Path(os.environ.get('HARBOR_REWARD_DIR','/logs/verifier'));reward.mkdir(parents=True,exist_ok=True)
(reward/'reward.txt').write_text('1\\n' if ok else '0\\n')
sys.exit(0 if ok else 1)
"""
  (task/'tests/verify.py').write_text(verify)
  (task/'tests/test.sh').write_text('#!/bin/sh\nexec python3 "$(dirname "$0")/verify.py"\n')
  (task/'tests/test.sh').chmod(0o755)
if __name__=='__main__':
 import sys
 if len(sys.argv)!=2:raise SystemExit('usage: generate_tasks.py NEW_DIRECTORY')
 generate(Path(sys.argv[1]))
