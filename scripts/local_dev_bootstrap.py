#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Prepare a local/dev router directory: env template and one caller.

This script composes existing CLIs. It does not add Fleet or signing authority
to metrum-ai-routerctl. Do not commit the output directory.

Runtime licensing was removed in 3.0.0. Generated config omits server.license.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_CONFIG = ROOT / "config.minimal.example.yaml"
DEFAULT_ENV = ROOT / "env.minimal.example.json"

SECRET_BASENAMES = frozenset(
    {
        "env.json",
        "router.token",
    }
)

_LICENSE_BLOCK = re.compile(
    r"(?m)^[ \t]*#.*license\.json.*\n"  # optional preceding comment
    r"^[ \t]*license:\n"
    r"(?:^[ \t]+.*\n|^[ \t]*\n)*",
)


def die(message: str, code: int = 2) -> None:
    print(message, file=sys.stderr)
    raise SystemExit(code)


def run(cmd: list[str], cwd: Path | None = None) -> str:
    env = os.environ.copy()
    env.pop("OPENAI_API_KEY", None)
    result = subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True)
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip()
        die(f"command failed ({result.returncode}): {' '.join(cmd)}\n{detail}")
    return result.stdout


def omit_server_license(config_text: str) -> str:
    """Drop server.license (and its preceding comment) from generated YAML."""
    if "\n  license:\n" not in config_text and not config_text.startswith("  license:\n"):
        # Tolerate templates that already omit the block.
        return config_text
    stripped, count = _LICENSE_BLOCK.subn("", config_text, count=1)
    if count != 1:
        die("minimal config is missing an expected server.license block to omit")
    return stripped


def rewrite_local_paths(config_text: str, out_dir: Path) -> str:
    replacements = {
        "path: usage.sqlite": f"path: {out_dir / 'usage.sqlite'}",
        "path: requests.jsonl": f"path: {out_dir / 'requests.jsonl'}",
        "state_path: router-state.json": f"state_path: {out_dir / 'router-state.json'}",
    }
    for old, new in replacements.items():
        if old not in config_text:
            die(f"minimal config is missing expected line {old!r}")
        config_text = config_text.replace(old, new, 1)
    return config_text


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out-dir", type=Path, required=True, help="gitignored output directory")
    parser.add_argument("--force", action="store_true", help="replace an existing out-dir")
    parser.add_argument("--repo-root", type=Path, default=ROOT)
    parser.add_argument("--smartrouterctl", default="", help="path to metrum-ai-routerctl")
    parser.add_argument("--owner-user", default="local-dev")
    parser.add_argument("--project", default="example-project")
    parser.add_argument("--allow", default="local")
    return parser.parse_args()


def resolve_cli(explicit: str, names: tuple[str, ...], repo_root: Path) -> str:
    if explicit:
        return explicit
    for name in names:
        found = shutil.which(name)
        if found:
            return found
    go_bin = shutil.which("go")
    if not go_bin:
        die(f"missing {' or '.join(names)}; install Go or pass --smartrouterctl")
    return go_bin


def ctl_argv(cli: str, repo_root: Path) -> list[str]:
    if cli.endswith("go") or Path(cli).name == "go":
        return [cli, "run", str(repo_root / "cmd" / "metrum-ai-routerctl")]
    return [cli]


def main() -> None:
    args = parse_args()
    repo_root = args.repo_root.resolve()
    out_dir = args.out_dir.expanduser().resolve()
    if out_dir.exists():
        if not args.force:
            die(f"{out_dir} already exists; pass --force to replace it")
        shutil.rmtree(out_dir)
    out_dir.mkdir(parents=True, mode=0o700)

    config_src = repo_root / "config.minimal.example.yaml"
    env_src = repo_root / "env.minimal.example.json"
    for path in (config_src, env_src):
        if not path.is_file():
            die(f"missing template {path}")

    config_path = out_dir / "config.yaml"
    env_path = out_dir / "env.json"
    token_path = out_dir / "router.token"

    generated = omit_server_license(config_src.read_text(encoding="utf-8"))
    generated = rewrite_local_paths(generated, out_dir)
    if re.search(r"(?m)^[ \t]*license:", generated):
        die("generated config still contains a license key")
    config_path.write_text(generated, encoding="utf-8")
    shutil.copyfile(env_src, env_path)
    os.chmod(env_path, 0o600)

    ctl = resolve_cli(args.smartrouterctl, ("metrum-ai-routerctl", "smartrouterctl"), repo_root)

    grant = run(
        ctl_argv(ctl, repo_root)
        + [
            "callers",
            "generate",
            "--owner-user",
            args.owner_user,
            "--project",
            args.project,
            "--env",
            "dev",
            "--allow",
            args.allow,
            "--config",
            str(config_path),
            "--write",
            "--token-out",
            str(token_path),
        ]
    )
    grant_json = json.loads(grant)
    token_name = Path(grant_json.get("token_file") or token_path.name).name
    if token_name != token_path.name:
        die("unexpected token_file in caller grant output")

    next_steps = out_dir / "NEXT_STEPS.txt"
    next_steps.write_text(
        "\n".join(
            [
                "Local/dev bootstrap complete. Keep this directory out of git.",
                "Fill OPENAI_API_KEY in env.json (empty placeholder only).",
                f"Start: go run ./cmd/metrum-ai-router --config {config_path}",
                f"Caller token file (mode 0600): {token_path}",
                "Do not paste the token into tickets, chat, or public docs.",
                "curl -fsS http://127.0.0.1:8080/readyz",
                'curl -fsS -H "Authorization: Bearer $(tr -d \'\\n\' < '
                + str(token_path)
                + ')" http://127.0.0.1:8080/v1/models',
                "",
            ]
        ),
        encoding="utf-8",
    )

    printed = {
        "schema": "metrum.ai/smartrouter-local-dev-bootstrap/v1",
        "out_dir": str(out_dir),
        "config": str(config_path),
        "env": str(env_path),
        "token_file": str(token_path),
        "caller_id": grant_json.get("caller_id"),
        "token_id": grant_json.get("token_id"),
        "activation": grant_json.get("activation"),
        "next_steps": str(next_steps),
    }
    for key, value in printed.items():
        if key in {"env", "config"}:
            continue
        text = str(value)
        if any(name in text for name in SECRET_BASENAMES) and key not in {"token_file", "next_steps", "out_dir"}:
            continue
    print(json.dumps(printed, indent=2))
    print(
        "Wrote local/dev files. Put OPENAI_API_KEY in env.json. "
        "The raw caller token is only in the mode-0600 token file.",
        file=sys.stderr,
    )


if __name__ == "__main__":
    main()
