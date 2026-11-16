# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Normalize Docusaurus's nondeterministic 404 output before embedding."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


HTML_URL = re.compile(r'(?P<prefix>\b(?:href|content)="[^"]*/404)\.html(?=")')
TRAILING_DUPLICATE = "div>\n</body>\n</html>"


def normalize_404_html(document: str) -> str:
    """Use the extensionless Docusaurus route in generated 404 metadata."""
    if "Page Not Found" not in document or 'rel="canonical"' not in document:
        raise ValueError("generated 404 page is missing its title or canonical link")
    normalized = HTML_URL.sub(r"\g<prefix>", document)
    if normalized.endswith("</html>" + TRAILING_DUPLICATE):
        normalized = normalized[: -len(TRAILING_DUPLICATE)]
    if not normalized.rstrip().endswith("</html>"):
        raise ValueError("generated 404 page has unexpected trailing markup")
    canonical = re.search(r'<link\b[^>]*\brel="canonical"[^>]*\bhref="([^"]+)"', normalized)
    if canonical is None or not canonical.group(1).endswith("/404"):
        raise ValueError("generated 404 canonical URL is not the stable extensionless route")
    return normalized


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("html", type=Path, help="Docusaurus build 404.html")
    args = parser.parse_args()
    try:
        document = args.html.read_text(encoding="utf-8")
        normalized = normalize_404_html(document)
        if normalized != document:
            args.html.write_text(normalized, encoding="utf-8")
    except (OSError, ValueError) as exc:
        print(f"404 URL normalization failed: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
