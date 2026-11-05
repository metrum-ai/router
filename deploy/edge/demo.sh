#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
#
# Edge bundle demo: the four things an operator needs to see on day one.
#
#   1. local-first answer      local-chat and local-tiny answer from the box
#   2. escalation             the escalate group leaves the box, and is separate
#   3. budget hit             the demo-budget caller is refused once its day budget is spent
#   4. spend report           tokens and cost from the router's own usage rows
#
# Each step prints PASS, SKIP (with its reason) or FAIL. A skip is not a pass.
# The script exits non-zero if any step fails.
#
# Usage: bash demo.sh [--data-dir DIR]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
. "$SCRIPT_DIR/lib/common.sh"

DATA_DIR="$(metrum_default_data_dir)"
while [ $# -gt 0 ]; do
  case "$1" in
    --data-dir) DATA_DIR="${2:?}"; shift 2 ;;
    -h|--help) sed -n '4,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "demo: unknown option $1" >&2; exit 2 ;;
  esac
done

INSTALL_ENV="$DATA_DIR/install.env"
[ -f "$INSTALL_ENV" ] || { echo "demo: $INSTALL_ENV not found; run install.sh first" >&2; exit 2; }
# shellcheck disable=SC1090
. "$INSTALL_ENV"
# The reconciliation and usage commands load the router configuration, which
# resolves the identifier key and provider keys from the environment.
if [ -f "$EDGE_ENV_FILE" ]; then
  # shellcheck disable=SC1090
  . "$EDGE_ENV_FILE"
  export LOCAL_VLLM_API_KEY ROUTER_ID_TRANSFORM_KEY METRUM_ADMIN_PASSWORD_HASH 2>/dev/null || true
fi

