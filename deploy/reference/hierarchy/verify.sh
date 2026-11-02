#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
#
# Verifies the layered deployment reference (#434) against a running stack.
#
#   ./verify.sh --local     build the shipped binaries, start both tiers and
#                           the demo frontier on loopback, run every check
#   ./verify.sh --compose   check an already running `docker compose` stack
#                           (EDGE_URL / DC_URL / token files from .env)
#
# Checks: readiness, model groups, a real two-hop request, hop-header hygiene
# at the frontier, loop protection (508), edge-cache hit, per-hop usage rows,
# and the cross-hop request-identifier limitation. Prints PASS/FAIL per check
# and exits non-zero if any check fails.
set -euo pipefail

cd "$(dirname "$0")"
HERE="$(pwd)"

MODE=""
case "${1:-}" in
  --local) MODE=local ;;
  --compose) MODE=compose ;;
  *) echo "usage: $0 --local|--compose" >&2; exit 2 ;;
esac

REPO_ROOT="$(cd ../../.. && pwd)"
WORK=""
PIDS=()
FAILURES=0
EDGE_URL="${EDGE_URL:-http://127.0.0.1:8080}"
DC_URL="${DC_URL:-http://127.0.0.1:8081}"
FRONTIER_URL="${FRONTIER_URL:-http://127.0.0.1:9100}"
FRONTIER_MODEL="${FRONTIER_MODEL:-frontier-demo}"

cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
  if [[ -n "$WORK" && "${KEEP:-0}" != "1" ]]; then
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT

pass() { printf 'PASS  %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; FAILURES=$((FAILURES + 1)); }
check() { if eval "$2"; then pass "$1"; else fail "$1${3:+: $3}"; fi; }
note() { printf '      %s\n' "$1"; }

# Evaluate an expression against the JSON document on stdin as `d`.
json_field() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1], {"d": d}))' "$1"; }

# The reference configs use migration_policy: deployment-job, so the usage
# store must pass the reviewed gate before a router may serve: apply the schema,
# complete the shipping data job, then verify the serving postcondition.
migrate_sqlite() {
  local db="$1" log="$2"
  "$WORK/bin/metrum-ai-router-migrate" --action apply --driver sqlite --db "$db" >"$log" 2>&1 || return 1
  "$WORK/bin/metrum-ai-router-migrate" --action resume --job historical-usage-validation-v1 \
    --checkpoint-ordinal 0 --driver sqlite --db "$db" >>"$log" 2>&1 || return 1
  "$WORK/bin/metrum-ai-router-migrate" --action verify-serving --driver sqlite --db "$db" >>"$log" 2>&1 || return 1
}

migrate_compose() {
  local dsn_env="$1" log="$2"
  docker compose --env-file .env run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate migrate \
    --action=apply --driver=postgres --dsn-env="$dsn_env" --scope=usage >"$log" 2>&1 || return 1
  docker compose --env-file .env run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate migrate \
    --action=resume --job=historical-usage-validation-v1 --checkpoint-ordinal=0 \
    --driver=postgres --dsn-env="$dsn_env" --scope=usage >>"$log" 2>&1 || return 1
  docker compose --env-file .env run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate migrate \
    --action=verify-serving --driver=postgres --dsn-env="$dsn_env" --scope=usage >>"$log" 2>&1 || return 1
}

wait_ready() {
  local url="$1" name="$2"
  for _ in $(seq 1 300); do
    if curl -fsS --max-time 2 "$url/readyz" >/dev/null 2>&1; then
      note "$name ready"
      return 0
    fi
    sleep 0.1
  done
  fail "$name did not become ready at $url"
  return 1
}

