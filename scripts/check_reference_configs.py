#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Validate deploy/reference tiers: layout, image pins, README sections, config.

The reference configurations are meant to be interchangeable: same file set,
same service and group names, official upstream images pinned by digest. This
checker fails when a tier drifts from that contract. It never contacts a
registry; digest pins are compared between compose.yaml and images.lock.
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[1]
REFERENCE_DIR = ROOT / "deploy" / "reference"

REQUIRED_FILES = (
    "README.md",
    "SIZING.md",
    "config.yaml",
    "compose.yaml",
    "images.lock",
    "results/README.md",
)
OPTIONAL_DIRS = {"kubernetes", "results"}

UPSTREAM_MODEL_SERVER_REPOS = frozenset({"vllm/vllm-openai", "rocm/vllm"})

README_SECTIONS = (
    "## Hardware",
    "## Images",
    "## Model groups",
    "## Deploy",
    "## Measured results",
    "## Actual cost",
    "## Limits",
    "## Rerun",
)

DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
IMAGE_RE = re.compile(r"^(?P<repo>[^@\s]+?)(?::(?P<tag>[^@\s]+))?(?:@(?P<digest>sha256:[0-9a-f]{64}))?$")
REVISION_RE = re.compile(r"^[0-9a-f]{40}$")

# Placeholder hash accepted only in the checked-in configuration; the README
# must tell the operator to generate a real token before serving.
PLACEHOLDER_TOKEN_SHA256 = "0" * 64


class Problems:
    def __init__(self) -> None:
        self.items: list[str] = []

    def add(self, where: Path | str, message: str) -> None:
        self.items.append(f"{where}: {message}")

    def extend(self, other: "Problems") -> None:
        self.items.extend(other.items)

    def __bool__(self) -> bool:
        return bool(self.items)


def split_image(reference: str) -> tuple[str, str | None, str | None]:
    match = IMAGE_RE.match(reference)
    if not match:
        raise ValueError(f"unparseable image reference {reference!r}")
    return match.group("repo"), match.group("tag"), match.group("digest")


def official_repo(repo: str) -> str | None:
    """Return the repository name for an official model-server image, else None."""
    if repo in UPSTREAM_MODEL_SERVER_REPOS:
        return repo
    return None


def check_tier_layout(tier: Path, problems: Problems) -> None:
    for relative in REQUIRED_FILES:
        if not (tier / relative).is_file():
            problems.add(tier.name, f"missing {relative}")
    for child in sorted(tier.iterdir()):
        if child.name in OPTIONAL_DIRS:
            continue
        if child.is_file() and child.name not in REQUIRED_FILES:
            problems.add(tier.name, f"unexpected file {child.name}")
        if child.is_dir():
            problems.add(tier.name, f"unexpected directory {child.name}/")


def check_readme(tier: Path, problems: Problems) -> None:
    text = (tier / "README.md").read_text(encoding="utf-8")
    for section in README_SECTIONS:
        if section not in text:
            problems.add(tier.name, f"README.md is missing section {section!r}")
    if PLACEHOLDER_TOKEN_SHA256 in (tier / "config.yaml").read_text(encoding="utf-8"):
        if "callers generate" not in text:
            problems.add(tier.name, "README.md must say how to replace the placeholder caller token")


def load_yaml(path: Path, problems: Problems) -> object | None:
    try:
        return yaml.safe_load(path.read_text(encoding="utf-8"))
    except yaml.YAMLError as err:
        problems.add(path.relative_to(ROOT), f"invalid YAML: {err}")
        return None


