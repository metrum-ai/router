#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Paired calculator-build agent. Runs candidate code only on an isolated SSH host."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs): return None


PROMPT = '''Build a Python 3 calculator program. Return ONLY source code for one file, with no Markdown.
The program reads newline-delimited JSON from stdin until EOF. Each object has a string field
"expression". For each input line print exactly one JSON object. Support decimal numbers,
parentheses, unary plus/minus, binary + - * / with normal precedence and whitespace. Return
{"result":"VALUE"} on success; VALUE is a base-10 decimal string without trailing fractional zeros
or decimal point, with zero represented as "0". For invalid expressions or division by zero return
{"error":"invalid expression"}. Disallow everything outside the listed arithmetic, including
identifiers, function calls, exponentiation and floor division. Never use eval or exec. Test
feedback may be provided; return a complete revised file when asked.'''


def code_from_response(text):
    text = text.strip()
    if text.startswith('```') and text.endswith('```') and '\n' in text:
        text = text.split('\n', 1)[1].rsplit('```', 1)[0]
    return text


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--url', required=True); p.add_argument('--token-file', required=True, type=Path)
    p.add_argument('--output', required=True, type=Path); p.add_argument('--ssh-host', required=True)
    p.add_argument('--ssh-key', required=True, type=Path); p.add_argument('--known-hosts', required=True, type=Path)
    p.add_argument('--image', required=True, help='official python:3.13-slim@sha256:<digest> pre-pulled on sandbox')
    a = p.parse_args(); u = urlparse(a.url)
    if u.scheme != 'http' or u.hostname != '127.0.0.1' or u.username or u.password or u.path not in ('', '/') or u.query or u.fragment: p.error('router must be local')
    if not re.fullmatch(r'[0-9.]+', a.ssh_host): p.error('sandbox must be an IPv4 address')
    if not re.fullmatch(r'python:3\.13-slim@sha256:[0-9a-f]{64}', a.image): p.error('image must be an official pinned Python digest')
    token = a.token_file.read_text().strip()
    if not token: p.error('empty token')
    fd = os.open(a.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    opener = build_opener(NoRedirect())
    ssh = ['ssh', '-i', str(a.ssh_key), '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes', '-o', f'UserKnownHostsFile={a.known_hosts}', '-o', 'ConnectTimeout=10', 'root@'+a.ssh_host,
           f'env CASE_STUDY_PYTHON_IMAGE={a.image} python3 -I /root/case423/judge.py']
    with os.fdopen(fd, 'w') as out:
        for arm in ('frontier', 'routed'):
            for trial in range(1, 4):
                messages = [{'role': 'user', 'content': PROMPT}]; requests = []; score = None; error = None
                for turn in range(2):
                    body = json.dumps({'model': arm, 'messages': messages, 'max_tokens': 2048, 'stream': False}).encode()
                    if len(body) > 16000: error = 'request-size-limit'; break
                    req = Request(a.url.rstrip('/')+'/v1/chat/completions', data=body, headers={'Authorization': 'Bearer '+token, 'Content-Type': 'application/json'})
                    start = time.monotonic()
                    try:
                        with opener.open(req, timeout=120) as r:
                            rid = r.headers.get('X-Request-Id'); status = r.status; raw = r.read(2_000_001)
                    except HTTPError as e:
                        requests.append({'request_id': e.headers.get('X-Request-Id'), 'status': e.code, 'latency_ms': round((time.monotonic()-start)*1000,3)}); error='http-error'; break
                    except (URLError, TimeoutError, OSError): error='transport-error-billing-unknown'; break
                    requests.append({'request_id': rid, 'status': status, 'latency_ms': round((time.monotonic()-start)*1000,3)})
                    try:
                        if len(raw) > 2_000_000: raise ValueError('oversized')
                        message = json.loads(raw)['choices'][0]['message'].get('content')
                        if message is None: message = ''  # Empty model output is a failed candidate, not an infrastructure error.
                        if not isinstance(message, str): raise ValueError('invalid content type')
                        source = code_from_response(message)
                        result = subprocess.run(ssh, input=json.dumps({'source': source}).encode(), capture_output=True, timeout=65)
                        if result.returncode: error='sandbox-error'; break
                        score = json.loads(result.stdout)
                        if score.get('status') not in ('ok', 'candidate-error', 'candidate-timeout') or score.get('total') != 20: error='sandbox-error'; break
                    except (ValueError, TypeError, KeyError, IndexError, subprocess.TimeoutExpired) as exc:
                        error='invalid-response-or-sandbox-error-' + type(exc).__name__; break
                    if score['passed'] == score['total']: break
                    messages.extend([{'role':'assistant','content':message}, {'role':'user','content':f"The independent test suite passed {score['passed']} of {score['total']} tests. Revise the complete file."}])
                row = {'arm':arm, 'trial_id':f'calculator-{trial}', 'passed':bool(score and score['passed']==score['total']), 'test_result':score, 'requests':requests, 'error':error}
                out.write(json.dumps(row, sort_keys=True)+'\n'); out.flush(); os.fsync(out.fileno())
                print(arm, row['trial_id'], 'pass' if row['passed'] else 'fail', 'error', error, flush=True)
                if error or any(not r['request_id'] for r in requests): raise SystemExit('Stopped after error; reconcile request usage before resuming.')


if __name__ == '__main__': main()
