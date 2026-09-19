#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

# Migrate / refresh pikachu compose pool. AppArmor is not required.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

if [[ ! -f .env ]]; then
  cp .env.example .env
  echo "Edit $ROOT/.env and set RUNNER_TOKEN (or ACCESS_TOKEN), then re-run."
  exit 1
fi

if ! grep -qE '^RUNNER_TOKEN=.+' .env && ! grep -qE '^ACCESS_TOKEN=.+' .env; then
  if command -v gh >/dev/null; then
    token="$(gh api -X POST repos/metrum-ai/router/actions/runners/registration-token --jq .token)"
    printf '\nRUNNER_TOKEN=%s\n' "$token" >> .env
    echo "Wrote short-lived RUNNER_TOKEN to .env"
  else
    echo "Set RUNNER_TOKEN or ACCESS_TOKEN in .env"
    exit 1
  fi
fi

if docker compose version >/dev/null 2>&1; then
  dc=(docker compose)
else
  dc=(sudo docker-compose)
fi

"${dc[@]}" build
"${dc[@]}" up -d --force-recreate
"${dc[@]}" ps

cat <<'EOF'

Pool refreshed. Labels: self-hosted,Linux,X64,router
Isolation: privileged containers + bubblewrap (no AppArmor requirement).
EOF
