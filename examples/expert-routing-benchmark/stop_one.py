# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Stop only a labeled benchmark container and retain before/after evidence."""
import json,os,subprocess,sys
from pathlib import Path
names=('baseline','biomedical','finance','math','coding')
if len(sys.argv)!=4 or sys.argv[3] not in names:raise SystemExit('usage: 426-stop-one.py STATE_DIR EVIDENCE_DIR NAME')
s=Path(sys.argv[1]).resolve();e=Path(sys.argv[2]).resolve();name=sys.argv[3];c=json.loads((s/'connection.json').read_text());w=json.loads((s/'workspace.json').read_text());docker='sudo -n docker' if w['docker_sudo'] else 'docker';env={k:os.environ[k] for k in ('PATH','HOME') if k in os.environ};capture=str(Path(__file__).resolve().with_name('capture.py'))
opts=['-i',str(s/'instance-key'),'-o','IdentitiesOnly=yes','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o',f'UserKnownHostsFile={s/"known_hosts"}','-o','ConnectTimeout=10']
def snapshot():
    python_env=dict(env)
    if 'LD_LIBRARY_PATH' in os.environ:python_env['LD_LIBRARY_PATH']=os.environ['LD_LIBRARY_PATH']
    return subprocess.run([sys.executable,capture,str(s),str(e),'once',name],env=python_env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=90).returncode==0
if not snapshot():raise SystemExit('container evidence unavailable before stop')
remote=f'label=$({docker} inspect --format \'{{{{ index .Config.Labels "metrum.task" }}}}\' proof426-{name}); [ "$label" = issue-426 ] || exit 2; running=$({docker} inspect --format \'{{{{.State.Running}}}}\' proof426-{name}); if [ "$running" = true ]; then {docker} stop proof426-{name} >/dev/null; fi'
r=subprocess.run(['ssh',*opts,'-p',str(c['port']),f"{c['user']}@{c['ip']}",remote],env=env,stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE,timeout=60)
if r.returncode:raise SystemExit('container stop failed')
if not snapshot():raise SystemExit('container evidence unavailable after stop')
print(name+' stopped and retained; logs and inspect captured',flush=True)