start_local() {
  WORK="$(mktemp -d)"
  mkdir -p "$WORK/bin" "$WORK/state"
  note "work directory: $WORK"
  for binary in metrum-ai-router metrum-ai-routerctl metrum-ai-router-migrate; do
    if [[ ! -x "$REPO_ROOT/bin/$binary" ]]; then
      ( cd "$REPO_ROOT" && go build -buildvcs=false -o "bin/$binary" "./cmd/$binary" )
    fi
    cp "$REPO_ROOT/bin/$binary" "$WORK/bin/$binary"
  done

  # Three deployment-generated tokens: application -> edge, edge -> dc, dc -> edge.
  for slug in app edge-to-dc dc-to-edge; do
    "$WORK/bin/metrum-ai-routerctl" callers generate \
      --owner-user ops --project edge-prod --env prod --key "$slug" \
      --allow edge-default --allow edge-loop-demo --allow dc-frontier --allow loop-demo \
      --token-out "$WORK/$slug.token" >/dev/null
    chmod 600 "$WORK/$slug.token"
    "$WORK/bin/metrum-ai-routerctl" callers hash --token-file "$WORK/$slug.token" > "$WORK/$slug.hash.json"
  done
  hash_field() {
    python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$WORK/$1.hash.json" "$2"
  }

  export ROUTER_ID_TRANSFORM_KEY="$(openssl rand -hex 64)"
  export ROUTER_ID_TRANSFORM_KEY_ID="ref-$(date -u +%Y%m%d)"
  export EDGE_USAGE_DRIVER=sqlite EDGE_USAGE_PATH="$WORK/state/edge-usage.sqlite" EDGE_USAGE_DSN=""
  export DC_USAGE_DRIVER=sqlite DC_USAGE_PATH="$WORK/state/dc-usage.sqlite" DC_USAGE_DSN=""
  export EDGE_STATE_PATH="$WORK/state/edge-state.json" EDGE_LOG_PATH="$WORK/state/edge-requests.jsonl"
  export DC_STATE_PATH="$WORK/state/dc-state.json" DC_LOG_PATH="$WORK/state/dc-requests.jsonl"
  export MAX_ROUTER_HOPS=4 DC_UPSTREAM_TIMEOUT_MS=480000
  export EDGE_OWNER_USER=ops EDGE_PROJECT=edge-prod EDGE_ENVIRONMENT=prod
  export EDGE_CALLER_ID=app EDGE_CALLER_TOKEN_HASH="$(hash_field app token_hash)" \
         EDGE_CALLER_TOKEN_ID="$(hash_field app token_id)"
  export EDGE_CALLER_RPM=600 EDGE_CALLER_TPM=400000 EDGE_CALLER_CONCURRENT=32
  export EDGE_CALLER_DAY_REQUESTS=0 EDGE_CALLER_DAY_TOKENS=0 EDGE_CALLER_MONTH_TOKENS=0
  export EDGE_LOOPBACK_CALLER_ID=dc-loopback \
         EDGE_LOOPBACK_TOKEN_HASH="$(hash_field dc-to-edge token_hash)" \
         EDGE_LOOPBACK_TOKEN_ID="$(hash_field dc-to-edge token_id)"
  export DC_OWNER_USER=ops DC_PROJECT=dc-prod DC_ENVIRONMENT=prod DC_CALLER_ID=edge-router \
         DC_CALLER_TOKEN_HASH="$(hash_field edge-to-dc token_hash)" \
         DC_CALLER_TOKEN_ID="$(hash_field edge-to-dc token_id)"
  export DC_CALLER_RPM=1200 DC_CALLER_TPM=1000000 DC_CALLER_CONCURRENT=64
  export DC_ROUTER_TOKEN="$(cat "$WORK/edge-to-dc.token")"
  export DC_LOOPBACK_TOKEN="$(cat "$WORK/dc-to-edge.token")"
  export APP_TOKEN="$(cat "$WORK/app.token")"
  export DC_ROUTER_URL="http://127.0.0.1:18081/v1"
  export EDGE_ROUTER_URL="http://127.0.0.1:18080/v1"
  export FRONTIER_BASE_URL="http://127.0.0.1:19100/v1"
  export FRONTIER_API_KEY=demo-no-key FRONTIER_MODEL=frontier-demo
  export FRONTIER_INPUT_PRICE_USD_PER_MTOK=0 FRONTIER_OUTPUT_PRICE_USD_PER_MTOK=0
  export FRONTIER_PRICING_SOURCE=deployment-cost-allocation FRONTIER_PRICING_UPDATED_AT=2026-11-02
  export EDGE_LISTEN=127.0.0.1:18080 DC_LISTEN=127.0.0.1:18081
  EDGE_URL="http://127.0.0.1:18080"
  DC_URL="http://127.0.0.1:18081"
  FRONTIER_URL="http://127.0.0.1:19100"

  migrate_sqlite "$WORK/state/edge-usage.sqlite" "$WORK/state/migrate-edge.log" \
    && migrate_sqlite "$WORK/state/dc-usage.sqlite" "$WORK/state/migrate-dc.log" \
    || { fail "usage migration gate"; tail -n 5 "$WORK/state/migrate-edge.log" "$WORK/state/migrate-dc.log"; }

  python3 demo-frontier/echo.py --port 19100 > "$WORK/state/frontier.log" 2>&1 &
  PIDS+=($!)

  # The reference configs listen on 0.0.0.0:8080 inside their containers; for a
  # local run the listen address is overridden on the command line.
  ( cd "$WORK/state" && exec "$WORK/bin/metrum-ai-router" -config "$HERE/config/dc.yaml" \
      -listen "$DC_LISTEN" > dc.log 2>&1 ) &
  PIDS+=($!)
  wait_ready "$DC_URL" "datacenter tier"
  ( cd "$WORK/state" && exec "$WORK/bin/metrum-ai-router" -config "$HERE/config/edge.yaml" \
      -listen "$EDGE_LISTEN" > edge.log 2>&1 ) &
  PIDS+=($!)
  wait_ready "$EDGE_URL" "edge tier"

  export ACCESS_TOKEN="$APP_TOKEN"
  export EDGE_USAGE_QUERY="python3 $HERE/usage_rows.py --sqlite $EDGE_USAGE_PATH"
  export DC_USAGE_QUERY="python3 $HERE/usage_rows.py --sqlite $DC_USAGE_PATH"
}

