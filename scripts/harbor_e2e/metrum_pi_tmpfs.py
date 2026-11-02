# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Harbor Pi adapter: router token read from a read-only tmpfs bind mount.

The host injects no credential value through Harbor CLI flags, environment,
container configuration, or command arguments. Only synthetic test tasks are
permitted; provider credentials remain in host tmpfs for the router.
"""
import json, os, re, shlex
from harbor.agents.installed.pi import Pi
from harbor.agents.installed.base import with_prompt_template

TOKEN_PATH='/run/harbor-e2e/router-token'
PI_VERSION='1.4.0'

class TmpfsPi(Pi):
    async def install(self, environment):
        await self.exec_as_agent(environment,command='set -euo pipefail; test "$(pi --version)" = '+shlex.quote(PI_VERSION)+'; python3 --version')

    @with_prompt_template
    async def run(self,instruction,environment,context):
        style=os.environ['HARBOR_E2E_STYLE']
        if style not in ('openai-responses','anthropic-messages'):raise ValueError('unsupported API style')
        group=(self.model_name or '').split('/')[-1]
        if group not in ('gpt','claude'):raise ValueError('unsupported test group')
        base=os.environ['HARBOR_E2E_ROUTER_URL'].rstrip('/')
        if not re.fullmatch(r'http://172\.17\.0\.1:[1-9][0-9]{0,4}',base):raise ValueError('router must be on docker bridge')
        mode=os.environ.get('HARBOR_E2E_MODE','routed')
        if mode not in ('routed','direct'):raise ValueError('unsupported run mode')
        if mode=='direct' and ((style=='openai-responses' and group!='gpt') or (style=='anthropic-messages' and group!='claude')):raise ValueError('invalid direct baseline')
        model=group if mode=='routed' else ('gpt-4.1-mini' if group=='gpt' else 'claude-haiku-4-5-20251001')
        suffix=('/openai/v1' if mode=='direct' else '/v1') if style=='openai-responses' else '/anthropic'
        models={'providers':{'metrum':{'baseUrl':base+suffix,'api':style,'apiKey':'!cat '+TOKEN_PATH,
          'models':[{'id':model,'reasoning':False,'input':['text'],'contextWindow':16384,'maxTokens':512,
          'cost':{'input':0,'output':0,'cacheRead':0,'cacheWrite':0}}]}}}
        command=('set -euo pipefail; umask 077; test -r '+shlex.quote(TOKEN_PATH)+'; '
          'mkdir -p "$HOME/.pi/agent" '+shlex.quote(str(self.environment_logs_dir))+'; '
          'printf %s '+shlex.quote(json.dumps(models))+' > "$HOME/.pi/agent/models.json"; '
          'pi --print --mode json --provider metrum --model '+shlex.quote(model)+' '
          '--tools read,write,edit,bash --no-extensions --no-mcp --no-context-files --no-skills '
          '--no-prompt-templates --no-themes --no-session --no-approve --offline '+shlex.quote(instruction)+' '
          '> '+shlex.quote(str(self.environment_logs_dir/'pi.txt')))
        await self.exec_as_agent(environment,command=command,env=None,cwd='/app')
