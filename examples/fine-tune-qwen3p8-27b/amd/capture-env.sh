#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
# Run on the remote GPU host; captures hardware only, never environment/secrets.
set -euo pipefail
OUT=${1:?usage: capture-env.sh private-output-directory}
mkdir -p "$OUT"
umask 077
uname -srmo > "$OUT/kernel.txt"
rocminfo > "$OUT/rocminfo.txt"
amd-smi version > "$OUT/amd-smi-version.txt"
amd-smi static > "$OUT/amd-smi-static.txt"
modinfo amdgpu > "$OUT/amdgpu-module.txt"
if [[ -f /opt/rocm/.info/version ]]; then
  cp /opt/rocm/.info/version "$OUT/rocm-version.txt"
fi
# Only explicit safe fields; never full docker inspect (may contain tokens).
docker image ls --digests --no-trunc --format '{{.Repository}} {{.Tag}} {{.Digest}} {{.ID}}' \
  | grep -E '^rocm/(pytorch|vllm) ' > "$OUT/image-digests.txt"
ss -H -lntup > "$OUT/listeners.txt"
echo 'Captured hardware/image inventory. Review private files before publication.'
