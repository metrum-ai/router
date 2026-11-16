#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

version=0.22.1
case "$(uname -m)" in
  x86_64|amd64) arch=x86_64; expected=0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0 ;;
  aarch64|arm64) arch=arm64; expected=898c0cff975f898a33e8c4580bdafb0e7c02c7faa33374e946762f97c4ab7110 ;;
  *) echo "unsupported crane host architecture: $(uname -m)" >&2; exit 2 ;;
esac
dest="${1:-${RUNNER_TEMP:-/tmp}/crane}"
tmp="${RUNNER_TEMP:-/tmp}/crane-${version}-${arch}.tar.gz"
curl --fail --silent --show-error --location --retry 2 \
  "https://github.com/google/go-containerregistry/releases/download/v${version}/go-containerregistry_Linux_${arch}.tar.gz" \
  --output "$tmp"
printf '%s  %s\n' "$expected" "$tmp" | sha256sum --check --status || {
  echo 'crane release checksum mismatch' >&2
  exit 1
}
tar -xzf "$tmp" -C "$(dirname "$dest")" crane
mv "$(dirname "$dest")/crane" "$dest"
chmod 0755 "$dest"
"$dest" version | grep -F "$version"
