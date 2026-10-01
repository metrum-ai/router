#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Create or refresh a local weighted coding-agent router instance.

Default shape matches the local pi setup:
  listen 0.0.0.0:18081
  group big-coder (weighted)
    40% kimi / kimi-k3
    40% minimax / MiniMax-M3
    20% openai / gpt-5.6-sol (Responses via chat_to_responses)

Upstream keys are copied from a source env.json (repo env.json by default).
The local caller token is stored as METRUM_API_KEY (file + out-dir env.json)
and reused when already present unless --rotate-key is set.

Output stays under a gitignored directory (default tmp/local-pi).
Run with: uv run --with pyyaml python scripts/local_pi_instance.py
"""

from __future__ import annotations

import argparse
import json
import os
import secrets
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

UPSTREAM_KEYS = ("MOONSHOT_API_KEY", "MINIMAX_API_KEY", "OPENAI_API_KEY")
CALLER_ID = "local-dev-example-project-dev"
OWNER_USER = "local-dev"
PROJECT = "example-project"
GROUP = "big-coder"

# Local coding-agent caller policy: 20x the original local-dev defaults.
LOCAL_RATE_RPM = 120 * 20
LOCAL_RATE_TPM = 200_000 * 20
LOCAL_RATE_CONCURRENT = 8 * 20
LOCAL_QUOTA_DAY_REQUESTS = 5_000 * 20
LOCAL_QUOTA_DAY_TOKENS = 20_000_000 * 20
LOCAL_QUOTA_MONTH_TOKENS = 400_000_000 * 20
LOCAL_KEY_LIFETIME_TOKENS = 2_000_000_000 * 20


def die(message: str, code: int = 2) -> None:
    print(message, file=sys.stderr)
    raise SystemExit(code)


def client_listen(listen: str) -> str:
    """Map wildcard bind addresses to a loopback client URL host."""
    host, sep, port = listen.rpartition(":")
    if not sep:
        return listen
    if host in ("0.0.0.0", "::", "[::]"):
        return f"127.0.0.1:{port}"
    return listen


def run(cmd: list[str], cwd: Path | None = None) -> str:
    result = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip()
        die(f"command failed ({result.returncode}): {' '.join(cmd)}\n{detail}")
    return result.stdout


def resolve_cli(explicit: str, repo_root: Path) -> list[str]:
    if explicit:
        return [explicit]
    for name in ("metrum-ai-routerctl", "smartrouterctl"):
        found = shutil.which(name)
        if found:
            return [found]
    go_bin = shutil.which("go")
    if not go_bin:
        die("missing metrum-ai-routerctl/smartrouterctl and go")
    return [go_bin, "run", str(repo_root / "cmd" / "metrum-ai-routerctl")]


def load_json_object(path: Path) -> dict[str, str]:
    if not path.is_file():
        return {}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as err:
        die(f"invalid JSON in {path}: {err}")
    if not isinstance(data, dict):
        die(f"{path} must be a JSON object")
    out: dict[str, str] = {}
    for key, value in data.items():
        if isinstance(value, str):
            out[str(key)] = value
    return out


def write_private(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    os.chmod(path, 0o600)


def public_token_id(token: str) -> str:
    token = token.strip()
    if not token.startswith("rtr_metrum_"):
        return token
    parts = token.split("_")
    if len(parts) < 7:
        return token
    return "_".join(parts[:-1])


def token_sha256(caller_token: str) -> str:
    """Return the router caller lookup digest for a bearer token.

    The router indexes callers by SHA-256 of the raw token bytes (same as
    `sha256sum` over the token). This is caller lookup, not password storage.
    """
    try:
        completed = subprocess.run(
            ["sha256sum"],
            input=caller_token.strip().encode("utf-8"),
            capture_output=True,
            check=True,
        )
    except FileNotFoundError:
        die("sha256sum is required to fingerprint the local caller token")
    except subprocess.CalledProcessError as err:
        detail = (err.stderr or err.stdout or b"").decode("utf-8", errors="replace").strip()
        die(f"sha256sum failed: {detail or err.returncode}")
    digest = completed.stdout.decode("ascii", errors="replace").split()
    if not digest:
        die("sha256sum produced empty output")
    return digest[0]


def resolve_existing_token(out_dir: Path, source_env: dict[str, str]) -> str:
    candidates = [
        os.environ.get("METRUM_API_KEY", "").strip(),
        source_env.get("METRUM_API_KEY", "").strip(),
        load_json_object(out_dir / "env.json").get("METRUM_API_KEY", "").strip(),
    ]
    for path in (out_dir / "METRUM_API_KEY", out_dir / "router.token"):
        if path.is_file():
            candidates.append(path.read_text(encoding="utf-8").strip())
    for token in candidates:
        if token:
            return token
    return ""


def build_config(
    *,
    out_dir: Path,
    listen: str,
    id_key_hex: str,
    token: str,
) -> dict:
    return {
        "server": {
            "listen": listen,
            "default_model_group": GROUP,
            "identifiers": {
                "mode": "rewrite",
                "transform": {
                    "current": {
                        "key_id": "idk-local-pi",
                        "key": id_key_hex,
                    }
                },
            },
            "cache": {"enabled": False, "max_bytes": 134217728, "default_ttl": "15m"},
            "logging": {
                "path": str(out_dir / "requests.jsonl"),
                "rotate_mb": 64,
                "keep": 7,
            },
            "usage_db": {
                "enabled": True,
                "driver": "sqlite",
                "path": str(out_dir / "usage.sqlite"),
                "migration_policy": "auto-safe",
            },
            "diagnostics": {
                "enabled": True,
                "retention_days": 7,
                "store_sanitized_upstream_errors": True,
                "max_error_bytes": 2048,
            },
            "retention": {"enabled": False},
            "admin_auth": {"basic": {"enabled": False, "users": []}},
        },
        "state_path": str(out_dir / "router-state.json"),
        "users": [
            {
                "id": OWNER_USER,
                "name": "Local Developer",
                "type": "service_account",
                "status": "active",
            }
        ],
        "projects": [
            {
                "id": PROJECT,
                "name": "Example Project",
                "status": "active",
            }
        ],
        "project_memberships": [
            {
                "user_id": OWNER_USER,
                "project": PROJECT,
                "role": "developer",
                "status": "active",
            }
        ],
        "providers": {
            "kimi": {
                "base_url": "https://api.moonshot.ai/v1",
                "dialect": "openai-chat",
                "api_key": "${MOONSHOT_API_KEY}",
                "api_key_env": "MOONSHOT_API_KEY",
                "key_id": "moonshot-kimi-local",
                "models": {
                    "kimi-k3": {
                        "model": "kimi-k3",
                        "tier": "frontier",
                        "context_tokens": 1000000,
                        "input_modalities": ["text"],
                        "output_modalities": ["text"],
                        "input_price_per_million_usd": 3.0,
                        "output_price_per_million_usd": 15.0,
                        "pricing_source": "https://platform.kimi.ai/",
                        "pricing_updated_at": "2026-07-19",
                        "request_shape_support": {
                            "supported_inbound_dialects": ["openai-chat"],
                        },
                        "tool_support": {
                            "openai_chat": ["tools", "tool_choice", "structured_outputs"],
                        },
                    }
                },
            },
            "minimax": {
                "base_url": "https://api.minimax.io/v1",
                "dialect": "openai-chat",
                "auth_scheme": "bearer",
                "api_key": "${MINIMAX_API_KEY}",
                "api_key_env": "MINIMAX_API_KEY",
                "key_id": "minimax-local",
                "models": {
                    "m3": {
                        "model": "MiniMax-M3",
                        "tier": "heavy",
                        "input_price_per_million_usd": 0.3,
                        "output_price_per_million_usd": 1.2,
                        "input_modalities": ["text"],
                        "output_modalities": ["text"],
                        "pricing_source": "https://platform.minimax.io/docs/pricing/overview",
                        "pricing_updated_at": "2026-06-17",
                        "request_shape_support": {
                            "supported_inbound_dialects": ["openai-chat"],
                        },
                        "tool_support": {
                            "openai_chat": ["tools", "tool_choice"],
                        },
                    }
                },
            },
            "openai": {
                "base_url": "https://api.openai.com/v1",
                "dialect": "openai-responses",
                "api_key": "${OPENAI_API_KEY}",
                "api_key_env": "OPENAI_API_KEY",
                "key_id": "openai-local",
                "models": {
                    "gpt-5.6-sol": {
                        "model": "gpt-5.6-sol",
                        "tier": "frontier",
                        "input_modalities": ["text"],
                        "output_modalities": ["text"],
                        "input_price_per_million_usd": 0.0,
                        "output_price_per_million_usd": 0.0,
                        "pricing_source": "openai-catalog",
                        "pricing_updated_at": "2026-09-30",
                        "force_store_false": True,
                        "tool_support": {"openai_responses": ["function"]},
                        "request_shape_support": {
                            "supported_inbound_dialects": ["openai-responses", "openai-chat"],
                            "validation_status": "passed",
                            "validation_notes": "local-pi chat_to_responses bridge for coding agent tools",
                        },
                        "reasoning": {
                            "supported": True,
                            "mode": "opt_in",
                            "control": "effort_enum",
                            "default_on": False,
                        },
                    }
                },
            },
        },
        "models": {
            GROUP: {
                "strategy": "weighted",
                "targets": [
                    {
                        "provider": "kimi",
                        "model_ref": "kimi-k3",
                        "weight": 40,
                        "default_openai_chat_thinking": {"type": "disabled"},
                    },
                    {"provider": "minimax", "model_ref": "m3", "weight": 40},
                    {
                        "provider": "openai",
                        "model_ref": "gpt-5.6-sol",
                        "weight": 20,
                        "bridges": {
                            "chat_to_responses": {
                                "enabled": True,
                                "tools": True,
                                "tool_choice": True,
                                "text": True,
                            }
                        },
                    },
                ],
            }
        },
        "callers": [
            {
                "id": CALLER_ID,
                "owner_user": OWNER_USER,
                "project": PROJECT,
                "environment": "dev",
                "status": "active",
                "token_sha256": token_sha256(token),
                "token_id": public_token_id(token),
                "allow": [GROUP],
                "rate": {
                    "rpm": LOCAL_RATE_RPM,
                    "tpm": LOCAL_RATE_TPM,
                    "concurrent": LOCAL_RATE_CONCURRENT,
                },
                "quota": {
                    "day": {
                        "requests": LOCAL_QUOTA_DAY_REQUESTS,
                        "tokens": LOCAL_QUOTA_DAY_TOKENS,
                    },
                    "month": {"requests": 0, "tokens": LOCAL_QUOTA_MONTH_TOKENS},
                    "soft_pct": 80,
                },
                "key": {
                    "lifetime_tokens": LOCAL_KEY_LIFETIME_TOKENS,
                    "soft_pct": 90,
                    "on_exhaust": "disable",
                },
            }
        ],
    }


def write_yaml(path: Path, data: dict) -> None:
    try:
        import yaml  # type: ignore
    except ImportError:
        die("PyYAML is required (uv run --with pyyaml python scripts/local_pi_instance.py)")
    path.write_text(yaml.safe_dump(data, sort_keys=False), encoding="utf-8")


def reuse_or_create_identifier_key(out_dir: Path, rotate: bool) -> str:
    cfg_path = out_dir / "config.yaml"
    if cfg_path.is_file() and not rotate:
        try:
            import yaml  # type: ignore

            existing = yaml.safe_load(cfg_path.read_text(encoding="utf-8")) or {}
            key = (
                existing.get("server", {})
                .get("identifiers", {})
                .get("transform", {})
                .get("current", {})
                .get("key", "")
            )
            if isinstance(key, str) and len(key.strip()) >= 128:
                return key.strip()
        except Exception:
            pass
    return secrets.token_hex(64)


def create_token_via_ctl(ctl: list[str], repo_root: Path, config_path: Path, token_path: Path) -> str:
    if token_path.exists():
        token_path.unlink()
    run(
        ctl
        + [
            "callers",
            "generate",
            "--owner-user",
            OWNER_USER,
            "--project",
            PROJECT,
            "--env",
            "dev",
            "--allow",
            GROUP,
            "--config",
            str(config_path),
            "--write",
            "--token-out",
            str(token_path),
        ],
        cwd=repo_root,
    )
    return token_path.read_text(encoding="utf-8").strip()


def write_start_script(out_dir: Path, repo_root: Path) -> None:
    start = out_dir / "start.sh"
    start.write_text(
        "\n".join(
            [
                "#!/usr/bin/env bash",
                "set -euo pipefail",
                f'ROOT="{repo_root}"',
                'cd "$ROOT"',
                f'mkdir -p "{out_dir / "logs"}"',
                f'exec go run ./cmd/metrum-ai-router --config "{out_dir / "config.yaml"}"',
                "",
            ]
        ),
        encoding="utf-8",
    )
    os.chmod(start, 0o700)


def write_readme(out_dir: Path, listen: str) -> None:
    base = f"http://{client_listen(listen)}/v1"
    (out_dir / "README.txt").write_text(
        "\n".join(
            [
                "Local weighted coding-agent router instance.",
                "",
                f"Listen: {listen}",
                f"Client base: {base}",
                "Group: big-coder (weighted)",
                "  40% kimi / kimi-k3",
                "  40% minimax / MiniMax-M3",
                "  20% openai / gpt-5.6-sol (chat_to_responses)",
                "",
                "Caller token files (mode 0600):",
                f"  {out_dir / 'METRUM_API_KEY'}",
                f"  {out_dir / 'router.token'}",
                "Also stored as METRUM_API_KEY in env.json (not printed by the script).",
                "",
                "Start:",
                "  make local-router",
                f"  # or: {out_dir / 'start.sh'}",
                "",
                "Smoke:",
                f'  export METRUM_API_KEY="$(tr -d \'\\n\' < {out_dir / "METRUM_API_KEY"})"',
                f'  curl -fsS -H "Authorization: Bearer $METRUM_API_KEY" {base}/models',
                "",
                "Pi (optional):",
                "  unset PI_CODING_AGENT_DIR",
                "  make local-router-configure-pi",
                "",
            ]
        ),
        encoding="utf-8",
    )


def configure_pi(out_dir: Path, listen: str) -> None:
    agent_dir = Path.home() / ".pi" / "agent"
    agent_dir.mkdir(parents=True, exist_ok=True)
    token = (out_dir / "METRUM_API_KEY").read_text(encoding="utf-8").strip()
    if not token:
        die("METRUM_API_KEY file is empty; run without --configure-pi first")
    write_private(agent_dir / "metrum-api-key", token)
    base = f"http://{client_listen(listen)}/v1"
    models = {
        "providers": {
            "metrum": {
                "baseUrl": base,
                "api": "openai-completions",
                "apiKey": "!cat ~/.pi/agent/metrum-api-key",
                "authHeader": True,
                "compat": {
                    "supportsStore": False,
                    "supportsDeveloperRole": False,
                    "supportsReasoningEffort": False,
                },
                "models": [
                    {
                        "id": GROUP,
                        "name": "Local big-coder (kimi-k3 40 / MiniMax-M3 40 / gpt-5.6-sol 20)",
                        "contextWindow": 124518,
                        "maxTokens": 8192,
                        "reasoning": False,
                        "input": ["text"],
                    }
                ],
            }
        }
    }
    write_private(agent_dir / "models.json", json.dumps(models, indent=2) + "\n")
    settings_path = agent_dir / "settings.json"
    settings: dict = {}
    if settings_path.is_file():
        try:
            settings = json.loads(settings_path.read_text(encoding="utf-8"))
        except json.JSONDecodeError:
            settings = {}
    if not isinstance(settings, dict):
        settings = {}
    settings["defaultProvider"] = "metrum"
    settings["defaultModel"] = GROUP
    settings.setdefault("packages", [])
    write_private(settings_path, json.dumps(settings, indent=2) + "\n")
    print(json.dumps({"pi_agent_dir": str(agent_dir), "baseUrl": base, "model": GROUP}))


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--out-dir",
        type=Path,
        default=ROOT / "tmp" / "local-pi",
        help="gitignored output directory (default: tmp/local-pi)",
    )
    parser.add_argument(
        "--repo-root",
        type=Path,
        default=ROOT,
        help="repository root",
    )
    parser.add_argument(
        "--env-json",
        type=Path,
        default=None,
        help="source env.json for upstream keys (default: <repo-root>/env.json)",
    )
    parser.add_argument(
        "--listen",
        default="0.0.0.0:18081",
        help="router listen address (default: 0.0.0.0:18081)",
    )
    parser.add_argument(
        "--smartrouterctl",
        default="",
        help="path to metrum-ai-routerctl (optional)",
    )
    parser.add_argument(
        "--rotate-key",
        action="store_true",
        help="create a new METRUM_API_KEY even when one already exists",
    )
    parser.add_argument(
        "--rotate-identifier-key",
        action="store_true",
        help="mint a new identifiers.transform.current key",
    )
    parser.add_argument(
        "--configure-pi",
        action="store_true",
        help="point ~/.pi/agent at this local instance",
    )
    parser.add_argument(
        "--run",
        action="store_true",
        help="start the router in the foreground after writing files",
    )
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    repo_root = args.repo_root.expanduser().resolve()
    out_dir = args.out_dir.expanduser().resolve()
    env_json = (args.env_json or (repo_root / "env.json")).expanduser().resolve()
    out_dir.mkdir(parents=True, mode=0o700, exist_ok=True)

    source_env = load_json_object(env_json)
    missing = [k for k in UPSTREAM_KEYS if not (source_env.get(k) or os.environ.get(k))]
    if missing:
        die(
            "missing upstream keys in "
            f"{env_json} or process env: {', '.join(missing)}"
        )

    id_key = reuse_or_create_identifier_key(out_dir, rotate=args.rotate_identifier_key)
    ctl = resolve_cli(args.smartrouterctl, repo_root)
    config_path = out_dir / "config.yaml"
    token_path = out_dir / "METRUM_API_KEY"
    router_token_path = out_dir / "router.token"

    existing = "" if args.rotate_key else resolve_existing_token(out_dir, source_env)
    if existing:
        token = existing
        write_yaml(
            config_path,
            build_config(out_dir=out_dir, listen=args.listen, id_key_hex=id_key, token=token),
        )
        token_reused = True
    else:
        # Seed a config the ctl can merge into, then replace with the full shape.
        write_yaml(
            config_path,
            build_config(
                out_dir=out_dir,
                listen=args.listen,
                id_key_hex=id_key,
                token="rtr_metrum_placeholder_example-project_dev_k00000000_PLACEHOLDER",
            ),
        )
        tmp_token = out_dir / ".metrum-api-key.new"
        token = create_token_via_ctl(ctl, repo_root, config_path, tmp_token)
        write_yaml(
            config_path,
            build_config(out_dir=out_dir, listen=args.listen, id_key_hex=id_key, token=token),
        )
        tmp_token.unlink(missing_ok=True)
        token_reused = False

    write_private(token_path, token)
    write_private(router_token_path, token)

    local_env = {key: (source_env.get(key) or os.environ.get(key, "")) for key in UPSTREAM_KEYS}
    local_env["METRUM_API_KEY"] = token
    write_private(out_dir / "env.json", json.dumps(local_env, indent=2) + "\n")

    write_start_script(out_dir, repo_root)
    write_readme(out_dir, args.listen)

    # Do not print bearer tokens or values derived from them on stdout.
    summary = {
        "schema": "metrum.ai/smartrouter-local-pi-instance/v1",
        "out_dir": str(out_dir),
        "config": str(config_path),
        "listen": args.listen,
        "group": GROUP,
        "targets": [
            {"provider": "kimi", "model": "kimi-k3", "weight": 40},
            {"provider": "minimax", "model": "MiniMax-M3", "weight": 40},
            {"provider": "openai", "model": "gpt-5.6-sol", "weight": 20},
        ],
        "token_file": str(token_path),
        "token_reused": token_reused,
        "caller_id": CALLER_ID,
        "start": str(out_dir / "start.sh"),
    }
    print(json.dumps(summary, indent=2))
    print(
        "Wrote local instance files. Raw METRUM_API_KEY is only in mode-0600 files "
        "under the out-dir (and env.json).",
        file=sys.stderr,
    )

    if args.configure_pi:
        configure_pi(out_dir, args.listen)

    if args.run:
        start = out_dir / "start.sh"
        os.execv(start, [str(start)])


if __name__ == "__main__":
    main()
