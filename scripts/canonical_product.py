#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Canonical Metrum AI Router product identifiers shared by release and docs QA.

Import these constants instead of hard-coding product slugs, archive prefixes,
docs origins, or package/image names. Go module and GitHub repository paths
remain github.com/metrum-ai/router and are intentionally not the product slug.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

# Display name for public docs, site metadata, and operator-facing prose.
PRODUCT_TITLE = "Metrum AI Router"

# Technical slug for binaries, archives, images, Compose, and Kubernetes.
PRODUCT_SLUG = "metrum-ai-router"

PKG_NAME = PRODUCT_SLUG
IMAGE_NAME = PRODUCT_SLUG

# Public documentation origins. docs-site/docs-origin.json is the single source
# of truth shared with docusaurus.config.js and RouterEndpoint.js; switching the
# canonical origin to docs.metrum.ai is a one-line change to "canonicalOrigin"
# in that file (Metrum AI docs hosting issue #123).
DOCS_ORIGIN_CONFIG_PATH = Path(__file__).resolve().parents[1] / "docs-site" / "docs-origin.json"
_DOCS_ORIGIN_CONFIG = json.loads(DOCS_ORIGIN_CONFIG_PATH.read_text(encoding="utf-8"))

DOCS_SITE_ORIGIN = _DOCS_ORIGIN_CONFIG["canonicalOrigin"]
DOCS_SITE_BASE_URL = _DOCS_ORIGIN_CONFIG["baseUrl"]
DOCS_SITE_URL = DOCS_SITE_ORIGIN + DOCS_SITE_BASE_URL.rstrip("/")

# Permanent origin (docs.metrum.ai) and the temporary origin it replaces.
PERMANENT_DOCS_SITE_ORIGIN = _DOCS_ORIGIN_CONFIG["permanentOrigin"]
TEMPORARY_DOCS_SITE_ORIGIN = _DOCS_ORIGIN_CONFIG["temporaryOrigin"]

# True until the canonical origin is switched to the permanent docs host.
DOCS_ORIGIN_SWITCH_PENDING = DOCS_SITE_ORIGIN != PERMANENT_DOCS_SITE_ORIGIN


def allowed_docs_origins() -> tuple[str, ...]:
    """Return docs origins that public docs, README, llms.txt and packages may link.

    While the switch is pending the canonical origin is the temporary host, and
    the permanent host is rejected because it does not serve /docs/ yet. After
    the switch only the permanent origin is allowed outside historical files.
    """

    origins = [DOCS_SITE_ORIGIN]
    if DOCS_ORIGIN_SWITCH_PENDING and TEMPORARY_DOCS_SITE_ORIGIN not in origins:
        origins.append(TEMPORARY_DOCS_SITE_ORIGIN)
    return tuple(origins)


# Compose / packaging environment variable for the loaded image tag.
VERSION_ENV = "METRUM_AI_ROUTER_VERSION"
VERSION_ENV_LEGACY = "SMART_LLMROUTER_VERSION"

# Public HTTP identity headers on non-docs responses.
HTTP_HEADER_VERSION = "X-Metrum-AI-Router-Version"
HTTP_HEADER_BUILD_DATE = "X-Metrum-AI-Router-Build-Date"
HTTP_HEADER_COMMIT = "X-Metrum-AI-Router-Commit"

# Prometheus metric name prefix (underscores, not hyphens).
METRIC_PREFIX = "metrum_ai_router"

# Customer-facing package binaries (no rename stubs).
PACKAGE_RUNTIME_BINARIES = (
    "metrum-ai-router",
    "metrum-ai-router-token-gen",
    "metrum-ai-router-usage-report",
    "metrum-ai-router-migrate",
    "metrum-ai-routerctl",
)
PACKAGE_BINARIES = PACKAGE_RUNTIME_BINARIES


# Obsolete technical identifiers that must not appear as current product names
# in release artifacts or new public docs (historical release-note titles exempt).
OBSOLETE_PRODUCT_SLUGS = (
    "genai-smart-router",
    "genai-smartrouter",
    "metrum-genai-smartrouter",
    "metrum-genai-smart-router",
    "metrum-router",
    "smart-llmrouter",
    "smart-llm-router",
    "smartllmrouter",
    "smartrouter",
)

RELEASE_ARCHIVE_RE = re.compile(
    rf"^{re.escape(PRODUCT_SLUG)}-(?P<version>.+?)(?P<docker>-docker)?-linux-(?P<arch>amd64|arm64)\.tar\.gz$"
)

# Host used by the temporary docs origin; only docs URLs are public-safe.
TEMPORARY_DOCS_HOST = TEMPORARY_DOCS_SITE_ORIGIN.removeprefix("https://")

# Exact temporary docs URL form that privacy scanners treat as public-safe. It
# stays valid after the switch because the old URLs redirect to docs.metrum.ai.
ALLOWED_DOCS_URL_RE = re.compile(
    re.escape(TEMPORARY_DOCS_SITE_ORIGIN + DOCS_SITE_BASE_URL.rstrip("/"))
    + r"(?:/[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]*)?"
)


def is_allowed_docs_url(url: str) -> bool:
    """Return True when url is a public-safe docs URL on the temporary host."""

    return ALLOWED_DOCS_URL_RE.fullmatch(url.strip()) is not None


def strip_allowed_docs_urls(text: str) -> str:
    """Remove allowed temporary docs URLs so private-host scanners stay fail-closed."""

    return ALLOWED_DOCS_URL_RE.sub(" ", text)


def obsolete_slug_in_filename(name: str) -> str | None:
    """Return the obsolete slug when name is a current product filename using it."""

    # Mask the canonical slug first so metrum-router is not matched inside it.
    masked = name.lower().replace(PRODUCT_SLUG, "«canonical»")
    for slug in OBSOLETE_PRODUCT_SLUGS:
        if re.search(rf"(^|[^A-Za-z0-9]){re.escape(slug)}([^A-Za-z0-9]|$)", masked):
            return slug
    return None
