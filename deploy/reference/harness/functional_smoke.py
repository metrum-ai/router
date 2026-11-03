#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Functional suite for a deploy/reference tier.

Checks the deployed stack the way a caller sees it: readiness, the advertised
model groups, one completion per local group, the escalation group when a
frontier key is present, and the authentication negatives. Writes one JSON
document and exits non-zero if any check failed.

Only the standard library is used so the suite runs on a bare host.

Environment:
  ROUTER_TOKEN          required; caller token (never printed or written)
  OPENAI_API_KEY        optional; when empty the escalation check is skipped
  RESTRICTED_TOKEN      optional; a caller token without the escalate group, used to prove
                        that group grants are enforced
  REFERENCE_ADMIN_USER  optional; with REFERENCE_ADMIN_PASSWORD checks the admin UI
  REFERENCE_ADMIN_PASSWORD

Usage:
  functional_smoke.py <vendor>-<tier> [--base-url URL] [--group NAME ...] [--out PATH]
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import sys
import urllib.error
import urllib.request
from datetime import datetime, timezone


def request(
    method: str,
    url: str,
    *,
    token: str | None = None,
    body: dict | None = None,
    timeout: float = 120.0,
    headers: dict[str, str] | None = None,
) -> tuple[int, str]:
    data = None
    all_headers = {"Accept": "application/json"}
    if token is not None:
        all_headers["Authorization"] = f"Bearer {token}"
    if headers:
        all_headers.update(headers)
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        all_headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, method=method, headers=all_headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            return response.status, response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode("utf-8", "replace")
    except urllib.error.URLError as err:
        return 0, str(err.reason)


class Suite:
    def __init__(self, tier: str) -> None:
        self.tier = tier
        self.checks: list[dict[str, str]] = []

    def record(self, name: str, status: str, detail: str, group: str | None = None) -> None:
        row = {"check": name, "status": status, "detail": detail}
        if group is not None:
            row["group"] = group
        self.checks.append(row)

    def ok(self, name: str, detail: str = "", group: str | None = None) -> None:
        self.record(name, "pass", detail, group)

    def fail(self, name: str, detail: str, group: str | None = None) -> None:
        self.record(name, "fail", detail, group)

    def skip(self, name: str, detail: str, group: str | None = None) -> None:
        self.record(name, "skip", detail, group)

    @property
    def failed(self) -> list[dict[str, str]]:
        return [check for check in self.checks if check["status"] == "fail"]


def completion_body(group: str) -> dict:
    return {
        "model": group,
        "messages": [{"role": "user", "content": "Reply with the single word ok."}],
        "max_tokens": 16,
        "stream": False,
    }


def check_ready(suite: Suite, base_url: str) -> None:
    status, text = request("GET", f"{base_url}/readyz", timeout=10)
    if status == 200:
        suite.ok("readyz", "200")
    else:
        suite.fail("readyz", f"status {status}: {text[:200]}")


def check_models(suite: Suite, base_url: str, token: str, expected: list[str]) -> None:
    status, text = request("GET", f"{base_url}/v1/models", token=token)
    if status != 200:
        suite.fail("models", f"status {status}: {text[:200]}")
        return
    try:
        served = {entry["id"] for entry in json.loads(text).get("data", [])}
    except (ValueError, KeyError, TypeError):
        suite.fail("models", f"unparseable body: {text[:200]}")
        return
    missing = [group for group in expected if group not in served]
    if missing:
        suite.fail("models", f"missing groups: {', '.join(sorted(missing))}")
    else:
        suite.ok("models", f"{len(served)} group(s) advertised")


def check_completion(suite: Suite, base_url: str, token: str, group: str) -> None:
    status, text = request("POST", f"{base_url}/v1/chat/completions", token=token, body=completion_body(group))
    if status != 200:
        suite.fail("completion", f"status {status}: {text[:300]}", group)
        return
    try:
        payload = json.loads(text)
        content = payload["choices"][0]["message"].get("content") or ""
    except (ValueError, KeyError, IndexError, TypeError):
        suite.fail("completion", f"unparseable body: {text[:300]}", group)
        return
    if not content.strip():
        suite.fail("completion", "empty content", group)
        return
    suite.ok("completion", f"{len(content)} chars", group)


def check_auth_negative(suite: Suite, base_url: str) -> None:
    status, _ = request("POST", f"{base_url}/v1/chat/completions", body=completion_body("local-tiny"))
    if status == 401:
        suite.ok("auth-required", "401 without a token")
    else:
        suite.fail("auth-required", f"expected 401 without a token, got {status}")
    status, _ = request("POST", f"{base_url}/v1/chat/completions", token="not-a-real-token", body=completion_body("local-tiny"))
    if status in (401, 403):
        suite.ok("auth-invalid", f"{status} with an unknown token")
    else:
        suite.fail("auth-invalid", f"expected 401 or 403 with an unknown token, got {status}")


