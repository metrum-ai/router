#!/usr/bin/env -S uv run --with pyyaml python
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Create or refresh a local weighted coding-agent router instance.

Default shape matches the local pi setup:
  listen 0.0.0.0:18081
  group big-coder only (weighted)
    70% openai / gpt-6-luna (Responses via chat_to_responses)
    30% fireworks / accounts/fireworks/models/deepseek-v4p1-flash
  server.responses.model_identity: requested_group
    caller-facing responses report model "big-coder"; usage rows, attempt
    rows and admin reports keep the serving upstream model

Upstream keys are copied from a source env.json (repo env.json by default).
The local caller token is stored as METRUM_API_KEY (file + out-dir env.json)
and reused when already present unless --rotate-key is set.

Optional d3-auto group (--d3-policy-url, issue #278): strategy external through
examples/external-routing-policy/d3_policy.py. By default tier cheap ->
fireworks deepseek-v4p1-flash and tier strong -> openai gpt-6-luna.
--d3-cheap-model / --d3-strong-model pick either tier from TIER_MODELS
(issue #287), for example --d3-strong-model gpt-6.1-sol; the chosen models are
added to the provider catalog with their list prices, so usage rows carry real
costs. --d3-tier-groups also adds single-target d3-auto-cheap and
d3-auto-strong groups for always-cheap and always-strong cost baselines. The
d3 groups are added to the caller allow list and to the pi model list. Without
--d3-policy-url the generated config is unchanged.

Output stays under a gitignored directory (default tmp/local-pi).
Run with: uv run --with pyyaml python scripts/local_pi_instance.py
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import secrets
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[1]

UPSTREAM_KEYS = ("FIREWORKS_API_KEY", "OPENAI_API_KEY")
ADMIN_HASH_ENV = "LOCAL_PI_ADMIN_PASSWORD_HASH"
# Demo-only default login: admin/admin. Override with ADMIN_HASH_ENV in env.json.
DEMO_ADMIN_PASSWORD_HASH = "$2b$12$o8v1ntt3UOcyHUoCRKF4.eYHga/4SHaiGlOTrw8YEcp97knODcP/m"
CALLER_ID = "local-dev-example-project-dev"
OWNER_USER = "local-dev"
PROJECT = "example-project"
GROUP = "big-coder"
D3_GROUP = "d3-auto"
D3_POLICY_MODES = ("shadow", "enforce", "baseline")
D3_ON_ERROR = ("fail_closed", "fallback")
D3_TIER_SUFFIXES = ("cheap", "strong")
MODEL_IDENTITY = "requested_group"


# Provider model catalog (Metrum AI Router local instance). Each entry is the
# provider "models" block for one model_ref; list prices are USD per 1M tokens.
# big-coder always uses gpt-6-luna and deepseek-v4p1-flash; the d3-auto tiers
# can use any entry (issue #287).
OPENAI_REASONING_OPT_IN = {"supported": True, "mode": "opt_in", "control": "effort_enum", "default_on": False}
OPENAI_TOOL_SHAPES = {
    "force_store_false": True,
    "tool_support": {"openai_responses": ["function"]},
    "request_shape_support": {
        "supported_inbound_dialects": ["openai-responses", "openai-chat"],
        "validation_status": "passed",
        "validation_notes": "local-pi chat_to_responses bridge for coding agent tools",
    },
}
FIREWORKS_CHAT_SHAPES = {
    "request_shape_support": {"supported_inbound_dialects": ["openai-chat"]},
    "tool_support": {"openai_chat": ["tools"]},
}
TIER_MODELS: dict[str, tuple[str, dict]] = {
    "deepseek-v4p1-flash": ("fireworks", {
        "model": "accounts/fireworks/models/deepseek-v4p1-flash",
        "tier": "coding",
        "context_tokens": 1000000,
        "input_modalities": ["text"],
        "output_modalities": ["text"],
        "input_price_per_million_usd": 0.22,
        "output_price_per_million_usd": 0.66,
        "pricing_source": "https://fireworks.ai/models/deepseek-ai/deepseek-v4p1-flash",
        "pricing_updated_at": "2026-10-01",
        **FIREWORKS_CHAT_SHAPES,
    }),
    "glm-5p3": ("fireworks", {
        "model": "accounts/fireworks/models/glm-5p3",
        "tier": "coding",
        "context_tokens": 1040000,
        "input_modalities": ["text"],
        "output_modalities": ["text"],
        "input_price_per_million_usd": 1.40,
        "cached_input_price_per_million_usd": 0.26,
        "output_price_per_million_usd": 4.40,
        "pricing_source": "https://fireworks.ai/models/fireworks/glm-5p3",
        "pricing_updated_at": "2026-10-11",
        **FIREWORKS_CHAT_SHAPES,
    }),
    "gpt-6-luna": ("openai", {
        "model": "gpt-6-luna",
        "tier": "frontier",
        "input_modalities": ["text"],
        "output_modalities": ["text"],
        "input_price_per_million_usd": 0.10,
        "cached_input_price_per_million_usd": 0.01,
        "output_price_per_million_usd": 0.50,
        "pricing_source": "https://developers.openai.com/api/docs/models/gpt-6-luna",
        "pricing_updated_at": "2026-10-07",
        "pricing_notes": (
            "Standard short-context rates. Prompts over 272K input "
            "tokens are 2x input and cache rates and 1.5x output."
        ),
        **OPENAI_TOOL_SHAPES,
        "reasoning": dict(OPENAI_REASONING_OPT_IN),
    }),
    "gpt-6.1-sol": ("openai", {
        "model": "gpt-6.1-sol",
        "tier": "frontier",
        "context_tokens": 1050000,
        "input_modalities": ["text"],
        "output_modalities": ["text"],
        "input_price_per_million_usd": 2.00,
        "cached_input_price_per_million_usd": 0.10,
        "output_price_per_million_usd": 10.00,
        "pricing_source": "https://developers.openai.com/api/docs/models/gpt-6.1-sol",
        "pricing_updated_at": "2026-10-11",
        "pricing_notes": (
            "Standard rates. Prompts over 272K input tokens are 2x input and "
            "cache rates and 1.5x output for the full request; cache writes are "
            "1.25x the uncached input rate. Default reasoning effort is medium; "
            "tool calling needs the Responses API (chat_to_responses bridge)."
        ),
        **OPENAI_TOOL_SHAPES,
        "reasoning": dict(OPENAI_REASONING_OPT_IN),
    }),
    "gpt-5.6-sol": ("openai", {
        "model": "gpt-5.6-sol",
        "tier": "frontier",
        "context_tokens": 1050000,
        "input_modalities": ["text"],
        "output_modalities": ["text"],
        "input_price_per_million_usd": 4.00,
        "cached_input_price_per_million_usd": 0.40,
        "output_price_per_million_usd": 20.00,
        "pricing_source": "https://developers.openai.com/api/docs/models/gpt-5.6-sol",
        "pricing_updated_at": "2026-10-11",
        "pricing_notes": (
            "Standard rates. Prompts over 272K input tokens are 2x input and "
            "1.5x output for the full request; cache writes are 1.25x the "
            "uncached input rate."
        ),
        **OPENAI_TOOL_SHAPES,
        "reasoning": dict(OPENAI_REASONING_OPT_IN),
    }),
}
D3_DEFAULT_TIERS = {"cheap": "deepseek-v4p1-flash", "strong": "gpt-6-luna"}
BIG_CODER_MODELS = ("deepseek-v4p1-flash", "gpt-6-luna")

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
    """Return the public token_id of a structured rtr_metrum_ token.

    Mirrors publicTokenID in internal/router/token.go: the first six
    "_"-separated parts (rtr, metrum, user, project, env, key). The secret
    suffix is base64url and may itself contain "_", so it must never be
    split from the right.
    """
    token = token.strip()
    if not token.startswith("rtr_metrum_"):
        return token
    parts = token.split("_")
    if len(parts) < 7:
        return token
    return "_".join(parts[:6])


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


CALLER_TOKEN_HASH_ITERATIONS = 20000


def caller_token_hash(caller_token: str) -> str:
    """Return a router token_hash verifier for a bearer token.

    Matches internal/router/caller_token_hash.go: PBKDF2-HMAC-SHA3-512 with a
    random 32-byte salt and a 64-byte key, rendered as
    pbkdf2-sha3-512:i=<iterations>:<salt>:<key> with unpadded base64url.
    """
    salt = secrets.token_bytes(32)
    key = hashlib.pbkdf2_hmac(
        "sha3_512", caller_token.strip().encode("utf-8"), salt, CALLER_TOKEN_HASH_ITERATIONS, 64
    )

    def b64(raw: bytes) -> str:
        return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")

    return f"pbkdf2-sha3-512:i={CALLER_TOKEN_HASH_ITERATIONS}:{b64(salt)}:{b64(key)}"


def caller_token_fields(caller_token: str) -> dict[str, str]:
    """Return the caller verifier and token_id config fields for a token.

    Structured rtr_metrum_ tokens get token_hash, which the router finds by
    its public token_id prefix. Any other token keeps the deprecated
    token_sha256 field, because token_hash lookup needs that prefix.
    """
    token_id = public_token_id(caller_token)
    if token_id != caller_token.strip() and caller_token.strip().startswith(token_id + "_"):
        return {"token_hash": caller_token_hash(caller_token), "token_id": token_id}
    return {"token_sha256": token_sha256(caller_token), "token_id": token_id}


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


def openai_luna_target(**extra: object) -> dict:
    return openai_target("gpt-6-luna", **extra)


def openai_target(model_ref: str, **extra: object) -> dict:
    return {
        "provider": "openai",
        "model_ref": model_ref,
        **extra,
        "bridges": {
            "chat_to_responses": {
                "enabled": True,
                "tools": True,
                "tool_choice": True,
                "text": True,
            }
        },
    }


def tier_target(model_ref: str, **extra: object) -> dict:
    """Group target for a TIER_MODELS entry; OpenAI models use the chat_to_responses bridge."""
    provider = TIER_MODELS[model_ref][0]
    if provider == "openai":
        return openai_target(model_ref, **extra)
    return {"provider": provider, "model_ref": model_ref, **extra}


def d3_tiers(d3_policy: dict | None) -> dict[str, str]:
    tiers = dict(D3_DEFAULT_TIERS)
    if d3_policy:
        tiers["cheap"] = d3_policy.get("cheap_model") or tiers["cheap"]
        tiers["strong"] = d3_policy.get("strong_model") or tiers["strong"]
    return tiers


def d3_group(
    policy_url: str,
    mode: str,
    on_error: str,
    timeout_ms: int,
    cheap_model: str = D3_DEFAULT_TIERS["cheap"],
    strong_model: str = D3_DEFAULT_TIERS["strong"],
    tier_groups: bool = False,
) -> dict:
    """d3-auto: external policy (d3_policy.py) choosing cheap vs strong (#278, #287)."""
    del tier_groups  # handled by build_config
    host = urlparse(policy_url).hostname or ""
    return {
        "strategy": "external",
        "external_policy": {
            "url": policy_url,
            "allow_hosts": [host],
            "include_request": True,
            "timeout_ms": timeout_ms,
            "max_response_bytes": 65536,
            "mode": mode,
            "on_error": on_error,
        },
        "targets": [
            tier_target(cheap_model, tier="cheap", weight=1),
            tier_target(strong_model, tier="strong", weight=1),
        ],
    }


def group_names(d3_policy: dict | None) -> list[str]:
    if not d3_policy:
        return [GROUP]
    names = [GROUP, D3_GROUP]
    if d3_policy.get("tier_groups"):
        names += [f"{D3_GROUP}-{suffix}" for suffix in D3_TIER_SUFFIXES]
    return names


def provider_models(d3_policy: dict | None) -> dict[str, dict]:
    """Provider -> models block: big-coder's models plus the selected d3 tier models."""
    refs = list(BIG_CODER_MODELS)
    if d3_policy:
        refs += [ref for ref in d3_tiers(d3_policy).values() if ref not in refs]
    out: dict[str, dict] = {"fireworks": {}, "openai": {}}
    for ref in refs:
        provider, entry = TIER_MODELS[ref]
        out[provider][ref] = json.loads(json.dumps(entry))
    return out


def build_config(
    *,
    out_dir: Path,
    listen: str,
    id_key_hex: str,
    token: str,
    d3_policy: dict | None = None,
) -> dict:
    models = {
        GROUP: {
            "strategy": "weighted",
            "targets": [
                openai_luna_target(weight=70),
                {
                    "provider": "fireworks",
                    "model_ref": "deepseek-v4p1-flash",
                    "weight": 30,
                },
            ],
        }
    }
    if d3_policy:
        models[D3_GROUP] = d3_group(**d3_policy)
        if d3_policy.get("tier_groups"):
            # Always-cheap / always-strong baselines for cost evaluation (#287).
            for suffix, ref in d3_tiers(d3_policy).items():
                models[f"{D3_GROUP}-{suffix}"] = {
                    "strategy": "weighted",
                    "targets": [tier_target(ref, tier=suffix, weight=1)],
                }
    catalog = provider_models(d3_policy)
    config = {
        "server": {
            "listen": listen,
            "default_model_group": GROUP,
            # Callers see the group name; operators keep the upstream in usage rows.
            "responses": {"model_identity": MODEL_IDENTITY},
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
            "admin_auth": {
                "basic": {
                    "enabled": True,
                    "realm": "Local Router Admin",
                    "allow_insecure_http": True,
                    "users": [{
                        "username": "admin",
                        "password_hash_env": ADMIN_HASH_ENV,
                        "subject": "basic:admin",
                        "domain": "example-project/dev",
                        "permissions": ["admin:auth:read"],
                    }],
                },
                "authorization": {
                    "enabled": True,
                    "source": "static",
                    "policy": [
                        "p, basic:admin, example-project/dev, admin:reports, read|export|drilldown",
                        "p, basic:admin, example-project/dev, admin:security_reports, read|export"
                    ],
                },
            },
            "admin_reports": {
                "enabled": True,
                "default_since": "24h",
                "max_range": "31d",
                "max_rows": 500,
                "security": {"enabled": True},
            },
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
            "fireworks": {
                "base_url": "https://api.fireworks.ai/inference/v1",
                "dialect": "openai-chat",
                "auth_scheme": "bearer",
                "api_key": "${FIREWORKS_API_KEY}",
                "api_key_env": "FIREWORKS_API_KEY",
                "key_id": "fireworks-local",
                "models": catalog["fireworks"],
            },
            "openai": {
                "base_url": "https://api.openai.com/v1",
                "dialect": "openai-responses",
                "api_key": "${OPENAI_API_KEY}",
                "api_key_env": "OPENAI_API_KEY",
                "key_id": "openai-local",
                "models": catalog["openai"],
            },
        },
        "models": models,
        "callers": [
            {
                "id": CALLER_ID,
                "owner_user": OWNER_USER,
                "project": PROJECT,
                "environment": "dev",
                "status": "active",
                **caller_token_fields(token),
                "allow": group_names(d3_policy),
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
    if d3_policy:
        # Shadow agreement and policy latency come from request_policy_executions.
        config["server"]["decision_telemetry"] = {"enabled": True}
    return config


def write_yaml(path: Path, data: dict) -> None:
    try:
        import yaml  # type: ignore
    except ImportError:
        die("PyYAML is required (uv run --with pyyaml python scripts/local_pi_instance.py)")
    if path.exists():
        # Exclusive creation prevents same-second or concurrent backup collisions.
        fd, backup = tempfile.mkstemp(prefix=path.name + ".bak.", dir=path.parent)
        with os.fdopen(fd, "wb") as dest:
            dest.write(path.read_bytes())
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
                f'make local-router-build LOCAL_ROUTER_OUT="{out_dir}"',
                f'exec "{out_dir / "metrum-ai-router"}" --config "{out_dir / "config.yaml"}"',
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
                "Group: big-coder (weighted; only group)",
                "  70% openai / gpt-6-luna (chat_to_responses)",
                "  30% fireworks / accounts/fireworks/models/deepseek-v4p1-flash",
                f"Model identity: {MODEL_IDENTITY} (responses report model: big-coder;",
                "  usage.sqlite request_usage.target_model keeps the serving upstream)",
                "",
                f"Admin reports: http://{client_listen(listen)}/admin/reports/",
                "Demo login: admin / admin (override LOCAL_PI_ADMIN_PASSWORD_HASH in env.json)",
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


def pi_models(d3_policy: dict | None) -> list[dict]:
    models = [
        {
            "id": GROUP,
            "name": "Local big-coder (gpt-6-luna 70 / deepseek-v4p1-flash 30)",
            "contextWindow": 124518,
            "maxTokens": 8192,
            "reasoning": False,
            "input": ["text"],
        }
    ]
    if d3_policy:
        tiers = d3_tiers(d3_policy)
        models.append(
            {
                "id": D3_GROUP,
                "name": f"Local d3-auto (d3 policy: {tiers['cheap']} cheap / {tiers['strong']} strong)",
                "contextWindow": 124518,
                "maxTokens": 8192,
                "reasoning": False,
                "input": ["text"],
            }
        )
    return models


def configure_pi(out_dir: Path, listen: str, d3_policy: dict | None = None) -> None:
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
                "models": pi_models(d3_policy),
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
        "--d3-policy-url",
        default="",
        help="add the d3-auto group using this d3_policy.py /route URL (e.g. http://127.0.0.1:18302/route)",
    )
    parser.add_argument(
        "--d3-policy-mode",
        default="shadow",
        choices=D3_POLICY_MODES,
        help="external_policy.mode for d3-auto (default: shadow)",
    )
    parser.add_argument(
        "--d3-policy-on-error",
        default="fail_closed",
        choices=D3_ON_ERROR,
        help="external_policy.on_error for d3-auto (default: fail_closed)",
    )
    parser.add_argument(
        "--d3-policy-timeout-ms",
        type=int,
        default=300,
        help="external_policy.timeout_ms for d3-auto (default: 300)",
    )
    parser.add_argument(
        "--d3-cheap-model",
        default=D3_DEFAULT_TIERS["cheap"],
        choices=sorted(TIER_MODELS),
        help=f"model_ref for the d3-auto cheap tier (default: {D3_DEFAULT_TIERS['cheap']})",
    )
    parser.add_argument(
        "--d3-strong-model",
        default=D3_DEFAULT_TIERS["strong"],
        choices=sorted(TIER_MODELS),
        help=f"model_ref for the d3-auto strong tier (default: {D3_DEFAULT_TIERS['strong']})",
    )
    parser.add_argument(
        "--d3-tier-groups",
        action="store_true",
        help="also add single-target d3-auto-cheap and d3-auto-strong groups (cost baselines)",
    )
    parser.add_argument(
        "--run",
        action="store_true",
        help="start the router in the foreground after writing files",
    )
    parser.add_argument("--force", action="store_true", help="regenerate config (backs up existing file); discards manual edits")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    repo_root = args.repo_root.expanduser().resolve()
    out_dir = args.out_dir.expanduser().resolve()
    env_json = (args.env_json or (repo_root / "env.json")).expanduser().resolve()
    out_dir.mkdir(parents=True, mode=0o700, exist_ok=True)

    config_path = out_dir / "config.yaml"
    if config_path.exists() and not args.force:
        if args.rotate_key or args.rotate_identifier_key:
            die("rotation regenerates config; pass --force after reviewing existing edits")
        import yaml
        current = yaml.safe_load(config_path.read_text())
        listen = current["server"]["listen"]
        if args.configure_pi:
            configure_pi(out_dir, listen)
        print(json.dumps({"config": str(config_path), "listen": listen, "token_reused": True}))
        print("Preserved existing local config (use --force to regenerate).", file=sys.stderr)
        if args.run:
            start = out_dir / "start.sh"
            os.execv(start, [str(start)])
        return
    if args.configure_pi:
        die("no existing config; run local-router-setup before configure-pi")

    source_env = load_json_object(env_json)
    missing = [k for k in UPSTREAM_KEYS if not (source_env.get(k) or os.environ.get(k))]
    if missing:
        die(
            "missing upstream keys in "
            f"{env_json} or process env: {', '.join(missing)}"
        )

    d3_policy = None
    if args.d3_policy_url:
        if urlparse(args.d3_policy_url).scheme not in ("http", "https"):
            die("--d3-policy-url must be an http(s) URL")
        d3_policy = {
            "policy_url": args.d3_policy_url,
            "mode": args.d3_policy_mode,
            "on_error": args.d3_policy_on_error,
            "timeout_ms": args.d3_policy_timeout_ms,
            "cheap_model": args.d3_cheap_model,
            "strong_model": args.d3_strong_model,
            "tier_groups": args.d3_tier_groups,
        }
        if args.d3_cheap_model == args.d3_strong_model:
            die("--d3-cheap-model and --d3-strong-model must differ")

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
            build_config(
                out_dir=out_dir, listen=args.listen, id_key_hex=id_key, token=token, d3_policy=d3_policy
            ),
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
                d3_policy=d3_policy,
            ),
        )
        tmp_token = out_dir / ".metrum-api-key.new"
        token = create_token_via_ctl(ctl, repo_root, config_path, tmp_token)
        write_yaml(
            config_path,
            build_config(
                out_dir=out_dir, listen=args.listen, id_key_hex=id_key, token=token, d3_policy=d3_policy
            ),
        )
        tmp_token.unlink(missing_ok=True)
        token_reused = False

    write_private(token_path, token)
    write_private(router_token_path, token)

    local_env = {key: (source_env.get(key) or os.environ.get(key, "")) for key in UPSTREAM_KEYS}
    local_env[ADMIN_HASH_ENV] = (
        source_env.get(ADMIN_HASH_ENV)
        or os.environ.get(ADMIN_HASH_ENV)
        or DEMO_ADMIN_PASSWORD_HASH
    )
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
        "groups": group_names(d3_policy),
        "model_identity": MODEL_IDENTITY,
        "targets": [
            {"provider": "openai", "model": "gpt-6-luna", "weight": 70},
            {
                "provider": "fireworks",
                "model": "accounts/fireworks/models/deepseek-v4p1-flash",
                "weight": 30,
            },
        ],
        "d3_tiers": (
            {
                tier: {"provider": TIER_MODELS[ref][0], "model_ref": ref}
                for tier, ref in d3_tiers(d3_policy).items()
            }
            if d3_policy
            else None
        ),
        "token_file": str(token_path),
        "token_reused": token_reused,
        "caller_id": CALLER_ID,
        "admin_reports_url": f"http://{client_listen(args.listen)}/admin/reports/",
        "start": str(out_dir / "start.sh"),
    }
    print(json.dumps(summary, indent=2))
    print(
        "Wrote local instance files. Raw METRUM_API_KEY is only in mode-0600 files "
        "under the out-dir (and env.json).",
        file=sys.stderr,
    )

    if args.run:
        start = out_dir / "start.sh"
        os.execv(start, [str(start)])


if __name__ == "__main__":
    main()
