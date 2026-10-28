#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline retrieval followed by a bounded loopback-router Q&A evaluation."""
import argparse
import json
import os
from pathlib import Path
import re
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def tokens(text):
    return set(re.findall(r'[a-z0-9]+', text.lower())) - {'the', 'a', 'an', 'of', 'to', 'in', 'for', 'or', 'and', 'how', 'many', 'does', 'do', 'return', 'only', 'integer', 'within', 'days'}


def retrieve(question, tasks):
    query = tokens(question)
    ranked = sorted(((len(query & tokens(t['document'])), t['id'], t['document']) for t in tasks), key=lambda r: (-r[0], r[1]))
    return ranked[0][1], ranked[0][2]


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--url', required=True)
    p.add_argument('--token-file', required=True, type=Path)
    p.add_argument('--output', required=True, type=Path)
    args = p.parse_args()
    u = urlparse(args.url)
    if u.scheme != 'http' or u.hostname != '127.0.0.1' or u.username or u.password or u.path not in ('', '/') or u.query or u.fragment:
        p.error('only a local 127.0.0.1 HTTP router root is supported')
    token = args.token_file.read_text().strip()
    if not token: p.error('token file empty')
    tasks = json.loads(Path(__file__).with_name('tasks.json').read_text())
    fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    opener = build_opener(NoRedirect())
    with os.fdopen(fd, 'w') as out:
        for arm in ('frontier', 'routed'):
            for task in tasks:
                doc_id, document = retrieve(task['question'], tasks)
                messages = [{'role': 'system', 'content': 'Answer the question using only the supplied document. Follow the requested answer format.'},
                            {'role': 'user', 'content': 'Document: ' + document + '\nQuestion: ' + task['question']}]
                body = json.dumps({'model': arm, 'messages': messages, 'max_tokens': 128, 'stream': False}).encode()
                if len(body) > 16000: raise SystemExit('Request-size limit')
                request = Request(args.url.rstrip('/') + '/v1/chat/completions', data=body,
                                  headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
                start = time.monotonic(); rid = None; status = None; answer = None; error = None
                try:
                    with opener.open(request, timeout=90) as r:
                        status = r.status; rid = r.headers.get('X-Request-Id'); raw = r.read(2_000_001)
                    if len(raw) > 2_000_000: raise ValueError('response too large')
                    answer = json.loads(raw)['choices'][0]['message']['content']
                    if not isinstance(answer, str): raise ValueError('missing content')
                except HTTPError as e:
                    status = e.code; rid = e.headers.get('X-Request-Id'); error = 'http-error'
                except (URLError, TimeoutError, OSError): error = 'transport-error-billing-unknown'
                except (ValueError, TypeError, KeyError, IndexError): error = 'invalid-response'
                row = {'arm': arm, 'trial_id': task['id'], 'retrieved_id': doc_id,
                       'passed': doc_id == task['id'] and answer is not None and answer.strip() == task['answer'],
                       'request_id': rid, 'status': status, 'latency_ms': round((time.monotonic()-start)*1000, 3), 'error': error}
                out.write(json.dumps(row, sort_keys=True) + '\n'); out.flush(); os.fsync(out.fileno())
                print(arm, task['id'], 'pass' if row['passed'] else 'fail', 'error', error, flush=True)
                if error or not rid: raise SystemExit('Stopped after error; reconcile request usage before resuming.')


if __name__ == '__main__': main()
