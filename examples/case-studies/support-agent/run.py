#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Run synthetic support trials through a loopback router; does not run code."""
import argparse
import json
import os
from pathlib import Path
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener

from backend import Backend, ORDERS


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def tool(name, description, properties, required):
    return {'type': 'function', 'function': {'name': name, 'description': description,
            'parameters': {'type': 'object', 'properties': properties, 'required': required,
                           'additionalProperties': False}}}


ORDER = {'type': 'string', 'description': 'Order ID'}
TOOLS = [
    tool('lookup_order', 'Look up an order.', {'order_id': ORDER}, ['order_id']),
    tool('calculate_credit', 'Calculate policy credit after lookup.', {'order_id': ORDER}, ['order_id']),
    tool('apply_credit', 'Apply the calculated credit, including a zero credit.',
         {'order_id': ORDER, 'credit_usd': {'type': 'string'}}, ['order_id', 'credit_usd']),
    tool('create_ticket', 'Create a support ticket after applying the credit.', {'order_id': ORDER}, ['order_id']),
]
SYSTEM = ('You are a support agent. For the order given, look it up, calculate the policy credit, '
          'apply that exact credit even when zero, then create a support ticket. Use the tools in that order. '
          'Finally return only JSON with order_id, credit_usd (a string), and ticket_id. '
          'Do not claim an action succeeded if a tool reported an error.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', required=True, help='local router URL, e.g. http://127.0.0.1:18081')
    parser.add_argument('--token-file', required=True, type=Path, help='private router caller token file')
    parser.add_argument('--output', required=True, type=Path, help='new private JSONL result file')
    args = parser.parse_args()
    u = urlparse(args.url)
    if u.scheme != 'http' or u.hostname != '127.0.0.1' or u.username or u.password or u.path not in ('', '/') or u.query or u.fragment:
        parser.error('only a local 127.0.0.1 HTTP router root is supported')
    token = args.token_file.read_text().strip()
    if not token:
        parser.error('token file empty')
    # Exclusive output prevents silently mixing runs. Raw answers/tool contents are not saved.
    fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    opener = build_opener(NoRedirect())
    with os.fdopen(fd, 'w') as out:
        for arm in ('frontier', 'routed'):
            for oid in ORDERS:
                b = Backend(oid)
                messages = [{'role': 'system', 'content': SYSTEM},
                            {'role': 'user', 'content': f'Please handle order {oid} and open a support ticket.'}]
                requests = []
                answer = None
                error = None
                for turn in range(8):
                    body = json.dumps({'model': arm, 'messages': messages, 'tools': TOOLS,
                                       'max_tokens': 512, 'stream': False}, separators=(',', ':')).encode()
                    if len(body) > 16000:
                        error = 'request-size-limit'
                        break
                    req = Request(args.url.rstrip('/') + '/v1/chat/completions', data=body,
                                  headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
                    start = time.monotonic()
                    try:
                        with opener.open(req, timeout=90) as resp:
                            raw = resp.read(2_000_001)
                            request_id = resp.headers.get('X-Request-Id')
                            status = resp.status
                    except HTTPError as exc:
                        requests.append({'request_id': exc.headers.get('X-Request-Id'), 'status': exc.code,
                                         'latency_ms': round((time.monotonic()-start)*1000, 3)})
                        error = 'http-error'
                        break
                    except (URLError, TimeoutError, OSError):
                        error = 'transport-error-billing-unknown'
                        break
                    requests.append({'request_id': request_id, 'status': status,
                                     'latency_ms': round((time.monotonic()-start)*1000, 3)})
                    if not request_id or len(raw) > 2_000_000:
                        error = 'missing-request-id-or-response-too-large'
                        break
                    try:
                        msg = json.loads(raw)['choices'][0]['message']
                        calls = msg.get('tool_calls') or []
                        if not calls:
                            answer = json.loads(msg.get('content') or '')
                            break
                        messages.append(msg)
                        for call in calls:
                            fn = call['function']
                            try:
                                tool_args = json.loads(fn['arguments'])
                            except (ValueError, TypeError):
                                tool_args = None
                            result = b.execute(fn['name'], tool_args)
                            messages.append({'role': 'tool', 'tool_call_id': call['id'], 'content': json.dumps(result)})
                    except (ValueError, TypeError, KeyError, IndexError):
                        error = 'invalid-response'
                        break
                else:
                    error = 'turn-limit'
                row = {'arm': arm, 'trial_id': oid, 'passed': b.verify(answer), 'error': error,
                       'tool_calls': len(b.history), 'requests': requests}
                out.write(json.dumps(row, sort_keys=True) + '\n')
                out.flush()
                os.fsync(out.fileno())
                print(arm, oid, 'pass' if row['passed'] else 'fail', 'requests', len(requests), 'error', error, flush=True)
                if error or any(not r['request_id'] for r in requests):
                    raise SystemExit('Stopped after error; reconcile request usage before resuming.')


if __name__ == '__main__':
    main()
