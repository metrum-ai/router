#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
# Run on the GPU host. /workspace must contain pinned base + canonical data.
# Default smoke; real training must follow a successful smoke on this hardware.
MODE=${1:-smoke}
GPUS=${GPUS:-1}
case "$GPUS" in 1|2|4|8) ;; *) echo 'GPUS must be 1, 2, 4 or 8' >&2; exit 2;; esac
IMAGE=axolotlai/axolotl@sha256:26a660139e62125256473dce660087786d68e23411c7013339deffa4141a8631
case "$MODE" in smoke|train) ;; *) echo 'mode must be smoke or train' >&2; exit 2;; esac
if [[ ${DRY_RUN:-1} == 1 ]]; then
  printf '%s\n' "Axolotl $MODE; GPUs=$GPUS; pinned image=$IMAGE; no credentials persisted"
  exit 0
fi
command -v nvidia-smi >/dev/null
if [[ "$MODE" == train ]]; then
  test -f /workspace/axolotl-smoke-passed || { echo 'Run hardware smoke first' >&2; exit 2; }
fi
# Canonical tiny smoke subset remains outside Git; both full arms use train.jsonl.
head -n 16 /workspace/canonical/train.jsonl > /workspace/canonical/smoke.jsonl
EXTRA=''
[[ "$MODE" != smoke ]] || EXTRA='--smoke'
sudo docker run --rm --gpus all --ipc=host --shm-size=32g \
  -v /workspace:/data --entrypoint bash "$IMAGE" -lc "
    set -euo pipefail
    python /data/standalone/render.py --gpus $GPUS $EXTRA --out /data/$MODE.yaml
    python /data/standalone/inventory.py > /data/axolotl-package-versions.txt
    axolotl train /data/$MODE.yaml
  "
if [[ "$MODE" == smoke ]]; then
  touch /workspace/axolotl-smoke-passed
fi
