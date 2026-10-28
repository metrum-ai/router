#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -Eeuo pipefail
# One container per invocation, retaining exited containers for diagnosis.
image='vllm/vllm-openai@sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34'
mode=${1:-}; root=${2:-}
[[ $root == /* && $root =~ ^/[A-Za-z0-9_./-]+$ && -d $root && ! -L $root ]] || { echo 'unsafe model root' >&2; exit 2; }
root=$(realpath "$root"); [[ $root =~ ^/[A-Za-z0-9_./-]+$ ]] || exit 2
extra=()
case "$mode" in
 baseline) gpu=all; port=8000; model=general; served=general-base; extra+=(--tensor-parallel-size 4) ;;
 biomedical) gpu=device=0; port=8001; model=medical-base; served=biomedical-base; extra+=(--enable-lora --max-lora-rank 64 --lora-modules biomedical-ft=/models/medical-adapter) ;;
 finance) gpu=device=1; port=8002; model=finance; served=finance-ft ;;
 math) gpu=device=2; port=8003; model=math; served=math-ft ;;
 coding) gpu=device=3; port=8004; model=coding; served=coding-ft ;;
 *) echo 'usage: serve-one.sh baseline|biomedical|finance|math|coding /absolute/model-root' >&2; exit 2 ;;
esac
[[ -d $root/$model ]] || { echo 'model directory missing' >&2; exit 1; }
[[ $mode != biomedical || -d $root/medical-adapter ]] || { echo 'adapter missing' >&2; exit 1; }
name=proof426-$mode
if docker container inspect "$name" >/dev/null 2>&1; then echo 'container already exists; refusing to replace it' >&2; exit 1; fi
# No --rm: stopped containers remain inspectable until the dedicated VM is deleted.
docker run -d --network host --gpus "$gpu" --name "$name" --label metrum.task=issue-426 \
  --mount "type=bind,src=$root,dst=/models,readonly" "$image" \
  --model "/models/$model" --served-model-name "$served" --host 127.0.0.1 --port "$port" \
  --max-model-len 8192 "${extra[@]}" >/dev/null
printf '%s server started; check readiness before the next server\n' "$mode"
