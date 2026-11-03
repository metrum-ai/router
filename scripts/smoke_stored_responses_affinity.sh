#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
#
# Bounded live smoke for owner-bound stored Responses affinity (#533).
#
# Requires OPENAI_API_KEY. Builds (or uses) the router, starts it with a
# disposable SQLite usage DB and affinity enabled for one model group, then
# exercises create, get, input items, previous_response_id continuation, a
# streamed create, a streamed get, cancel, delete and the after-delete 404.
# Prints statuses and the redacted local metadata; stops the router on exit.
#
# Usage: OPENAI_API_KEY=... [ROUTER_BIN=./metrum-ai-router] [MODEL=gpt-6-luna] \
#          bash scripts/smoke_stored_responses_affinity.sh [workdir]
#
# The caller token and affinity key are generated locally and never printed.
set -euo pipefail

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY is required" >&2
  exit 2
fi
MODEL="${MODEL:-gpt-6-luna}"
PORT="${PORT:-18080}"
WORKDIR="${1:-$(mktemp -d)}"
mkdir -p "$WORKDIR"
BIN="${ROUTER_BIN:-}"
if [[ -z "$BIN" ]]; then
  BIN="$WORKDIR/metrum-ai-router"
  (cd "$(git rev-parse --show-toplevel)" && go build -o "$BIN" ./cmd/metrum-ai-router)
fi

TOKEN="$(openssl rand -hex 16)"
TOKEN_SHA="$(printf '%s' "$TOKEN" | sha256sum | cut -d' ' -f1)"
AFFINITY_SMOKE_KEY="$(openssl rand -hex 32)"
export AFFINITY_SMOKE_KEY
python3 - "$WORKDIR/config.yaml" "$PORT" "$MODEL" "$TOKEN_SHA" <<'PY'
import sys
path, port, model, token_sha = sys.argv[1:5]
open(path, "w").write(f"""server:
  listen: 127.0.0.1:{port}
  default_model_group: live
  identifiers:
    mode: passthrough
  usage_db:
    enabled: true
    driver: sqlite
    path: {path.rsplit('/', 1)[0]}/usage.sqlite
    migration_policy: auto-safe
  retention:
    enabled: true
    dry_run: true
  responses_affinity:
    enabled_groups: [live]
    key_env: AFFINITY_SMOKE_KEY
    key_id: smoke-1
    retention_days: 1
providers:
  openai:
    base_url: https://api.openai.com/v1
    dialect: openai-responses
    api_key: ${{OPENAI_API_KEY}}
    key_id: openai-smoke
    models:
      {model}:
        model: {model}
models:
  live:
    strategy: static
    targets:
      - provider: openai
        model: {model}
callers:
  - id: smoke
    owner_user: smoke
    project: affinity-smoke
    environment: test
    status: active
    token_sha256: {token_sha}
    token_id: rtr_smoke
    allow: [live]
    rate: {{rpm: 60, tpm: 100000, concurrent: 2}}
""")
PY

"$BIN" --config "$WORKDIR/config.yaml" >"$WORKDIR/router.log" 2>&1 &
ROUTER_PID=$!
cleanup() {
  kill "$ROUTER_PID" >/dev/null 2>&1 || true
  wait "$ROUTER_PID" >/dev/null 2>&1 || true
}
trap cleanup EXIT
for _ in $(seq 1 60); do
  if curl -fsS -o /dev/null "http://127.0.0.1:$PORT/readyz" 2>/dev/null; then break; fi
  sleep 1
done

SMOKE_CALLER_TOKEN="$TOKEN" python3 - "$PORT" "$WORKDIR/evidence" <<'PY'
import json, os, pathlib, sys, urllib.error, urllib.request
port, out = sys.argv[1], pathlib.Path(sys.argv[2])
token = os.environ["SMOKE_CALLER_TOKEN"]
out.mkdir(parents=True, exist_ok=True)
base = f"http://127.0.0.1:{port}"
def call(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("Authorization", "Bearer " + token)
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()

status, raw = call("POST", "/v1/responses", {"model": "live", "input": "Reply with the single word: affinity.", "store": True, "max_output_tokens": 256})
(out / "01-create.json").write_text(raw)
print("create", status)
first = json.loads(raw); rid = first.get("id"); print("id", rid, "usage", first.get("usage"))
status, raw = call("GET", "/v1/responses/" + rid); (out / "02-get.json").write_text(raw); print("get", status)
status, raw = call("GET", "/v1/responses/" + rid + "/input_items"); (out / "03-input_items.json").write_text(raw); print("input_items", status)
status, raw = call("GET", "/v1/responses/" + rid + "?stream=true"); (out / "04-stream-get.sse").write_text(raw)
print("stream get", status, "(providers allow this only for background responses)")
status, raw = call("POST", "/v1/responses", {"model": "live", "input": "Now reply with the single word: pinned.", "previous_response_id": rid, "store": True, "max_output_tokens": 256})
(out / "05-continuation.json").write_text(raw); print("continuation", status)
status, raw = call("POST", "/v1/responses", {"model": "live", "input": "Stream a short greeting.", "stream": True, "store": True, "max_output_tokens": 256})
(out / "06-stream.sse").write_text(raw); print("stream create", status, "created", "response.created" in raw, "completed", "response.completed" in raw)
status, raw = call("POST", "/v1/responses/" + rid + "/cancel"); (out / "07-cancel.json").write_text(raw); print("cancel", status)
status, raw = call("DELETE", "/v1/responses/" + rid); (out / "08-delete.json").write_text(raw); print("delete", status)
status, raw = call("GET", "/v1/responses/" + rid); (out / "09-after-delete.json").write_text(raw); print("after delete", status)
PY

python3 - "$WORKDIR/usage.sqlite" "$WORKDIR/evidence/10-rows.json" <<'PY'
import json, sqlite3, sys
path, out = sys.argv[1], sys.argv[2]
db = sqlite3.connect(path); db.row_factory = sqlite3.Row
rows = []
for r in db.execute("select id,tenant,project,environment,provider,account_ref,model,model_group,provider_dialect,state,upstream_delete_state,created_at,expires_at from response_affinity order by id"):
    rows.append(dict(r))
open(out, "w").write(json.dumps(rows, indent=2))
for r in rows:
    print("row", r["id"], r["state"], r["upstream_delete_state"] or "-")
PY
echo "evidence: $WORKDIR/evidence"
