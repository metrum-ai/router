#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Validate release package contents for package-safe docs and private markers."""

from __future__ import annotations

import argparse
import io
import json
import re
import sys
import tarfile
from pathlib import Path
from typing import Iterable

import canonical_product as product


TEXT_SCAN_LIMIT = 10 * 1024 * 1024
_PACKAGE_BINARY_PATHS = {f"bin/{name}" for name in product.PACKAGE_BINARIES}
BINARY_PACKAGE_FILES = _PACKAGE_BINARY_PATHS | {
    "config/config.example.yaml",
    "config/env.example.json",
    "config/scripts/router.ts",
    "caddy/Caddyfile",
    "LICENSE",
    "NOTICE",
    "THIRD_PARTY_NOTICES.md",
    "MODEL_LICENSES.md",
}
DOCKER_PACKAGE_FILES = {
    "compose/docker-compose.yml",
    "compose/docker-compose.postgres-localhost.yml",
    "compose/Caddyfile.compose",
    "compose/.env.example",
    "compose/.env",
    "config/config.example.yaml",
    "config/env.example.json",
    "config/scripts/router.ts",
    "LICENSE",
    "NOTICE",
    "THIRD_PARTY_NOTICES.md",
    "MODEL_LICENSES.md",
}
PACKAGE_BINARIES = set(_PACKAGE_BINARY_PATHS)
FLEET_ONLY_BINARY_NAMES = set(product.PACKAGE_FLEET_BINARIES)
DOCKER_RUNTIME_BINARIES = {f"/app/bin/{name}" for name in product.PACKAGE_RUNTIME_BINARIES}
EXPECTED_ELF_MACHINE = {"amd64": 62, "arm64": 183}
DOCKER_IMAGE_RE = re.compile(
    rf"^images/{re.escape(product.IMAGE_NAME)}-.+-linux-(amd64|arm64)\.tar$"
)
FORBIDDEN_IMAGE_PATH_RE = re.compile(
    r"^/?(?:"
    r"src/|"
    r"app/(?:docs/|docs-site/|internal/|cmd/|go\.mod|go\.sum|env\.json|config\.production\.yaml|ROUTER_TOKEN[^/]*\.txt|license\.json)|"
    r"docs/|"
    r"docs-site/|"
    r".*\.go$|"
    r".*\.map|"
    r".*\.log|"
    r".*\.jsonl|"
    r".*\.sqlite3?|"
    r".*\.db"
    r")"
)

# Packaged artifacts must never include Go source or repo layout for CLIs.
FORBIDDEN_SOURCE_PATH_RE = re.compile(
    r"(^|/)"
    r"(?:"
    r"cmd/|"
    r"internal/|"
    r"docs-site/|"
    r"\.git/|"
    r"go\.mod$|"
    r"go\.sum$|"
    r".*\.go$"
    r")"
)

