# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Harbor pi adapter that routes every model call through a Metrum AI Router.

Used for the HARBOR-REAL-AGENT live cell (issue #94). It subclasses Harbor's
built-in ``Pi`` agent and only changes two things:

* install pins ``@earendil-works/pi-coding-agent`` (the current package name)
  instead of Harbor's historical unpinned package;
* run writes an isolated ``~/.pi/agent/models.json`` inside the task container
  with a single ``metrum`` provider pointing at ``METRUM_ROUTER_BASE_URL``, so pi
  cannot fall back to a direct provider account.

The router caller token comes from ``METRUM_ROUTER_KEY`` on the host and is
written to a mode-0600 file in the container; it is never placed on a command
line or in Harbor logs.
"""

from __future__ import annotations

import json
import os
import shlex

from harbor.agents.installed.pi import Pi
from harbor.agents.installed.base import with_prompt_template
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

PI_PACKAGE = "@earendil-works/pi-coding-agent"
PI_VERSION = os.environ.get("METRUM_PI_VERSION", "1.1.0")


class MetrumPi(Pi):
    async def install(self, environment: BaseEnvironment) -> None:
        await self.exec_as_root(
            environment,
            command="apt-get update && apt-get install -y curl ca-certificates",
            env={"DEBIAN_FRONTEND": "noninteractive"},
        )
        await self.exec_as_agent(
            environment,
            command=(
                "set -euo pipefail; "
                "curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.2/install.sh | bash && "
                'export NVM_DIR="$HOME/.nvm" && '
                '\\. "$NVM_DIR/nvm.sh" || true && '
                "nvm install 22 && "
                f"npm install -g {PI_PACKAGE}@{PI_VERSION} && "
                "pi --version"
            ),
        )

    @with_prompt_template
    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        base_url = os.environ["METRUM_ROUTER_BASE_URL"].rstrip("/")
        group = (self.model_name or "").split("/", 1)[-1]
        models = {
            "providers": {
                "metrum": {
                    "baseUrl": base_url,
                    "api": "openai-completions",
                    "apiKey": "!cat $HOME/.pi/agent/metrum.key",
                    "authHeader": True,
                    "compat": {
                        "supportsStore": False,
                        "supportsDeveloperRole": False,
                        "supportsReasoningEffort": False,
                    },
                    "models": [{"id": group}],
                }
            }
        }
        await self.exec_as_agent(
            environment,
            command=(
                "set -eu; umask 077; mkdir -p $HOME/.pi/agent; "
                'printf %s "$METRUM_ROUTER_KEY" > $HOME/.pi/agent/metrum.key; '
                f"printf %s {shlex.quote(json.dumps(models))} > $HOME/.pi/agent/models.json"
            ),
            env={"METRUM_ROUTER_KEY": os.environ["METRUM_ROUTER_KEY"]},
        )
        await Pi.run.__wrapped__(self, instruction, environment, context)