FAILURES=0
BODY_FILE="$(mktemp)"
trap 'rm -f "$BODY_FILE"' EXIT
pass() { printf 'PASS  %s\n' "$*"; }
skip() { printf 'SKIP  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; FAILURES=$((FAILURES + 1)); }
note() { printf '      %s\n' "$*"; }

# The config path the router process itself sees: docker mode mounts the data
# directory at /app, so the host path from install.env is wrong there.
router_config_path() {
  metrum_config_path_for_runtime "$EDGE_CONFIG" "${EDGE_MODE:-docker}"
}

routerctl() {
  if [ "$EDGE_MODE" = "native" ]; then
    "${EDGE_BIN_DIR:+$EDGE_BIN_DIR/}metrum-ai-routerctl" "$@"
  else
    docker compose --env-file "$EDGE_ENV_FILE" \
      -f "${EDGE_DATA_DIR}/compose/compose.yaml" \
      -f "${EDGE_DATA_DIR}/compose/compose.${EDGE_ACCELERATOR}.yaml" \
      exec -T router /app/bin/metrum-ai-routerctl "$@"
  fi
}

# ask TOKEN GROUP PROMPT -> sets STATUS, ELAPSED, BODY_FILE
ask() {
  local token="$1" group="$2" prompt="$3" result
  if ! result="$(curl -sS -o "$BODY_FILE" -w '%{http_code} %{time_total}' \
      -H "Authorization: Bearer ${token}" \
      -H 'Content-Type: application/json' \
      -d "{\"model\":\"${group}\",\"messages\":[{\"role\":\"user\",\"content\":\"${prompt}\"}],\"max_tokens\":64}" \
      "${EDGE_BASE_URL}/v1/chat/completions" 2>/dev/null)"; then
    result="000 0"
  fi
  STATUS="${result%% *}"
  ELAPSED="${result##* }"
}

# The router answers nothing until its model servers are healthy, and a restart (the
# key-injection step recreates it, an upgrade, a model-server retry) leaves the published
# port refusing connections for a while. Every later step would then report HTTP 000,
# which is not a server status at all. Wait for readiness first and say what is happening
# instead of printing four connection failures.
wait_for_router() {
  local limit="${EDGE_DEMO_READY_TIMEOUT:-300}" waited=0
  until curl -fsS -o /dev/null --max-time 2 "${EDGE_BASE_URL}/readyz" 2>/dev/null; do
    if [ "$waited" -ge "$limit" ]; then
      # A failure, never a skip: an unready router means the demo proved nothing.
      fail "NOT READY: router at ${EDGE_BASE_URL} did not answer /readyz within ${limit}s"
      note "the model servers may still be loading: check \`docker compose ps\` and \`logs router\`; raise EDGE_DEMO_READY_TIMEOUT only if they are still loading"
      return 1
    fi
    if [ "$waited" = 0 ]; then
      note "router not ready yet at ${EDGE_BASE_URL}; waiting for /readyz (up to ${limit}s)"
    fi
    sleep 2
    waited=$((waited + 2))
  done
  if [ "$waited" -gt 0 ]; then
    pass "router became ready after ${waited}s"
  fi
  return 0
}

DEMO_TOKEN="$(tr -d '\n' <"$EDGE_DEMO_TOKEN_FILE")"
BUDGET_TOKEN="$(tr -d '\n' <"$EDGE_BUDGET_TOKEN_FILE")"

echo "Edge bundle demo"
echo "  router        ${EDGE_BASE_URL}"
echo "  mode          ${EDGE_MODE} (${EDGE_ACCELERATOR} model servers)"
echo "  escalation    ${EDGE_ESCALATION_BASE_URL} model=${EDGE_ESCALATION_MODEL}"
echo

# 1. local-first -------------------------------------------------------------
echo "1. local-first answer"
if ! wait_for_router; then
  echo
  echo "demo: NOT READY - the router never answered /readyz, so no step was attempted"
  echo "demo: 1 step(s) failed"
  exit 1
fi
ask "$DEMO_TOKEN" local-chat "In one sentence, what is a model group?"
if [ "$STATUS" = "200" ]; then
  model="$(grep -o '"model": *"[^"]*"' "$BODY_FILE" | head -1 | sed 's/.*: *"//; s/"$//')"
  pass "local-chat answered in ${ELAPSED}s (upstream model: ${model:-unreported})"
  note "the answer came from the box; the usage row groups it under local-chat"
else
  fail "local-chat returned HTTP ${STATUS}: $(head -c 300 "$BODY_FILE")"
fi
ask "$DEMO_TOKEN" local-tiny "Reply with the single word ok."
if [ "$STATUS" = "200" ]; then
  pass "local-tiny answered in ${ELAPSED}s"
else
  fail "local-tiny returned HTTP ${STATUS}: $(head -c 300 "$BODY_FILE")"
fi
echo

# 2. escalation --------------------------------------------------------------
if [ "${EDGE_MODE:-docker}" = "native" ]; then
  echo "2. escalation group (native mode: answered by a loopback mock, not a provider)"
else
  echo "2. escalation off the box"
fi
ask "$DEMO_TOKEN" escalate "In one sentence, when should a task leave a local model?"
if [ "$STATUS" = "200" ]; then
  pass "escalate answered in ${ELAPSED}s from ${EDGE_ESCALATION_BASE_URL}"
  if [ "${EDGE_MODE:-docker}" = "native" ]; then
    note "native mode substitutes a loopback mock for escalation: routing and reporting are proven, a provider call is not"
  else
    note "reports keep this as its own group, so off-box spend is separable"
  fi
elif printf '%s' "$(head -c 400 "$BODY_FILE")" | grep -qi 'upstream'; then
  # The router reports a provider-side refusal (missing key, rejected key,
  # unreachable endpoint) as an upstream error. That is the fail-closed path: the
  # request did not quietly turn into a local answer.
  skip "escalate failed closed with HTTP ${STATUS}: $(head -c 160 "$BODY_FILE")"
  # Say which case this is: a missing key is the operator's next step, a rejected request
  # with the key already set is a provider/model mismatch, and telling them to set the
  # variable again sends them in circles.
  if grep -q "^${EDGE_ESCALATION_KEY_ENV}='\?[^']" "$EDGE_ENV_FILE" 2>/dev/null; then
    # Name what the provider actually answered, with the model that was tried: a 400 on a
    # model the key cannot use and a 401 on a bad key need different fixes.
    note "${EDGE_ESCALATION_KEY_ENV} is set in ${EDGE_ENV_FILE}; the provider answered HTTP ${STATUS} for model ${EDGE_ESCALATION_MODEL} at ${EDGE_ESCALATION_BASE_URL}"
    note "if that model is not available to this key, install with --escalation-model/--escalation-base-url or edit providers.escalate in the rendered config and recreate the router"
  else
    note "set ${EDGE_ESCALATION_KEY_ENV} in ${EDGE_ENV_FILE} and restart the router to demo a real escalation"
  fi
else
  fail "escalate returned HTTP ${STATUS}: $(head -c 300 "$BODY_FILE")"
fi
echo

# 3. budget hit --------------------------------------------------------------
echo "3. budget hit on the demo-budget caller"
hit=0
attempt=0
while [ "$attempt" -lt 8 ]; do
  attempt=$((attempt + 1))
  ask "$BUDGET_TOKEN" local-tiny "Reply with the single word ok."
  if [ "$STATUS" = "429" ]; then
    if grep -q 'quota' "$BODY_FILE"; then
      pass "request ${attempt}: HTTP 429 quota-exhausted once the day request budget was spent"
    else
      pass "request ${attempt}: HTTP 429 rate limited: $(head -c 160 "$BODY_FILE")"
    fi
    hit=1
    break
  fi
  if [ "$STATUS" != "200" ]; then
    fail "request ${attempt}: unexpected HTTP ${STATUS}: $(head -c 200 "$BODY_FILE")"
    break
  fi
done
if [ "$hit" = "0" ]; then
  fail "no budget rejection after ${attempt} requests; check the demo-budget caller quota in ${EDGE_CONFIG}"
fi
echo

# 4. spend report ------------------------------------------------------------
echo "4. spend report from the router's usage rows"
report_file="$EDGE_DATA_DIR/logs/usage-summary.md"
usage_err="$EDGE_DATA_DIR/logs/usage-summary.err"
report=""
attempt=0
# Usage rows are written by a best-effort asynchronous writer, so the report can
# trail the requests by a moment. Retry briefly rather than reading a half-written
# window; if the group rows still have not appeared, say so instead of passing.
while [ "$attempt" -lt 5 ]; do
  attempt=$((attempt + 1))
  if ! routerctl usage summary --config "$(router_config_path)" --since 1h >"$report_file" 2>"$usage_err"; then
    fail "usage summary failed: $(head -c 300 "$usage_err" 2>/dev/null)"
    report=""
    break
  fi
  report="$(tr -d '\000' <"$report_file" | sed -n '/^# /,$p' | grep -v -E 'migrator\.go|\[[0-9.]+ms\]' || true)"
  if printf '%s' "$report" | grep -qi 'local-chat'; then
    break
  fi
  sleep 1
done
if [ -n "$report" ]; then
  printf '%s\n' "$report" | head -40 | sed 's/^/      /'
  if printf '%s' "$report" | grep -qi 'local-chat'; then
    pass "report lists the local-chat group with token counts after ${attempt} attempt(s)"
  else
    skip "report generated but had no local-chat group rows after ${attempt} attempt(s); see ${report_file}"
  fi
  if [ "${EDGE_MODE:-docker}" = "native" ]; then
    note "on-box groups carry zero price by default; token counts here come from mocks, not from model inference"
  else
    note "on-box groups carry zero price by default: the GPU is already paid for"
  fi
  note "escalate rows carry the contract price from ${EDGE_CONFIG}"
fi
echo

if [ "$FAILURES" -gt 0 ]; then
  echo "demo: ${FAILURES} step(s) failed"
  exit 1
fi
echo "demo: every step passed or skipped with a reason"