def check_images_lock(tier: Path, problems: Problems) -> dict[str, str]:
    """Return image reference -> digest for every locked model-server image."""
    lock = load_yaml(tier / "images.lock", problems)
    locked: dict[str, str] = {}
    if not isinstance(lock, dict):
        return locked
    if lock.get("schema") != "metrum.ai/reference-images/v1":
        problems.add(tier.name, "images.lock schema must be metrum.ai/reference-images/v1")
    images = lock.get("images")
    if not isinstance(images, list) or not images:
        problems.add(tier.name, "images.lock must list at least one image")
        return locked
    for entry in images:
        if not isinstance(entry, dict):
            problems.add(tier.name, "images.lock entries must be mappings")
            continue
        image = str(entry.get("image", ""))
        digest = str(entry.get("digest", ""))
        try:
            repo, tag, inline = split_image(image)
        except ValueError as err:
            problems.add(tier.name, str(err))
            continue
        if inline is not None:
            problems.add(tier.name, f"images.lock must not inline a digest in image: {image}")
        if not tag:
            problems.add(tier.name, f"{image} has no tag")
        elif tag == "latest":
            problems.add(tier.name, f"{image} resolves to latest")
        if inline is None:
            reference = f"{repo}:{tag}"
            if official_repo(repo) is None:
                problems.add(tier.name, f"{reference} is not an official model-server image")
            locked[reference] = digest
        if not DIGEST_RE.match(digest):
            problems.add(tier.name, f"{image} digest must be sha256:<64 hex>, got {digest!r}")
    models = lock.get("models")
    if not isinstance(models, list) or not models:
        problems.add(tier.name, "images.lock must pin at least one model revision")
    else:
        for model in models:
            if not isinstance(model, dict):
                problems.add(tier.name, "images.lock model entries must be mappings")
                continue
            if not REVISION_RE.match(str(model.get("revision", ""))):
                problems.add(tier.name, f"model {model.get('id')!r} must pin a 40-hex Hugging Face revision")
            if not model.get("license"):
                problems.add(tier.name, f"model {model.get('id')!r} must record its license")
    return locked


def compose_images(compose_path: Path, problems: Problems) -> list[tuple[str, str | None]]:
    """Return (reference, service) pairs for every image used by a service."""
    compose = load_yaml(compose_path, problems)
    if not isinstance(compose, dict):
        return []
    services = compose.get("services")
    if not isinstance(services, dict):
        problems.add(compose_path.relative_to(ROOT), "compose file has no services")
        return []
    found: list[tuple[str, str | None]] = []
    for name, service in services.items():
        if not isinstance(service, dict):
            continue
        image = service.get("image")
        if image is None:
            problems.add(compose_path.relative_to(ROOT), f"service {name} has no image")
            continue
        found.append((str(image), service.get("build") and name or name))
    return found


def check_compose(tier: Path, locked: dict[str, str], problems: Problems) -> set[str]:
    """Check compose image pins and return the model-server service names."""
    compose_path = tier / "compose.yaml"
    services = set()
    for reference, service in compose_images(compose_path, problems):
        if reference.startswith("${ROUTER_IMAGE"):
            if "${ROUTER_IMAGE:?" not in reference:
                problems.add(tier.name, "router image must require ROUTER_IMAGE to be set")
            services.add(str(service))
            continue
        if ":" not in reference:
            problems.add(tier.name, f"image {reference} is unpinned")
            continue
        try:
            repo, tag, digest = split_image(reference)
        except ValueError as err:
            problems.add(tier.name, str(err))
            continue
        if digest is None:
            problems.add(tier.name, f"image {reference} must be pinned by digest")
            continue
        if tag == "latest":
            problems.add(tier.name, f"image {reference} resolves to latest")
        if official_repo(repo) is None:
            problems.add(tier.name, f"image {reference} is not an official model-server image")
            continue
        base = f"{repo}:{tag}"
        if base not in locked:
            problems.add(tier.name, f"image {base} is not in images.lock")
        elif locked[base] != digest:
            problems.add(tier.name, f"image {base} digest disagrees with images.lock")
        services.add(str(service))
    for required in ("router",):
        if required not in services:
            problems.add(tier.name, "compose must define a router service")
    check_rocm_entrypoint(tier, problems)
    return services


