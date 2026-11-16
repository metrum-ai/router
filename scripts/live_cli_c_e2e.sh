#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
WORKDIR="${LIVE_E2E_WORKDIR:-$(mktemp -d)}"
PORT="${ROUTER_PORT:-18080}"
BASE_URL="http://127.0.0.1:${PORT}"
TOKEN="${ROUTER_TOKEN:-rtr_metrum_live_c_e2e_k1_localsecret}"
TIMEOUT_SECONDS="${CLI_TIMEOUT_SECONDS:-240}"
CLAUDE_BIN="${CLAUDE_BIN:-claude}"
CODEX_BIN="${CODEX_BIN:-codex}"
CONTINUE_ON_ERROR="${LIVE_E2E_CONTINUE_ON_ERROR:-1}"
CASE_REGEX="${LIVE_E2E_CASE_REGEX:-}"
C_SANDBOX_IMAGE="${LIVE_E2E_C_SANDBOX_IMAGE:-gcc:14-bookworm}"
C_SANDBOX_DRY_RUN="${LIVE_E2E_C_SANDBOX_DRY_RUN:-0}"
CURRENT_ROUTER_PID=""
FAILURES=()

KEEP_WORKDIR="${KEEP_LIVE_E2E_WORKDIR:-0}"

cleanup() {
  if [[ -n "${CURRENT_ROUTER_PID:-}" ]]; then
    kill "$CURRENT_ROUTER_PID" >/dev/null 2>&1 || true
    wait "$CURRENT_ROUTER_PID" >/dev/null 2>&1 || true
    CURRENT_ROUTER_PID=""
  fi
  if [[ "$KEEP_WORKDIR" != "1" ]]; then
    rm -rf "$WORKDIR"
  fi
}
trap cleanup EXIT

stop_router() {
  if [[ -n "${CURRENT_ROUTER_PID:-}" ]]; then
    kill "$CURRENT_ROUTER_PID" >/dev/null 2>&1 || true
    wait "$CURRENT_ROUTER_PID" >/dev/null 2>&1 || true
    CURRENT_ROUTER_PID=""
  fi
}

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

hash_token() {
  python3 - "$TOKEN" <<'PY'
import hashlib
import sys
print(hashlib.sha256(sys.argv[1].encode()).hexdigest())
PY
}

write_config() {
  local group="$1"
  local provider="$2"
  local model="$3"
  local dialect="$4"
  local base_url="$5"
  local auth_scheme="${6:-}"
  local token_meta token_hash token_id
  token_meta="$(printf %s "$TOKEN" | python3 scripts/caller_token.py)"
  token_hash="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["token_hash"])' <<<"$token_meta")"
  token_id="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["token_id"])' <<<"$token_meta")"

  cat >"$WORKDIR/${group}.yaml" <<YAML
server:
  listen: ":${PORT}"
  cache: { enabled: false }
  logging:
    path: ${WORKDIR}/${group}.jsonl
state_path: ${WORKDIR}/${group}-state.json
providers:
  ${provider}:
    base_url: ${base_url}
    dialect: ${dialect}
    auth_scheme: ${auth_scheme}
    api_key: \${OPENROUTER_API_KEY}
    api_key_env: OPENROUTER_API_KEY
    key_id: ${provider}-live
models:
  ${group}:
    strategy: static
    targets:
      - { provider: ${provider}, model: "${model}" }
callers:
  - id: live-e2e
    token_hash: "${token_hash}"
    token_id: "${token_id}"
    allow: ["${group}"]
    rate: { rpm: 120, tpm: 200000, concurrent: 2 }
    quota:
      day: { requests: 1000, tokens: 2000000 }
      month: { tokens: 10000000 }
    key: { lifetime_tokens: 10000000, soft_pct: 90, on_exhaust: disable }
YAML
}

wait_ready() {
  for _ in $(seq 1 80); do
    if curl -fsS "${BASE_URL}/readyz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  return 1
}

openrouter_key() {
  if [[ -n "${OPENROUTER_API_KEY:-}" ]]; then
    printf '%s' "$OPENROUTER_API_KEY"
    return 0
  fi
  python3 - "$ROOT/env.json" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
if not path.exists():
    raise SystemExit(1)
print(json.loads(path.read_text()).get("OPENROUTER_API_KEY", ""), end="")
PY
}