FORBIDDEN_NAME_RE = re.compile(
    r"(^|/)(?:"
    r"PRODUCTION_RUNBOOK\.md|"
    r"config\.production\.yaml|"
    r"env\.json|"
    r"ROUTER_TOKENS?[^/]*\.txt|"
    r"license\.json|"
    r".*license-state.*\.json|"
    r".*license-private.*\.json|"
    r".*signing.*\.(?:json|key)|"
    r".*private-key.*\.pem|"
    r".*\.sqlite3?|"
    r".*\.db|"
    r".*\.log|"
    r".*\.jsonl|"
    r"router-state\.json|"
    r"requests\.jsonl"
    r")$"
)
# llm-api.apps.metrum.ai is allowed only as https://llm-api.apps.metrum.ai/docs/...
FORBIDDEN_TEXT_PATTERNS = [
    (
        "private production host marker",
        re.compile(
            r"\b(?:"
            r"100\.30\.225\.66|54\.84\.22\.33|52\.3\.128\.72|"
            r"llm-api-engg\.metrum\.ai|llm-api\.metrum\.ai|llm-api\.apps\.metrum\.ai|"
            r"backups\.metrum\.ai"
            r")\b"
        ),
    ),
    ("private AWS account", re.compile(r"\b121701826775\b")),
    ("stale Metrum-issued license wording", re.compile(r"Metrum-issued")),
    ("private personal Caddy email default", re.compile(r"CADDY_EMAIL:chetan@metrum\.ai|chetan@metrum\.ai\}")),
    (
        "private SSH user or key path",
        re.compile(r"(?:\bubuntu@[A-Za-z0-9_.-]+|~/.ssh/[^\s'\"`]+\.pem|\bssh\s+-i\s+[^\n]+\.pem)"),
    ),
    (
        "live production compose/config/env path",
        re.compile(
            r"/opt/smart-llmrouter/compose/(?:"
            r"config/(?:config\.yaml|env\.json|scripts/router\.ts)|"
            r"ROUTER_TOKEN[^\s'\"`]*|"
            r"\.env|"
            r"state/router-state\.json|"
            r"logs/requests\.jsonl"
            r")"
        ),
    ),
    ("raw Anthropic API key", re.compile(r"\bsk-ant-[A-Za-z0-9_-]{20,}\b")),
    ("raw OpenRouter API key", re.compile(r"\bsk-or-v1-[A-Za-z0-9_-]{20,}\b")),
    ("raw OpenAI-style API key", re.compile(r"\bsk-[A-Za-z0-9_-]{20,}\b")),
    ("raw xAI API key", re.compile(r"\bxai-[A-Za-z0-9_-]{20,}\b")),
    (
        "raw router token",
        re.compile(r"\brtr_metrum(?:_[A-Za-z0-9-]+){4,}_[A-Za-z0-9_-]{20,}\b"),
    ),
    ("GitHub token", re.compile(r"\bgh[pousr]_[A-Za-z0-9_]{20,}\b")),
]


def load_allowlist(path: Path) -> set[str]:
    docs: set[str] = set()
    for line in path.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        docs.add(Path(stripped).name)
    if not docs:
        raise SystemExit(f"{path} does not list any package docs")
    return docs


def package_relative_name(member_name: str) -> str:
    parts = Path(member_name).parts
    if len(parts) <= 1:
        return member_name
    return str(Path(*parts[1:]))


def is_docs_member(member_name: str) -> bool:
    rel = package_relative_name(member_name)
    return rel.startswith("docs/") and rel != "docs/"


def has_appledouble_component(member_name: str) -> bool:
    return any(part.startswith("._") for part in Path(member_name).parts)


def expected_arch(archive: Path) -> str | None:
    name = archive.name
    if "-linux-amd64.tar" in name:
        return "amd64"
    if "-linux-arm64.tar" in name:
        return "arm64"
    return None


def is_docker_package(archive: Path) -> bool:
    return "-docker-linux-" in archive.name


def expected_package_files(archive: Path, allowed_docs: set[str]) -> tuple[set[str], str | None]:
    docs = {f"docs/{doc}" for doc in allowed_docs}
    arch = expected_arch(archive)
    if is_docker_package(archive):
        return DOCKER_PACKAGE_FILES | docs, arch
    return BINARY_PACKAGE_FILES | docs, arch


def validate_elf_arch(blob: bytes, arch: str) -> str | None:
    if len(blob) < 20 or blob[:4] != b"\x7fELF":
        return "is not an ELF binary"
    if blob[4] != 2:
        return "is not a 64-bit ELF binary"
    if blob[5] not in {1, 2}:
        return "uses an unsupported ELF byte order"
    byteorder = "little" if blob[5] == 1 else "big"
    actual_machine = int.from_bytes(blob[18:20], byteorder)
    expected_machine = EXPECTED_ELF_MACHINE[arch]
    if actual_machine != expected_machine:
        return f"has ELF machine {actual_machine}, expected {expected_machine} for linux-{arch}"
    return None


