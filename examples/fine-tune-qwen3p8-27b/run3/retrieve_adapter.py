# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Retrieve final LoRA files with bounded retries and remote SHA-256 verification.

This utility never signals teardown. The independent lease watcher remains
responsible for the deadline, and the operator must retain the VM until gate
results and verified adapter retrieval are available.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

FILES=('adapter_config.json','adapter_model.safetensors')


def digest(path):
    h=hashlib.sha256()
    with path.open('rb') as f:
        for chunk in iter(lambda:f.read(1024*1024),b''):h.update(chunk)
    return h.hexdigest()


def emit_stderr(result, label):
    message=result.stderr or b''
    if isinstance(message,bytes):message=message.decode('utf-8','replace')
    if message:print(f'{label} stderr: {message[-4000:]}',file=sys.stderr,flush=True)


def retrieve(host,port,identity,known_hosts,remote_root,out,runner=subprocess.run,sleep=time.sleep):
    if not re.fullmatch(r'[A-Za-z0-9._-]+@[A-Za-z0-9.:-]+',host):raise ValueError('unsafe SSH destination')
    if not re.fullmatch(r'/[A-Za-z0-9_./-]+',remote_root) or '/..' in remote_root:raise ValueError('unsafe remote root')
    if not 1<=port<=65535:raise ValueError('invalid SSH port')
    out=Path(out)
    if out.is_symlink():raise ValueError('output directory cannot be a symlink')
    out.mkdir(mode=0o700,parents=True,exist_ok=True)
    if out.stat().st_mode & 0o077:raise ValueError('output directory must be private')
    ssh_opts=['-i',str(identity),'-o','IdentitiesOnly=yes','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o',f'UserKnownHostsFile={known_hosts}','-o','ConnectTimeout=10']
    env={k:os.environ[k] for k in ('PATH','HOME') if k in os.environ};manifest={}
    for name in FILES:
        remote=f'{remote_root}/adapter/{name}';final=out/name;partial=out/(name+'.partial')
        if final.is_symlink() or partial.is_symlink():raise ValueError('adapter output cannot be a symlink')
        # Refuse symlinks even when sudo is needed; only the named final file is read.
        cmd=f'if [ -r {remote} ]; then [ -f {remote} ] && [ ! -L {remote} ] && sha256sum {remote}; else sudo -n test -f {remote} && sudo -n test ! -L {remote} && sudo -n sha256sum {remote}; fi'
        p=runner(['ssh',*ssh_opts,'-p',str(port),host,cmd],env=env,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=120)
        emit_stderr(p,'remote checksum')
        if p.returncode:raise RuntimeError('remote checksum failed; VM remains under watcher')
        match=re.fullmatch(rb'([0-9a-f]{64})\s+[^\n]+\n?',p.stdout)
        if not match:raise RuntimeError('invalid remote checksum; VM remains under watcher')
        expected=match.group(1).decode();verified=False
        if final.exists():
            if not final.is_file() or digest(final)!=expected:raise FileExistsError('existing adapter file differs from remote checksum')
            manifest[name]={'sha256':expected,'bytes':final.stat().st_size};continue
        for attempt in range(1,4):
            try:
                p=runner(['scp',*ssh_opts,'-P',str(port),f'{host}:{remote}',str(partial)],env=env,stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE,timeout=180)
                emit_stderr(p,f'scp attempt {attempt}')
                verified=p.returncode==0 and partial.is_file() and digest(partial)==expected
                if p.returncode==0 and not verified:print(f'scp attempt {attempt}: SHA-256 mismatch',file=sys.stderr,flush=True)
            except (OSError,subprocess.TimeoutExpired) as error:
                if isinstance(error,subprocess.TimeoutExpired):emit_stderr(error,f'scp attempt {attempt}')
                print(f'scp attempt {attempt}: {type(error).__name__}',file=sys.stderr,flush=True)
            if verified:break
            if attempt<3:sleep(2**attempt)
        if not verified:
            # Some container-created files are root-owned; one bounded read-only
            # fallback streams through sudo. No remote permissions are changed.
            try:
                fd=os.open(partial,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
                with os.fdopen(fd,'wb') as f:
                    p=runner(['ssh',*ssh_opts,'-p',str(port),host,f'sudo -n cat {remote}'],env=env,stdin=subprocess.DEVNULL,stdout=f,stderr=subprocess.PIPE,timeout=180)
                emit_stderr(p,'sudo read fallback');verified=p.returncode==0 and digest(partial)==expected
            except (OSError,subprocess.TimeoutExpired) as error:
                if isinstance(error,subprocess.TimeoutExpired):emit_stderr(error,'sudo read fallback')
                print(f'sudo read fallback: {type(error).__name__}',file=sys.stderr,flush=True)
        if not verified:raise RuntimeError('adapter retrieval failed or hash mismatched; VM remains under watcher')
        os.chmod(partial,0o600);os.replace(partial,final);manifest[name]={'sha256':expected,'bytes':final.stat().st_size}
    fd=os.open(out/'manifest.json',os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600)
    with os.fdopen(fd,'w') as f:json.dump(manifest,f,indent=2)
    return manifest


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--host',required=True);p.add_argument('--port',required=True,type=int);p.add_argument('--identity',required=True,type=Path);p.add_argument('--known-hosts',required=True,type=Path);p.add_argument('--remote-root',required=True);p.add_argument('--out',required=True,type=Path);a=p.parse_args()
    print(json.dumps(retrieve(a.host,a.port,a.identity,a.known_hosts,a.remote_root,a.out),sort_keys=True))

if __name__=='__main__':main()
