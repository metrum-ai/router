#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
#
# Edge bundle installer: router + local model stack + default model groups +
# escalation path + admin UI, in one command.
#
#   sudo bash install.sh --router-image metrum-ai-router:4.7.0-linux-amd64
#   bash install.sh --mode native --bin-dir ./bin          # no containers, CPU mock upstreams
#   bash install.sh --dry-run                              # render and check, start nothing
#
# Everything it creates lives under --data-dir (default: /var/lib/metrum-edge as root,
# otherwise $HOME/metrum-edge; lib/common.sh holds that rule for all four scripts):
#   config/config.yaml      rendered router configuration (model groups)
#   secrets/env             generated secrets and settings, mode 0600
#   secrets/*.token         caller tokens, mode 0600
#   state/ logs/            router state and request logs
#   compose/                composed deployment for the chosen accelerator
#   install.env             what demo.sh, upgrade.sh and uninstall.sh read
#
# Re-running is safe: existing secrets and tokens are kept unless --force.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
. "$SCRIPT_DIR/lib/common.sh"
MODE="docker"
DATA_DIR="$(metrum_default_data_dir)"
LISTEN_ADDR="127.0.0.1"
ROUTER_PORT="8080"
ROUTER_IMAGE=""
BIN_DIR=""
ROUTERCTL_OVERRIDE="${ROUTERCTL_OVERRIDE:-}"
ACCELERATOR="auto"
ESCALATE_URL="https://api.openai.com/v1"
ESCALATE_MODEL="gpt-5.4"
ESCALATE_INPUT_PRICE="2.5"
ESCALATE_OUTPUT_PRICE="15.0"
ESCALATE_KEY_ENV="OPENAI_API_KEY"
ESCALATE_PRICING_SOURCE="https://developers.openai.com/api/docs/pricing"
ADMIN_PASSWORD=""
ADMIN_PASSWORD_HASH=""
BUDGET_DAILY_REQUESTS="2"
DRY_RUN=0
FORCE=0
AIR_GAPPED=0
EDGE_TINY_VLLM_PORT="${EDGE_TINY_VLLM_PORT:-18081}"
EDGE_PRIMARY_VLLM_PORT="${EDGE_PRIMARY_VLLM_PORT:-18082}"
EDGE_ESCALATION_PORT="${EDGE_ESCALATION_PORT:-18083}"

usage() {
  cat <<'EOF'
Edge bundle installer: router + local model stack + default model groups +
escalation path + admin UI, in one command.

  sudo bash install.sh --router-image metrum-ai-router:4.7.0-linux-amd64
  bash install.sh --mode native --bin-dir ./bin     # no containers, CPU mock upstreams
  bash install.sh --dry-run                         # render and check, start nothing

Options:
  --mode docker|native   docker (default) runs the model stack in containers;
                         native runs the router binary and CPU mock upstreams,
                         for evaluation, air-gapped trials and CI.
  --data-dir DIR         state, config, secrets and logs (default: see lib/common.sh -
                         /var/lib/metrum-edge as root, else $HOME/metrum-edge)
  --router-image IMAGE   required for docker mode, for example
                         metrum-ai-router:4.7.0-linux-amd64
  --bin-dir DIR          native mode: directory holding metrum-ai-router,
                         metrum-ai-routerctl and metrum-ai-router-migrate
  --routerctl PATH       use this metrum-ai-routerctl to generate caller tokens
                         instead of running one from the router image
  --accelerator amd|nvidia|auto   model-server image family (default auto)
  --escalation-url URL   OpenAI-compatible escalation endpoint
  --escalation-model ID  model or group name at that endpoint
  --escalation-key-env N environment variable holding that key (default OPENAI_API_KEY)
  --admin-password PW    admin UI password (default: generated)
  --admin-password-hash H bcrypt hash instead of --admin-password
  --budget-daily-requests N  daily request budget for the demo-budget caller (default 2)
  --listen ADDR          router bind address (default 127.0.0.1)
  --port PORT            router port (default 8080)
  --air-gapped           skip network preflights
  --dry-run              render everything, start nothing
  --force                regenerate secrets and tokens
  -h, --help             this help
EOF
}

