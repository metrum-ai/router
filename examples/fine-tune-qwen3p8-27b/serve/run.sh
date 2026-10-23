#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
# Run on GPU host only. Publish HTTP on host loopback; reach it by SSH tunnel.
: "${MODEL_DIR:?local pinned BF16 snapshot required}"
MODE=${MODE:-lora}
case "$MODE" in base|lora|merged) ;; *) echo 'MODE must be base, lora or merged' >&2; exit 2;; esac
IMAGE=vllm/vllm-openai@sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34
VOLUMES=(-v "$MODEL_DIR:/model:ro")
ARGS=(--model /model --served-model-name qwen3p8-27b-merged)
if [[ "$MODE" == base ]]; then
  ARGS=(--model /model --served-model-name qwen3p8-27b-base)
fi
if [[ "$MODE" == lora ]]; then
  : "${ADAPTER_DIR:?trained rank-8 or rank-16 adapter required}"
  VOLUMES+=(-v "$ADAPTER_DIR:/adapter:ro")
  ARGS=(--model /model --served-model-name qwen3p8-27b-base
        --enable-lora --lora-modules metrum-ft=/adapter --max-lora-rank 16)
fi
if [[ ${DRY_RUN:-1} == 1 ]]; then
  printf '%s\n' "docker image: $IMAGE; mode=$MODE; listen 127.0.0.1:8000"
  exit 0
fi
exec sudo docker run --name metrum-ft-vllm --rm --gpus all --ipc=host \
  -p 127.0.0.1:8000:8000 "${VOLUMES[@]}" "$IMAGE" "${ARGS[@]}" \
  --dtype bfloat16 --max-model-len 8192 --max-num-seqs 32 --max-num-batched-tokens 8192 \
  --gpu-memory-utilization 0.88 --host 0.0.0.0 --port 8000 \
  --default-chat-template-kwargs '{"enable_thinking":false}' \
  --enable-auto-tool-choice --tool-call-parser qwen3_coder
