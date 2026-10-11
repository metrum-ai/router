#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Shadeform helper for the d3 live GPU run (issue #278), Metrum AI Router.

Subcommands: types, list, sshkey-add, sshkey-delete, create, wait, delete.
SHADEFORM_API_KEY comes from the main checkout's ignored ops.env.json, else the
environment. The key is never printed. Output is scalar JSON (IDs, status,
type, price, region, IP) only.

Copyright 2026 Metrum AI.
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
ALLOWED = {"L40S", "H100", "H200", "B200", "RTXPro6000"}
NAME_PREFIX = "metrum-d3-"


def api_key(ops_env: Path) -> str:
    # ops.env.json wins: a stale SHADEFORM_API_KEY may linger in the shell environment.
    key = ""
    if ops_env.is_file():
        key = json.loads(ops_env.read_text(encoding="utf-8")).get("SHADEFORM_API_KEY", "")
    key = key or os.environ.get("SHADEFORM_API_KEY", "")
    if not key:
        sys.exit("SHADEFORM_API_KEY missing")
    return key


def call(key: str, method: str, path: str, body: dict | None = None) -> tuple[int, dict]:
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(API + path, data=data, method=method,
                                 headers={"X-API-KEY": key, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw.strip() else {})
    except urllib.error.HTTPError as err:
        raw = err.read()
        try:
            return err.code, json.loads(raw)
        except ValueError:
            return err.code, {"error": raw[:200].decode(errors="replace")}


def out(obj: object) -> None:
    print(json.dumps(obj, indent=1))


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--ops-env", type=Path, default=Path("/home/cgadgil/src/genai-smart-router/ops.env.json"))
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("types")
    sub.add_parser("list")
    k = sub.add_parser("sshkey-add")
    k.add_argument("--name", required=True)
    k.add_argument("--public-key-file", type=Path, required=True)
    kd = sub.add_parser("sshkey-delete")
    kd.add_argument("--id", required=True)
    c = sub.add_parser("create")
    c.add_argument("--cloud", required=True)
    c.add_argument("--region", required=True)
    c.add_argument("--type", required=True)
    c.add_argument("--os", required=True)
    c.add_argument("--name", required=True)
    c.add_argument("--ssh-key-id", required=True)
    w = sub.add_parser("wait")
    w.add_argument("--id", required=True)
    w.add_argument("--timeout", type=int, default=1500)
    d = sub.add_parser("delete")
    d.add_argument("--id", required=True)
    args = ap.parse_args()
    key = api_key(args.ops_env)

    if args.cmd == "types":
        code, data = call(key, "GET", "/instances/types?available=true&sort=price")
        rows = []
        for it in data.get("instance_types", []):
            cfg = it["configuration"]
            if cfg["num_gpus"] != 1 or cfg["gpu_type"] not in ALLOWED:
                continue
            for av in it.get("availability", []):
                if av.get("available"):
                    rows.append({"cloud": it["cloud"], "type": it["shade_instance_type"], "gpu": cfg["gpu_type"],
                                 "vram_gb": cfg.get("vram_per_gpu_in_gb"), "usd_per_h": it["hourly_price"] / 100,
                                 "region": av["region"], "os": cfg.get("os_options", [])})
        out({"http": code, "single_gpu_allowed_available": rows})
    elif args.cmd == "list":
        code, data = call(key, "GET", "/instances")
        out({"http": code, "instances": [{k: i.get(k) for k in ("id", "name", "status", "shade_instance_type",
                                                               "cloud", "region", "hourly_price", "created_at")}
                                         for i in data.get("instances", [])]})
    elif args.cmd == "sshkey-add":
        code, data = call(key, "POST", "/sshkeys/add",
                          {"name": args.name, "public_key": args.public_key_file.read_text().strip()})
        out({"http": code, "id": data.get("id"), "error": data.get("error")})
    elif args.cmd == "sshkey-delete":
        code, data = call(key, "POST", f"/sshkeys/{args.id}/delete")
        out({"http": code})
    elif args.cmd == "create":
        if not args.name.startswith(NAME_PREFIX):
            sys.exit(f"instance name must start with {NAME_PREFIX}")
        body = {"cloud": args.cloud, "region": args.region, "shade_instance_type": args.type,
                "shade_cloud": True, "name": args.name, "os": args.os, "ssh_key_id": args.ssh_key_id}
        code, data = call(key, "POST", "/instances/create", body)
        out({"http": code, "id": data.get("id"), "error": data.get("error") or data.get("message")})
    elif args.cmd == "wait":
        deadline = time.time() + args.timeout
        while True:
            code, data = call(key, "GET", f"/instances/{args.id}/info")
            status = data.get("status")
            print(json.dumps({"t": time.strftime("%H:%M:%SZ", time.gmtime()), "http": code, "status": status}),
                  file=sys.stderr)
            if status == "active" or time.time() > deadline or status in ("error", "deleted"):
                out({"http": code, "id": args.id, "status": status, "ip": data.get("ip"),
                     "ssh_user": data.get("ssh_user"), "ssh_port": data.get("ssh_port"),
                     "type": data.get("shade_instance_type"), "region": data.get("region"),
                     "hourly_price": data.get("hourly_price"), "created_at": data.get("created_at"),
                     "active_at": data.get("active_at")})
                return 0 if status == "active" else 1
            time.sleep(20)
    elif args.cmd == "delete":
        code, data = call(key, "POST", f"/instances/{args.id}/delete")
        out({"http": code, "error": data.get("error")})
    return 0


if __name__ == "__main__":
    sys.exit(main())
