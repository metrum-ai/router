#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Tests for scripts/local_pi_instance.py."""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import stat
import subprocess
import tempfile
import textwrap
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "local_pi_instance.py"


def uv_python(*args: str) -> list[str]:
    uv = shutil.which("uv")
    if not uv:
        raise AssertionError("uv is required")
    return [uv, "run", "--with", "pyyaml", "python", *args]


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
                    "FIREWORKS_API_KEY": "fireworks-test",
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
            uv_python(
                str(SCRIPT),
                "--out-dir",
                str(out),
                "--repo-root",
                str(ROOT),
                "--env-json",
                str(env_json),
                "--smartrouterctl",
                str(root / "smartrouterctl"),
            ),
            capture_output=True,
            text=True,
        )
        if first.returncode != 0:
            raise AssertionError(first.stderr or first.stdout)
        summary = json.loads(first.stdout)
        if summary.get("token_reused") is not False:
            raise AssertionError(summary)
        if summary.get("listen") != "0.0.0.0:18081":
            raise AssertionError(summary.get("listen"))
        if summary.get("model_identity") != "requested_group":
            raise AssertionError(summary.get("model_identity"))
        token = (out / "METRUM_API_KEY").read_text(encoding="utf-8").strip()
        if token != "rtr_metrum_local-dev_example-project_dev_kdemo_SECRET":
            raise AssertionError(token)
        if stat.S_IMODE((out / "METRUM_API_KEY").stat().st_mode) != 0o600:
            raise AssertionError("METRUM_API_KEY mode")
        local_env = json.loads((out / "env.json").read_text(encoding="utf-8"))
        if local_env.get("METRUM_API_KEY") != token:
            raise AssertionError(local_env)
        if local_env.get("FIREWORKS_API_KEY") != "fireworks-test":
            raise AssertionError(local_env)
        if set(local_env) != {"FIREWORKS_API_KEY", "OPENAI_API_KEY", "METRUM_API_KEY", "LOCAL_PI_ADMIN_PASSWORD_HASH"}:
            raise AssertionError(sorted(local_env))

        import yaml  # type: ignore

        cfg = yaml.safe_load((out / "config.yaml").read_text(encoding="utf-8"))
        if cfg["server"]["listen"] != "0.0.0.0:18081":
            raise AssertionError(cfg["server"]["listen"])
        if cfg["server"]["responses"] != {"model_identity": "requested_group"}:
            raise AssertionError(cfg["server"].get("responses"))
        if "Model identity: requested_group" not in (out / "README.txt").read_text(encoding="utf-8"):
            raise AssertionError("README.txt missing model identity")
        admin = cfg["server"]["admin_auth"]
        if not admin["basic"]["enabled"] or not admin["basic"]["allow_insecure_http"]:
            raise AssertionError(admin["basic"])
        if admin["basic"]["users"][0]["password_hash_env"] != "LOCAL_PI_ADMIN_PASSWORD_HASH":
            raise AssertionError(admin["basic"]["users"])
        if not admin["authorization"]["enabled"] or not cfg["server"]["admin_reports"]["enabled"]:
            raise AssertionError("admin reports disabled")
        if not any("admin:reports, read|export|drilldown" in p for p in admin["authorization"]["policy"]):
            raise AssertionError(admin["authorization"]["policy"])
        if not local_env["LOCAL_PI_ADMIN_PASSWORD_HASH"].startswith("$2b$"):
            raise AssertionError("missing demo admin bcrypt hash")
        if list(cfg["models"]) != ["big-coder"]:
            raise AssertionError(list(cfg["models"]))
        if set(cfg["providers"]) != {"fireworks", "openai"}:
            raise AssertionError(set(cfg["providers"]))
        flash = cfg["providers"]["fireworks"]["models"]["deepseek-v4p1-flash"]
        if flash["model"] != "accounts/fireworks/models/deepseek-v4p1-flash":
            raise AssertionError(flash["model"])
        targets = cfg["models"]["big-coder"]["targets"]
        weights = [(t["provider"], t["model_ref"], t["weight"]) for t in targets]
        if weights != [("openai", "gpt-6-luna", 70), ("fireworks", "deepseek-v4p1-flash", 30)]:
            raise AssertionError(weights)
        if cfg["providers"]["openai"]["dialect"] != "openai-responses":
            raise AssertionError(cfg["providers"]["openai"]["dialect"])
        luna = cfg["providers"]["openai"]["models"]["gpt-6-luna"]
        if luna["model"] != "gpt-6-luna":
            raise AssertionError(luna["model"])
        prices = (
            luna["input_price_per_million_usd"],
            luna["cached_input_price_per_million_usd"],
            luna["output_price_per_million_usd"],
        )
        if prices != (0.1, 0.01, 0.5):
            raise AssertionError("gpt-6-luna pricing metadata")
        if luna["pricing_source"] != "https://developers.openai.com/api/docs/models/gpt-6-luna":
            raise AssertionError(luna["pricing_source"])
        want_hash = hashlib.sha256(token.encode()).hexdigest()
        if cfg["callers"][0]["token_sha256"] != want_hash:
            raise AssertionError(cfg["callers"][0]["token_sha256"])
        rate = cfg["callers"][0]["rate"]
        if rate != {"rpm": 2400, "tpm": 4_000_000, "concurrent": 160}:
            raise AssertionError(rate)
        quota = cfg["callers"][0]["quota"]
        if quota["day"] != {"requests": 100_000, "tokens": 400_000_000}:
            raise AssertionError(quota["day"])
        if quota["month"]["tokens"] != 8_000_000_000:
            raise AssertionError(quota["month"])
        if cfg["callers"][0]["key"]["lifetime_tokens"] != 40_000_000_000:
            raise AssertionError(cfg["callers"][0]["key"])
        readme = (out / "README.txt").read_text(encoding="utf-8")
        if "http://127.0.0.1:18081/v1" not in readme:
            raise AssertionError(readme)

        second = subprocess.run(
            uv_python(
                str(SCRIPT),
                "--out-dir",
                str(out),
                "--repo-root",
                str(ROOT),
                "--env-json",
                str(env_json),
                "--smartrouterctl",
                str(root / "smartrouterctl"),
            ),
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
        for key in ("FIREWORKS_API_KEY", "OPENAI_API_KEY", "METRUM_API_KEY"):
            env.pop(key, None)
        result = subprocess.run(
            uv_python(
                str(SCRIPT),
                "--out-dir",
                str(out),
                "--repo-root",
                str(ROOT),
                "--env-json",
                str(env_json),
            ),
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
