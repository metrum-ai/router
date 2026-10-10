#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Fail-closed scan for obsolete product filenames and docs origins.

Checks release archives under dist/ (when present). Optional flags enforce
the canonical docs origin and packaging contract scripts. Unit tests always
exercise the scanner against fixtures. Full enforcement is enabled from
docs-qa after the rename PRs land.
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

import canonical_product as product
import check_docs_origin


ROOT = Path(__file__).resolve().parents[1]

CONTRACT_PATHS = [
    ROOT / "scripts" / "canonical_product.py",
    ROOT / "scripts" / "release_artifact_inventory.py",
    ROOT / "scripts" / "validate_release_matrix.py",
]

DOCS_LINK_PATHS = [
    ROOT / "README.md",
    ROOT / "docs-site" / "docusaurus.config.js",
    ROOT / "docs-site" / "static" / "llms.txt",
    ROOT / "docs" / "LEARNED_ROUTING_POLICY.md",
    ROOT / "docs" / "LEARNED_ROUTING_POLICY_EVIDENCE.md",
]



def scan_dist(dist_dir: Path) -> list[str]:
    errors: list[str] = []
    if not dist_dir.is_dir():
        return errors
    for path in sorted(dist_dir.glob("*.tar.gz")):
        obsolete = product.obsolete_slug_in_filename(path.name)
        if obsolete:
            errors.append(f"{path.relative_to(ROOT)}: obsolete product filename uses {obsolete!r}")
            continue
        if product.RELEASE_ARCHIVE_RE.fullmatch(path.name) is None:
            errors.append(
                f"{path.relative_to(ROOT)}: release archive must match "
                f"{product.PRODUCT_SLUG}-<version>[-docker]-linux-<arch>.tar.gz"
            )
    return errors


def scan_contract_files() -> list[str]:
    errors: list[str] = []
    for path in CONTRACT_PATHS:
        if not path.exists():
            continue
        text = path.read_text(encoding="utf-8")
        rel = path.relative_to(ROOT)
        if path.name == "canonical_product.py":
            if f'PRODUCT_SLUG = "{product.PRODUCT_SLUG}"' not in text:
                errors.append(f"{rel}: PRODUCT_SLUG must be {product.PRODUCT_SLUG!r}")
            continue
        if f'"{product.PRODUCT_SLUG}"' not in text and f"'{product.PRODUCT_SLUG}'" not in text:
            # Inventory uses regex anchored on the slug; matrix sets PKG_NAME.
            if product.PRODUCT_SLUG not in text:
                errors.append(f"{rel}: must reference canonical slug {product.PRODUCT_SLUG!r}")
        obsolete = product.obsolete_slug_in_filename(text.replace(product.PRODUCT_SLUG, "«canonical»"))
        # Text scan: look for obsolete archive prefixes as current defaults.
        for slug in ("metrum-router", "smart-llmrouter", "genai-smart-router"):
            if re.search(rf'["\']={slug}["\']|PKG_NAME.:\s*["\']{slug}["\']|IMAGE_NAME.:\s*["\']{slug}["\']', text):
                errors.append(f"{rel}: still defaults to obsolete slug {slug!r}")
            if f'"{slug}"' in text or f"'{slug}'" in text:
                # Allow mentions only inside comments that say obsolete/legacy.
                for line_no, line in enumerate(text.splitlines(), start=1):
                    if slug not in line:
                        continue
                    if re.search(rf'["\']={re.escape(slug)}["\']|["\']{re.escape(slug)}["\']', line):
                        if re.search(r"obsolete|legacy|formerly|historical", line, re.IGNORECASE):
                            continue
                        if product.PRODUCT_SLUG in line:
                            continue
                        errors.append(f"{rel}:{line_no}: obsolete slug {slug!r} used as a current name")
    return errors


def scan_docs_links(*, enforce_docs_origin: bool) -> list[str]:
    if not enforce_docs_origin:
        return []
    # Same origin policy as docs-qa (scripts/check_docs_origin.py), so flipping
    # docs-site/docs-origin.json moves both checks together.
    policy = check_docs_origin.current_policy()
    errors: list[str] = []
    for path in DOCS_LINK_PATHS:
        if not path.exists():
            continue
        for line_no, line in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
            if "branding-exception:" in line:
                continue
            errors.extend(check_docs_origin.line_errors(path.relative_to(ROOT), line_no, line, policy))
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist-dir", type=Path, default=ROOT / "dist")
    parser.add_argument(
        "--enforce-docs-origin",
        action="store_true",
        help="Reject docs links that do not use the canonical docs origin",
    )
    parser.add_argument(
        "--enforce-contract",
        action="store_true",
        help="Require packaging contract scripts to use the canonical slug",
    )
    args = parser.parse_args()

    dist_dir = args.dist_dir if args.dist_dir.is_absolute() else ROOT / args.dist_dir
    errors = scan_dist(dist_dir)
    if args.enforce_contract:
        errors.extend(scan_contract_files())
    errors.extend(scan_docs_links(enforce_docs_origin=args.enforce_docs_origin))

    if errors:
        print("stale product name scan failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print("stale product name scan passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