def validate_docker_image_tar(archive: Path, image_rel: str, blob: bytes, expected_arch: str) -> list[str]:
    errors: list[str] = []
    required = set(DOCKER_RUNTIME_BINARIES)
    actual: set[str] = set()
    try:
        with tarfile.open(fileobj=io.BytesIO(blob), mode="r:*") as image:
            image_members = {member.name: member for member in image.getmembers()}
            layer_names: list[str] = []
            config_names: list[str] = []
            for image_member in image.getmembers():
                if has_appledouble_component(image_member.name):
                    errors.append(f"{archive}: {image_rel} contains AppleDouble metadata entry: {image_member.name}")
                if image_member.name == "manifest.json" and image_member.isfile():
                    manifest_file = image.extractfile(image_member)
                    if manifest_file is not None:
                        manifest = json.loads(manifest_file.read().decode("utf-8"))
                        if isinstance(manifest, list):
                            for item in manifest:
                                layers = item.get("Layers") if isinstance(item, dict) else None
                                if isinstance(layers, list):
                                    layer_names.extend(layer for layer in layers if isinstance(layer, str))
                                config_name = item.get("Config") if isinstance(item, dict) else None
                                if isinstance(config_name, str):
                                    config_names.append(config_name)
                elif image_member.isfile() and (
                    image_member.name == "layer.tar" or image_member.name.endswith("/layer.tar")
                ):
                    layer_names.append(image_member.name)

            for config_name in dict.fromkeys(config_names):
                config_member = image_members.get(config_name)
                if config_member is None or not config_member.isfile():
                    errors.append(f"{archive}: {image_rel} manifest references missing config {config_name}")
                    continue
                config_file = image.extractfile(config_member)
                if config_file is None:
                    continue
                config = json.loads(config_file.read().decode("utf-8"))
                actual_arch = config.get("architecture") if isinstance(config, dict) else None
                actual_os = config.get("os") if isinstance(config, dict) else None
                if actual_os != "linux" or actual_arch != expected_arch:
                    errors.append(
                        f"{archive}: {image_rel} image platform {actual_os}/{actual_arch} "
                        f"does not match linux/{expected_arch}"
                    )

            for layer_name in dict.fromkeys(layer_names):
                layer_member = image_members.get(layer_name)
                if layer_member is None:
                    errors.append(f"{archive}: {image_rel} manifest references missing layer {layer_name}")
                    continue
                layer_file = image.extractfile(layer_member)
                if layer_file is None:
                    continue
                with tarfile.open(fileobj=io.BytesIO(layer_file.read()), mode="r:*") as layer:
                    for layer_member in layer.getmembers():
                        name = "/" + layer_member.name.lstrip("./")
                        normalized_layer_name = name.lstrip("/")
                        if has_appledouble_component(layer_member.name):
                            errors.append(f"{archive}: {image_rel} layer contains AppleDouble metadata entry: {layer_member.name}")
                        if FORBIDDEN_IMAGE_PATH_RE.search(normalized_layer_name):
                            errors.append(f"{archive}: {image_rel} layer contains forbidden runtime/source path: {layer_member.name}")
                        if layer_member.isfile() and Path(normalized_layer_name).name in FLEET_ONLY_BINARY_NAMES:
                            errors.append(f"{archive}: {image_rel} contains forbidden fleet lifecycle binary {name}")
                        if name in required and layer_member.isfile():
                            actual.add(name)
    except (json.JSONDecodeError, tarfile.TarError, UnicodeDecodeError) as exc:
        return [f"{archive}: {image_rel} is not a readable Docker image tar: {exc}"]

    for path in sorted(required - actual):
        errors.append(f"{archive}: {image_rel} is missing required image file {path}")
    return errors


def should_scan_text(member_name: str, size: int) -> bool:
    if size > TEXT_SCAN_LIMIT:
        return False
    suffixes = Path(member_name).suffixes
    if any(suffix in {".tar", ".gz", ".zip"} for suffix in suffixes):
        return False
    return True


def decode_text(blob: bytes) -> str | None:
    if b"\x00" in blob:
        return None
    try:
        return blob.decode("utf-8")
    except UnicodeDecodeError:
        return None


