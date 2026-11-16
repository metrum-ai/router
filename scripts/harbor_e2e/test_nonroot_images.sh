#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

build_and_smoke() {
  local name="$1" dockerfile="$2" context="$3" image logs token
  image="router-harbor-check:${name,,}"
  logs="$tmp/logs-${name}"
  token="$tmp/router-token-${name}"
  mkdir -p "$logs"
  printf %s harbor-smoke-token > "$token"
  docker build --file "$dockerfile" --tag "$image" "$context"
  docker run --rm --user 0:0 --env "HOST_UID=$(id -u)" \
    --env "TOKEN_FILE=$(basename "$token")" \
    --volume "$tmp:/smoke" --volume "$logs:/logs/verifier" \
    --entrypoint /bin/sh "$image" -ec '
      chown "$HOST_UID:10001" "/smoke/$TOKEN_FILE"
      chmod 0640 "/smoke/$TOKEN_FILE"
      test "$(stat -c %u /smoke/$TOKEN_FILE)" = "$HOST_UID"
      test "$(stat -c %g /smoke/$TOKEN_FILE)" = 10001
      test "$(stat -c %a /smoke/$TOKEN_FILE)" = 640
      chown 10001:10001 /logs/verifier
      chmod 0755 /logs/verifier
    '
  docker run --rm --volume "$logs:/logs/verifier" \
    --volume "$token:/run/harbor-e2e/router-token:ro" \
    --entrypoint /bin/sh "$image" -ec '
    test "$(id -u)" = 10001
    test "$(id -g)" = 10001
    getent passwd 10001 | grep -F "/home/harbor"
    test "$HOME" = /home/harbor
    test "$(stat -c %u "$HOME")" = 10001
    test "$(stat -c %u /logs/agent)" = 10001
    mkdir -p "$HOME/.pi/agent"
    test "$(stat -c %u /app)" = 10001
    : > /app/.nonroot-smoke
    test "$(stat -c %u /app/.nonroot-smoke)" = 10001
    test -r /run/harbor-e2e/router-token
    test "$(stat -c %g /run/harbor-e2e/router-token)" = 10001
    test "$(stat -c %a /run/harbor-e2e/router-token)" = 640
    test "$(cat /run/harbor-e2e/router-token)" = harbor-smoke-token
    test ! -w /run/harbor-e2e/router-token
    : > /logs/verifier/reward.txt
  '
  test "$(stat -c %u "$logs/reward.txt")" = 10001
  docker image rm "$image" >/dev/null
}

python3 "$root/scripts/harbor_e2e/credential_permissions_test.py"

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
