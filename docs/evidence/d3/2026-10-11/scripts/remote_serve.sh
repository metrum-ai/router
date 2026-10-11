#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
# Metrum AI Router, issue #278: start d3_server.py for one pinned model on
# 127.0.0.1:<port> (never a public interface), with DECISION_API_KEY read from
# ~/d3.key (mode 0600, never printed). Measures cold load (process start to
# /health ready, including the default kernel warmup) and GPU memory after load.
# Usage: remote_serve.sh <d3-lite|d3-nano|d3> <revision> [port] [extra d3_server args]
set -euo pipefail
m="$1"; rev="$2"; port="${3:-8000}"; shift 3 || shift $#
cd "$HOME"
source d3venv/bin/activate
pkill -f "d3_server.py" || true
for _ in $(seq 1 30); do pgrep -f d3_server.py >/dev/null || break; sleep 1; done
mem0=$(nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits)
t0=$(date +%s.%N)
DECISION_API_KEY="$(cat "$HOME/d3.key")" nohup python "models/$m/d3_server.py" --model "models/$m" \
  --device cuda:0 --host 127.0.0.1 --port "$port" "$@" > "logs/server-$m.log" 2>&1 &
pid=$!
for _ in $(seq 1 1800); do
  if ! kill -0 "$pid" 2>/dev/null; then echo "{\"model\":\"$m\",\"error\":\"server exited\"}"; tail -20 "logs/server-$m.log"; exit 1; fi
  if curl -fs "http://127.0.0.1:$port/health" 2>/dev/null | grep -q '"ready"'; then break; fi
  sleep 1
done
t1=$(date +%s.%N)
mem1=$(nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits)
health=$(curl -fs "http://127.0.0.1:$port/health")
python3 -c "import json,sys; h=json.loads(sys.argv[1]); print(json.dumps({'model':'$m','revision':'$rev','port':$port,'pid':$pid,'extra_args':'$*','cold_load_s':round($t1-$t0,1),'gpu_mem_before_mib':$mem0,'gpu_mem_after_load_mib':$mem1,'health':h}))" "$health" | tee -a logs/serve.jsonl
