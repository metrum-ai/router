#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -Eeuo pipefail

# Run INSIDE the pinned official vLLM container on a fresh dedicated four-GPU
# host. GPU selection is non-secret. Never pass credentials in these commands.
mode=${1:-}; root=${2:-}
[[ $mode == experts || $mode == baseline ]] || { echo 'usage: serve-in-container.sh experts|baseline /absolute/model-root' >&2; exit 2; }
[[ $root == /* && $root =~ ^/[A-Za-z0-9_./-]+$ && -d $root ]] || { echo 'invalid model root' >&2; exit 2; }
root=$(realpath "$root"); [[ $root =~ ^/[A-Za-z0-9_./-]+$ ]] || exit 2
for dir in medical-base medical-adapter finance math coding general; do [[ -d "$root/$dir" ]] || { echo "missing $dir" >&2; exit 2; }; done
pids=()
cleanup() {
  trap - EXIT INT TERM HUP
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP
start() {
  local gpu=$1 port=$2 model=$3 served=$4; shift 4
  CUDA_VISIBLE_DEVICES="$gpu" vllm serve "$root/$model" --served-model-name "$served" \
    --host 127.0.0.1 --port "$port" --max-model-len 8192 "$@" &
  pids+=("$!")
}
if [[ $mode == experts ]]; then
  start 0 8001 medical-base biomedical-base --enable-lora --max-lora-rank 64 --lora-modules "biomedical-ft=$root/medical-adapter"
  start 1 8002 finance finance-ft
  start 2 8003 math math-ft
  start 3 8004 coding coding-ft
else
  start '0,1,2,3' 8000 general general-base --tensor-parallel-size 4
fi
printf '%s server processes started; verify readiness before measurement\n' "$mode"
# If any server stops, tear down the entire arm; no silent partial expert set.
wait -n "${pids[@]}"
echo 'server exited unexpectedly' >&2
exit 1