log() { printf '%s install: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --mode) MODE="${2:?}"; shift 2 ;;
    --data-dir) DATA_DIR="${2:?}"; shift 2 ;;
    --router-image) ROUTER_IMAGE="${2:?}"; shift 2 ;;
    --bin-dir) BIN_DIR="${2:?}"; shift 2 ;;
    --routerctl) ROUTERCTL_OVERRIDE="${2:?}"; shift 2 ;;
    --accelerator) ACCELERATOR="${2:?}"; shift 2 ;;
    --escalation-url) ESCALATE_URL="${2:?}"; shift 2 ;;
    --escalation-model) ESCALATE_MODEL="${2:?}"; shift 2 ;;
    --escalation-input-price) ESCALATE_INPUT_PRICE="${2:?}"; shift 2 ;;
    --escalation-output-price) ESCALATE_OUTPUT_PRICE="${2:?}"; shift 2 ;;
    --escalation-key-env) ESCALATE_KEY_ENV="${2:?}"; shift 2 ;;
    --admin-password) ADMIN_PASSWORD="${2:?}"; shift 2 ;;
    --admin-password-hash) ADMIN_PASSWORD_HASH="${2:?}"; shift 2 ;;
    --budget-daily-requests) BUDGET_DAILY_REQUESTS="${2:?}"; shift 2 ;;
    --listen) LISTEN_ADDR="${2:?}"; shift 2 ;;
    --port) ROUTER_PORT="${2:?}"; shift 2 ;;
    --air-gapped) AIR_GAPPED=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --force) FORCE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option $1 (see --help)" ;;
  esac
done

case "$MODE" in docker|native) ;; *) die "--mode must be docker or native" ;; esac
# The data directory default comes from lib/common.sh: root gets
# /var/lib/metrum-edge because /root is 0700 and the router runs as uid 65532, and
# every bundle script applies the same rule so `sudo bash demo.sh` finds what
# install.sh created.
if [ "$DATA_DIR" = "/var/lib/metrum-edge" ] && [ "$(id -u)" = "0" ]; then
  DATA_DIR_DEFAULTED=1
fi
case "$ACCELERATOR" in amd|nvidia|auto) ;; *) die "--accelerator must be amd, nvidia or auto" ;; esac
mkdir -p "$DATA_DIR" || die "cannot create --data-dir $DATA_DIR"
DATA_DIR="$(cd "$DATA_DIR" && pwd)"

CONFIG_DIR="$DATA_DIR/config"
SECRET_DIR="$DATA_DIR/secrets"
STATE_DIR="$DATA_DIR/state"
LOG_DIR="$DATA_DIR/logs"
COMPOSE_DIR="$DATA_DIR/compose"
ENV_FILE="$SECRET_DIR/env"
INSTALL_ENV="$DATA_DIR/install.env"
CONFIG_FILE="$CONFIG_DIR/config.yaml"
DEMO_TOKEN_FILE="$SECRET_DIR/demo.token"
BUDGET_TOKEN_FILE="$SECRET_DIR/demo-budget.token"
BASE_URL="http://${LISTEN_ADDR}:${ROUTER_PORT}"

detect_accelerator() {
  if [ "$ACCELERATOR" != "auto" ]; then
    return
  fi
  if [ -e /dev/kfd ] && [ -d /dev/dri ]; then
    ACCELERATOR="amd"
  elif command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi -L >/dev/null 2>&1; then
    ACCELERATOR="nvidia"
  else
    ACCELERATOR="amd"
    log "no accelerator detected; defaulting to the AMD overlay (override with --accelerator)"
  fi
}

random_hex() {
  local bytes="$1"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes"
    return
  fi
  od -An -tx1 -N "$bytes" /dev/urandom | tr -d ' \n'
}

admin_hash() {
  if [ -n "$ADMIN_PASSWORD_HASH" ]; then
    printf '%s' "$ADMIN_PASSWORD_HASH"
    return
  fi
  if [ -z "$ADMIN_PASSWORD" ]; then
    ADMIN_PASSWORD="$(random_hex 12)"
    log "generated an admin password"
  fi
  umask 077
  printf '%s\n' "$ADMIN_PASSWORD" >"$SECRET_DIR/admin-password"
  chmod 0600 "$SECRET_DIR/admin-password"
  if command -v htpasswd >/dev/null 2>&1; then
    htpasswd -bnBC 12 '' "$ADMIN_PASSWORD" | tr -d ':\n'
    return
  fi
  if command -v python3 >/dev/null 2>&1 && python3 -c 'import bcrypt' >/dev/null 2>&1; then
    ADMIN_PASSWORD="$ADMIN_PASSWORD" python3 -c 'import os,bcrypt;print(bcrypt.hashpw(os.environ["ADMIN_PASSWORD"].encode(), bcrypt.gensalt(rounds=12)).decode())'
    return
  fi
  # Perl's crypt(3) reaches the system libxcrypt, which carries bcrypt on stock
  # Ubuntu; the password goes in on stdin, never on a command line.
  if command -v perl >/dev/null 2>&1; then
    local perl_hash
    perl_hash="$(printf '%s' "$ADMIN_PASSWORD" | perl -0777 -e '
      my @alphabet = split //, "./ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
      my $salt = "";
      $salt .= $alphabet[int(rand(scalar @alphabet))] for 1 .. 22;
      my $pw = <STDIN>;
      my $hash = crypt($pw, "\$2b\$12\$" . $salt);
      exit 1 unless $hash =~ /^\$2[aby]\$/;
      print $hash, "\n";
    ')"
    if [ -n "$perl_hash" ]; then
      printf '%s' "$perl_hash"
      return
    fi
  fi
  die "the admin UI needs a bcrypt password hash and no local tool can make one: install apache2-utils (apt-get install -y apache2-utils) or pass --admin-password-hash <bcrypt>. The password itself was written to $SECRET_DIR/admin-password"
}