preflight_openrouter() {
  local group="$1"
  local model="$2"
  local dialect="$3"
  local key
  key="$(openrouter_key)"
  if [[ -z "$key" ]]; then
    echo "${group}: missing OPENROUTER_API_KEY" >&2
    return 1
  fi
  local body_file="$WORKDIR/${group}-preflight.json"
  local code
  if [[ "$dialect" == "anthropic" ]]; then
    code="$(curl -sS -o "$body_file" -w '%{http_code}' \
      https://openrouter.ai/api/v1/messages \
      -H "Authorization: Bearer ${key}" \
      -H "Anthropic-Version: 2023-06-01" \
      -H "Content-Type: application/json" \
      -d "{\"model\":\"${model}\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":\"Reply ok\"}]}")"
  else
    code="$(curl -sS -o "$body_file" -w '%{http_code}' \
      https://openrouter.ai/api/v1/chat/completions \
      -H "Authorization: Bearer ${key}" \
      -H "Content-Type: application/json" \
      -d "{\"model\":\"${model}\",\"max_tokens\":8,\"stream\":false,\"messages\":[{\"role\":\"user\",\"content\":\"Reply ok\"}]}")"
  fi
  if [[ "$code" != 2* ]]; then
    local message
    message="$(python3 - "$body_file" <<'PY'
import json
import sys
from pathlib import Path

try:
    data = json.loads(Path(sys.argv[1]).read_text(errors="replace"))
    print(data.get("error", {}).get("message") or data.get("message") or data)
except Exception:
    print(Path(sys.argv[1]).read_text(errors="replace")[:300])
PY
)"
    echo "${group}: OpenRouter preflight failed HTTP ${code}: ${message}" >&2
    return 1
  fi
}

extract_c_candidates() {
  local input="$1"
  local output_dir="$2"
  rm -rf "$output_dir"
  mkdir -p "$output_dir"
  python3 - "$input" "$output_dir" <<'PY'
import re
import sys
from pathlib import Path

text = Path(sys.argv[1]).read_text(errors="replace")
out = Path(sys.argv[2])
blocks = re.findall(r"```(?:c|C)?\s*(.*?)```", text, re.S)
sources = blocks + [text]
seen = set()
idx = 0
for source in sources:
    starts = [m.start() for m in re.finditer(r"#\s*include\b", source)]
    if not starts:
        starts = [source.find("#include")] if "#include" in source else [0]
    for pos_i, start in enumerate(starts):
        if start < 0:
            continue
        ends = starts[pos_i + 1:] + [len(source)]
        for end in ends:
            candidate = source[start:end].strip()
            if not candidate or "main" not in candidate:
                continue
            key = re.sub(r"\s+", " ", candidate)
            if key in seen:
                continue
            seen.add(key)
            idx += 1
            (out / f"candidate-{idx}.c").write_text(candidate + "\n")
if idx == 0:
    cleaned = text.strip()
    if cleaned:
        (out / "candidate-1.c").write_text(cleaned + "\n")
PY
}

run_claude() {
  local group="$1"
  local prompt="$2"
  env -u ANTHROPIC_API_KEY \
    ANTHROPIC_BASE_URL="$BASE_URL" \
    ANTHROPIC_AUTH_TOKEN="$TOKEN" \
    ANTHROPIC_MODEL="$group" \
    timeout "$TIMEOUT_SECONDS" "$CLAUDE_BIN" --bare --print --model "$group" "$prompt" </dev/null
}

run_codex() {
  local group="$1"
  local prompt="$2"
  local codex_dir="$WORKDIR/${group}-codex-work"
  local final_message="$WORKDIR/${group}-codex-final.txt"
  mkdir -p "$codex_dir"
  METRUM_ROUTER_KEY="$TOKEN" \
    timeout "$TIMEOUT_SECONDS" "$CODEX_BIN" exec --ignore-user-config --ephemeral \
      --ignore-rules \
      --skip-git-repo-check \
      -C "$codex_dir" \
      -o "$final_message" \
      -c "model=\"${group}\"" \
      -c 'model_provider="metrum-ai-router"' \
      -c 'model_providers.metrum-ai-router.name="Metrum AI Router"' \
      -c "model_providers.metrum-ai-router.base_url=\"${BASE_URL}/v1\"" \
      -c 'model_providers.metrum-ai-router.env_key="METRUM_ROUTER_KEY"' \
      -c 'model_providers.metrum-ai-router.wire_api="responses"' \
      "$prompt" </dev/null >&2
  cat "$final_message"
}

