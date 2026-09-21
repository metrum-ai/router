#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Validate a production runtime bundle config for path and caller parity.

Reads a local config.yaml (for example production-identical.yaml) and checks that
state paths, trusted proxy CIDRs, and caller token_sha256 entries are present.
Never reads or prints raw router tokens.
"""

from __future__ import annotations

import argparse
import pathlib
import sys

import yaml


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("config", type=pathlib.Path)
    parser.add_argument(
        "--require-trusted-proxy",
        default="192.168.0.0/16",
        help="Expected server.client_ip.trusted_proxy_cidrs entry",
    )
    args = parser.parse_args()
    data = yaml.safe_load(args.config.read_text())
    server = data.get("server") or {}
    state_path = data.get("state_path", "")
    errors: list[str] = []
    if not str(state_path).startswith("/var/lib/smart-llmrouter"):
        errors.append(f"state_path must use /var/lib/smart-llmrouter, got {state_path!r}")
    client_ip = server.get("client_ip") or {}
    cidrs = client_ip.get("trusted_proxy_cidrs") or []
    if args.require_trusted_proxy not in cidrs:
        errors.append(f"missing trusted_proxy_cidrs entry {args.require_trusted_proxy}")
    admin_auth = server.get("admin_auth") or {}
    basic = admin_auth.get("basic") or {}
    basic_enabled = bool(basic.get("enabled"))
    oidc_enabled = bool((admin_auth.get("oidc") or {}).get("enabled"))
    if not basic_enabled and not oidc_enabled:
        errors.append("production admin reports require Basic or OIDC admin authentication")
    # Basic Auth evaluates the forwarded-HTTPS check before comparing the
    # password, so an admin_auth trusted range that excludes the reverse proxy
    # challenges every request whether or not the credentials are correct.
    if basic_enabled and not bool(basic.get("allow_insecure_http")):
        admin_cidrs = basic.get("trusted_proxy_cidrs") or []
        if args.require_trusted_proxy not in admin_cidrs:
            errors.append(
                "missing admin_auth.basic.trusted_proxy_cidrs entry "
                f"{args.require_trusted_proxy}; admin Basic Auth returns 401 for correct "
                "passwords when the reverse proxy network is not trusted"
            )
    if not bool((admin_auth.get("authorization") or {}).get("enabled")):
        errors.append("production admin reports require admin authorization")
    admin_reports = server.get("admin_reports") or {}
    if not bool(admin_reports.get("enabled")):
        errors.append("production admin_reports.enabled must be true")
    if str(admin_reports.get("path_prefix") or "/admin/reports").rstrip("/") != "/admin/reports":
        errors.append("production admin_reports.path_prefix must be /admin/reports")
    callers = data.get("callers") or []
    if not callers:
        errors.append("callers list is empty")
    for caller in callers:
        token_hash = caller.get("token_sha256") or ""
        if len(token_hash) != 64:
            errors.append(f"caller {caller.get('id')} missing valid token_sha256")
    if errors:
        for err in errors:
            print(err, file=sys.stderr)
        return 1
    print(f"ok: {len(callers)} callers, trusted proxy {args.require_trusted_proxy}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
