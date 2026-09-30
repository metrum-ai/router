#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Shadeform helpers for serving OpenJev (discover / create / wait / print SSH).

Never prints API keys. OpenJev weights are CC BY-NC 4.0.
Prefer RTXPro6000 (Blackwell Server Edition ~96GB) when available.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

API = "https://api.shadeform.ai/v1"
ROOT = Path(__file__).resolve().parents[1]


def load_api_key() -> str:
    v = os.environ.get("SHADEFORM_API_KEY")
    if v:
        return v
    for name in ("ops.env.json", "env.json"):
        path = ROOT / name
        if not path.is_file():
            continue
        try:
            data = json.loads(path.read_text())
        except json.JSONDecodeError:
            continue
        if data.get("SHADEFORM_API_KEY"):
            return str(data["SHADEFORM_API_KEY"])
    raise SystemExit("SHADEFORM_API_KEY missing (env or ignored ops.env.json)")


def api(method: str, path: str, key: str, body: dict | None = None) -> dict:
    data = None if body is None else json.dumps(body).encode()
    headers = {
        "X-API-KEY": key,
        "Accept": "application/json",
        "User-Agent": "metrum-openjev-shadeform/1.0",
    }
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(
        API + path,
        data=data,
        method=method,
        headers=headers,
    )
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return json.loads(resp.read().decode())
    except urllib.error.HTTPError as err:
        detail = err.read().decode("utf-8", errors="replace")[:800]
        raise SystemExit(f"shadeform {method} {path} -> HTTP {err.code}: {detail}") from err


def score_candidate(gpu: str, num_gpus: int, price: float) -> tuple:
    g = gpu.upper()
    # Prefer single-GPU Blackwell / Pro 6000, then H100 80GB-class, then L40S.
    rank = 50
    if "RTXPRO6000" in g.replace(" ", "") or g == "RTXPRO6000":
        rank = 0
    elif "BLACKWELL" in g:
        rank = 1
    elif "H100" in g:
        rank = 2
    elif "H200" in g:
        rank = 3
    elif "A100" in g and "80" in g:
        rank = 4
    elif "L40S" in g:
        rank = 5
    elif "RTX6000" in g:
        rank = 6
    return (rank, num_gpus != 1, price)


def cmd_discover(args: argparse.Namespace) -> int:
    key = load_api_key()
    data = api("GET", "/instances/types?available=true&sort=price", key)
    rows = []
    for item in data.get("instance_types") or []:
        cfg = item.get("configuration") or {}
        gpu = str(cfg.get("gpu_type") or "")
        num = int(cfg.get("num_gpus") or 0)
        price = float(item.get("hourly_price") or 0)
        for avail in item.get("availability") or []:
            if not avail.get("available"):
                continue
            rows.append(
                {
                    "cloud": item.get("cloud"),
                    "region": avail.get("region"),
                    "sku": item.get("shade_instance_type"),
                    "gpu": gpu,
                    "num_gpus": num,
                    "hourly_price_cents": price,
                    "os_options": item.get("available_os") or item.get("os_options") or [],
                }
            )
    rows.sort(key=lambda r: score_candidate(r["gpu"], r["num_gpus"], r["hourly_price_cents"]))
    preferred = [r for r in rows if "RTXPRO6000" in r["gpu"].upper().replace(" ", "")]
    chosen = (preferred or rows)[: args.limit]
    out = {
        "preferred_found": bool(preferred),
        "note": "Prices are Shadeform hourly_price units (commonly USD cents).",
        "candidates": chosen,
    }
    print(json.dumps(out, indent=2))
    return 0


def cmd_create(args: argparse.Namespace) -> int:
    key = load_api_key()
    if not args.ssh_key_id:
        raise SystemExit("--ssh-key-id is required")
    body = {
        "cloud": args.cloud,
        "region": args.region,
        "shade_instance_type": args.sku,
        "shade_cloud": True,
        "name": args.name,
        "ssh_key_id": args.ssh_key_id,
    }
    if args.os:
        body["os"] = args.os
    resp = api("POST", "/instances/create", key, body)
    # Avoid dumping unexpected secret fields; keep id/status only.
    print(json.dumps({"id": resp.get("id") or resp.get("instance_id"), "status": resp.get("status"), "raw_keys": sorted(resp.keys())}, indent=2))
    return 0