compile_and_run() {
  local tool="$1"
  local group="$2"
  local output="$3"
  local candidates_dir="$WORKDIR/${group}-${tool}-candidates"
  local run_file="$WORKDIR/${group}-${tool}.run.txt"
  extract_c_candidates "$output" "$candidates_dir"
  : >"$run_file"
  local c_file
  for c_file in "$candidates_dir"/*.c; do
    [[ -f "$c_file" ]] || continue
    if sandbox_compile_and_run "$c_file" "$run_file" "${c_file}.compile.err" "${c_file}.run.err"; then
      if grep -qi "router c e2e" "$run_file"; then
        cp "$c_file" "$WORKDIR/${group}-${tool}.c"
        cat "$run_file"
        return 0
      fi
    fi
  done
  echo "${group}/${tool}: no extracted C candidate compiled and printed expected output" >&2
  return 1
}

sandbox_compile_and_run() {
  local c_file="$1"
  local run_file="$2"
  local compile_err="$3"
  local run_err="$4"
  local candidate_name
  candidate_name="$(basename "$c_file")"
  local candidate_dir
  candidate_dir="$(cd "$(dirname "$c_file")" && pwd)"

  if ! command -v docker >/dev/null 2>&1; then
    echo "docker is required for sandboxed generated-C validation" >&2
    return 2
  fi

  local docker_cmd=(
    docker run --rm
    --network none
    --cpus 1
    --memory 128m
    --pids-limit 64
    --read-only
    --cap-drop ALL
    --security-opt no-new-privileges
    --tmpfs /tmp:rw,nosuid,nodev,size=32m
    --mount "type=bind,source=${candidate_dir},target=/work,readonly"
    -w /work
    "$C_SANDBOX_IMAGE"
    sh -ceu
    'cc -std=c11 -Wall -Wextra -Werror -O2 "$1" -o /tmp/candidate 2>/tmp/compile.err || { cat /tmp/compile.err >&2; exit 10; }; timeout 5 /tmp/candidate'
    sh
    "$candidate_name"
  )

  if [[ "$C_SANDBOX_DRY_RUN" == "1" ]]; then
    printf '%q ' "${docker_cmd[@]}" >"$run_file"
    printf '\n' >>"$run_file"
    return 0
  fi

  if "${docker_cmd[@]}" >"$run_file" 2>"$run_err"; then
    : >"$compile_err"
    return 0
  fi
  cp "$run_err" "$compile_err"
  return 1
}

run_case() {
  local group="$1"
  local provider="$2"
  local model="$3"
  local dialect="$4"
  local base_url="$5"
  local auth_scheme="${6:-}"
  local prompt="Your final answer must be only C source code starting with #include <stdio.h>. Do not use markdown. Do not run the program. Do not return the program output. The program must compile with cc -std=c11 -Wall -Wextra -Werror and print exactly: router c e2e ${group}"

  echo "== ${group} :: preflight :: ${model}"
  preflight_openrouter "$group" "$model" "$dialect" || return 1

  write_config "$group" "$provider" "$model" "$dialect" "$base_url" "$auth_scheme"
  "$ROOT/router" --config "$WORKDIR/${group}.yaml" >"$WORKDIR/${group}-router.log" 2>&1 &
  CURRENT_ROUTER_PID=$!
  wait_ready
  local status=0

  echo "== ${group} :: claude :: ${model}"
  run_claude "$group" "$prompt" >"$WORKDIR/${group}-claude.out" 2>"$WORKDIR/${group}-claude.err" || status=1
  if [[ "$status" == "0" ]]; then
    compile_and_run claude "$group" "$WORKDIR/${group}-claude.out" || status=1
  fi

  echo "== ${group} :: codex :: ${model}"
  if [[ "$status" == "0" ]]; then
    run_codex "$group" "$prompt" >"$WORKDIR/${group}-codex.out" 2>"$WORKDIR/${group}-codex.err" || status=1
  fi
  if [[ "$status" == "0" ]]; then
    compile_and_run codex "$group" "$WORKDIR/${group}-codex.out" || status=1
  fi

  stop_router
  return "$status"
}

if [[ ! -x "$ROOT/router" ]]; then
  (cd "$ROOT" && go build -o metrum-ai-router ./cmd/metrum-ai-router)
fi

if [[ ! -f "$ROOT/env.json" && -z "${OPENROUTER_API_KEY:-}" ]]; then
  echo "OPENROUTER_API_KEY must be present in env.json or the environment" >&2
  exit 2
fi

CASES=(
  "or-deepseek-v4-flash openrouter deepseek/deepseek-v4-flash:nitro openai-chat https://openrouter.ai/api/v1"
  "or-gemma-4-26b openrouter google/gemma-4-26b-a4b-it:nitro openai-chat https://openrouter.ai/api/v1"
)

for case_line in "${CASES[@]}"; do
  if [[ -n "$CASE_REGEX" ]]; then
    case_name="${case_line%% *}"
    if [[ ! "$case_name" =~ $CASE_REGEX ]]; then
      continue
    fi
  fi
  if ! run_case $case_line; then
    # shellcheck disable=SC2086
    set -- $case_line
    FAILURES+=("$1")
    if [[ "$CONTINUE_ON_ERROR" != "1" ]]; then
      break
    fi
  fi
done

if (( ${#FAILURES[@]} > 0 )); then
  printf 'live CLI C e2e failures: %s\n' "${FAILURES[*]}" >&2
  if [[ "$KEEP_WORKDIR" == "1" ]]; then
    printf 'live CLI C e2e workdir: %s\n' "$WORKDIR" >&2
  fi
  exit 1
fi

echo "live CLI C e2e: ok"