ensure_binaries() {
  if [ "$MODE" = "native" ]; then
    if [ -n "$BIN_DIR" ]; then
      ROUTER_BIN="$BIN_DIR/metrum-ai-router"
      ROUTERCTL_BIN="$BIN_DIR/metrum-ai-routerctl"
      MIGRATE_BIN="$BIN_DIR/metrum-ai-router-migrate"
    else
      ROUTER_BIN="$(command -v metrum-ai-router || true)"
      ROUTERCTL_BIN="$(command -v metrum-ai-routerctl || true)"
      MIGRATE_BIN="$(command -v metrum-ai-router-migrate || true)"
    fi
    for binary in "$ROUTER_BIN" "$ROUTERCTL_BIN" "$MIGRATE_BIN"; do
      [ -n "$binary" ] && [ -x "$binary" ] || die "native mode needs metrum-ai-router, metrum-ai-routerctl and metrum-ai-router-migrate: pass --bin-dir or put them on PATH"
    done
    log "native binaries: $ROUTER_BIN"
    return
  fi
  command -v curl >/dev/null 2>&1 || die "curl is required by the installer and the demo"
  [ -n "$ROUTER_IMAGE" ] || die "docker mode needs --router-image (for example metrum-ai-router:<version>-linux-amd64)"
  case "$ROUTER_IMAGE" in *:latest) die "--router-image must not be latest" ;; esac
  # The runtime image runs as uid 65532 and the installer hands it config, state and
  # logs; a non-root user cannot do that, and carrying on reproduces the failure this
  # exists to prevent - files owned by the caller with mode 0600, a router that never
  # becomes ready, and a 360 s wait before a misleading timeout. EDGE_TEST_HANDOVER is
  # the seam the install test uses; --dry-run starts nothing and needs no handover.
  if [ "$(id -u)" != "0" ] && [ "${EDGE_TEST_HANDOVER:-}" != "1" ] && [ "$DRY_RUN" != "1" ]; then
    die "docker mode must run as root: the router image runs as uid 65532 and the installer must hand it config, state and logs, which a non-root user cannot do. Run it with sudo (as the README shows); on rootless Podman see the Rootless Podman section of the bundle README."
  fi
  if [ "$DRY_RUN" != "1" ] && ! path_ancestors_searchable_by_others "$DATA_DIR"; then
    die "the data directory $DATA_DIR is not traversable by the router container's user (uid 65532): a parent directory is not readable/searchable by others (for example /root is mode 0700). Pass --data-dir with a path the container user can reach, such as /var/lib/metrum-edge."
  fi
  if [ "$DRY_RUN" = "1" ]; then
    log "dry run: not checking for docker; nothing will be started"
    command -v docker >/dev/null 2>&1 || [ -n "$ROUTERCTL_OVERRIDE" ] || \
      die "a docker-mode dry run still generates caller tokens; pass --routerctl PATH (or install docker)"
    return
  fi
  command -v docker >/dev/null 2>&1 || die "docker is required for --mode docker; use --mode native for a no-container trial"
  docker compose version >/dev/null 2>&1 || die "the docker compose plugin is required (docker compose version failed)"
}

compose() {
  docker compose \
    --env-file "$ENV_FILE" \
    -f "$COMPOSE_DIR/compose.yaml" \
    -f "$COMPOSE_DIR/compose.${ACCELERATOR}.yaml" \
    "$@"
}

routerctl() {
  if [ -n "$ROUTERCTL_OVERRIDE" ]; then
    "$ROUTERCTL_OVERRIDE" "$@"
  elif [ "$MODE" = "native" ]; then
    "$ROUTERCTL_BIN" "$@"
  else
    docker run --rm --user "$(id -u):$(id -g)" --entrypoint /app/bin/metrum-ai-routerctl \
      -v "$DATA_DIR:$DATA_DIR" "$ROUTER_IMAGE" "$@"
  fi
}

