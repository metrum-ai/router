#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Guard current docs against stale no-eligible-target capability claims."""
from pathlib import Path
import hashlib
import re
import sys

ROOT = Path(__file__).resolve().parent.parent / "docs-site" / "docs"
PATTERN = re.compile(r"no[-_]eligible[-_]targets?", re.IGNORECASE)
# Hash entire permitted lines so a stale capability claim cannot be appended
# to an allowed 502, historical, or telemetry-label mention unnoticed.
ALLOWED = {
    "configuration/routing-typescript.md": "9cfc2299e67d3284dad0606a720fb40126004327bf47a165006e699f3e6a1e47",
    "configuration/model-group-contracts.md": "0c41cfb326b4bdbb8886a912431c4ff2a07c05d756b682cd7c48b95948cedeea",
    "faq.md": "650ccb56895fe621abf28e3217c75295d067cdc3453fee155d2c6d9b358002e5",
    "evaluation/prove-router-quality.md": "82685dafd0db38f0575b09b5543477bb26102a7af5cd6cecbf32a04d88c43844",
    "reference/errors.md": "47f0d39ce241e2d1d6477c79f8d1593fde34a4ecab160c3081df745c5f4c5fc4",
    "getting-started/claude-code-cli.mdx": "1f7e61108c3ff2d98957d972a5fa53c88597f2e8bfffba21a7f160b5e056e05d",
}


def check(root: Path) -> list[str]:
    failures = []
    for path in sorted(root.rglob("*")):
        if path.suffix not in {".md", ".mdx"} or not path.is_file():
            continue
        rel = path.relative_to(root).as_posix()
        if rel.startswith("release-notes/"):
            continue
        text = path.read_text(encoding="utf-8")
        for number, line in enumerate(text.splitlines(), 1):
            if not PATTERN.search(line):
                continue
            digest = hashlib.sha256(line.strip().encode()).hexdigest()
            allowed = digest == ALLOWED.get(rel)
            if rel == "getting-started/claude-code-cli.mdx":
                allowed = allowed and "v4.1.0 (pre-4.5 behavior)" in text
            if not allowed:
                failures.append(f"{rel}:{number}: unapproved no-eligible-target mention")
    return failures


def main() -> int:
    failures = check(ROOT)
    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    print("Eligibility error docs check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