start_compose() {
  if [[ -z "${ACCESS_TOKEN:-}" ]]; then
    echo "set ACCESS_TOKEN to the raw application caller token for the running stack" >&2
    exit 2
  fi
  # Complete the reviewed migration gate for both hops before the routers may
  # serve; they fail closed while the data job is pending.
  migrate_compose EDGE_USAGE_DSN "$WORK/migrate-edge.log" \
    && migrate_compose DC_USAGE_DSN "$WORK/migrate-dc.log" \
    || { fail "usage migration gate"; tail -n 5 "$WORK/migrate-edge.log" "$WORK/migrate-dc.log"; }
  wait_ready "$EDGE_URL" "edge tier"
  wait_ready "$DC_URL" "datacenter tier"
  export EDGE_USAGE_QUERY="docker compose --env-file .env exec -T postgres psql -U router -d edge_usage -A -t -F, -c"
  export DC_USAGE_QUERY="docker compose --env-file .env exec -T postgres psql -U router -d dc_usage -A -t -F, -c"
}

# Auth headers go through a mode-0600 curl config, never argv.
auth_header_file() {
  local path="$WORK/auth.curl" token="$1"
  umask 077
  printf 'header = "Authorization: Bearer %s"\n' "$token" > "$path"
  printf '%s' "$path"
}

# temperature 0 is required for the response cache to consider a request at
# all; a streaming request is always bypassed, so cache checks use unary.
chat() {
  local model="$1" stream="$2" auth="$3" temperature="${4:-}"
  local extra=""
  if [[ -n "$temperature" ]]; then extra=",\"temperature\":$temperature"; fi
  curl -sS --max-time 60 -K "$auth" -H 'Content-Type: application/json' \
    -H 'Via: 1.1 evil-client-proxy (should never cross the hop)' \
    -d "{\"model\":\"$model\",\"max_tokens\":16,\"stream\":$stream$extra,\"messages\":[{\"role\":\"user\",\"content\":\"hierarchy check\"}]}" \
    -w '\n%{http_code}' "$EDGE_URL/v1/chat/completions"
}

if [[ "$MODE" == local ]]; then
  start_local
else
  WORK="$(mktemp -d)"; start_compose
fi
AUTH="$(auth_header_file "${ACCESS_TOKEN}")"

echo
echo "== 1. readiness and model groups"
models_json="$(curl -sS --max-time 10 -K "$AUTH" "$EDGE_URL/v1/models")"
check "edge lists the caller's model group" \
  "python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print(any(m[\"id\"]==\"edge-default\" for m in d[\"data\"]))' '$models_json' | grep -q True"

