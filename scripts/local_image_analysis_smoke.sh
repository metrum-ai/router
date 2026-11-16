#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
work=$(mktemp -d)
port=${VLM_SMOKE_PORT:-18091}
token=${VLM_SMOKE_TOKEN:-rtr_metrum_local_vlm_smoke_k1_secret}
receipt=https://cdn.learnopencv.com/wp-content/uploads/2018/06/04100007/receipt.png
pid=""
cleanup() { [ -n "$pid" ] && kill "$pid" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT

for key in OPENAI_API_KEY XAI_API_KEY OPENROUTER_API_KEY MINIMAX_API_KEY; do
  test -n "${!key:-}" || { echo "$key is required" >&2; exit 2; }
done
token_meta=$(printf %s "$token" | python3 scripts/caller_token.py)
hash=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["token_hash"])' <<<"$token_meta")
token_id=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["token_id"])' <<<"$token_meta")
cat >"$work/config.yaml" <<YAML
server:
  listen: ":${port}"
  cache: {enabled: false}
providers:
  openai: {base_url: https://api.openai.com/v1, dialect: openai-responses, api_key: \${OPENAI_API_KEY}, models: {gpt54: {model: gpt-5.4, input_modalities: [text, image], tool_support: {openai_responses: [function]}}}}
  xai: {base_url: https://api.x.ai/v1, dialect: openai-responses, api_key: \${XAI_API_KEY}, models: {grok45: {model: grok-4.5, input_modalities: [text, image], tool_support: {openai_responses: [function]}}}}
  openrouter: {base_url: https://openrouter.ai/api/v1, dialect: openai-responses, api_key: \${OPENROUTER_API_KEY}, models: {claude46: {model: anthropic/claude-sonnet-4.6, input_modalities: [text, image], tool_support: {openai_responses: [function]}}}}
  minimax: {base_url: https://api.minimax.io/v1, dialect: openai-responses, api_key: \${MINIMAX_API_KEY}, models: {m3: {model: MiniMax-M3, input_modalities: [text, image], tool_support: {openai_responses: [function]}}}}
models:
  image-analysis-smoke-gpt54: {strategy: static, targets: [{provider: openai, model_ref: gpt54, request_shape_support: {required_input_modalities: [image], supported_inbound_dialects: [openai-responses], min_requested_output_tokens: 512}}]}
  image-analysis-smoke-grok45: {strategy: static, targets: [{provider: xai, model_ref: grok45, request_shape_support: {required_input_modalities: [image], supported_inbound_dialects: [openai-responses], min_requested_output_tokens: 512}}]}
  image-analysis-smoke-claude46: {strategy: static, targets: [{provider: openrouter, model_ref: claude46, request_shape_support: {required_input_modalities: [image], supported_inbound_dialects: [openai-responses], min_requested_output_tokens: 512}}]}
  image-analysis-smoke-minimax-m3: {strategy: static, targets: [{provider: minimax, model_ref: m3, request_shape_support: {required_input_modalities: [image], supported_inbound_dialects: [openai-responses], min_requested_output_tokens: 512}}]}
callers:
  - id: local-vlm-smoke
    token_hash: "$hash"
    token_id: "$token_id"
    allow: [image-analysis-smoke-gpt54, image-analysis-smoke-grok45, image-analysis-smoke-claude46, image-analysis-smoke-minimax-m3]
YAML
go run -tags dev_no_license ./cmd/metrum-ai-router -config "$work/config.yaml" >"$work/router.log" 2>&1 & pid=$!
for _ in $(seq 1 25); do curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null && break; sleep 1; done
curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null
for group in image-analysis-smoke-gpt54 image-analysis-smoke-grok45 image-analysis-smoke-claude46 image-analysis-smoke-minimax-m3; do
  jq -nc --arg model "$group" --arg image "$receipt" '{model:$model,input:[{role:"user",content:[{type:"input_text",text:"Synthetic VLM acceptance test. Read the receipt and call receipt_result with its merchant."},{type:"input_image",image_url:$image}]}],tools:[{type:"function",name:"receipt_result",parameters:{type:"object",properties:{merchant:{type:"string"}},required:["merchant"]}}],tool_choice:{type:"function",name:"receipt_result"},max_output_tokens:512}' >"$work/request.json"
  code=$(curl -sS --max-time 180 -o "$work/$group.json" -w '%{http_code}' "http://127.0.0.1:$port/v1/responses" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' --data-binary @"$work/request.json" || true)
  tool=$(jq -r '[.output[]? | select(.type == "function_call" and .name == "receipt_result")] | length > 0' "$work/$group.json" 2>/dev/null || echo false)
  merchant=$(jq -r '[.output[]? | select(.type == "function_call") | .arguments] | join(" ") | test("rite aid"; "i")' "$work/$group.json" 2>/dev/null || echo false)
  printf '%s http=%s function_call=%s rite_aid=%s\n' "$group" "$code" "$tool" "$merchant"
  jq '.stream = true' "$work/request.json" >"$work/stream.json"
  stream_code=$(curl -sS -N --max-time 240 -o "$work/$group.sse" -w '%{http_code}' "http://127.0.0.1:$port/v1/responses" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' --data-binary @"$work/stream.json" || true)
  stream_tool=$(grep -q 'receipt_result' "$work/$group.sse" && echo true || echo false)
  stream_done=$(grep -q 'response.completed\|\[DONE\]' "$work/$group.sse" && echo true || echo false)
  printf '%s stream_http=%s function_call=%s completed=%s\n' "$group" "$stream_code" "$stream_tool" "$stream_done"
done
