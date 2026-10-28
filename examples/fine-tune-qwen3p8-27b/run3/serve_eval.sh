#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
# On dedicated #425 host; loopback only, no credentials in Docker.
set -Eeuo pipefail
root=${1:?expected workspace}; mode=${2:-final}
args=(); adapter=''
case "$mode" in
  base) name=proof425-base-smoke ;;
  smoke) adapter=smoke-adapter; name=proof425-smoke-eval ;;
  smoke-eager) adapter=smoke-adapter; name=proof425-smoke-eager; args+=(--enforce-eager) ;;
  final) adapter=adapter; name=proof425-eval ;;
  final-eager) adapter=adapter; name=proof425-eval; args+=(--enforce-eager) ;;
  *) echo 'expected base, smoke, smoke-eager, final, or final-eager' >&2; exit 1 ;;
esac
[[ $root =~ ^/[A-Za-z0-9_./-]+$ && -d $root/base ]] || exit 1
if [[ -n $adapter ]]; then
  [[ -f $root/$adapter/adapter_model.safetensors ]] || exit 1
  args+=(--enable-lora --max-lora-rank 8 --lora-modules "metrum-ft=/models/$adapter")
fi
image='vllm/vllm-openai@sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34'
d=(docker); if ! docker info >/dev/null 2>&1; then sudo -n docker info >/dev/null; d=(sudo -n docker); fi
if "${d[@]}" container inspect "$name" >/dev/null 2>&1; then echo 'existing eval container' >&2; exit 1; fi
space=$(df -Pk "$("${d[@]}" info --format '{{.DockerRootDir}}')" | awk 'NR==2 {print $4}')
[[ $space -ge 30000000 ]] || { echo 'less than 30GB free for serving image' >&2; exit 1; }
"${d[@]}" pull "$image"
"${d[@]}" run -d --network host --gpus all --name "$name" --label metrum.task=issue-425 \
  --mount "type=bind,src=$root,dst=/models,readonly" "$image" \
  --model /models/base --served-model-name qwen3p8-27b-base --host 127.0.0.1 --port 18526 \
  --max-model-len 8192 --max-num-seqs 128 "${args[@]}" >/dev/null
printf '%s server started\n' "$mode"