echo
echo "== 2. two-hop request and hop-header hygiene"
curl -sS --max-time 10 -X POST "$FRONTIER_URL/__reset" >/dev/null
chain="$(chat edge-default true "$AUTH")"
chain_status="$(printf '%s' "$chain" | tail -n1)"
chain_body="$(printf '%s' "$chain" | sed '$d')"
check "streamed request through both hops returns 200" "[[ '$chain_status' == 200 ]]"
edge_headers="$(curl -sS -o /dev/null -D - --max-time 30 -K "$AUTH" -H 'Content-Type: application/json' \
  -d '{"model":"edge-default","max_tokens":8,"messages":[{"role":"user","content":"id check"}]}' \
  "$EDGE_URL/v1/chat/completions")"
edge_request_id="$(grep -i '^x-request-id:' <<<"$edge_headers" | tr -d '\r' | cut -d' ' -f2-)"
check "the response carries an edge request id" "[[ -n '$edge_request_id' ]]"
echo_e="$(curl -sS --max-time 10 "$FRONTIER_URL/__echo")"
echo_via="$(json_field 'd["last_headers"].get("via","")' <<<"$echo_e")"
echo_count="$(json_field 'd["count"]' <<<"$echo_e")"
note "frontier observed Via: ${echo_via:-<none>}"
router_entries="$(printf '%s' "$echo_via" | grep -o 'metrum-ai-router' | wc -l | tr -d ' ')"
check "the frontier sees exactly two router hop entries" "[[ '$router_entries' == 2 ]]"
check "a caller-supplied Via entry never crosses the hop" "! grep -qi 'evil-client-proxy' <<<\"\$echo_via\""
frontier_id="$(json_field 'd["last_headers"].get("x-request-id","")' <<<"$echo_e")"
note "edge request id: ${edge_request_id:-<none>}; frontier saw x-request-id: ${frontier_id:-<none>} (cross-hop correlation is not automatic)"

echo
echo "== 3. loop protection"
loop="$(chat edge-loop-demo false "$AUTH")"
loop_status="$(printf '%s' "$loop" | tail -n1)"
loop_body="$(printf '%s' "$loop" | sed '$d')"
check "a request that bounces between the tiers ends as 508" "[[ '$loop_status' == 508 ]]"
check "the 508 names the loop" "grep -q 'loop' <<<\"\$loop_body\""

echo
echo "== 4. caching at the nearest safe level"
curl -sS --max-time 10 -X POST "$FRONTIER_URL/__reset" >/dev/null
first="$(chat edge-default false "$AUTH" 0)"
second="$(chat edge-default false "$AUTH" 0)"
after="$(json_field 'd["count"]' <<<"$(curl -sS --max-time 10 "$FRONTIER_URL/__echo")")"
check "both identical requests succeed" \
  "[[ '$(printf '%s' "$first" | tail -n1)' == 200 && '$(printf '%s' "$second" | tail -n1)' == 200 ]]"
check "the edge served the repeat request without another frontier call" "[[ '$after' == 1 ]]"
note "frontier calls for two identical requests: $after"

echo
echo "== 5. per-hop usage rows"
edge_row="$(eval "$EDGE_USAGE_QUERY \"SELECT request_id || '|' || resolved_group || '|' || status || '|' || latency_ms || '|' || coalesce(ttfb_ms,0) || '|' || total_tokens || '|' || coalesce(input_price_per_million_usd,0) FROM request_usage ORDER BY ts DESC LIMIT 1\"" 2>/dev/null || true)"
dc_row="$(eval "$DC_USAGE_QUERY \"SELECT request_id || '|' || resolved_group || '|' || status || '|' || latency_ms || '|' || coalesce(ttfb_ms,0) || '|' || total_tokens || '|' || coalesce(input_price_per_million_usd,0) FROM request_usage ORDER BY ts DESC LIMIT 1\"" 2>/dev/null || true)"
note "edge usage  request_id|group|status|latency_ms|ttfb_ms|tokens|input_price: ${edge_row:-<none>}"
note "dc usage    request_id|group|status|latency_ms|ttfb_ms|tokens|input_price: ${dc_row:-<none>}"
dc_request_id="${dc_row%%|*}"
note "the two hops record different request ids (${edge_request_id:-<edge>} vs ${dc_request_id:-<dc>}): correlate by caller, group and time window"
check "both hops wrote a usage row for their own work" "[[ -n '$edge_row' && -n '$dc_row' ]]"

echo
if [[ "$FAILURES" -gt 0 ]]; then
  echo "$FAILURES check(s) failed"
  exit 1
fi
echo "all layered-deployment checks passed"