def check_rocm_entrypoint(tier: Path, problems: Problems) -> None:
    """A rocm/vllm service must start the server itself.

    The official ROCm image carries no entrypoint and its default CMD only pins
    the matching wheel, so a service that relies on the image defaults never
    serves a model.
    """
    compose = load_yaml(tier / "compose.yaml", Problems())
    if not isinstance(compose, dict):
        return
    services = compose.get("services")
    if not isinstance(services, dict):
        return
    for name, service in services.items():
        if not isinstance(service, dict):
            continue
        image = str(service.get("image", ""))
        if not image.startswith("rocm/vllm:"):
            continue
        entrypoint = service.get("entrypoint")
        rendered = entrypoint if isinstance(entrypoint, str) else " ".join(str(part) for part in entrypoint or [])
        if "vllm" not in rendered or "serve" not in rendered:
            problems.add(tier.name, f"service {name} uses rocm/vllm but does not set an explicit vllm serve entrypoint")


def check_config(tier: Path, problems: Problems) -> set[str]:
    config = load_yaml(tier / "config.yaml", problems)
    if not isinstance(config, dict):
        problems.add(tier.name, "config.yaml must be a mapping")
        return set()
    providers = config.get("providers")
    if not isinstance(providers, dict) or not providers:
        problems.add(tier.name, "config.yaml must define providers")
        return set()
    for name, provider in providers.items():
        if not isinstance(provider, dict):
            problems.add(tier.name, f"provider {name} must be a mapping")
            continue
        api_key = str(provider.get("api_key", ""))
        if not re.fullmatch(r"\$\{[A-Z0-9_]+\}", api_key):
            problems.add(tier.name, f"provider {name} api_key must be a ${{VAR}} reference")
        if provider.get("dialect") != "openai-chat":
            problems.add(tier.name, f"provider {name} must use the openai-chat dialect")
        if not provider.get("api_key_env"):
            problems.add(tier.name, f"provider {name} must set api_key_env")
    groups = config.get("models")
    if not isinstance(groups, dict) or "local-tiny" not in groups:
        problems.add(tier.name, "config.yaml must define the shared model groups")
        return set()
    for name, group in groups.items():
        if not isinstance(group, dict) or not group.get("strategy"):
            problems.add(tier.name, f"model group {name} needs a strategy")
    callers = config.get("callers")
    if not isinstance(callers, list) or not callers:
        problems.add(tier.name, "config.yaml must define a demo caller")
    else:
        for caller in callers:
            for group in caller.get("allow") or []:
                if group not in groups:
                    problems.add(tier.name, f"caller {caller.get('id')} allows unknown group {group}")
    return set(groups)


def check_tier(tier: Path) -> Problems:
    problems = Problems()
    check_tier_layout(tier, problems)
    if problems:
        return problems
    check_readme(tier, problems)
    locked = check_images_lock(tier, problems)
    check_compose(tier, locked, problems)
    check_config(tier, problems)
    return problems


def tier_dirs(root: Path) -> list[Path]:
    return sorted(
        child
        for child in root.iterdir()
        if child.is_dir()
        and not child.name.startswith((".", "_"))
        and child.name != "harness"
        and ((child / "images.lock").is_file() or (child / "SIZING.md").is_file())
    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=REFERENCE_DIR, help="reference configuration root")
    args = parser.parse_args(argv)

    root = args.root
    if not root.is_dir():
        print(f"reference root not found: {root}", file=sys.stderr)
        return 2
    tiers = tier_dirs(root)
    if not tiers:
        print(f"no reference tiers found under {root}", file=sys.stderr)
        return 2

    problems = Problems()
    for tier in tiers:
        problems.extend(check_tier(tier))
    if problems:
        for item in problems.items:
            print(item, file=sys.stderr)
        print(f"{len(problems.items)} reference configuration problem(s)", file=sys.stderr)
        return 1
    print(f"reference configurations: {len(tiers)} tier(s) valid")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
