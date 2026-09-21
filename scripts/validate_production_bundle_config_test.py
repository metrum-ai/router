#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import subprocess
import tempfile
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "validate_production_bundle_config.py"


def run_check(config: dict) -> subprocess.CompletedProcess[str]:
    with tempfile.TemporaryDirectory() as tmp:
        path = Path(tmp) / "config.yaml"
        path.write_text(yaml.safe_dump(config))
        return subprocess.run(
            ["python3", str(SCRIPT), str(path)],
            check=False,
            capture_output=True,
            text=True,
        )


def valid_config() -> dict:
    return {
        "state_path": "/var/lib/smart-llmrouter/router-state.json",
        "server": {
            "client_ip": {"trusted_proxy_cidrs": ["192.168.0.0/16"]},
            "admin_auth": {
                "basic": {
                    "enabled": True,
                    "allow_insecure_http": False,
                    "trusted_proxy_cidrs": ["192.168.0.0/16"],
                },
                "authorization": {"enabled": True},
            },
            "admin_reports": {
                "enabled": True,
                "path_prefix": "/admin/reports",
            },
        },
        "callers": [{"id": "test", "token_sha256": "a" * 64}],
    }


def main() -> int:
    passed = run_check(valid_config())
    assert passed.returncode == 0, passed.stderr

    missing = valid_config()
    del missing["server"]["admin_reports"]
    failed = run_check(missing)
    assert failed.returncode == 1
    assert "admin_reports.enabled must be true" in failed.stderr

    no_auth = valid_config()
    no_auth["server"]["admin_auth"] = {"authorization": {"enabled": True}}
    failed = run_check(no_auth)
    assert failed.returncode == 1
    assert "require Basic or OIDC" in failed.stderr

    wrong_path = valid_config()
    wrong_path["server"]["admin_reports"]["path_prefix"] = "/admin/reports/internal"
    failed = run_check(wrong_path)
    assert failed.returncode == 1
    assert "path_prefix must be /admin/reports" in failed.stderr

    # 2026-09-08 production incident: a kind/Docker reverse-proxy range left the
    # cluster ingress untrusted, so correct passwords were challenged with 401.
    untrusted_proxy = valid_config()
    untrusted_proxy["server"]["admin_auth"]["basic"]["trusted_proxy_cidrs"] = ["172.18.0.0/16"]
    failed = run_check(untrusted_proxy)
    assert failed.returncode == 1
    assert "admin_auth.basic.trusted_proxy_cidrs" in failed.stderr

    no_proxy = valid_config()
    del no_proxy["server"]["admin_auth"]["basic"]["trusted_proxy_cidrs"]
    failed = run_check(no_proxy)
    assert failed.returncode == 1
    assert "admin_auth.basic.trusted_proxy_cidrs" in failed.stderr

    # allow_insecure_http bypasses the forwarded-HTTPS gate, so the reverse
    # proxy range is not a precondition for reaching the password check.
    insecure = valid_config()
    insecure["server"]["admin_auth"]["basic"] = {"enabled": True, "allow_insecure_http": True}
    assert run_check(insecure).returncode == 0

    print("production bundle admin reports contract test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
