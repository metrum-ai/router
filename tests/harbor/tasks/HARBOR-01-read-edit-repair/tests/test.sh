#!/usr/bin/env bash
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
# Harbor verifier entrypoint. Python reads the program from stdin, so resolve
# the tests directory here; HARBOR_WORKSPACE / HARBOR_REWARD_DIR exist only so
# the offline suite can exercise this exact script outside a container.
set -euo pipefail
HARBOR_TESTS_DIR="$(cd "$(dirname "$0")" && pwd)"
export HARBOR_TESTS_DIR
python3 - <<'PY'
import os
import sys
from pathlib import Path

tests = Path(os.environ["HARBOR_TESTS_DIR"])
sys.path.insert(0, str(tests))
from verify import verify

default_ws = Path("/app") if Path("/app/normalize.py").is_file() else tests.parent / "environment" / "workspace"
ws = Path(os.environ.get("HARBOR_WORKSPACE") or default_ws)
result = verify(ws)
reward_dir = Path(os.environ.get("HARBOR_REWARD_DIR") or "/logs/verifier")
reward_dir.mkdir(parents=True, exist_ok=True)
(reward_dir / "reward.txt").write_text(f"{result['reward']}\n", encoding="utf-8")
print(result)
sys.exit(0 if result["passed"] else 1)
PY
