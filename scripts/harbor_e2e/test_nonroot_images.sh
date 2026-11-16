#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

build_and_smoke() {
  local name="$1" dockerfile="$2" context="$3" image logs
  image="router-harbor-check:${name,,}"
  logs="$tmp/logs-${name}"
  mkdir -p "$logs"
  chmod 0777 "$logs"
  docker build --file "$dockerfile" --tag "$image" "$context"
  docker run --rm --volume "$logs:/logs/verifier" --entrypoint /bin/sh "$image" -ec '
    test "$(id -u)" = 10001
    test "$(id -g)" = 10001
    getent passwd 10001 | grep -F "/home/harbor"
    test "$HOME" = /home/harbor
    test "$(stat -c %u "$HOME")" = 10001
    mkdir -p "$HOME/.pi/agent"
    test "$(stat -c %u /app)" = 10001
    : > /app/.nonroot-smoke
    test "$(stat -c %u /app/.nonroot-smoke)" = 10001
    : > /logs/verifier/reward.txt
  '
  test "$(stat -c %u "$logs/reward.txt")" = 10001
  docker image rm "$image" >/dev/null
}

for dockerfile in "$root"/tests/harbor/tasks/HARBOR-*/environment/Dockerfile; do
  name="$(basename "$(dirname "$(dirname "$dockerfile")")")"
  build_and_smoke "$name" "$dockerfile" "$(dirname "$dockerfile")"
done

python3 "$root/scripts/harbor_e2e/prepare_catalog_tasks.py" "$tmp/catalog"
catalog_dockerfile="$tmp/catalog/HARBOR-02-nonce-chain/environment/Dockerfile"
build_and_smoke catalog "$catalog_dockerfile" "$(dirname "$catalog_dockerfile")"

python3 "$root/scripts/harbor_e2e/generate_tasks.py" "$tmp/generated"
generated_dockerfile="$tmp/generated/hello/environment/Dockerfile"
build_and_smoke generated "$generated_dockerfile" "$(dirname "$generated_dockerfile")"
