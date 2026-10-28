# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Generate caller token_hash with the router's own CLI, without argv secrets."""
import json
import os
from pathlib import Path
import subprocess
import tempfile


def minimal_env():
    return {k:os.environ[k] for k in ('PATH','HOME','TMPDIR','SYSTEMROOT','GOCACHE','GOMODCACHE','GOROOT','GOPATH','GOPROXY','GOTOOLCHAIN') if k in os.environ}


def prepare(repo: Path) -> Path:
    # go run compiles into an executable temporary directory, not noexec tmpfs.
    return repo.resolve()


def apply(caller: dict, repo: Path, token: str, state: Path) -> None:
    # The raw token is only in a mode-0600 file inside the private state dir.
    fd,path=tempfile.mkstemp(prefix='caller-',dir=state)
    try:
        with os.fdopen(fd,'w') as f:f.write(token+'\n')
        env=minimal_env();env['TMPDIR']='/tmp'
        result=subprocess.run(['go','run','./cmd/metrum-ai-routerctl','callers','hash','--token-file',path],cwd=repo,env=env,capture_output=True,timeout=300)
        if result.returncode:raise RuntimeError('routerctl caller hash failed')
        out=json.loads(result.stdout);token_id=out.get('token_id');token_hash=out.get('token_hash')
        if not isinstance(token_id,str) or not token.startswith(token_id+'_') or not isinstance(token_hash,str) or not token_hash.startswith('pbkdf2-sha3-512:'):raise RuntimeError('invalid routerctl token hash result')
        caller.pop('token_sha256',None);caller.update(token_id=token_id,token_hash=token_hash)
    finally:
        Path(path).unlink(missing_ok=True)
