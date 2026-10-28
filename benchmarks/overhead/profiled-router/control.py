#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Start or stop one benchmark-only profiling session over a private Unix socket."""
import argparse
import json
import socket


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--socket', required=True)
    parser.add_argument('command', choices=('start', 'stop', 'status', 'stop32', 'stop128'))
    args = parser.parse_args()
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(30)
        client.connect(args.socket)
        client.sendall((args.command + '\n').encode())
        with client.makefile('r', encoding='utf-8') as stream:
            reply = json.loads(stream.readline())
    if not reply.get('ok'):
        raise SystemExit('profile control failed: ' + str(reply.get('error', 'unknown')))
    if args.command == 'status':
        print('active chat requests: ' + str(reply.get('active')))
    else:
        print(args.command + ' complete')


if __name__ == '__main__':
    main()
