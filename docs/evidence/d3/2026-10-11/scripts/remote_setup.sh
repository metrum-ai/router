#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
# Metrum AI Router, issue #278: prepare a Shadeform GPU host to serve d3 with the
# d3_server.py that ships in each model package. Downloads the pinned snapshots,
# creates a Python 3.12 venv and installs requirements.txt + fastapi + uvicorn.
# flash-linear-attention is tried; if it fails, the PyTorch reference kernels are used.
# Usage: remote_setup.sh <model-list e.g. "d3-lite d3-nano d3">
set -euo pipefail
MODELS="${1:-d3-lite d3-nano}"
declare -A REV=(
  [d3-lite]=a8d25a91ef75552803f7009d25b7bd435991abd9
  [d3-nano]=3aeb1c22a5632aac72353c36b3d0111c40e7dbf4
  [d3]=cbff3a3cb0b074b6585fe9f747b9f9f5557a0e75
)
cd "$HOME"
if ! command -v uv >/dev/null && [ ! -x "$HOME/.local/bin/uv" ]; then
  curl -LsSf https://astral.sh/uv/install.sh | sh >/dev/null
fi
export PATH="$HOME/.local/bin:$PATH"
[ -d d3venv ] || uv venv --python 3.12 d3venv
# shellcheck disable=SC1091
source d3venv/bin/activate
uv pip install -q "huggingface_hub>=1.0" hf_transfer
export HF_HUB_ENABLE_HF_TRANSFER=1
mkdir -p models logs
for m in $MODELS; do
  t0=$(date +%s)
  python - "$m" "${REV[$m]}" <<'PY'
import sys
from huggingface_hub import snapshot_download
name, rev = sys.argv[1], sys.argv[2]
snapshot_download(f"vllm-sr/{name}", revision=rev, local_dir=f"models/{name}")
PY
  echo "{\"download\":\"$m\",\"revision\":\"${REV[$m]}\",\"seconds\":$(( $(date +%s) - t0 )),\"bytes\":$(du -sb "models/$m" | cut -f1)}" | tee -a logs/setup.jsonl
done
first=$(echo "$MODELS" | awk '{print $1}')
t0=$(date +%s)
if uv pip install -r "models/$first/requirements.txt" fastapi uvicorn > logs/pip.log 2>&1; then
  fla=installed
else
  echo "full requirements failed; retrying without flash-linear-attention" | tee -a logs/pip.log
  grep -v '^flash-linear-attention' "models/$first/requirements.txt" > /tmp/req-nofla.txt
  uv pip install -r /tmp/req-nofla.txt fastapi uvicorn >> logs/pip.log 2>&1
  fla=failed
fi
python - <<PY | tee -a logs/setup.jsonl
import json, importlib.metadata as md, torch
def ver(p):
    try:
        return md.version(p)
    except md.PackageNotFoundError:
        return None
print(json.dumps({"pip_seconds": $(( $(date +%s) - t0 )), "fla": "$fla", "torch": torch.__version__,
  "torch_cuda": torch.version.cuda, "cuda_available": torch.cuda.is_available(),
  "gpu": torch.cuda.get_device_name(0) if torch.cuda.is_available() else None,
  "transformers": ver("transformers"), "flash_linear_attention": ver("flash-linear-attention"),
  "triton": ver("triton"), "python": __import__("sys").version.split()[0]}))
PY
nvidia-smi --query-gpu=name,driver_version,memory.total --format=csv,noheader | tee -a logs/setup.jsonl
nvidia-smi | sed -n '3p' | tee -a logs/setup.jsonl
