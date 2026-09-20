#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Canonical Metrum AI Router product identifiers shared by release and docs QA.

Import these constants instead of hard-coding product slugs, archive prefixes,
docs origins, or package/image names. Go module and GitHub repository paths
remain github.com/metrum-ai/router and are intentionally not the product slug.
"""

from __future__ import annotations

import re

# Display name for public docs, site metadata, and operator-facing prose.
PRODUCT_TITLE = "Metrum AI Router"

# Technical slug for binaries, archives, images, Compose, and Kubernetes.
PRODUCT_SLUG = "metrum-ai-router"

PKG_NAME = PRODUCT_SLUG
IMAGE_NAME = PRODUCT_SLUG

# Temporary public documentation origin until docs.metrum.ai is hosted.
DOCS_SITE_ORIGIN = "https://llm-api.apps.metrum.ai"
DOCS_SITE_BASE_URL = "/docs/"
DOCS_SITE_URL = DOCS_SITE_ORIGIN + DOCS_SITE_BASE_URL.rstrip("/")

# Future documentation origin (tracked by the docs.metrum.ai hosting issue).
FUTURE_DOCS_SITE_ORIGIN = "https://docs.metrum.ai"

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
PACKAGE_FLEET_BINARIES = (
    "metrum-ai-router-fleetctl",
    "metrum-ai-router-fleet-sign",
)
PACKAGE_BINARIES = PACKAGE_RUNTIME_BINARIES + PACKAGE_FLEET_BINARIES

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

# Exact temporary docs URL form allowed in public docs and packages.
ALLOWED_DOCS_URL_RE = re.compile(
    r"https://llm-api\.apps\.metrum\.ai/docs(?:/[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]*)?"
)

# Host used by the temporary docs origin; only docs URLs are public-safe.
TEMPORARY_DOCS_HOST = "llm-api.apps.metrum.ai"


def is_allowed_docs_url(url: str) -> bool:
    """Return True when url is the temporary public documentation origin."""

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
