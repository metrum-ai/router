#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

version=2.104.0
asset="gh_${version}_linux_amd64.tar.gz"
checksum_asset="gh_${version}_checksums.txt"
case "$(uname -m)" in
  x86_64|amd64) expected=98cb101ad87dbbd3f6b2f2bfcbd43197ccd07643140121d8a9cfaa91eadbc24e ;;
  aarch64|arm64) asset="gh_${version}_linux_arm64.tar.gz"; expected=cb34cefc6f4e77b90731374a2b737516bb8f3033c6ca46aa949e5344f68069e3 ;;
  *) echo "unsupported GitHub CLI host architecture: $(uname -m)" >&2; exit 2 ;;
esac
dest="${1:?destination directory is required}"
tmp_root="${RUNNER_TEMP:?RUNNER_TEMP must be set}/github-cli-${version}"
mkdir -p "$dest" "$tmp_root"
curl --fail --silent --show-error --location --retry 2 \
  "https://github.com/cli/cli/releases/download/v${version}/${checksum_asset}" \
  --output "${tmp_root}/${checksum_asset}"
curl --fail --silent --show-error --location --retry 2 \
  "https://github.com/cli/cli/releases/download/v${version}/${asset}" \
  --output "${tmp_root}/${asset}"
published_checksum="$(awk -v name="$asset" '$2 == name { print $1 }' "${tmp_root}/${checksum_asset}")"
if [[ "$published_checksum" != "$expected" ]]; then
  echo 'GitHub CLI release checksum file did not match its pinned checksum' >&2
  exit 1
fi
printf '%s  %s\n' "$expected" "${tmp_root}/${asset}" | sha256sum --check --status || {
  echo 'GitHub CLI release tarball checksum mismatch' >&2
  exit 1
}
extract_dir="${tmp_root}/extract"
mkdir -p "$extract_dir"
tar -xzf "${tmp_root}/${asset}" -C "$extract_dir" --strip-components=1 \
  "gh_${version}_linux_$( [[ "$asset" == *linux_arm64* ]] && echo arm64 || echo amd64 )/bin/gh"
install -m 0755 "${extract_dir}/bin/gh" "${dest}/gh"
: "${GITHUB_PATH:?GITHUB_PATH must be set by GitHub Actions}"
printf '%s\n' "$dest" >> "$GITHUB_PATH"
"${dest}/gh" version