migrate() {
  if [ "$MODE" = "native" ]; then
    "$MIGRATE_BIN" "$@"
  else
    # Host and container path are identical here on purpose: the data directory is
    # bind-mounted at its own absolute path, so the host paths passed in the
    # arguments resolve inside the helper container.
    docker run --rm --user "$(id -u):$(id -g)" --entrypoint /app/bin/metrum-ai-router-migrate \
      -v "$DATA_DIR:$DATA_DIR" "$ROUTER_IMAGE" "$@"
  fi
}

preflight() {
  if [ "$AIR_GAPPED" = "1" ]; then
    log "air-gapped install: registry and weight downloads are not preflighted"
    return
  fi
  if [ "$MODE" = "docker" ]; then
    docker image inspect "$ROUTER_IMAGE" >/dev/null 2>&1 || log "router image $ROUTER_IMAGE is not present locally; docker will pull it"
  fi
}

create_directories() {
  mkdir -p "$CONFIG_DIR" "$SECRET_DIR" "$STATE_DIR" "$LOG_DIR" "$COMPOSE_DIR"
  chmod 0700 "$SECRET_DIR" "$CONFIG_DIR"
  chmod 0750 "$STATE_DIR" "$LOG_DIR"
}

# detect_gpu_group_ids fills RENDER_GID and VIDEO_GID for the AMD overlay. The pinned
# ROCm image has no `render` group in its /etc/group, so passing group names cannot
# work; the numeric host GIDs of the device nodes are what the container needs.
# EDGE_RENDER_GID/EDGE_VIDEO_GID are an explicit override for hosts where the nodes
# are not visible to the installer (containers, VMs) and for the install test.
detect_gpu_group_ids() {
  local dev_root="${EDGE_DEV_ROOT:-/dev}" render_node card_node
  RENDER_GID="${EDGE_RENDER_GID:-}"
  VIDEO_GID="${EDGE_VIDEO_GID:-}"
  if [ -n "$RENDER_GID" ] && [ -n "$VIDEO_GID" ]; then
    log "using supplied GPU group ids (render=$RENDER_GID video=$VIDEO_GID)"
    return 0
  fi
  render_node="$(ls "$dev_root"/dri/renderD* 2>/dev/null | head -1 || true)"
  card_node="$dev_root/dri/card0"
  if [ -z "$render_node" ]; then
    die "AMD docker mode needs a render node to read its group id: expected $dev_root/dri/renderD*. On a host where the devices are not visible here, pass EDGE_RENDER_GID and EDGE_VIDEO_GID (stat -c %g /dev/dri/renderD128, stat -c %g /dev/dri/card0)."
  fi
  if [ -z "$RENDER_GID" ]; then
    RENDER_GID="$(stat -c %g "$render_node")"
  fi
  if [ -z "$VIDEO_GID" ]; then
    if [ -e "$card_node" ]; then
      VIDEO_GID="$(stat -c %g "$card_node")"
    else
      # /dev/kfd belongs to the render group, not video, so it is not a fallback.
      VIDEO_GID="$(getent group video 2>/dev/null | cut -d: -f3 || true)"
    fi
  fi
  if [ -z "$VIDEO_GID" ]; then
    die "AMD docker mode could not determine the video group id: no $card_node and no 'video' group in the host group database. Set EDGE_VIDEO_GID explicitly (stat -c %g /dev/dri/card0)."
  fi
  log "detected GPU group ids: render=$RENDER_GID video=$VIDEO_GID"
}

# path_ancestors_searchable_by_others is true when every directory above $1 is
# searchable by "others". A container's uid 65532 reaches a bind mount through those
# components, so a 0700 parent (like /root) makes the mount unreachable no matter who
# owns the files inside it.
path_ancestors_searchable_by_others() {
  local path last
  path="$(dirname "$1")"
  while :; do
    if [ -e "$path" ]; then
      last="$(stat -c %a "$path")"
      last="${last: -1}"
      case "$last" in
        1|3|5|7) ;;
        *) return 1 ;;
      esac
    fi
    [ "$path" = "/" ] && break
    path="$(dirname "$path")"
  done
  return 0
}

