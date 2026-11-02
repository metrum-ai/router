#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
#
# Remove an installed edge bundle.
#
#   ./uninstall.sh                     # stop and remove containers, keep data
#   ./uninstall.sh --purge             # also delete config, secrets, state, logs
#   ./uninstall.sh --native-only       # stop native-mode processes only
#
# Uninstall is not a data-destroying default: caller tokens, the usage database
# and logs stay on disk unless --purge is passed, because the spend history is
# evidence. --purge prints exactly what it is about to delete and requires
# --yes to continue.

set -euo pipefail

DATA_DIR="${HOME}/metrum-edge"
PURGE=0
YES=0
NATIVE_ONLY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --data-dir) DATA_DIR="${2:?}"; shift 2 ;;
    --purge) PURGE=1; shift ;;
    --yes) YES=1; shift ;;
    --native-only) NATIVE_ONLY=1; shift ;;
    -h|--help) sed -n '4,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "uninstall: unknown option $1" >&2; exit 2 ;;
  esac
done

DATA_DIR="$(cd "$DATA_DIR" && pwd)"
INSTALL_ENV="$DATA_DIR/install.env"
[ -f "$INSTALL_ENV" ] || { echo "uninstall: $INSTALL_ENV not found; nothing to uninstall here" >&2; exit 2; }
# shellcheck disable=SC1090
. "$INSTALL_ENV"
log() { printf '%s uninstall: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }

stop_native() {
  local pidfile pid
  for pidfile in "$EDGE_DATA_DIR"/state/router.pid "$EDGE_DATA_DIR"/state/mock-*.pid; do
    [ -f "$pidfile" ] || continue
    pid="$(cat "$pidfile")"
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      log "stopped $(basename "$pidfile" .pid) (pid $pid)"
    fi
    rm -f "$pidfile"
  done
}

if [ "$EDGE_MODE" = "native" ] || [ "$NATIVE_ONLY" = "1" ]; then
  stop_native
fi

if [ "$EDGE_MODE" = "docker" ] && [ "$NATIVE_ONLY" = "0" ]; then
  if command -v docker >/dev/null 2>&1; then
    docker compose --env-file "$EDGE_ENV_FILE" \
      -f "$EDGE_DATA_DIR/compose/compose.yaml" \
      -f "$EDGE_DATA_DIR/compose/compose.${EDGE_ACCELERATOR}.yaml" \
      down --remove-orphans || log "docker compose down reported a problem; check for leftover containers"
    log "containers, networks and the weight volume removed"
  else
    log "docker is not installed; nothing to stop"
  fi
fi

if [ "$PURGE" = "1" ]; then
  echo "About to delete everything under $EDGE_DATA_DIR:"
  echo "  config/config.yaml     rendered groups"
  echo "  secrets/               caller tokens and the secret environment"
  echo "  state/                 usage database and router state (spend history)"
  echo "  logs/                  request logs"
  echo "  backups/               configuration snapshots"
  if [ "$YES" != "1" ]; then
    echo "Refusing to continue without --yes." >&2
    exit 2
  fi
  rm -rf --one-file-system "$EDGE_DATA_DIR"
  log "purged $EDGE_DATA_DIR"
  exit 0
fi

log "kept $EDGE_DATA_DIR (config, secrets, state, logs). Re-run install.sh to use it again, or --purge --yes to delete it."
