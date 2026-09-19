#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Check public-facing docs for private markers, stale routes and stale branding.

Three independent rule sets run over two independent path lists:

* PUBLIC_DOC_PATHS keeps the privacy, stale-route and doc_type coverage.
* BRANDING_PATHS carries the wider product-branding coverage (root/community
  docs, internal and package docs, docs-site config and components, issue
  templates, product-facing runtime/generator sources and the tracked
  generated admin UI output).

Widening branding coverage never adds privacy rules to a file, and the
historical-file privacy exemptions never exempt a file from branding rules.
"""

from __future__ import annotations

import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

import canonical_product


ROOT = Path(__file__).resolve().parents[1]

PUBLIC_DOC_PATHS = [
    ROOT / "README.md",
    ROOT / "GOVERNANCE.md",
    ROOT / "SUPPORT.md",
    ROOT / "SECURITY.md",
    ROOT / "docs-site" / "docs",
    ROOT / "docs-site" / "src",
    ROOT / "docs" / "DOCKER_DEPLOYMENT.md",
    ROOT / "docs" / "DEPLOYMENT.md",
    ROOT / "docs" / "solution-brief.md",
    ROOT / "docs" / "PRODUCT_CAPABILITY_MATRIX.md",
    ROOT / "docs" / "SMOKE_TEST_MATRIX.md",
    ROOT / "docs" / "CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md",
    ROOT / "docs" / "TROUBLESHOOTING_RUNBOOK.md",
    ROOT / "docs" / "LICENSE_OPERATIONS.md",
    ROOT / "docs" / "evidence",
    ROOT / "ops.env.example.json",
    ROOT / "config.example.yaml",
    ROOT / "deploy" / "Caddyfile.compose",
    ROOT / "examples" / "customer-lifecycle" / "onboard-acme.sandbox.example.json",
]

# Exempt from the privacy rules only. Branding rules still apply to these
# files: a case study may keep historical commands and results, but must not
# keep an obsolete current-product title or navigation label.
HISTORICAL_FILES = {
    Path("docs-site/docs/evaluation/harbor-case-study.mdx"),
    Path("docs/harbor-case-study.md"),
}

DOC_TYPE_VALUES = {"tutorial", "howto", "reference", "explanation"}
DOCS_SITE_DOCS = ROOT / "docs-site" / "docs"

# llm-api.apps.metrum.ai is allowed only as the temporary public docs origin
# (https://llm-api.apps.metrum.ai/docs/...). Other uses of that host remain private.
PRIVATE_PATTERNS = [
    (
        "private production host/IP",
        re.compile(
            r"\b(?:"
            r"100\.30\.225\.66|54\.84\.22\.33|52\.3\.128\.72|"
            r"llm-api-engg\.metrum\.ai|llm-api\.metrum\.ai|llm-api\.apps\.metrum\.ai|"
            r"backups\.metrum\.ai"
            r")\b"
        ),
    ),
    ("private AWS account", re.compile(r"\b121701826775\b")),
    ("private backup bucket", re.compile(r"metrum-backups/smart-llmrouter|RESTIC_REPO_PATH.:\s*[\"']metrum-cto")),
    ("stale Metrum-issued license wording", re.compile(r"Metrum-issued")),
    (
        "private personal copy-paste email",
        re.compile(r"(?:CADDY_EMAIL=|email\s+\{?\$\{?CADDY_EMAIL:)chetan@metrum\.ai"),
    ),
    ("private real caller-id prefix", re.compile(r"rtr_metrum_chetan_metrum-insights_")),
    ("private SSH detail", re.compile(r"(?:\bubuntu@[A-Za-z0-9_.-]+|~/.ssh/[^\s'\"`]+\.pem|\bssh\s+-i\s+[^\n]+\.pem)")),
    # Broader infrastructure leakage (evidence artifacts, operator notes).
    # Placeholder docs that use <angle-brackets> are skipped in rel_privacy_errors.
    ("ssh identity invocation", re.compile(r"\bssh\s+-i\s+\S+")),
    (
        "user@public-IPv4 SSH target",
        re.compile(r"\b[A-Za-z0-9._-]+@(?:\d{1,3}\.){3}\d{1,3}\b"),
    ),
    (
        "public IPv4 address",
        re.compile(
            r"\b(?!(?:127|10|0)\.|192\.168\.|172\.(?:1[6-9]|2\d|3[0-1])\.)"
            r"(?:\d{1,3}\.){3}\d{1,3}\b"
        ),
    ),
    (
        "cloud instance id field",
        re.compile(
            r'"(?:shadeform_)?instance_id(?:_cpu|_gpu)?"\s*:\s*"[0-9a-fA-F-]{8,}"'
            r"|\b(?:shadeform_)?instance_id(?:_cpu|_gpu)?\b[^\n]{0,40}"
            r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
        ),
    ),
    (
        "live bearer or API token literal",
        re.compile(
            r"(?i)(?:authorization:\s*bearer\s+(?!\$\{|<|REPLACE)[A-Za-z0-9._\-+=/]{20,}"
            r"|\b(?:sk-ant-|sk-or-v1-|sk_live_|sk_test_|gh[pousr]_)[A-Za-z0-9._\-]{12,})"
        ),
    ),
    ("object-storage bucket URL", re.compile(r"\b(?:s3|gs)://[A-Za-z0-9._/-]+")),
    (
        "live production compose/config path",
        re.compile(
            r"/opt/smart-llmrouter/compose/(?:"
            r"config/(?:config\.yaml|env\.json|scripts/router\.ts)|"
            r"ROUTER_TOKEN[^\s'\"`]*|"
            r"\.env|state/router-state\.json|logs/requests\.jsonl"
            r")"
        ),
    ),
]

# Patterns that must not fire on documentation placeholders such as
# `ssh -i <operator-ssh-key> <user>@<operator-host>`.
_PLACEHOLDER_INFRA_LABELS = {
    "ssh identity invocation",
    "user@public-IPv4 SSH target",
    "public IPv4 address",
}

STALE_CURRENT_ROUTE_PATTERNS = [
    (
        "stale active OpenRouter DeepSeek route claim",
        re.compile(r"\b(?:current|active|production|reference|hosted)[^\n.]{0,120}\bOpenRouter\b[^\n.]{0,120}\bDeepSeek\b|\bOpenRouter\b[^\n.]{0,120}\bDeepSeek\b[^\n.]{0,120}\b(?:current|active|production|reference|hosted)\b", re.IGNORECASE),
    ),
    (
        "stale active Qwen route claim",
        re.compile(r"\b(?:current|active|production|reference|hosted)[^\n.]{0,120}\bQwen 3\.6\b|\bQwen 3\.6\b[^\n.]{0,120}\b(?:current|active|production|reference|hosted)\b", re.IGNORECASE),
    ),
    (
        "stale Crusoe catalog-only claim for Gemma",
        re.compile(r"\bCrusoe\b[^\n.]{0,160}\bGemma\b[^\n.]{0,160}\bcatalog-only\b|\bcatalog-only\b[^\n.]{0,160}\bCrusoe\b[^\n.]{0,160}\bGemma\b", re.IGNORECASE),
    ),
]

# Canonical current product display name for public docs, site metadata,
# operator-facing prose, runtime banners, and legal attribution. Obsolete
# display names below are rejected wherever they name the current product;
# company-only "Metrum AI", generic "router"/"routing" prose, third-party
# router names and hyphenated or concatenated technical identifiers
# (genai-smart-router, smart-llmrouter, smartrouterctl, metrum-ai-router, ...)
# are deliberately not branding errors. Category prose such as "LLM smart
# router" or bare "smart router" is not a product title and is allowed.
CANONICAL_PRODUCT_TITLE = "Metrum AI Router"

# Display names are whitespace-separated words. Requiring real whitespace
# between the words is what keeps command names, package names, schema keys,
# Kubernetes labels and Helm chart IDs out of the branding rules.
_WS = r"[ \t\u00a0]+"
# Optional whitespace covers concatenated display spellings such as
# "Metrum SmartRouter" while still refusing to match "smartrouterctl".
_OPTWS = r"[ \t\u00a0]*"
# Reject adjacency with identifier characters so hyphenated/underscored/slashed
# identifiers and longer words ("Smart Routing") never match.
_LEFT = r"(?<![\w./-])"
_RIGHT = r"(?![\w/-])"


def _title_pattern(*words: str) -> re.Pattern[str]:
    """Build a case-insensitive, spacing-tolerant display-name pattern."""

    return re.compile(_LEFT + _WS.join(words) + _RIGHT, re.IGNORECASE)


# Ordered longest-first: the first pattern that matches a span wins, so
# "Metrum GenAI Smart Router" reports one precise diagnostic instead of three.
# Bare "smart router" is not obsolete: category prose may use those words.
# "Metrum Smart Router" is an obsolete product title and is rejected.
OBSOLETE_PRODUCT_TITLE_PATTERNS = [
    (
        "malformed product title",
        _title_pattern(r"Metrum", r"Metrum(?:" + _WS + r"AI)?", r"Router"),
    ),
    (
        "malformed product title",
        _title_pattern(r"Metrum", r"AI", r"AI", r"Router"),
    ),
    (
        "obsolete product title Metrum GenAI Smart Router",
        _title_pattern(r"Metrum", r"Gen" + _OPTWS + r"AI", r"Smart" + _OPTWS + r"Router"),
    ),
    (
        "obsolete product title Metrum AI Smart Router",
        _title_pattern(r"Metrum", r"AI", r"Smart" + _OPTWS + r"Router"),
    ),
    (
        "obsolete product title Metrum Smart LLM Router",
        _title_pattern(r"Metrum", r"Smart", r"LLM", r"Router"),
    ),
    (
        "obsolete product title Metrum Smart Router",
        _title_pattern(r"Metrum", r"Smart" + _OPTWS + r"Router"),
    ),
    (
        "obsolete product title GenAI Smart Router",
        _title_pattern(r"Gen" + _OPTWS + r"AI", r"Smart" + _OPTWS + r"Router"),
    ),
    (
        "obsolete product title Smart LLM Router",
        _title_pattern(r"Smart", r"LLM", r"Router"),
    ),
    (
        "obsolete product title Metrum Router",
        _title_pattern(r"Metrum", r"Router"),
    ),
]

# Backwards-compatible alias for callers that import the previous name.
STALE_PRODUCT_TITLE_PATTERNS = OBSOLETE_PRODUCT_TITLE_PATTERNS

# One alternation over all display names. Regex alternation prefers earlier
# alternatives at the same start offset, so the longest-first ordering above
# yields exactly one diagnostic per occurrence instead of nested duplicates.
OBSOLETE_TITLE_LABELS = {
    f"b{index}": label for index, (label, _) in enumerate(OBSOLETE_PRODUCT_TITLE_PATTERNS)
}
OBSOLETE_TITLE_SCANNER = re.compile(
    "|".join(
        f"(?P<b{index}>{pattern.pattern})"
        for index, (_, pattern) in enumerate(OBSOLETE_PRODUCT_TITLE_PATTERNS)
    ),
    re.IGNORECASE,
)

# Any line may keep an obsolete display name when it carries an explicit,
# reasoned marker, on the line itself or on the line immediately above:
#   <!-- branding-exception: published v1.2.0 release title -->
BRANDING_EXCEPTION_MARKER = re.compile(r"branding-exception:[ \t]*(?P<reason>\S.*?)\s*(?:-->|\*/)?\s*$")


BRANDING_EXCEPTION_WINDOW = 80


@dataclass(frozen=True)
class BrandingException:
    """Narrow contextual exception for one kind of genuine historical usage.

    The exception is evaluated per occurrence rather than per line or per file,
    against a bounded window of the text immediately before and after the
    matched name. A line that lists former names therefore still fails when it
    also presents an obsolete name as the current product.
    """

    path: Path
    reason: str
    before: re.Pattern[str] | None = None
    after: re.Pattern[str] | None = None

    def allows(self, rel: Path, line: str, start: int, end: int) -> bool:
        if self.path != rel:
            return False
        if self.before is not None:
            prefix = line[max(0, start - BRANDING_EXCEPTION_WINDOW) : start]
            if not self.before.search(prefix):
                return False
        if self.after is not None:
            suffix = line[end : end + BRANDING_EXCEPTION_WINDOW]
            if not self.after.search(suffix):
                return False
        return True


# Precise contextual exceptions replace the previous blanket per-file
# exemptions. Anything not covered here needs an explicit per-line marker.
BRANDING_EXCEPTIONS = (
    BrandingException(
        path=Path("TRADEMARKS.md"),
        reason="former-name enumeration needed for trademark history",
        # The former-name wording must precede the name, inside the same
        # sentence, so a line cannot smuggle in a current-product claim.
        before=re.compile(
            r"\b(?:former|formerly|former name|previously|prior name|historical|"
            r"historically|no longer used)\b[^.]*$",
            re.IGNORECASE,
        ),
    ),
    BrandingException(
        path=Path("docs-site/docs/release-notes/index.md"),
        reason="published release titles are immutable artifact identity",
        # Only "<obsolete name> v1.2.3" survives: prose about the current
        # product in the same file is still rejected.
        after=re.compile(r"^\s+v\d+\.\d+\.\d+\b"),
    ),
)

# The policy definition itself necessarily spells out the rejected names.
POLICY_SOURCE_FILES = {
    Path("scripts/check_docs_public_face.py"),
    Path("scripts/check_docs_public_face_test.py"),
}

# Branding coverage is intentionally wider than the privacy/version coverage in
# PUBLIC_DOC_PATHS, and is evaluated independently so neither check is weakened
# by the other's path list.
BRANDING_PATHS = [
    # Root / community / shared configuration.
    ROOT / "README.md",
    ROOT / "CONTRIBUTING.md",
    ROOT / "CODE_OF_CONDUCT.md",
    ROOT / "GOVERNANCE.md",
    ROOT / "SUPPORT.md",
    ROOT / "SECURITY.md",
    ROOT / "TRADEMARKS.md",
    ROOT / "NOTICE",
    ROOT / "MODEL_LICENSES.md",
    ROOT / "THIRD_PARTY_NOTICES.md",
    ROOT / "Makefile",
    ROOT / "config.example.yaml",
    ROOT / "config.minimal.example.yaml",
    ROOT / ".github" / "CODEOWNERS",
    ROOT / ".github" / "PULL_REQUEST_TEMPLATE.md",
    ROOT / ".github" / "ISSUE_TEMPLATE",
    # Internal / operator / package documentation.
    ROOT / "docs",
    # Public documentation and website source, including Docusaurus config and
    # React components that render titles, navbar, footer and metadata.
    ROOT / "docs-site" / "docs",
    ROOT / "docs-site" / "src",
    ROOT / "docs-site" / "docusaurus.config.js",
    ROOT / "docs-site" / "sidebars.js",
    ROOT / "docs-site" / "package.json",
    # Examples and deployment metadata.
    ROOT / "examples",
    ROOT / "deploy",
    # Product-facing runtime and generator sources (admin reports, fallback
    # docs, model catalog, config defaults, blueprints, Helm metadata, CLI
    # help) plus the admin UI source and the tracked generated admin output.
    ROOT / "internal",
    ROOT / "cmd",
    ROOT / "services",
    ROOT / "evaluators",
    # Harness and generator scripts that emit product-facing display names,
    # for example agent provider names written into generated client config.
    ROOT / "scripts",
]

BRANDING_SUFFIXES = {
    ".md",
    ".mdx",
    ".txt",
    ".json",
    ".yaml",
    ".yml",
    ".toml",
    ".js",
    ".jsx",
    ".ts",
    ".tsx",
    ".html",
    ".css",
    ".go",
    ".py",
    ".c",
    ".h",
    ".sh",
}

# Extensionless files worth scanning when they appear in BRANDING_PATHS.
BRANDING_EXTRA_NAMES = {"Makefile", "NOTICE", "CODEOWNERS"}

# Generated or vendored trees, lockfiles and test files are excluded: lockfiles
# carry only package identifiers, and tests hold deliberate negative fixtures
# and expected-output literals that their own suites own.
BRANDING_EXCLUDED_DIRS = {"node_modules", "docsdist", "build", ".docusaurus", "dist"}
BRANDING_EXCLUDED_NAMES = {"package-lock.json", "yarn.lock", "pnpm-lock.yaml"}


def iter_public_files() -> Iterable[Path]:
    for path in PUBLIC_DOC_PATHS:
        if path.is_dir():
            yield from sorted(
                p
                for p in path.rglob("*")
                if p.is_file()
                and p.suffix in {".md", ".mdx", ".js", ".jsx", ".ts", ".tsx", ".json", ".yaml", ".yml"}
                and "node_modules" not in p.parts
            )
        elif path.exists():
            yield path


def iter_branding_files() -> Iterable[Path]:
    for path in BRANDING_PATHS:
        if path.is_dir():
            yield from sorted(p for p in path.rglob("*") if is_branding_file(p))
        elif path.exists():
            yield path


def is_branding_file(path: Path) -> bool:
    if not path.is_file():
        return False
    if path.name in BRANDING_EXCLUDED_NAMES:
        return False
    if BRANDING_EXCLUDED_DIRS.intersection(path.parts):
        return False
    if path.stem.endswith("_test") or path.stem.startswith("test_"):
        return False
    return path.suffix in BRANDING_SUFFIXES or path.name in BRANDING_EXTRA_NAMES


COMMENT_TERMINATORS = ("-->", "*/}", "*/", "#}", "}}")


def strip_comment_terminator(reason: str) -> str:
    """Drop trailing comment syntax so HTML, Go, JSX and Jinja markers agree."""

    reason = reason.strip()
    changed = True
    while changed:
        changed = False
        for terminator in COMMENT_TERMINATORS:
            if reason.endswith(terminator):
                reason = reason[: -len(terminator)].strip()
                changed = True
    return reason


def marker_exception_reason(line: str, prev_line: str = "") -> str | None:
    """Return the reason from an explicit per-line branding exception marker."""

    for candidate in (line, prev_line):
        match = BRANDING_EXCEPTION_MARKER.search(candidate)
        if not match:
            continue
        reason = strip_comment_terminator(match.group("reason"))
        if reason:
            return reason
    return None


def contextual_exception_reason(rel: Path, line: str, start: int, end: int) -> str | None:
    """Return the reason when a narrow historical context covers a match."""

    for exception in BRANDING_EXCEPTIONS:
        if exception.allows(rel, line, start, end):
            return exception.reason
    return None


def branding_line_errors(
    path: Path,
    line_no: int,
    line: str,
    prev_line: str = "",
) -> Iterable[str]:
    """Report obsolete current-product display names on a single line."""

    return rel_branding_errors(path.relative_to(ROOT), line_no, line, prev_line)


def rel_branding_errors(
    rel: Path,
    line_no: int,
    line: str,
    prev_line: str = "",
) -> Iterable[str]:
    if rel in POLICY_SOURCE_FILES:
        return

    if "branding-exception:" in line and marker_exception_reason(line) is None:
        yield f"{rel}:{line_no}: branding-exception marker needs a written reason"
        return

    # Every obsolete display name ends in "router"; the substring test keeps the
    # regex pass off the overwhelming majority of lines in large source files.
    if "router" not in line.lower():
        return

    matches = list(OBSOLETE_TITLE_SCANNER.finditer(line))
    if not matches:
        return

    if marker_exception_reason(line, prev_line) is not None:
        return

    for match in matches:
        start, end = match.span()
        if contextual_exception_reason(rel, line, start, end) is not None:
            continue
        label = next(
            OBSOLETE_TITLE_LABELS[name]
            for name, value in match.groupdict().items()
            if value is not None
        )
        yield (
            f"{rel}:{line_no}: contains {label} {match.group(0)!r}; "
            f"use {CANONICAL_PRODUCT_TITLE} "
            "or annotate the line with 'branding-exception: <reason>'"
        )


def privacy_line_errors(path: Path, line_no: int, line: str) -> Iterable[str]:
    """Report private markers and stale current-route claims on a single line."""

    yield from rel_privacy_errors(path.relative_to(ROOT), line_no, line)


def rel_privacy_errors(rel: Path, line_no: int, line: str) -> Iterable[str]:
    # Temporary public docs URLs are stripped before private-host matching so
    # https://llm-api.apps.metrum.ai/docs remains documentable while /v1 and
    # bare-host API examples stay forbidden.
    privacy_line = canonical_product.strip_allowed_docs_urls(line)
    placeholder_line = "<" in privacy_line and ">" in privacy_line
    if rel not in HISTORICAL_FILES:
        for label, pattern in PRIVATE_PATTERNS:
            if placeholder_line and label in _PLACEHOLDER_INFRA_LABELS:
                continue
            if pattern.search(privacy_line):
                yield f"{rel}:{line_no}: contains {label}"

    for label, pattern in STALE_CURRENT_ROUTE_PATTERNS:
        if pattern.search(line):
            yield f"{rel}:{line_no}: contains {label}; mark historical or update to config.example.yaml"


def line_errors(path: Path, line_no: int, line: str, prev_line: str = "") -> Iterable[str]:
    yield from privacy_line_errors(path, line_no, line)
    yield from branding_line_errors(path, line_no, line, prev_line)


def doc_type_error(path: Path, text: str) -> str | None:
    if not path.is_relative_to(DOCS_SITE_DOCS) or path.suffix not in {".md", ".mdx"}:
        return None
    rel = path.relative_to(ROOT)
    if not text.startswith("---\n"):
        return f"{rel}: missing frontmatter with doc_type"
    end = text.find("\n---\n", 4)
    if end == -1:
        return f"{rel}: malformed frontmatter"
    frontmatter = text[4:end]
    matches = re.findall(r"^doc_type:\s*([A-Za-z_-]+)\s*$", frontmatter, flags=re.MULTILINE)
    if not matches:
        return f"{rel}: missing doc_type frontmatter"
    if len(matches) > 1:
        return f"{rel}: has multiple doc_type frontmatter fields"
    if matches[0] not in DOC_TYPE_VALUES:
        return f"{rel}: invalid doc_type {matches[0]!r}; expected one of {', '.join(sorted(DOC_TYPE_VALUES))}"
    return None


def file_errors(path: Path, text: str, *, privacy: bool, branding: bool) -> Iterable[str]:
    rel = path.relative_to(ROOT)
    if privacy:
        doc_type = doc_type_error(path, text)
        if doc_type:
            yield doc_type
    prev_line = ""
    for idx, line in enumerate(text.splitlines(), start=1):
        if privacy:
            yield from rel_privacy_errors(rel, idx, line)
        if branding:
            yield from rel_branding_errors(rel, idx, line, prev_line)
        prev_line = line


def main() -> int:
    errors: list[str] = []
    public_files = set(iter_public_files())
    branding_files = set(iter_branding_files())
    for path in sorted(public_files | branding_files):
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        errors.extend(
            file_errors(
                path,
                text,
                privacy=path in public_files,
                branding=path in branding_files,
            )
        )

    if errors:
        print("public docs QA failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print("public docs QA passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
