#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
# All actions dry-run unless --execute is explicitly supplied.
exec python3 "$(dirname "$0")/run.py" "$@"
