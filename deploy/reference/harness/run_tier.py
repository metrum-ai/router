#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Collect one committed run for a deploy/reference tier.

Creates `results/<YYYY-MM-DD>-<commit>/`, runs the functional suite and the
concurrency sweep, and writes `ENV.md` from what the host reports. The result
directory is what the tier README cites instead of "pending".

Requirements on the host: the tier deployed from its own `compose.yaml`, reachable
router and model-server URLs, `ROUTER_TOKEN` in the environment, and `RUN_COMMIT`
set to the commit the deployment was built from (defaults to `git rev-parse HEAD`
of this checkout).

Usage:
  RUN_COMMIT=$(git rev-parse HEAD) ROUTER_TOKEN=... \\
    python3 deploy/reference/harness/run_tier.py nvidia-datacenter
"""

from __future__ import annotations

import argparse
import json
import os
import platform
import re
import shutil
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
REFERENCE = ROOT / "deploy" / "reference"
HARNESS = Path(__file__).resolve().parent


def run(args: list[str], timeout: int = 1800) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env.pop("OPENAI_API_KEY", None)
    return subprocess.run(args, capture_output=True, text=True, timeout=timeout, env=env, check=False)


def command_output(args: list[str]) -> str:
    if shutil.which(args[0]) is None:
        return f"{args[0]}: not present on this host"
    result = run(args, timeout=120)
    text = (result.stdout or result.stderr or "").strip()
    return text if result.returncode == 0 else f"{' '.join(args)} failed: {text[:400]}"


def git_commit() -> str:
    commit = os.environ.get("RUN_COMMIT", "").strip()
    if commit:
        return commit
    result = run(["git", "-C", str(ROOT), "rev-parse", "HEAD"], timeout=60)
    return result.stdout.strip() if result.returncode == 0 else "unknown"


def locked_images(tier: Path) -> list[str]:
    lock = tier / "images.lock"
    lines: list[str] = []
    for line in lock.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if stripped.startswith("image:"):
            lines.append(stripped.removeprefix("image:").strip())
        elif stripped.startswith("digest:") and lines:
            lines[-1] = f"{lines[-1]}@{stripped.removeprefix('digest:').strip()}"
        elif stripped.startswith("- id:"):
            lines.append(f"model {stripped.removeprefix('- id:').strip()}")
        elif stripped.startswith("revision:") and lines and lines[-1].startswith("model "):
            lines[-1] = f"{lines[-1]} @ {stripped.removeprefix('revision:').strip()}"
    return lines


def environment_markdown(tier: Path, commit: str, base_url: str, sweep: bool) -> str:
    accelerator = command_output(["nvidia-smi", "-L"])
    rocm = command_output(["rocminfo"])
    if len(rocm) > 4000:
        rocm = rocm[:4000] + "\n... (truncated)"
    rows = [
        ("Tier", tier.name),
        ("Commit", commit),
        ("Recorded at (UTC)", datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")),
        ("Router base URL", base_url),
        ("Kernel", platform.platform()),
        ("CPU", command_output(["bash", "-c", "nproc"]) + " logical CPUs"),
        ("Memory", command_output(["bash", "-c", "awk '/MemTotal/ {printf \"%.0f GiB\", $2/1048576}' /proc/meminfo"])),
        ("Docker", command_output(["docker", "version", "--format", "{{.Server.Version}}"])),
        ("NVIDIA", accelerator),
        ("ROCm", rocm),
        ("Concurrency sweep", "included (sweep.json)" if sweep else "not run for this collection"),
    ]
    table = "\n".join(f"| {key} | {value.replace('|', '/')} |" for key, value in rows)
    images = "\n".join(f"- `{line}`" for line in locked_images(tier))
    return f"""<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Run environment: {tier.name}

| Item | Value |
|---|---|
{table}

## Pinned inputs

{images}

## What this run is not

One box, one commit, one configuration. It says nothing about a different card,
a different model revision or a different context length. Anything the sweep did
not reach is reported as the highest zero-failure worker count, not extrapolated.
"""


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("tier")
    parser.add_argument("--base-url", default=os.environ.get("ROUTER_BASE_URL", "http://127.0.0.1:8080"))
    parser.add_argument("--group", action="append", dest="groups", default=None,
                        help="local group for the functional suite (repeatable)")
    parser.add_argument("--sweep-group", default="local-chat")
    parser.add_argument("--direct-port", type=int, default=int(os.environ.get("DIRECT_VLLM_PORT", "8001")))
    parser.add_argument("--skip-sweep", action="store_true")
    parser.add_argument("--out-root", type=Path, default=None, help="override deploy/reference/<tier>/results")
    args = parser.parse_args(argv)

    tier = REFERENCE / args.tier
    if not tier.is_dir():
        print(f"unknown tier {args.tier}; expected one of: {', '.join(sorted(p.name for p in REFERENCE.iterdir() if p.is_dir() and p.name != 'harness'))}", file=sys.stderr)
        return 2
    commit = git_commit()
    stamp = datetime.now(timezone.utc).strftime("%Y-%m-%d")
    out_root = args.out_root or (tier / "results")
    run_dir = out_root / f"{stamp}-{commit[:12]}"
    if run_dir.exists():
        print(f"{run_dir} already exists; a run directory is never reused", file=sys.stderr)
        return 2
    run_dir.mkdir(parents=True, mode=0o755)

    env = {"RUN_COMMIT": commit, "ROUTER_BASE_URL": args.base_url}
    if not os.environ.get("ROUTER_TOKEN"):
        print("ROUTER_TOKEN is required", file=sys.stderr)
        return 2
    env["ROUTER_TOKEN"] = os.environ["ROUTER_TOKEN"]

    python = sys.executable
    functional = [python, str(HARNESS / "functional_smoke.py"), args.tier,
                  "--base-url", args.base_url, "--out", str(run_dir / "functional.json")]
    for group in args.groups or []:
        functional += ["--group", group]
    print("running the functional suite", file=sys.stderr)
    result = subprocess.run(functional, env={**os.environ, **env}, check=False)
    if result.returncode != 0:
        print("functional suite failed; the run directory keeps its partial evidence and is not a pass", file=sys.stderr)

    sweep_ok = False
    if not args.skip_sweep:
        sweep = [python, str(HARNESS / "concurrency_sweep.py"), args.tier,
                 "--base-url", args.base_url, "--group", args.sweep_group,
                 "--direct-port", str(args.direct_port), "--out-dir", str(run_dir)]
        print("running the concurrency sweep", file=sys.stderr)
        sweep_result = subprocess.run(sweep, env={**os.environ, **env}, check=False)
        sweep_ok = sweep_result.returncode == 0

    (run_dir / "ENV.md").write_text(environment_markdown(tier, commit, args.base_url, sweep_ok), encoding="utf-8")
    print(json.dumps({"run_dir": str(run_dir), "commit": commit, "sweep": sweep_ok,
                      "functional_passed": result.returncode == 0}, indent=2))
    if result.returncode != 0:
        return result.returncode
    if not args.skip_sweep and not sweep_ok:
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