def validate_archive(archive: Path, allowed_docs: set[str]) -> list[str]:
    errors: list[str] = []
    actual_docs: set[str] = set()
    actual_files: set[str] = set()
    image_files: set[str] = set()
    expected_files, arch = expected_package_files(archive, allowed_docs)

    try:
        package = tarfile.open(archive, "r:*")
    except tarfile.TarError as exc:
        return [f"{archive}: cannot read tar archive: {exc}"]

    with package:
        members = package.getmembers()
        member_roots = {Path(member.name).parts[0] for member in members if Path(member.name).parts}
        expected_root = archive.name.removesuffix(".tar.gz") if arch is not None else None
        if len(member_roots) != 1:
            errors.append(f"{archive}: archive must contain exactly one top-level directory")
        elif expected_root is not None and member_roots != {expected_root}:
            errors.append(f"{archive}: top-level directory must be {expected_root}")
        for member in members:
            parts = Path(member.name).parts
            if (
                not parts
                or member.name.startswith("/")
                or any(part in {"", ".", ".."} for part in parts)
            ):
                errors.append(f"{archive}: unsafe archive path: {member.name}")
            if not (member.isfile() or member.isdir()):
                errors.append(f"{archive}: links and special archive entries are forbidden: {member.name}")
            rel = package_relative_name(member.name)
            if has_appledouble_component(member.name):
                errors.append(f"{archive}: AppleDouble metadata entry included: {rel}")
            if FORBIDDEN_NAME_RE.search(member.name):
                errors.append(f"{archive}: forbidden local secret/state file included: {rel}")
            if FORBIDDEN_SOURCE_PATH_RE.search(rel):
                errors.append(f"{archive}: forbidden source path included: {rel}")

            if member.isfile():
                actual_files.add(rel)
                if DOCKER_IMAGE_RE.fullmatch(rel):
                    image_files.add(rel)

            if is_docs_member(member.name) and member.isfile():
                actual_docs.add(Path(rel).name)

            if not member.isfile():
                continue

            extracted = package.extractfile(member)
            if extracted is None:
                continue

            blob = extracted.read()
            if rel in PACKAGE_BINARIES and arch is not None:
                arch_error = validate_elf_arch(blob[:64], arch)
                if arch_error:
                    errors.append(f"{archive}: {rel} {arch_error}")

            if DOCKER_IMAGE_RE.fullmatch(rel):
                image_arch = DOCKER_IMAGE_RE.fullmatch(rel).group(1)
                errors.extend(validate_docker_image_tar(archive, rel, blob, arch or image_arch))

            if not should_scan_text(member.name, member.size):
                continue

            text = decode_text(blob[: TEXT_SCAN_LIMIT + 1])
            if text is None:
                continue
            privacy_text = product.strip_allowed_docs_urls(text)
            for label, pattern in FORBIDDEN_TEXT_PATTERNS:
                if pattern.search(privacy_text):
                    errors.append(f"{archive}: {rel} contains {label}")

    unexpected_files = actual_files - expected_files
    missing_files = expected_files - actual_files
    if is_docker_package(archive):
        for path in sorted(unexpected_files - image_files):
            errors.append(f"{archive}: unexpected package file included: {path}")
        if len(image_files) != 1:
            errors.append(f"{archive}: expected exactly one packaged Docker image tar, found {len(image_files)}")
        elif arch is not None:
            image_arch = DOCKER_IMAGE_RE.fullmatch(next(iter(image_files))).group(1)
            if image_arch != arch:
                errors.append(f"{archive}: Docker image tar architecture {image_arch} does not match package linux-{arch}")
    else:
        for path in sorted(unexpected_files):
            errors.append(f"{archive}: unexpected package file included: {path}")

    for path in sorted(missing_files):
        errors.append(f"{archive}: required package file is missing: {path}")

    extra_docs = actual_docs - allowed_docs
    missing_docs = allowed_docs - actual_docs
    for doc in sorted(extra_docs):
        errors.append(f"{archive}: docs/{doc} is not in package docs allowlist")
    for doc in sorted(missing_docs):
        errors.append(f"{archive}: docs/{doc} from package docs allowlist is missing")

    return errors


def validate_archives(archives: Iterable[Path], allowlist: Path) -> list[str]:
    allowed_docs = load_allowlist(allowlist)
    errors: list[str] = []
    for archive in archives:
        errors.extend(validate_archive(archive, allowed_docs))
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--allowlist", default="scripts/package_docs_allowlist.txt", type=Path)
    parser.add_argument("archives", nargs="+", type=Path)
    args = parser.parse_args()

    errors = validate_archives(args.archives, args.allowlist)
    if errors:
        print("package content validation failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print(f"validated {len(args.archives)} package artifact(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
