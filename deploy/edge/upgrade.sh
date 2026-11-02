#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
#
# Upgrade an installed edge bundle to a new router image.
#
#   ./upgrade.sh --router-image metrum-ai-router:4.7.1-linux-amd64
#   ./upgrade.sh --config-only              # re-render groups from the current template
#
# What it does, in order: snapshot state, re-render the configuration from the
# current template with the existing tokens and secrets, run the migration
# sequence, then recreate the router. Model servers are not restarted unless
# --restart-models is given (weights do not change in a router upgrade).
# A failed migration leaves the previous configuration in place and does not
# recreate the router.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DATA_DIR="${HOME}/metrum-edge"
ROUTER_IMAGE=""
CONFIG_ONLY=0
RESTART_MODELS=0

while [ $# -gt 0 ]; do
  case "$1" in
    --data-dir) DATA_DIR="${2:?}"; shift 2 ;;
    --router-image) ROUTER_IMAGE="${2:?}"; shift 2 ;;
    --config-only) CONFIG_ONLY=1; shift ;;
    --restart-models) RESTART_MODELS=1; shift ;;
    -h|--help) sed -n '4,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "upgrade: unknown option $1" >&2; exit 2 ;;
  esac
done

DATA_DIR="$(cd "$DATA_DIR" && pwd)"
INSTALL_ENV="$DATA_DIR/install.env"
[ -f "$INSTALL_ENV" ] || { echo "upgrade: $INSTALL_ENV not found; run install.sh first" >&2; exit 2; }
# shellcheck disable=SC1090
. "$INSTALL_ENV"
log() { printf '%s upgrade: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }
die() { printf 'upgrade: %s\n' "$*" >&2; exit 1; }

case "$EDGE_MODE" in
  native)
    die "native mode has no managed upgrade path: stop the processes in $EDGE_DATA_DIR/state/*.pid, replace the binaries, and re-run install.sh --mode native --bin-dir" ;;
  docker) ;;
  *) die "unknown EDGE_MODE=$EDGE_MODE in $INSTALL_ENV" ;;
esac

[ "$CONFIG_ONLY" = "1" ] || [ -n "$ROUTER_IMAGE" ] || die "--router-image is required unless --config-only is given"
case "$ROUTER_IMAGE" in *:latest) die "--router-image must not be latest" ;; esac

compose() {
  docker compose --env-file "$EDGE_ENV_FILE" \
    -f "$EDGE_DATA_DIR/compose/compose.yaml" \
    -f "$EDGE_DATA_DIR/compose/compose.${EDGE_ACCELERATOR}.yaml" "$@"
}

snapshot() {
  local stamp backup
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  backup="$EDGE_DATA_DIR/backups/$stamp"
  mkdir -p "$backup"
  chmod 0700 "$EDGE_DATA_DIR/backups"
  cp "$EDGE_CONFIG" "$backup/config.yaml"
  cp "$EDGE_ENV_FILE" "$backup/env"
  log "config and secret environment snapshotted to $backup"
  log "for the usage database, stop the router and follow the offline copy rules in docs/DOCKER_COMPOSE_INSTALL.md"
}

if [ "$CONFIG_ONLY" != "1" ]; then
  log "stopping the router"
  compose stop router >/dev/null
  sed -i "s|^ROUTER_IMAGE=.*|ROUTER_IMAGE=${ROUTER_IMAGE}|" "$EDGE_ENV_FILE"
  sed -i "s|^EDGE_ROUTER_IMAGE=.*|EDGE_ROUTER_IMAGE=${ROUTER_IMAGE}|" "$INSTALL_ENV" 2>/dev/null || true
  log "router image set to ${ROUTER_IMAGE} in $EDGE_ENV_FILE"
fi

snapshot

log "re-rendering configuration from the current template"
"$SCRIPT_DIR/install.sh" --mode docker --data-dir "$DATA_DIR" --router-image "${ROUTER_IMAGE:-$EDGE_ROUTER_IMAGE}" \
  --accelerator "$EDGE_ACCELERATOR" --dry-run >/dev/null

log "running the migration sequence against the existing database"
for action in plan apply verify-serving status; do
  if ! docker run --rm --user "$(id -u):$(id -g)" --entrypoint /app/bin/metrum-ai-router-migrate \
      -v "$EDGE_DATA_DIR:$EDGE_DATA_DIR" "$ROUTER_IMAGE" \
      --action="$action" --driver=sqlite --db="$EDGE_DATA_DIR/state/usage.sqlite" --json >/dev/null; then
    die "migration action $action failed; the router stays stopped and $EDGE_CONFIG holds the previous groups. Restore the snapshot in $EDGE_DATA_DIR/backups if needed."
  fi
done

if [ "$RESTART_MODELS" = "1" ]; then
  log "recreating the router and the model servers"
  compose up -d --remove-orphans --force-recreate
else
  log "recreating the router (model servers keep running)"
  compose up -d --no-deps router
fi

attempts=0
until curl -fsS "${EDGE_BASE_URL}/readyz" >/dev/null 2>&1; do
  attempts=$((attempts + 1))
  [ "$attempts" -lt 90 ] || die "router did not become ready after the upgrade"
  sleep 2
done
log "upgrade complete; router is ready at ${EDGE_BASE_URL}"
