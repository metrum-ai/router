#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
# Run on the GPU host. /workspace must contain pinned base + canonical data.
# Default smoke; real training must follow a successful smoke on this hardware.
MODE=${1:-smoke}
GPUS=${GPUS:-1}
RECIPE=${RECIPE:-run1}
case "$RECIPE" in run1|run2) ;; *) echo 'RECIPE must be run1 or run2' >&2; exit 2;; esac
RECIPE_FILE=/workspace/standalone/axolotl.yaml
TRAIN_FILE=/workspace/canonical/train.jsonl
VAL_FILE=/workspace/canonical/val.jsonl
if [[ "$RECIPE" == run2 ]]; then
  RECIPE_FILE=/workspace/run2/axolotl.yaml
  TRAIN_FILE=/workspace/canonical/train-axolotl.jsonl
  VAL_FILE=/workspace/canonical/val-axolotl.jsonl
fi
case "$GPUS" in 1|2|4|8) ;; *) echo 'GPUS must be 1, 2, 4 or 8' >&2; exit 2;; esac
IMAGE=axolotlai/axolotl@sha256:26a660139e62125256473dce660087786d68e23411c7013339deffa4141a8631
case "$MODE" in smoke|preprocess|train) ;; *) echo 'mode must be smoke, preprocess or train' >&2; exit 2;; esac
if [[ ${DRY_RUN:-1} == 1 ]]; then
  printf '%s\n' "Axolotl $MODE; GPUs=$GPUS; recipe=$RECIPE; pinned image=$IMAGE; no credentials persisted"
  exit 0
fi
command -v nvidia-smi >/dev/null
if [[ "$RECIPE" == run2 ]]; then
  READY_EXTRA=''
  [[ "$MODE" != train ]] || READY_EXTRA='--require-prepared'
  python3 /workspace/run2/readiness.py --workspace /workspace $READY_EXTRA
fi
DATA_ID=$(sha256sum "$TRAIN_FILE" "$VAL_FILE" "$RECIPE_FILE" /workspace/standalone/render.py /workspace/standalone/run.sh | sha256sum | cut -c1-64)
SMOKE_MARKER=/workspace/axolotl-smoke-passed-$RECIPE-g$GPUS-$DATA_ID
if [[ "$MODE" == train ]]; then
  test -f "$SMOKE_MARKER" || { echo 'Run matching hardware/recipe/data smoke first' >&2; exit 2; }
fi
# Canonical tiny smoke subset remains outside Git; both full arms use train.jsonl.
head -n 16 "$TRAIN_FILE" > /workspace/canonical/smoke.jsonl
CACHE_MODE=$MODE
[[ "$MODE" != preprocess ]] || CACHE_MODE=train
AXO_COMMAND=train
[[ "$MODE" != preprocess ]] || AXO_COMMAND=preprocess
EXTRA=''
[[ "$MODE" != smoke ]] || EXTRA='--smoke'
sudo docker run --rm --gpus all --ipc=host --shm-size=32g \
  -v /workspace:/data --entrypoint bash "$IMAGE" -lc "
    set -euo pipefail
    python /data/standalone/render.py --gpus $GPUS --recipe '$RECIPE' $EXTRA --data-id '$CACHE_MODE-$DATA_ID' --out /data/$MODE.yaml
    python /data/standalone/inventory.py > /data/axolotl-package-versions.txt
    axolotl $AXO_COMMAND /data/$MODE.yaml
  "
if [[ "$MODE" == smoke ]]; then
  touch "$SMOKE_MARKER"
fi