write_environment() {
  if [ -f "$ENV_FILE" ] && [ "$FORCE" != "1" ]; then
    log "keeping existing $ENV_FILE"
    return
  fi
  local vllm_key transform_key hash
  vllm_key="$(random_hex 24)"
  transform_key="$(random_hex 64)"
  hash="$(admin_hash)"
  umask 077
  # Values are single quoted: the bcrypt hash and the tokens contain '$', and
  # this file is both sourced by the bundle scripts and read by docker compose,
  # which treats a single-quoted value literally.
  {
    printf '# Generated by install.sh. Contains secrets: keep mode 0600, never commit.\n'
    printf "EDGE_DATA_DIR='%s'\n" "$DATA_DIR"
    printf "EDGE_BIND_ADDRESS='%s'\n" "$LISTEN_ADDR"
    printf "EDGE_ROUTER_PORT='%s'\n" "$ROUTER_PORT"
    printf "ROUTER_IMAGE='%s'\n" "$ROUTER_IMAGE"
    printf "LOCAL_VLLM_API_KEY='%s'\n" "$vllm_key"
    printf "ROUTER_ID_TRANSFORM_KEY='%s'\n" "$transform_key"
    printf "METRUM_ADMIN_PASSWORD_HASH='%s'\n" "$hash"
    printf "EDGE_ESCALATION_API_KEY='%s'\n" "${EDGE_ESCALATION_API_KEY:-}"
    printf "HF_HUB_OFFLINE='%s'\n" "$([ "$AIR_GAPPED" = "1" ] && echo 1 || echo 0)"
  } >"$ENV_FILE"
  chmod 0600 "$ENV_FILE"
  if [ "$MODE" = "docker" ] && [ "$ACCELERATOR" = "amd" ]; then
    detect_gpu_group_ids
    set_env_value RENDER_GID "$RENDER_GID"
    set_env_value VIDEO_GID "$VIDEO_GID"
  fi
  log "wrote $ENV_FILE"
}

