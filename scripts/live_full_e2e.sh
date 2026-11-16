#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WORKDIR="${LIVE_FULL_E2E_WORKDIR:-$(mktemp -d)}"
PORT="${ROUTER_PORT:-18082}"
BASE_URL="http://127.0.0.1:${PORT}"
TOKEN="${ROUTER_TOKEN:-rtr_metrum_live_full_e2e_k1_localsecret}"
MODEL="${LIVE_FULL_E2E_MODEL:-deepseek/deepseek-v4-flash:nitro}"
GROUP="${LIVE_FULL_E2E_GROUP:-live-cache}"
KEEP_WORKDIR="${KEEP_LIVE_E2E_WORKDIR:-0}"
ROUTER_PID=""

cleanup() {
  if [[ -n "${ROUTER_PID:-}" ]]; then
    kill "$ROUTER_PID" >/dev/null 2>&1 || true
    wait "$ROUTER_PID" >/dev/null 2>&1 || true
  fi
  if [[ "$KEEP_WORKDIR" != "1" ]]; then
    rm -rf "$WORKDIR"
  fi
}
trap cleanup EXIT

mkdir -p "$WORKDIR"

load_project_env() {
  python3 - "$ROOT/env.json" <<'PY'
import json
import shlex
import sys
from pathlib import Path

path = Path(sys.argv[1])
if not path.exists():
    raise SystemExit(0)
data = json.loads(path.read_text())
for key, value in data.items():
    if isinstance(value, str):
        print(f"export {key}={shlex.quote(value)}")
PY
}

eval "$(load_project_env)"

if [[ -z "${OPENROUTER_API_KEY:-}" ]]; then
  echo "OPENROUTER_API_KEY must be present in env.json or environment" >&2
  exit 2
fi

TOKEN_META="$(printf %s "$TOKEN" | python3 scripts/caller_token.py)"
TOKEN_HASH="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["token_hash"])' <<<"$TOKEN_META")"
TOKEN_ID="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["token_id"])' <<<"$TOKEN_META")"
cat >"$WORKDIR/config.yaml" <<YAML
server:
  listen: ":${PORT}"
  cache:
    enabled: true
    max_bytes: 1048576
    default_ttl: 10m
  logging:
    path: ${WORKDIR}/requests.jsonl
state_path: ${WORKDIR}/state.json
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    dialect: openai-chat
    api_key: \${OPENROUTER_API_KEY}
    api_key_env: OPENROUTER_API_KEY
    key_id: openrouter-live-full
models:
  ${GROUP}:
    strategy: static
    targets:
      - { provider: openrouter, model: "${MODEL}" }
callers:
  - id: live-full
    token_hash: "${TOKEN_HASH}"
    token_id: "${TOKEN_ID}"
    allow: ["${GROUP}"]
    rate: { rpm: 120, tpm: 200000, concurrent: 4 }
    quota:
      day: { requests: 1000, tokens: 2000000 }
      month: { tokens: 10000000 }
    key: { lifetime_tokens: 10000000, soft_pct: 90, on_exhaust: disable }
YAML

"$ROOT/router" --config "$WORKDIR/config.yaml" >"$WORKDIR/router.log" 2>&1 &
ROUTER_PID=$!

for _ in $(seq 1 80); do
  if curl -fsS "$BASE_URL/readyz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done
curl -fsS "$BASE_URL/readyz" >/dev/null

post_completion() {
  local out="$1"
  curl -fsS "$BASE_URL/v1/chat/completions" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"${GROUP}\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: router cache e2e\"}],\"temperature\":0}" \
    >"$out"
}

post_completion "$WORKDIR/first.json"
post_completion "$WORKDIR/second.json"

python3 - "$WORKDIR/first.json" "$WORKDIR/second.json" "$WORKDIR/requests.jsonl" "$GROUP" <<'PY'
import json
import sys
from pathlib import Path

first = json.loads(Path(sys.argv[1]).read_text())
second = json.loads(Path(sys.argv[2]).read_text())
logs = [json.loads(line) for line in Path(sys.argv[3]).read_text().splitlines() if line.strip()]
group = sys.argv[4]
if first.get("id") == second.get("id"):
    raise SystemExit(f"cache responses reused id {first.get('id')}")
if not str(first.get("id", "")).startswith("resp_") or not str(second.get("id", "")).startswith("resp_"):
    raise SystemExit(f"router response ids not generated: {first.get('id')} {second.get('id')}")
if "router cache e2e" not in json.dumps(second):
    raise SystemExit("unexpected cached response body")
if [entry.get("cache") for entry in logs if entry.get("requested_model") == group][-2:] != ["miss", "hit"]:
    raise SystemExit(f"expected cache miss/hit logs, got {[entry.get('cache') for entry in logs]}")
PY

bash "$ROOT/scripts/live_cli_c_e2e.sh"

echo "live full e2e: ok"
