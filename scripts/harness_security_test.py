#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Static security regression checks for live E2E harness scripts."""

from __future__ import annotations

import re
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def main() -> int:
    live_c = (ROOT / "scripts/live_cli_c_e2e.sh").read_text(encoding="utf-8")
    compose = (ROOT / "scripts/compose_live_e2e.sh").read_text(encoding="utf-8")
    hierarchy_compose = (ROOT / "deploy/reference/hierarchy/docker-compose.yml").read_text(encoding="utf-8")

    require("docker run" in live_c, "C candidate execution must go through docker run")
    require("--network none" in live_c, "C sandbox must disable networking")
    require("--read-only" in live_c, "C sandbox must use a read-only root filesystem")
    require("--pids-limit" in live_c and "--memory" in live_c and "--cpus" in live_c, "C sandbox must set resource limits")
    require(not re.search(r"\n\s*cc\s+-std=c11", live_c), "live C harness must not compile directly on host")
    require(not re.search(r'\n\s*if "\$bin_file"', live_c), "live C harness must not execute generated binary on host")

    require("COMPOSE_E2E_TOOL_SANDBOX_IMAGE" not in compose, "Compose E2E must not require a CLI sandbox image")
    require("TOOL_SANDBOX_IMAGE" not in compose, "Compose E2E must not retain a CLI sandbox image variable")
    require('command -v codex' in compose, "Compose E2E must preflight the local Codex CLI")
    require("unshare -Ur true" in compose, "Codex workspace-write must preflight user namespace support")
    require('command -v claude' in compose, "Compose E2E must preflight the local Claude CLI")
    require("COMPOSE_E2E_PERMISSIONS_IMAGE" in compose, "compose permissions helper image must be configurable")
    require("protect_compose_config_for_router" in compose, "compose config permissions helper is missing")
    require("chown -R 65532:65532 /config" in compose, "router UID 65532 must own bind-mounted config")
    require("find /config -type d -exec chmod 0700" in compose, "compose config directories must remain private")
    require("find /config -type f -exec chmod 0600" in compose, "compose config files must remain non-world-readable")
    require("reset_config_permissions" in compose, "cleanup must restore config ownership for retained/removable workdirs")
    require('env -u ANTHROPIC_API_KEY' in compose, "Claude tool smoke must unset direct Anthropic credentials")
    require('ANTHROPIC_BASE_URL="$BASE_URL"' in compose and 'ANTHROPIC_AUTH_TOKEN="$TOKEN"' in compose, "Claude tool smoke must target the Compose router with its caller token")
    require('cd "$CLAUDE_WORK"' in compose, "Claude tool smoke must be rooted in its disposable work directory")
    require('--permission-mode bypassPermissions' in compose and '--allowedTools "Write,Bash"' in compose, "Claude tool smoke must use only the Write and Bash allowlist")
    require('cd "$CODEX_TOOL_WORK"' in compose and '-C "$CODEX_TOOL_WORK"' in compose, "Codex workspace-write sandbox must be rooted in its disposable work directory")
    require('--sandbox workspace-write' in compose, "Codex tool smoke must use workspace-write sandboxing")
    require("--dangerously-bypass-approvals-and-sandbox" not in compose, "Codex tool smoke must not bypass approvals or sandboxing")
    require('model_providers.metrum-ai-router.base_url=\\"${BASE_URL}/v1\\"' in compose, "Codex tool smoke must target the Compose router Responses URL")
    require('grep -qx "claude-tool-ok" "$CLAUDE_WORK/claude_tool_smoke.txt"' in compose, "Claude tool smoke must assert the exact created file")
    require('grep -qx "codex-tool-ok" "$CODEX_TOOL_WORK/codex_tool_smoke.txt"' in compose, "Codex tool smoke must assert the exact created file")
    require('chmod 0700 "$WORKDIR" "$WORKDIR/config" "$WORKDIR/config/scripts"' in compose, "compose secret directories must be private")
    require("scrub_retained_secrets" in compose, "retained compose workdirs must scrub secret files")
    require('"127.0.0.1:${CPU_LISTEN_PORT:-8002}:8000"' in hierarchy_compose,
            "reference CPU inference port must bind to loopback instead of all host interfaces")

    print("harness security self-test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
