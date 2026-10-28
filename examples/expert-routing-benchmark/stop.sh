#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
docker info >/dev/null
for name in proof426-biomedical proof426-finance proof426-math proof426-coding proof426-baseline; do
  if docker container inspect "$name" >/dev/null 2>&1; then
    label=$(docker inspect --format '{{ index .Config.Labels "metrum.task" }}' "$name")
    [[ $label == issue-426 ]] || { echo 'refusing to stop unlabelled container' >&2; exit 1; }
    docker stop "$name" >/dev/null
    if docker container inspect "$name" >/dev/null 2>&1; then echo 'container removal unverified' >&2; exit 1; fi
  fi
done
printf 'Benchmark-labelled containers are absent.\n'
