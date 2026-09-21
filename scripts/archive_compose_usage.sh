#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

# Archive the Compose Postgres usage database to restic before EKS cutover.
# Forensics only; EKS production SQLite does not import this dump.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_ROOT="${INSTALL_ROOT:-/opt/smart-llmrouter}"
COMPOSE_DIR="${INSTALL_ROOT}/compose"
ARCHIVE_DIR="${ARCHIVE_DIR:-${ROOT}/tmp/restic-compose-usage-archive}"
VERSION_TAG="${VERSION_TAG:-$(date -u +%Y%m%dT%H%M%SZ)}"
REMOTE="${REMOTE:-}"
SSH_IDENTITY="${SSH_IDENTITY:-}"

usage() {
  cat <<'EOF'
Usage: archive_compose_usage.sh [--remote user@host] [--install-root PATH]

Requires ROUTER_USAGE_DB_DSN in compose/.env and the postgres override compose file.
Never prints DSN values, tokens, or provider keys.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --remote) REMOTE="$2"; shift 2 ;;
    --install-root) INSTALL_ROOT="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

run_local() {
  bash -lc "$1"
}

run_cmd() {
  if [[ -n "${REMOTE}" ]]; then
    local identity=()
    if [[ -n "${SSH_IDENTITY}" ]]; then
      identity=(-i "${SSH_IDENTITY}")
    fi
    ssh "${identity[@]}" "${REMOTE}" "set -euo pipefail; $1"
  else
    run_local "$1"
  fi
}

mkdir -p "${ARCHIVE_DIR}"
DUMP_PATH="${ARCHIVE_DIR}/usage-${VERSION_TAG}.dump"

echo "Creating usage dump at ${DUMP_PATH}"
run_cmd "cd '${COMPOSE_DIR}' && set -a && source .env && set +a && pg_dump \"\${ROUTER_USAGE_DB_DSN}\" > /tmp/smart-llmrouter-usage.dump"
if [[ -n "${REMOTE}" ]]; then
  scp ${SSH_IDENTITY:+-i "${SSH_IDENTITY}"} "${REMOTE}:/tmp/smart-llmrouter-usage.dump" "${DUMP_PATH}"
  run_cmd "rm -f /tmp/smart-llmrouter-usage.dump"
else
  mv /tmp/smart-llmrouter-usage.dump "${DUMP_PATH}"
fi

cat > "${ARCHIVE_DIR}/MANIFEST-${VERSION_TAG}.txt" <<EOF
purpose=compose-usage-archive
version=${VERSION_TAG}
driver=postgres
note=EKS cutover forensics; not imported into router usage state
EOF

if command -v restic >/dev/null 2>&1 && [[ -f "${ROOT}/env.json" ]]; then
  echo "Archiving to restic with tags purpose:compose-usage-archive version:${VERSION_TAG}"
  RESTIC_PASSWORD="$(python3 - <<'PY'
import json, pathlib
print(json.loads(pathlib.Path("env.json").read_text())["RESTIC_PASSWORD"])
PY
)"
  export RESTIC_PASSWORD
  restic -r "$(python3 - <<'PY'
import json, pathlib, sys
cfg=json.loads(pathlib.Path("env.json").read_text())
repo=str(cfg.get("RESTIC_REPOSITORY","")).strip()
if not repo:
    print("RESTIC_REPOSITORY is required in env.json", file=sys.stderr)
    sys.exit(2)
print(repo)
PY
)" backup "${DUMP_PATH}" "${ARCHIVE_DIR}/MANIFEST-${VERSION_TAG}.txt" \
    --tag "purpose:compose-usage-archive" --tag "version:${VERSION_TAG}"
else
  echo "restic or env.json unavailable; local dump retained at ${DUMP_PATH}" >&2
fi

echo "Archive complete: ${DUMP_PATH}"
