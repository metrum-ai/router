#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
# Host-only downloader. No Docker, credentials or model execution.
set -Eeuo pipefail
root=${1:?expected absolute private workspace}
mode=${2:-}; [[ -z $mode || $mode == --tiny-self-test ]] || { echo 'invalid mode' >&2; exit 2; }
[[ $root =~ ^/[A-Za-z0-9_./-]+$ && -d $root && ! -L $root ]] || { echo 'unsafe host workspace' >&2; exit 1; }
root=$(realpath "$root")
[[ $root =~ ^/[A-Za-z0-9_./-]+$ && -x $root/uv && -f $root/download_models.py ]] || { echo 'missing downloader or uv' >&2; exit 1; }
UV_CACHE_DIR="$root/uv-cache" "$root/uv" venv --python /usr/bin/python3 "$root/hf-venv"
UV_CACHE_DIR="$root/uv-cache" "$root/uv" pip install --python "$root/hf-venv/bin/python" 'huggingface_hub==1.14.0' 'click==8.3.1'
extra=(); [[ -z $mode ]] || extra+=("$mode")
"$root/hf-venv/bin/python" "$root/download_models.py" --hf "$root/hf-venv/bin/hf" --out "$root/weights" --execute "${extra[@]}"
