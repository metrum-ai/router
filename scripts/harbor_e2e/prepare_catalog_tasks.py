# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Stage the bespoke HARBOR-02…06 tasks for the pinned live Harbor adapter."""

from __future__ import annotations

import shutil
import sys
from pathlib import Path

TASKS = {
    "HARBOR-02": "HARBOR-02-nonce-chain",
    "HARBOR-03": "HARBOR-03-parallel-tool-ids",
    "HARBOR-04": "HARBOR-04-tool-error-retry",
    "HARBOR-05": "HARBOR-05-large-stream-patch",
    "HARBOR-06": "HARBOR-06-no-replay-disconnect",
}
ROOT = Path(__file__).resolve().parents[2]
DOCKERFILE = """FROM node:22.19.0-bookworm-slim
RUN apt-get update -qq && apt-get install -y -qq python3 && npm install -g --ignore-scripts --engine-strict @earendil-works/pi-coding-agent@1.4.0 && pi --version
RUN useradd --uid 10001 --user-group --create-home --home-dir /home/harbor --shell /usr/sbin/nologin harbor && install -d -o 10001 -g 10001 /app /logs/agent /logs/verifier /run/harbor-e2e
ENV HOME=/home/harbor
WORKDIR /app
COPY --chown=10001:10001 workspace/ /app/
USER 10001:10001
CMD [\"sleep\", \"infinity\"]
"""
TEST_SH = """#!/bin/sh
set -u
python3 - <<'PY'
import importlib.util
import os
import pathlib
import traceback

workspace = pathlib.Path(os.environ.get('HARBOR_WORKSPACE', '/app'))
tests = pathlib.Path(os.environ.get('HARBOR_TESTS_DIR', '/tests'))
reward = pathlib.Path(os.environ.get('HARBOR_REWARD_DIR', '/logs/verifier')) / 'reward.txt'
reward.parent.mkdir(parents=True, exist_ok=True)
try:
    spec = importlib.util.spec_from_file_location('harbor_live_verify', tests / 'verify.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    result = module.verify(workspace)
    score = float(result.get('reward', 1.0 if result.get('passed') else 0.0))
    if not result.get('passed'):
        score = 0.0
except Exception:
    score = 0.0
    traceback.print_exc()
reward.write_text(f'{score:.0f}\\n', encoding='utf-8')
print(f'reward={score:.1f}')
PY
"""


def prepare(destination: Path, source_root: Path = ROOT / "tests" / "harbor" / "tasks") -> None:
    if destination.exists() and any(destination.iterdir()):
        raise FileExistsError(f"refusing to reuse nonempty task directory: {destination}")
    destination.mkdir(parents=True, exist_ok=True)
    for task_id, folder in TASKS.items():
        source = source_root / folder
        target = destination / folder
        target.mkdir()
        shutil.copy2(source / "task.toml", target / "task.toml")
        task_toml = target / "task.toml"
        task_toml.write_text(task_toml.read_text(encoding="utf-8").replace(
            'docker_image = "python:3.11.13-slim-bookworm"\n', ""), encoding="utf-8")
        shutil.copy2(source / "instruction.md", target / "instruction.md")
        env = target / "environment"
        workspace = env / "workspace"
        shutil.copytree(source / "environment" / "workspace", workspace)
        (env / "Dockerfile").write_text(DOCKERFILE, encoding="utf-8")
        tools = source / "tools"
        if tools.is_dir():
            target_tools = workspace / "tools"
            shutil.copytree(tools, target_tools)
        tests = target / "tests"
        tests.mkdir()
        shutil.copy2(source / "tests" / "verify.py", tests / "verify.py")
        (tests / "test.sh").write_text(TEST_SH, encoding="utf-8")
        (tests / "test.sh").chmod(0o755)


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: prepare_catalog_tasks.py DESTINATION", file=sys.stderr)
        return 2
    prepare(Path(sys.argv[1]))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