set_env_value() {
  local key="$1" value="$2"
  local escaped
  escaped="$(printf '%s' "$value" | sed "s/['\&|]/\\&/g")"
  if grep -q "^${key}=" "$ENV_FILE"; then
    sed -i "s|^${key}=.*|${key}='${escaped}'|" "$ENV_FILE"
  else
    printf "%s='%s'\n" "$key" "$escaped" >>"$ENV_FILE"
  fi
}

ensure_token() {
  local caller="$1" owner="$2" project="$3" env_name="$4" key="$5" allow="$6" token_file="$7"
  if [ -s "$token_file" ] && [ "$FORCE" != "1" ]; then
    log "keeping existing $(basename "$token_file")"
    return
  fi
  rm -f "$token_file"
  routerctl callers generate \
    --owner-user "$owner" --project "$project" --env "$env_name" --key "$key" \
    --allow "$allow" --token-out "$token_file" >/dev/null
  chmod 0600 "$token_file"
  log "generated $(basename "$token_file") for caller $caller"
}

token_hash_json() {
  routerctl callers hash --token-file "$1"
}

json_field() {
  local field="$1"
  grep -o "\"${field}\": *\"[^\"]*\"" | sed 's/.*: *"//; s/"$//'
}

render_config() {
  local demo_json budget_json demo_hash demo_tid budget_hash budget_tid
  demo_json="$(token_hash_json "$DEMO_TOKEN_FILE")"
  budget_json="$(token_hash_json "$BUDGET_TOKEN_FILE")"
  demo_hash="$(printf '%s' "$demo_json" | json_field token_hash)"
  demo_tid="$(printf '%s' "$demo_json" | json_field token_id)"
  budget_hash="$(printf '%s' "$budget_json" | json_field token_hash)"
  budget_tid="$(printf '%s' "$budget_json" | json_field token_id)"
  [ -n "$demo_hash" ] && [ -n "$budget_hash" ] || die "could not read token hashes from routerctl output"

  local tiny_base primary_base escalate_base escalate_key_env
  escalate_base="$ESCALATE_URL"
  escalate_key_env="$ESCALATE_KEY_ENV"
  if [ "$MODE" = "native" ]; then
    tiny_base="http://127.0.0.1:${EDGE_TINY_VLLM_PORT}/v1"
    primary_base="http://127.0.0.1:${EDGE_PRIMARY_VLLM_PORT}/v1"
    # Native mode has no accelerator: the escalation group points at the third
    # CPU mock upstream so the demo runs offline. It is not a model.
    escalate_base="http://127.0.0.1:${EDGE_ESCALATION_PORT}/v1"
    escalate_key_env="LOCAL_VLLM_API_KEY"
  else
    tiny_base="http://vllm-tiny:8000/v1"
    primary_base="http://vllm-primary:8000/v1"
  fi

  # Docker mode mounts the data directory at /app (state, logs, config), so the
  # rendered configuration must carry those in-container paths: rendering the host
  # path made the router try to create /root/metrum-edge inside its own filesystem.
  # The same applies to the listen address: inside a container 127.0.0.1 is the
  # container's own loopback, so a process bound there never answers the published
  # port (docker-proxy connects to the container address). Docker mode therefore
  # listens on all interfaces inside the container while the host publish stays on
  # EDGE_BIND_ADDRESS, which defaults to loopback.
  local runtime_root="$DATA_DIR" runtime_listen="$LISTEN_ADDR"
  if [ "$MODE" = "docker" ]; then
    runtime_root="/app"
    runtime_listen="0.0.0.0"
  fi
  umask 077
  sed \
    -e "s|@@LISTEN@@|${runtime_listen}:${ROUTER_PORT}|g" \
    -e "s|@@DATA_DIR@@|${runtime_root}|g" \
    -e "s|@@TINY_BASE_URL@@|${tiny_base}|g" \
    -e "s|@@PRIMARY_BASE_URL@@|${primary_base}|g" \
    -e "s|@@ESCALATE_BASE_URL@@|${escalate_base}|g" \
    -e "s|@@ESCALATE_MODEL@@|${ESCALATE_MODEL}|g" \
    -e "s|@@ESCALATE_KEY_ENV@@|${escalate_key_env}|g" \
    -e "s|@@ESCALATE_INPUT_PRICE@@|${ESCALATE_INPUT_PRICE}|g" \
    -e "s|@@ESCALATE_OUTPUT_PRICE@@|${ESCALATE_OUTPUT_PRICE}|g" \
    -e "s|@@ESCALATE_PRICING_SOURCE@@|${ESCALATE_PRICING_SOURCE}|g" \
    -e "s|@@DEMO_TOKEN_HASH@@|${demo_hash}|g" \
    -e "s|@@DEMO_TOKEN_ID@@|${demo_tid}|g" \
    -e "s|@@BUDGET_TOKEN_HASH@@|${budget_hash}|g" \
    -e "s|@@BUDGET_TOKEN_ID@@|${budget_tid}|g" \
    -e "s|@@BUDGET_DAILY_REQUESTS@@|${BUDGET_DAILY_REQUESTS}|g" \
    "$SCRIPT_DIR/config.template.yaml" >"$CONFIG_FILE"
  chmod 0600 "$CONFIG_FILE"
  if grep -Eq '@@[A-Z_]+@@' "$CONFIG_FILE"; then
    die "rendered config still contains an unreplaced placeholder: $(grep -Eo '@@[A-Z_]+@@' "$CONFIG_FILE" | sort -u | tr '\n' ' ')"
  fi
  log "wrote $CONFIG_FILE"
}

copy_compose() {
  [ "$MODE" = "docker" ] || return 0
  cp "$SCRIPT_DIR/compose.yaml" "$COMPOSE_DIR/compose.yaml"
  cp "$SCRIPT_DIR/compose.${ACCELERATOR}.yaml" "$COMPOSE_DIR/compose.${ACCELERATOR}.yaml"
  log "composed deployment for $ACCELERATOR in $COMPOSE_DIR"
}

run_migrations() {
  local db="$STATE_DIR/usage.sqlite" action
  for action in plan apply; do
    log "migrate $action"
    if ! migrate --action="$action" --driver=sqlite --db="$db" --json >/dev/null; then
      die "migration action $action failed; see $STATE_DIR. The router will not start until the sequence completes."
    fi
  done
  # verify-serving refuses to pass while a bound data job is still pending; the
  # documented order resumes the job at checkpoint 0 first (DOCKER_COMPOSE_INSTALL.md).
  log "migrate resume historical-usage-validation-v1"
  if ! migrate --action=resume --job=historical-usage-validation-v1 --checkpoint-ordinal=0 \
      --driver=sqlite --db="$db" --json >/dev/null; then
    die "migration resume failed; see $STATE_DIR. The router will not start until the sequence completes."
  fi
  for action in verify-serving status; do
    log "migrate $action"
    if ! migrate --action="$action" --driver=sqlite --db="$db" --json >/dev/null; then
      die "migration action $action failed; see $STATE_DIR. The router will not start until the sequence completes."
    fi
  done
}

# A cold docker install pulls ~20 GB of weights and compiles two model servers before the
# router can answer. Measured: 6 min 18 s to the first healthy model server on an A6000
# 48 GB, and 516 s to load the 30B primary on the Strix Halo box - both longer than the
# 360 s this used to wait, so a first install could fail while the stack was still coming
# up. Native mode has no weights to pull.
wait_ready() {
  local timeout="${EDGE_READY_TIMEOUT:-1200}" interval=2 elapsed=0 announced=0
  if [ "$MODE" = "native" ]; then
    timeout="${EDGE_READY_TIMEOUT:-180}"
  fi
  while [ "$elapsed" -lt "$timeout" ]; do
    if curl -fsS "${BASE_URL}/readyz" >/dev/null 2>&1; then
      log "router is ready at ${BASE_URL} after ${elapsed}s"
      return 0
    fi
    if [ "$announced" = "0" ] && [ "$elapsed" -ge 30 ]; then
      announced=1
      # One line, then quiet: a 10-minute silence looks like a hang.
      log "waiting for ${BASE_URL}/readyz (model servers still loading; up to ${timeout}s, EDGE_READY_TIMEOUT to change)"
    fi
    sleep "$interval"
    elapsed=$((elapsed + interval))
  done
  die "router did not become ready at ${BASE_URL}/readyz within ${timeout}s (raise EDGE_READY_TIMEOUT on a slow first pull)"
}

# hand_over_data_dirs makes the directories the router container mounts readable by
# the runtime image's user (a scratch image runs as uid 65532). It must be the last
# thing that touches the data directory before the stack starts: the config, the
# tokens and the usage database are all written by earlier steps, many of them as
# root (migrations run with --user), and a chown that happens before them leaves
# root-owned files the router cannot read. secrets/ is deliberately NOT handed over:
# the router container does not mount it.
hand_over_data_dirs() {
  if [ "$MODE" != "docker" ]; then
    return 0
  fi
  # Root is the normal case (installs run with sudo). EDGE_TEST_HANDOVER=1 exists so
  # the install test can exercise this ordering without running as root.
  if [ "$(id -u)" != "0" ] && [ "${EDGE_TEST_HANDOVER:-}" != "1" ]; then
    log "not running as root: leaving ownership of $CONFIG_DIR, $STATE_DIR and $LOG_DIR as is"
    return 0
  fi
  chown -R 65532:65532 "$CONFIG_DIR" "$STATE_DIR" "$LOG_DIR"
  log "handed config, state and logs to uid 65532 (the runtime image's user); secrets stay root-owned"
}

start_docker() {
  hand_over_data_dirs
  log "starting the stack (first start downloads model weights)"
  compose up -d --remove-orphans
  wait_ready
}

stop_native_processes() {
  local pidfile pid
  for pidfile in "$STATE_DIR"/router.pid "$STATE_DIR"/mock-*.pid; do
    [ -f "$pidfile" ] || continue
    pid="$(cat "$pidfile" 2>/dev/null || true)"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      log "stopped $(basename "$pidfile" .pid) from a previous run (pid $pid)"
    fi
    rm -f "$pidfile"
  done
  # Give a stopping router its drain budget before a new one takes the port.
  sleep 1
}

start_native() {
  mkdir -p "$LOG_DIR"
  stop_native_processes
  export LOCAL_VLLM_API_KEY ROUTER_ID_TRANSFORM_KEY METRUM_ADMIN_PASSWORD_HASH EDGE_ESCALATION_API_KEY
  MOCK_LABEL=tiny MOCK_PORT="$EDGE_TINY_VLLM_PORT" MOCK_SERVED_NAME=local-tiny \
    python3 "$SCRIPT_DIR/mock/mock-upstream.py" >>"$LOG_DIR/mock-tiny.log" 2>&1 &
  echo $! >"$STATE_DIR/mock-tiny.pid"
  MOCK_LABEL=primary MOCK_PORT="$EDGE_PRIMARY_VLLM_PORT" MOCK_SERVED_NAME=local-primary \
    python3 "$SCRIPT_DIR/mock/mock-upstream.py" >>"$LOG_DIR/mock-primary.log" 2>&1 &
  echo $! >"$STATE_DIR/mock-primary.pid"
  MOCK_LABEL=escalation MOCK_PORT="$EDGE_ESCALATION_PORT" MOCK_SERVED_NAME="$ESCALATE_MODEL" \
    python3 "$SCRIPT_DIR/mock/mock-upstream.py" >>"$LOG_DIR/mock-escalation.log" 2>&1 &
  echo $! >"$STATE_DIR/mock-escalation.pid"

  "$ROUTER_BIN" --config "$CONFIG_FILE" >>"$LOG_DIR/router.log" 2>&1 &
  echo $! >"$STATE_DIR/router.pid"
  log "native mode: mock upstreams and router started (not a model; see deploy/edge/README.md)"
  wait_ready
}

verify() {
  local token models
  token="$(tr -d '\n' <"$DEMO_TOKEN_FILE")"
  models="$(curl -fsS -H "Authorization: Bearer ${token}" "${BASE_URL}/v1/models")" || die "caller token does not authenticate; run: $0 --force"
  case "$models" in
    *local-chat*) ;;
    *) die "the local-chat group is not advertised by ${BASE_URL}/v1/models" ;;
  esac
  log "caller token authenticates and the local-chat group is advertised"
}