def cmd_wait(args: argparse.Namespace) -> int:
    key = load_api_key()
    deadline = time.time() + args.timeout_s
    last = {}
    while time.time() < deadline:
        info = api("GET", f"/instances/{args.instance_id}/info", key)
        last = {
            "id": args.instance_id,
            "status": info.get("status"),
            "ip": info.get("ip") or info.get("public_ip"),
            "ssh_user": info.get("ssh_user") or info.get("user"),
            "ssh_port": info.get("ssh_port") or 22,
            "gpu": (info.get("configuration") or {}).get("gpu_type"),
        }
        print(json.dumps(last))
        if str(last["status"]).lower() in {"active", "running", "ready"}:
            return 0
        if str(last["status"]).lower() in {"failed", "error", "deleted"}:
            return 1
        time.sleep(args.poll_s)
    print(json.dumps({"error": "timeout", "last": last}), file=sys.stderr)
    return 1


def cmd_serve_script(_: argparse.Namespace) -> int:
    # Emit the remote bootstrap script (operator pastes over SSH).
    script = r'''#!/usr/bin/env bash
# Run on the Shadeform GPU host. Binds OpenJev to localhost only.
set -euo pipefail
export PATH="$HOME/.local/bin:$PATH"
ROOT="${OPENJEV_DIR:-$HOME/openjev-serve}"
mkdir -p "$ROOT"
cd "$ROOT"
python3 -m pip install -U pip
python3 -m pip install "vllm==0.29.0" "openai==3.16.2" "httpx==0.28.1" "huggingface_hub"
if [[ ! -f openjev/config.json ]]; then
  hf download openjev/openjev --local-dir openjev
fi
# Terminal 1: model
# FP8 primary recipe from the OpenJev model card.
nohup vllm serve ./openjev --host 127.0.0.1 --served-model-name qwen --port 8000 \
  --enable-prefix-caching --max-model-len 16384 --gpu-memory-utilization 0.90 \
  --limit-mm-per-prompt '{"image":1}' --trust-remote-code --max-num-seqs 256 \
  --max-logprobs 64 --gdn-prefill-backend triton --quantization fp8 \
  >"$ROOT/vllm.log" 2>&1 &
echo "vllm pid $!"
# Terminal 2: decision shim (after vLLM is healthy)
export VLLM=http://127.0.0.1:8000/v1 TOKENIZER=./openjev
export READOUT_T=0.85 READOUT_NOUL_T=1.829074 READOUT_NOUL_BIAS=0
export READOUT_TARGETED=1 READOUT_INSTR_STYLE=pyrepr SHIM_STAGGER=1
nohup python openjev/helper/shim.py --host 127.0.0.1 --port 3000 \
  >"$ROOT/shim.log" 2>&1 &
echo "shim pid $!"
echo "Tunnel from laptop: ssh -N -L 3000:127.0.0.1:3000 <user>@<ip>"
echo "Then: OPENJEV_URL=http://127.0.0.1:3000 python3 examples/external-routing-policy/openjev_policy.py"
'''
    print(script)
    return 0


def cmd_delete(args: argparse.Namespace) -> int:
    key = load_api_key()
    resp = api("POST", f"/instances/{args.instance_id}/delete", key, {})
    print(json.dumps({"id": args.instance_id, "response_keys": sorted(resp.keys()) if isinstance(resp, dict) else type(resp).__name__}, indent=2))
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    sub = ap.add_subparsers(dest="cmd", required=True)

    d = sub.add_parser("discover", help="List preferred available GPUs")
    d.add_argument("--limit", type=int, default=15)
    d.set_defaults(func=cmd_discover)

    c = sub.add_parser("create", help="Create an instance (async)")
    c.add_argument("--cloud", required=True)
    c.add_argument("--region", required=True)
    c.add_argument("--sku", required=True)
    c.add_argument("--ssh-key-id", required=True)
    c.add_argument("--name", default="openjev-routing-demo")
    c.add_argument("--os", default="")
    c.set_defaults(func=cmd_create)

    w = sub.add_parser("wait", help="Poll until active; print ssh endpoint")
    w.add_argument("--instance-id", required=True)
    w.add_argument("--timeout-s", type=float, default=900)
    w.add_argument("--poll-s", type=float, default=10)
    w.set_defaults(func=cmd_wait)

    s = sub.add_parser("serve-script", help="Print remote OpenJev bootstrap script")
    s.set_defaults(func=cmd_serve_script)

    dl = sub.add_parser("delete", help="Delete an instance")
    dl.add_argument("--instance-id", required=True)
    dl.set_defaults(func=cmd_delete)

    args = ap.parse_args()
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
