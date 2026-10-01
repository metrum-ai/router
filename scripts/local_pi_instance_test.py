#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Tests for scripts/local_pi_instance.py."""

from __future__ import annotations

import hashlib
import json
import os
import stat
import subprocess
import sys
import tempfile
import textwrap
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "local_pi_instance.py"


def write_executable(path: Path, body: str) -> None:
    first_line, *remaining_lines = body.splitlines()
    path.write_text(first_line + "\n" + textwrap.dedent("\n".join(remaining_lines)), encoding="utf-8")
    path.chmod(0o755)


def test_creates_weighted_instance_and_reuses_metrum_key() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        out = root / "local-pi"
        env_json = root / "env.json"
        env_json.write_text(
            json.dumps(
                {
                    "MOONSHOT_API_KEY": "moon-test",
                    "MINIMAX_API_KEY": "mini-test",
                    "OPENAI_API_KEY": "openai-test",
                }
            ),
            encoding="utf-8",
        )
        write_executable(
            root / "smartrouterctl",
            """#!/usr/bin/env python3
            import json, pathlib, sys
            token = pathlib.Path(sys.argv[sys.argv.index('--token-out') + 1])
            token.write_text('rtr_metrum_local-dev_example-project_dev_kdemo_SECRET\\n')
            print(json.dumps({
                'schema': 'metrum.ai/smartrouter-caller-grant/v1',
                'caller_id': 'local-dev-example-project-dev',
                'token_id': 'rtr_metrum_local-dev_example-project_dev_kdemo',
                'token_file': token.name,
                'activation': 'local-config-written-restart-required',
            }))
            """,
        )
        first = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--out-dir",
                str(out),
                "--repo-root",
                str(ROOT),
                "--env-json",
                str(env_json),
                "--smartrouterctl",
                str(root / "smartrouterctl"),
            ],
            capture_output=True,
            text=True,
        )
        if first.returncode != 0:
            raise AssertionError(first.stderr or first.stdout)
        summary = json.loads(first.stdout)
        if summary.get("token_reused") is not False:
            raise AssertionError(summary)
        token = (out / "METRUM_API_KEY").read_text(encoding="utf-8").strip()
        if token != "rtr_metrum_local-dev_example-project_dev_kdemo_SECRET":
            raise AssertionError(token)
        if stat.S_IMODE((out / "METRUM_API_KEY").stat().st_mode) != 0o600:
            raise AssertionError("METRUM_API_KEY mode")
        local_env = json.loads((out / "env.json").read_text(encoding="utf-8"))
        if local_env.get("METRUM_API_KEY") != token:
            raise AssertionError(local_env)
        if local_env.get("MOONSHOT_API_KEY") != "moon-test":
            raise AssertionError(local_env)

        import yaml  # type: ignore

        cfg = yaml.safe_load((out / "config.yaml").read_text(encoding="utf-8"))
        targets = cfg["models"]["big-coder"]["targets"]
        weights = [(t["provider"], t["model_ref"], t["weight"]) for t in targets]
        if weights != [("kimi", "kimi-k3", 40), ("minimax", "m3", 40), ("openai", "gpt-5.6-sol", 20)]:
            raise AssertionError(weights)
        if cfg["providers"]["openai"]["dialect"] != "openai-responses":
            raise AssertionError(cfg["providers"]["openai"]["dialect"])
        want_hash = hashlib.sha256(token.encode()).hexdigest()
        if cfg["callers"][0]["token_sha256"] != want_hash:
            raise AssertionError(cfg["callers"][0]["token_sha256"])

        second = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--out-dir",
                str(out),
                "--repo-root",
                str(ROOT),
                "--env-json",
                str(env_json),
                "--smartrouterctl",
                str(root / "smartrouterctl"),
            ],
            capture_output=True,
            text=True,
        )
        if second.returncode != 0:
            raise AssertionError(second.stderr or second.stdout)
        summary2 = json.loads(second.stdout)
        if summary2.get("token_reused") is not True:
            raise AssertionError(summary2)
        if (out / "METRUM_API_KEY").read_text(encoding="utf-8").strip() != token:
            raise AssertionError("token changed on reuse")


def test_missing_upstream_keys_fails() -> None:
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        out = root / "local-pi"
        env_json = root / "env.json"
        env_json.write_text(json.dumps({"OPENAI_API_KEY": "only-one"}), encoding="utf-8")
        env = os.environ.copy()
        for key in ("MOONSHOT_API_KEY", "MINIMAX_API_KEY", "OPENAI_API_KEY", "METRUM_API_KEY"):
            env.pop(key, None)
        result = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--out-dir",
                str(out),
                "--repo-root",
                str(ROOT),
                "--env-json",
                str(env_json),
            ],
            env=env,
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            raise AssertionError("accepted incomplete env.json")
        if "missing upstream keys" not in result.stderr:
            raise AssertionError(result.stderr)


if __name__ == "__main__":
    test_creates_weighted_instance_and_reuses_metrum_key()
    test_missing_upstream_keys_fails()
    print("local_pi_instance_test: ok")
