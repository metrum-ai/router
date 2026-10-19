#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Tests for scripts/local_pi_instance.py."""

from __future__ import annotations

import base64
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
        flash = cfg["providers"]["fireworks"]["models"]["deepseek-v4p1-flash-1017"]
        if flash["model"] != "accounts/fireworks/models/deepseek-v4p1-flash-1017":
            raise AssertionError(flash["model"])
        if (flash["input_price_per_million_usd"], flash["cached_input_price_per_million_usd"],
                flash["output_price_per_million_usd"]) != (0.30, 0.006, 1.20):
            raise AssertionError("deepseek-v4p1-flash-1017 pricing metadata")
        targets = cfg["models"]["big-coder"]["targets"]
        weights = [(t["provider"], t["model_ref"], t["weight"]) for t in targets]
        if weights != [("fireworks", "deepseek-v4p1-flash-1017", 70), ("openai", "gpt-6-luna", 30)]:
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
        caller = cfg["callers"][0]
        if "token_sha256" in caller:
            raise AssertionError("structured caller tokens must use token_hash, not token_sha256")
        if caller["token_id"] != "rtr_metrum_local-dev_example-project_dev_kdemo":
            raise AssertionError(caller["token_id"])
        scheme, iterations, salt, key = caller["token_hash"].split(":")
        if scheme != "pbkdf2-sha3-512" or iterations != "i=20000":
            raise AssertionError("token_hash scheme/iterations")

        def b64decode(value: str) -> bytes:
            return base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))

        derived = hashlib.pbkdf2_hmac("sha3_512", token.encode(), b64decode(salt), 20000, 64)
        if derived != b64decode(key) or len(b64decode(salt)) != 32:
            raise AssertionError("token_hash does not verify the local caller token")
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


