# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Fail closed unless the release environment requires approval and tag-only deployments."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path


def validate(environment: dict[str, object], policies: dict[str, object]) -> list[str]:
    errors: list[str] = []
    if environment.get("name") != "release":
        errors.append("release environment is missing")
    rules = environment.get("protection_rules")
    reviewers = []
    if isinstance(rules, list):
        for rule in rules:
            if isinstance(rule, dict) and rule.get("type") == "required_reviewers":
                found = rule.get("reviewers")
                if isinstance(found, list):
                    reviewers.extend(found)
    if not reviewers:
        errors.append("release environment has no required reviewer")

    branch_policy = environment.get("deployment_branch_policy")
    if not isinstance(branch_policy, dict) or branch_policy.get("custom_branch_policies") is not True:
        errors.append("release environment does not restrict deployments to custom tag policies")
    raw_policies = policies.get("branch_policies")
    found_policies = set()
    if isinstance(raw_policies, list):
        for policy in raw_policies:
            if isinstance(policy, dict):
                found_policies.add((policy.get("name"), policy.get("type")))
    if found_policies != {("v*", "tag")}:
        errors.append("release environment must allow only tags matching v*")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("environment", type=Path)
    parser.add_argument("policies", type=Path)
    args = parser.parse_args()
    try:
        environment = json.loads(args.environment.read_text(encoding="utf-8"))
        policies = json.loads(args.policies.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"release environment check failed: {exc}", file=sys.stderr)
        return 2
    errors = validate(environment, policies)
    if errors:
        print("release environment check failed: " + "; ".join(errors), file=sys.stderr)
        return 1
    print("release environment requires a reviewer and allows only v* tags")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
