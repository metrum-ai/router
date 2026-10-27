# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""List legacy caller IDs in an unexpanded YAML config, without printing verifiers.

This inventories a single file only; database-backed control-plane configs
must be inventoried separately before upgrading.
"""

import argparse
import json
import sys
from pathlib import Path

import yaml


def legacy_caller_ids(document):
    if not isinstance(document, dict):
        raise ValueError("config must be a YAML mapping")
    callers = document.get("callers", [])
    if not isinstance(callers, list):
        raise ValueError("callers must be a list")
    ids = []
    for caller in callers:
        if not isinstance(caller, dict):
            raise ValueError("each caller must be a mapping")
        if "token_sha256" not in caller:
            continue
        caller_id = caller.get("id")
        if not isinstance(caller_id, str) or not caller_id.strip():
            raise ValueError("legacy caller has no usable ID")
        ids.append(caller_id)
    return sorted(ids)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config", type=Path, help="unexpanded router YAML configuration")
    args = parser.parse_args()
    try:
        document = yaml.safe_load(args.config.read_text(encoding="utf-8"))
        ids = legacy_caller_ids(document)
    except (OSError, UnicodeError, yaml.YAMLError, ValueError):
        # Parser exceptions can include snippets of secret-bearing input.
        print("Unable to inventory config: invalid or unreadable YAML/caller entries", file=sys.stderr)
        return 2
    print(json.dumps({"legacy_caller_ids": ids, "count": len(ids), "scope": "yaml-file-only"}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