def test_public_token_id_never_includes_secret_with_underscores() -> None:
    import importlib.util

    spec = importlib.util.spec_from_file_location("local_pi_instance", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    token = "rtr_metrum_local-dev_example-project_dev_k20261011_abc_def-ghi_jkl"
    if module.public_token_id(token) != "rtr_metrum_local-dev_example-project_dev_k20261011":
        raise AssertionError(module.public_token_id(token))
    fields = module.caller_token_fields(token)
    if fields["token_id"] != "rtr_metrum_local-dev_example-project_dev_k20261011" or "token_hash" not in fields:
        raise AssertionError(sorted(fields))
    if "abc" in fields["token_id"]:
        raise AssertionError("token_id leaked secret material")


def test_d3_group_caller_uses_token_hash() -> None:
    """--d3-policy-url (#282) and token_hash callers compose in one config."""
    import importlib.util

    spec = importlib.util.spec_from_file_location("local_pi_instance", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    token = "rtr_metrum_local-dev_example-project_dev_k20261011_abc_def-ghi_jkl"
    cfg = module.build_config(
        out_dir=Path("/nonexistent"),
        listen="127.0.0.1:18081",
        id_key_hex="00" * 32,
        token=token,
        d3_policy={
            "policy_url": "http://127.0.0.1:18302/route",
            "mode": "shadow",
            "on_error": "fail_closed",
            "timeout_ms": 300,
        },
    )
    if list(cfg["models"]) != ["big-coder", "d3-auto"]:
        raise AssertionError(list(cfg["models"]))
    caller = cfg["callers"][0]
    if caller["allow"] != ["big-coder", "d3-auto"]:
        raise AssertionError(caller["allow"])
    if "token_sha256" in caller or not caller["token_hash"].startswith("pbkdf2-sha3-512:i=20000:"):
        raise AssertionError("d3 config caller must use token_hash")
    if caller["token_id"] != "rtr_metrum_local-dev_example-project_dev_k20261011":
        raise AssertionError(caller["token_id"])


def load_module():
    import importlib.util

    spec = importlib.util.spec_from_file_location("local_pi_instance", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def test_d3_tiers_are_selectable_with_prices() -> None:
    """--d3-strong-model / --d3-tier-groups (#287): real price tiers, default unchanged."""
    module = load_module()
    kwargs = {
        "out_dir": Path("/nonexistent"),
        "listen": "127.0.0.1:18081",
        "id_key_hex": "00" * 32,
        "token": "rtr_metrum_local-dev_example-project_dev_k20261011_abc",
    }
    policy = {"policy_url": "http://127.0.0.1:18302/route", "mode": "enforce", "on_error": "fail_closed",
              "timeout_ms": 300}

    default = module.build_config(**kwargs, d3_policy=policy)
    tiers = [(t["tier"], t["provider"], t["model_ref"]) for t in default["models"]["d3-auto"]["targets"]]
    if tiers != [("cheap", "fireworks", "deepseek-v4p1-flash"), ("strong", "openai", "gpt-6-luna")]:
        raise AssertionError(tiers)
    if set(default["providers"]["openai"]["models"]) != {"gpt-6-luna"}:
        raise AssertionError("default config must not add catalog models")
    if set(default["models"]) != {"big-coder", "d3-auto"}:
        raise AssertionError(list(default["models"]))

    cfg = module.build_config(**kwargs, d3_policy={**policy, "strong_model": "gpt-6.1-sol", "tier_groups": True})
    strong = cfg["models"]["d3-auto"]["targets"][1]
    if (strong["provider"], strong["model_ref"], strong["tier"]) != ("openai", "gpt-6.1-sol", "strong"):
        raise AssertionError(strong)
    if not strong["bridges"]["chat_to_responses"]["tools"]:
        raise AssertionError("OpenAI strong tier must keep the chat_to_responses tool bridge")
    sol = cfg["providers"]["openai"]["models"]["gpt-6.1-sol"]
    prices = (sol["input_price_per_million_usd"], sol["cached_input_price_per_million_usd"],
              sol["output_price_per_million_usd"])
    if prices != (2.0, 0.1, 10.0) or "gpt-6.1-sol" not in sol["pricing_source"]:
        raise AssertionError(prices)
    if "gpt-6-luna" not in cfg["providers"]["openai"]["models"]:
        raise AssertionError("big-coder still needs gpt-6-luna")
    for suffix, ref in (("cheap", "deepseek-v4p1-flash"), ("strong", "gpt-6.1-sol")):
        group = cfg["models"][f"d3-auto-{suffix}"]
        if group["strategy"] != "weighted" or [t["model_ref"] for t in group["targets"]] != [ref]:
            raise AssertionError((suffix, group))
    if cfg["callers"][0]["allow"] != ["big-coder", "d3-auto", "d3-auto-cheap", "d3-auto-strong"]:
        raise AssertionError(cfg["callers"][0]["allow"])
    names = [m["name"] for m in module.pi_models({**policy, "strong_model": "gpt-6.1-sol"})]
    if "deepseek-v4p1-flash cheap / gpt-6.1-sol strong" not in names[1]:
        raise AssertionError(names)

    fallback = module.build_config(**kwargs, d3_policy={**policy, "strong_model": "gpt-5.6-sol"})
    if fallback["providers"]["openai"]["models"]["gpt-5.6-sol"]["output_price_per_million_usd"] != 20.0:
        raise AssertionError("gpt-5.6-sol fallback price")
    glm = module.build_config(**kwargs, d3_policy={**policy, "strong_model": "glm-5p3"})
    glm_entry = glm["providers"]["fireworks"]["models"]["glm-5p3"]
    if glm_entry["model"] != "accounts/fireworks/models/glm-5p3" or glm_entry["output_price_per_million_usd"] != 4.40:
        raise AssertionError(glm_entry)
    if "bridges" in glm["models"]["d3-auto"]["targets"][1]:
        raise AssertionError("Fireworks targets take chat requests directly")


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
    test_public_token_id_never_includes_secret_with_underscores()
    test_d3_group_caller_uses_token_hash()
    test_d3_tiers_are_selectable_with_prices()
    test_missing_upstream_keys_fails()
    print("local_pi_instance_test: ok")