def check_group_grant(suite: Suite, base_url: str, token: str, group: str = "escalate") -> None:
    """A caller that was not granted a group must not reach it.

    The router answers `404 model_not_found` here rather than `403`: the group does
    not exist from that caller's point of view. Whatever the code, the point is
    that the request does not reach the upstream.
    """
    status, text = request("POST", f"{base_url}/v1/chat/completions", token=token, body=completion_body(group))
    if status in (403, 404):
        suite.ok("group-grant", f"{group} is not reachable with a caller that was not granted it ({status})", group)
    elif status == 200:
        suite.fail("group-grant", f"{group} answered a caller that was not granted it", group)
    else:
        suite.skip("group-grant", f"unexpected {status}: {text[:160]}", group)


def check_escalation(suite: Suite, base_url: str, token: str) -> None:
    if not os.environ.get("OPENAI_API_KEY"):
        suite.skip("escalation", "OPENAI_API_KEY is not set; the escalate group has no key to use", "escalate")
        return
    check_completion(suite, base_url, token, "escalate")


def check_admin(suite: Suite, base_url: str) -> None:
    status, _ = request("GET", f"{base_url}/admin/reports/")
    if status not in (401, 403):
        suite.fail("admin-requires-auth", f"expected 401 or 403 without credentials, got {status}")
    else:
        suite.ok("admin-requires-auth", f"{status} without credentials")
    user = os.environ.get("REFERENCE_ADMIN_USER")
    password = os.environ.get("REFERENCE_ADMIN_PASSWORD")
    if not (user and password):
        suite.skip("admin-ui", "REFERENCE_ADMIN_USER/REFERENCE_ADMIN_PASSWORD not set")
        return
    credentials = base64.b64encode(f"{user}:{password}".encode("utf-8")).decode("ascii")
    status, text = request("GET", f"{base_url}/admin/reports/", headers={"Authorization": f"Basic {credentials}"})
    if status == 200:
        suite.ok("admin-ui", "reports page served")
    else:
        suite.fail("admin-ui", f"status {status}: {text[:200]}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tier")
    parser.add_argument("--base-url", default=os.environ.get("ROUTER_BASE_URL", "http://127.0.0.1:8080"))
    parser.add_argument(
        "--group",
        action="append",
        dest="groups",
        default=None,
        help="local group to complete (repeatable; default: local-tiny and local-chat)",
    )
    parser.add_argument("--out", default="", help="write the JSON result here instead of stdout")
    parser.add_argument("--group-grant-check", default="escalate",
                        help="group that RESTRICTED_TOKEN must not be able to reach")
    args = parser.parse_args(argv)

    token = os.environ.get("ROUTER_TOKEN", "")
    if not token:
        print("ROUTER_TOKEN is required", file=sys.stderr)
        return 2
    base_url = args.base_url.rstrip("/")
    groups = args.groups or ["local-tiny", "local-chat"]

    suite = Suite(args.tier)
    check_ready(suite, base_url)
    check_models(suite, base_url, token, groups)
    for group in groups:
        check_completion(suite, base_url, token, group)
    check_escalation(suite, base_url, token)
    if os.environ.get("RESTRICTED_TOKEN"):
        check_group_grant(suite, base_url, os.environ["RESTRICTED_TOKEN"], args.group_grant_check)
    else:
        suite.skip(
            "group-grant",
            "RESTRICTED_TOKEN is not set; a caller without the group is not exercised",
        )
    check_auth_negative(suite, base_url)
    check_admin(suite, base_url)

    result = {
        "schema": "metrum.ai/reference-functional-suite/v1",
        "tier": args.tier,
        "base_url": base_url,
        "commit": os.environ.get("RUN_COMMIT", ""),
        "finished_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "groups": groups,
        "checks": suite.checks,
        "passed": sum(1 for check in suite.checks if check["status"] == "pass"),
        "failed": len(suite.failed),
        "skipped": sum(1 for check in suite.checks if check["status"] == "skip"),
    }
    document = json.dumps(result, indent=2, sort_keys=False) + "\n"
    if args.out:
        path = os.path.dirname(os.path.abspath(args.out))
        if path:
            os.makedirs(path, exist_ok=True)
        with open(args.out, "w", encoding="utf-8") as handle:
            handle.write(document)
        print(f"wrote {args.out}: {result['passed']} passed, {result['failed']} failed, {result['skipped']} skipped")
    else:
        sys.stdout.write(document)

    for check in suite.failed:
        group = f" [{check['group']}]" if "group" in check else ""
        print(f"FAIL {check['check']}{group}: {check['detail']}", file=sys.stderr)
    return 1 if suite.failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
