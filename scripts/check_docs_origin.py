#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Reject non-canonical public docs origins in Metrum AI Router sources.

Scans docs, README, llms.txt, docs-site source and package metadata for
documentation URLs (any metrum.ai URL under /docs, any docs.* host) and fails
when one does not use an allowed origin from canonical_product. The allowed
set comes from docs-site/docs-origin.json: while the docs.metrum.ai switch is
pending only the temporary origin is allowed; after the one-line flip only
docs.metrum.ai is. Historical changelog and release-note lines may keep the
temporary origin because the old URLs redirect.

After flipping the canonical origin, run with --rewrite to move every
non-historical docs link to the new origin while keeping paths and fragments.
"""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

import canonical_product as product


ROOT = Path(__file__).resolve().parents[1]

EXCEPTION_MARKER = "docs-origin-exception:"

# Files whose lines record history; they may keep the temporary origin.
HISTORICAL_FILES = {
    Path("CHANGELOG.md"),
    Path("docs-site/docs/release-notes/index.md"),
}

SCAN_GLOBS = (
    "README.md",
    "CHANGELOG.md",
    "docs/**/*.md",
    "docs-site/docs/**/*.md",
    "docs-site/docs/**/*.mdx",
    "docs-site/static/**/*.txt",
    "docs-site/src/**/*.js",
    "docs-site/src/**/*.mjs",
    "docs-site/docusaurus.config.js",
    # Package metadata and package-shipped config.
    "docs-site/package.json",
    "Dockerfile",
    "config.example.yaml",
    "config.minimal.example.yaml",
    "deploy/**/*",
)

# Trailing sentence punctuation is not part of the URL path.
URL_RE = re.compile(
    r"(?P<origin>https?://(?P<host>[A-Za-z0-9.-]*[A-Za-z0-9]))"
    r"(?P<path>[^\s)\]\"'`<>|]*?)(?=[.,;:!?]*(?:[\s)\]\"'`<>|]|$))"
)
DOCS_PATH_RE = re.compile(r"^/docs(?:[/#?]|$)")


@dataclass(frozen=True)
class OriginPolicy:
    canonical: str
    allowed: tuple[str, ...]
    temporary: str
    permanent: str

    @property
    def switch_pending(self) -> bool:
        return self.canonical != self.permanent


def current_policy() -> OriginPolicy:
    return OriginPolicy(
        canonical=product.DOCS_SITE_ORIGIN,
        allowed=product.allowed_docs_origins(),
        temporary=product.TEMPORARY_DOCS_SITE_ORIGIN,
        permanent=product.PERMANENT_DOCS_SITE_ORIGIN,
    )


def is_docs_reference(host: str, path: str) -> bool:
    """Return True when a URL points at a hosted Metrum AI docs site."""

    host = host.lower().rstrip(".")
    if host.endswith(".github.io") and host.startswith("metrum-ai."):
        return True
    if not (host == "metrum.ai" or host.endswith(".metrum.ai")):
        return False
    return host.startswith("docs.") or DOCS_PATH_RE.match(path) is not None


def docs_urls(line: str) -> Iterable[re.Match[str]]:
    for match in URL_RE.finditer(line):
        if is_docs_reference(match.group("host"), match.group("path")):
            yield match


def line_errors(rel: Path, line_no: int, line: str, policy: OriginPolicy) -> Iterable[str]:
    if EXCEPTION_MARKER in line:
        return
    for match in docs_urls(line):
        origin = match.group("origin").lower()
        if origin in policy.allowed:
            continue
        if rel in HISTORICAL_FILES and origin == policy.temporary:
            continue
        if origin == policy.permanent and policy.switch_pending:
            reason = "docs.metrum.ai does not serve /docs/ yet"
        else:
            reason = "non-canonical docs origin"
        yield (
            f"{rel}:{line_no}: {match.group(0)!r}: {reason}; use {policy.canonical}"
            f"{product.DOCS_SITE_BASE_URL.rstrip('/')}/..."
        )


def scan_text(rel: Path, text: str, policy: OriginPolicy) -> list[str]:
    errors: list[str] = []
    for line_no, line in enumerate(text.splitlines(), start=1):
        errors.extend(line_errors(rel, line_no, line, policy))
    return errors


def rewrite_text(rel: Path, text: str, policy: OriginPolicy) -> str:
    """Move known docs origins to the canonical origin, keeping paths and fragments."""

    if rel in HISTORICAL_FILES:
        return text
    movable = {policy.temporary, policy.permanent} - {policy.canonical}

    def replace_line(line: str) -> str:
        if EXCEPTION_MARKER in line:
            return line

        def replace(match: re.Match[str]) -> str:
            if not is_docs_reference(match.group("host"), match.group("path")):
                return match.group(0)
            if match.group("origin").lower() not in movable:
                return match.group(0)
            return policy.canonical + match.group("path")

        return URL_RE.sub(replace, line)

    return "".join(replace_line(line) for line in text.splitlines(keepends=True))


def scan_paths(root: Path) -> list[Path]:
    paths: set[Path] = set()
    for pattern in SCAN_GLOBS:
        for path in root.glob(pattern):
            if path.is_file() and "node_modules" not in path.parts:
                paths.add(path)
    return sorted(paths)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--rewrite",
        action="store_true",
        help="Rewrite non-historical docs links to the canonical origin before checking",
    )
    args = parser.parse_args()

    policy = current_policy()
    errors: list[str] = []
    rewritten: list[Path] = []
    for path in scan_paths(ROOT):
        rel = path.relative_to(ROOT)
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        if args.rewrite:
            updated = rewrite_text(rel, text, policy)
            if updated != text:
                path.write_text(updated, encoding="utf-8")
                rewritten.append(rel)
                text = updated
        errors.extend(scan_text(rel, text, policy))

    for rel in rewritten:
        print(f"rewrote docs links in {rel}")
    if errors:
        print("docs origin check failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1
    state = "pending" if policy.switch_pending else "complete"
    print(f"docs origin check passed (canonical {policy.canonical}, docs.metrum.ai switch {state})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