write_install_env() {
  local escalation_base_url="$ESCALATE_URL"
  if [ "$MODE" = "native" ]; then
    escalation_base_url="http://127.0.0.1:${EDGE_ESCALATION_PORT}/v1"
  fi
  umask 077
  {
    printf '# Written by install.sh; sourced by demo.sh, upgrade.sh and uninstall.sh.\n'
    printf 'EDGE_MODE=%s\n' "$MODE"
    printf 'EDGE_ACCELERATOR=%s\n' "$ACCELERATOR"
    printf 'EDGE_DATA_DIR=%s\n' "$DATA_DIR"
    printf 'EDGE_BASE_URL=%s\n' "$BASE_URL"
    printf 'EDGE_CONFIG=%s\n' "$CONFIG_FILE"
    printf 'EDGE_ENV_FILE=%s\n' "$ENV_FILE"
    printf 'EDGE_DEMO_TOKEN_FILE=%s\n' "$DEMO_TOKEN_FILE"
    printf 'EDGE_BUDGET_TOKEN_FILE=%s\n' "$BUDGET_TOKEN_FILE"
    printf 'EDGE_ROUTER_IMAGE=%s\n' "$ROUTER_IMAGE"
    printf 'EDGE_BIN_DIR=%s\n' "$BIN_DIR"
    printf 'EDGE_ESCALATION_KEY_ENV=%s\n' "$ESCALATE_KEY_ENV"
    printf 'EDGE_ESCALATION_BASE_URL=%s\n' "$escalation_base_url"
    printf 'EDGE_ESCALATION_MODEL=%s\n' "$ESCALATE_MODEL"
    printf 'EDGE_INSTALLED_AT=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } >"$INSTALL_ENV"
  chmod 0600 "$INSTALL_ENV"
}

summary() {
  local MODE_DESCRIPTION
  if [ "$MODE" = "native" ]; then
    MODE_DESCRIPTION="native mode: router plus CPU mock upstreams (no accelerator, no containers)"
  else
    MODE_DESCRIPTION="docker mode: $ACCELERATOR model servers in containers"
  fi
  cat <<EOF

Edge bundle installed ($MODE_DESCRIPTION).

  router            $BASE_URL
  admin UI          $BASE_URL/admin/reports/   (user: admin)
  model groups      local-tiny, local-chat, escalate
  config            $CONFIG_FILE
  secrets           $ENV_FILE (0600), $SECRET_DIR/admin-password
  caller tokens     $DEMO_TOKEN_FILE, $BUDGET_TOKEN_FILE
  logs              $LOG_DIR

Next:
  bash $SCRIPT_DIR/demo.sh --data-dir $DATA_DIR      # local-first, escalation, budget hit, spend report
  bash $SCRIPT_DIR/upgrade.sh --data-dir $DATA_DIR   # after replacing the router image
  bash $SCRIPT_DIR/uninstall.sh --data-dir $DATA_DIR
EOF
}

main() {
  detect_accelerator
  ensure_binaries
  preflight
  create_directories
  write_environment
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  export LOCAL_VLLM_API_KEY ROUTER_ID_TRANSFORM_KEY METRUM_ADMIN_PASSWORD_HASH EDGE_ESCALATION_API_KEY
  ensure_token demo edge-operator edge demo k1 "local-tiny,local-chat,escalate" "$DEMO_TOKEN_FILE"
  ensure_token demo-budget edge-operator edge demo k2 "local-tiny,local-chat" "$BUDGET_TOKEN_FILE"
  render_config
  copy_compose
  if [ "$DRY_RUN" = "1" ]; then
    # A dry run must not rewrite an existing installation's state: upgrade.sh re-renders
    # through this path, and writing install.env here dropped the image an operator had
    # installed with, which then broke the migration step ("repository name must have at
    # least one component").
    if [ -f "$INSTALL_ENV" ]; then
      log "dry run: rendered $CONFIG_FILE; left the existing $INSTALL_ENV untouched"
    else
      write_install_env
      log "dry run: rendered $CONFIG_FILE and wrote $INSTALL_ENV; nothing started"
    fi
    return 0
  fi
  run_migrations
  if [ "$MODE" = "docker" ]; then start_docker; else start_native; fi
  verify
  write_install_env
  summary
}

main "$@"
