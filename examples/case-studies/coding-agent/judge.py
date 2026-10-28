#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Execute untrusted candidate ONLY inside a constrained Docker container.

Run on an isolated throwaway host. Input is JSON containing `source`. The
trusted host, not the candidate process, compares outputs to reference values.
"""
import json
import os
from pathlib import Path
import resource
import re
import subprocess
import sys
import tempfile
import uuid

# Input/output contract is documented in the workload prompt.
CASES = [
    ('1+2', '3'), ('12-20', '-8'), ('6*7', '42'), ('15/3', '5'),
    ('0.1+0.2', '0.3'), ('2*(3+4)', '14'), ('(9-3)/2', '3'),
    (' -2 * 3 ', '-6'), ('3 + -2', '1'), ('10/4', '2.5'),
    ('1.20*2', '2.4'), ('(1+2)*(3+4)', '21'), ('0-0', '0'),
    ('1/0', None), ('foo', None), ('__import__("os")', None),
    ('2**4', None), ('3//2', None), ('', None), ('1+', None),
]


def file_limit():
    resource.setrlimit(resource.RLIMIT_FSIZE, (1_048_576, 1_048_576))


def main():
    raw = sys.stdin.buffer.read(100_001)
    if len(raw) > 100_000: raise SystemExit('candidate too large')
    obj = json.loads(raw)
    source = obj.get('source')
    if not isinstance(source, str) or len(source.encode()) > 80_000: raise SystemExit('invalid source')
    image = os.environ.get('CASE_STUDY_PYTHON_IMAGE', 'python:3.13-slim')
    if not re.fullmatch(r'python:3\.13-slim@sha256:[0-9a-f]{64}', image):
        raise SystemExit('pin official python image by sha256 digest')
    name = 'case423-' + uuid.uuid4().hex
    with tempfile.TemporaryDirectory(prefix='case423-') as d:
        path = Path(d)/'calculator.py'; path.write_text(source, encoding='utf-8'); path.chmod(0o444)
        outpath, errpath = Path(d)/'stdout', Path(d)/'stderr'
        command = ['docker', 'run', '--rm', '--interactive', '--name', name, '--pull=never', '--network=none',
                   '--read-only', '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m', '--memory=256m',
                   '--cpus=0.5', '--pids-limit=32', '--cap-drop=ALL', '--security-opt=no-new-privileges',
                   '--user=65534:65534', '--mount', f'type=bind,src={path},dst=/work/calculator.py,readonly',
                   image, 'python', '-I', '/work/calculator.py']
        data = ''.join(json.dumps({'expression': expression})+'\n' for expression, _ in CASES).encode()
        status = 'ok'
        try:
            with outpath.open('wb') as out, errpath.open('wb') as err:
                try:
                    result = subprocess.run(command, input=data, stdout=out, stderr=err, timeout=30, preexec_fn=file_limit)
                    if result.returncode in (125, 126, 127): status = 'container-error'
                    elif result.returncode != 0: status = 'candidate-error'
                except subprocess.TimeoutExpired:
                    status = 'candidate-timeout'
        finally:
            subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15, check=False)
        lines = outpath.read_text(encoding='utf-8', errors='replace').splitlines() if outpath.exists() else []
        outcomes = []
        for index, (_, expected) in enumerate(CASES):
            try:
                answer = json.loads(lines[index])
                ok = answer == ({'result': expected} if expected is not None else {'error': 'invalid expression'})
            except (IndexError, ValueError): ok = False
            outcomes.append(ok)
        # Extra output and container failures invalidate the whole run.
        if status != 'ok' or len(lines) != len(CASES): outcomes = [False] * len(CASES)
        print(json.dumps({'status': status, 'passed': sum(outcomes), 'total': len(CASES), 'outcomes': outcomes}))


if __name__ == '__main__': main()
