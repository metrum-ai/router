#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -Eeuo pipefail

# Run only on the dedicated four-GPU machine after arming teardown and
# verifying public model revisions. No credentials are passed to containers.
IMAGE='vllm/vllm-openai@sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34'
mode=${1:-}; root=${2:-}
[[ $mode == experts || $mode == baseline ]] || { echo 'usage: serve.sh experts|baseline /absolute/model-root' >&2; exit 2; }
[[ $root == /* && $root =~ ^/[A-Za-z0-9_./-]+$ && -d $root ]] || { echo 'model root must be an existing safe absolute path' >&2; exit 2; }
root=$(realpath "$root")
[[ $root =~ ^/[A-Za-z0-9_./-]+$ ]] || { echo 'unsafe resolved model root' >&2; exit 2; }
for dir in medical-base medical-adapter finance math coding general; do
  [[ -d "$root/$dir" ]] || { echo "missing model directory: $dir" >&2; exit 2; }
done
names=(proof426-biomedical proof426-finance proof426-math proof426-coding proof426-baseline)
for name in "${names[@]}"; do
  if docker container inspect "$name" >/dev/null 2>&1; then echo 'benchmark container already exists; refusing to alter it' >&2; exit 1; fi
done
started=()
cleanup_failed() { for name in "${started[@]}"; do docker stop "$name" >/dev/null || true; done; }
trap cleanup_failed ERR
start() {
  local name=$1 gpu=$2 port=$3 model=$4 served=$5; shift 5
  docker run -d --network host --gpus "$gpu" --name "proof426-$name" --label metrum.task=issue-426 \
    --mount "type=bind,src=$root,dst=/models,readonly" "$IMAGE" \
    --model "/models/$model" --served-model-name "$served" --host 127.0.0.1 --port "$port" \
    --max-model-len 8192 "$@" >/dev/null
  started+=("proof426-$name")
}
if [[ $mode == experts ]]; then
  start biomedical 'device=0' 8001 medical-base biomedical-base --enable-lora --max-lora-rank 64 --lora-modules biomedical-ft=/models/medical-adapter
  start finance 'device=1' 8002 finance finance-ft --max-num-seqs 128
  start math 'device=2' 8003 math math-ft --max-num-seqs 128
  start coding 'device=3' 8004 coding coding-ft
else
  start baseline all 8000 general general-base --tensor-parallel-size 4 --max-num-seqs 128
fi
trap - ERR
printf '%s servers started; perform readiness and compatibility checks before measurement\n' "$mode"
