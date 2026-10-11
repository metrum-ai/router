#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import subprocess
import tempfile
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "validate_production_bundle_config.py"


def run_check(config: dict, *extra: str) -> subprocess.CompletedProcess[str]:
    with tempfile.TemporaryDirectory() as tmp:
        path = Path(tmp) / "config.yaml"
        path.write_text(yaml.safe_dump(config))
        return subprocess.run(
            ["python3", str(SCRIPT), str(path), *extra],
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

    # Output echoes only the canonical CIDR, never a free-form CLI value.
    assert "trusted proxy 192.168.0.0/16" in passed.stdout, passed.stdout
    not_cidr = "not-a-cidr-sk-live-value"
    failed = run_check(valid_config(), "--require-trusted-proxy", not_cidr)
    assert failed.returncode == 2
    assert "--require-trusted-proxy must be a CIDR network" in failed.stderr
    assert not_cidr not in failed.stderr + failed.stdout
    failed = run_check(valid_config(), "--require-trusted-proxy", "10.0.0.0/8")
    assert failed.returncode == 1
    assert "missing trusted_proxy_cidrs entry 10.0.0.0/8" in failed.stderr

    # token_hash (PBKDF2-SHA3-512) callers are accepted alongside legacy
    # token_sha256 callers; malformed verifiers fail without being echoed.
    good_hash = "pbkdf2-sha3-512:i=20000:" + "s" * 43 + ":" + "k" * 86
    hashed = valid_config()
    hashed["callers"] = [
        {"id": "new", "token_hash": good_hash, "token_id": "rtr_metrum_new_p_dev_k1"},
        {"id": "legacy", "token_sha256": "a" * 64},
    ]
    passed = run_check(hashed)
    assert passed.returncode == 0, passed.stderr

    for bad in (
        "pbkdf2-sha3-512$i=20000$" + "s" * 43 + "$" + "k" * 86,
        "pbkdf2-sha3-512:i=100:" + "s" * 43 + ":" + "k" * 86,
        "pbkdf2-sha256:i=20000:" + "s" * 43 + ":" + "k" * 86,
        "pbkdf2-sha3-512:i=20000:" + "s" * 43 + ":" + "k" * 43,
    ):
        bad_hash = valid_config()
        bad_hash["callers"] = [{"id": "new", "token_hash": bad, "token_id": "rtr_metrum_new_p_dev_k1"}]
        failed = run_check(bad_hash)
        assert failed.returncode == 1
        assert "caller new has invalid token_hash" in failed.stderr
        assert bad not in failed.stderr

    no_token_id = valid_config()
    no_token_id["callers"] = [{"id": "new", "token_hash": good_hash}]
    failed = run_check(no_token_id)
    assert failed.returncode == 1
    assert "token_hash requires token_id" in failed.stderr

    print("production bundle admin reports contract test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
