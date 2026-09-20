#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

# Manual control for the pikachu router GitHub Actions compose pool.
# Usage: ./manage.sh {start|stop|restart|status|logs|rebuild|register-token|ps}
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

if docker compose version >/dev/null 2>&1; then
  dc() { docker compose "$@"; }
elif command -v docker-compose >/dev/null 2>&1; then
  # Ubuntu 24.04 often only has the v1 binary; needs docker.sock access.
  dc() { sudo docker-compose "$@"; }
else
  echo "docker compose / docker-compose not found" >&2
  exit 1
fi

require_env() {
  if [[ ! -f .env ]]; then
    cp .env.example .env
    chmod 600 .env
    echo "Created .env from .env.example — set RUNNER_TOKEN or ACCESS_TOKEN, then re-run." >&2
    exit 1
  fi
}

refresh_token() {
  if ! command -v gh >/dev/null 2>&1; then
    echo "gh not installed; paste a registration token into .env as RUNNER_TOKEN=" >&2
    exit 1
  fi
  token="$(gh api -X POST repos/metrum-ai/router/actions/runners/registration-token --jq .token)"
  if grep -q '^RUNNER_TOKEN=' .env; then
    # portable in-place replace without printing the token
    python3 - "$token" <<'PY'
import pathlib, re, sys
token = sys.argv[1]
path = pathlib.Path(".env")
text = path.read_text()
text, n = re.subn(r"(?m)^RUNNER_TOKEN=.*$", "RUNNER_TOKEN=" + token, text, count=1)
if n == 0:
    text = text.rstrip() + "\nRUNNER_TOKEN=" + token + "\n"
path.write_text(text)
PY
  else
    printf '\nRUNNER_TOKEN=%s\n' "$token" >> .env
  fi
  chmod 600 .env
  echo "Wrote fresh RUNNER_TOKEN to .env (expires ~1h)."
}

cmd="${1:-}"
case "$cmd" in
  start)
    require_env
    dc up -d
    dc ps
    ;;
  stop)
    dc down
    ;;
  restart)
    require_env
    dc up -d --force-recreate
    dc ps
    ;;
  status|ps)
    dc ps
    echo
    echo "GitHub runners (router label):"
    if command -v gh >/dev/null 2>&1; then
      gh api repos/metrum-ai/router/actions/runners --jq '.runners[] | select([.labels[].name] | index("router")) | "\(.name)\t\(.status)\tbusy=\(.busy)"' 2>/dev/null || true
    fi
    ;;
  logs)
    shift || true
    if [[ $# -eq 0 ]]; then
      dc logs --tail=100 -f
    elif [[ "${1:-}" == "-f" || "${1:-}" == "--follow" ]]; then
      shift || true
      dc logs --tail=100 -f "$@"
    else
      dc logs --tail=200 "$@"
    fi
    ;;
  rebuild)
    require_env
    refresh_token
    dc build
    dc up -d --force-recreate
    dc ps
    ;;
  register-token)
    require_env
    refresh_token
    ;;
  *)
    cat <<'EOF'
Usage: ./manage.sh <command>

  start            Start the pool (docker-compose up -d)
  stop             Stop and remove containers (down)
  restart          Recreate containers from current images/env
  status | ps      Show compose + GitHub runner status
  logs [svc]       Tail logs (default: all services, follow)
  rebuild          Fresh registration token, rebuild images, recreate
  register-token   Only refresh RUNNER_TOKEN in .env

Pool dir: this directory (compose + Dockerfile + .env).
Labels: self-hosted,Linux,X64,router
Notes:
  - Containers are privileged so LRP bubblewrap works; AppArmor is not required.
  - EPHEMERAL=true → after each job the runner exits; compose restart policy brings it back.
  - Registration tokens expire in ~1 hour; use register-token/rebuild if runners fail to join.
EOF
    exit 1
    ;;
esac
