#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Self-test package content validation rules."""

from __future__ import annotations

import io
import json
import re
import tarfile
import tempfile
from pathlib import Path

import validate_package_contents

PACKAGE_DOCS = [
    "PACKAGE_README.md",
    "BINARY_INSTALL.md",
    "DOCKER_COMPOSE_INSTALL.md",
    "KUBERNETES_INSTALL.md",
    "PACKAGE_VALIDATION.md",
    "solution-brief.md",
    "LICENSE.md",
]


def markdown_section(document: str, heading: str) -> str:
    marker = f"## {heading}\n"
    start = document.find(marker)
    if start == -1:
        raise AssertionError(f"missing {heading!r} section")
    end = document.find("\n## ", start + len(marker))
    return document[start:] if end == -1 else document[start:end]


def assert_offline_package_documentation_contract() -> None:
    repository = Path(__file__).resolve().parent.parent
    makefile = (repository / "Makefile").read_text(encoding="utf-8")
    match = re.search(r"^FLEET_ONLY_BINARIES := (.+)$", makefile, re.MULTILINE)
    if match is None:
        raise AssertionError("Makefile: missing FLEET_ONLY_BINARIES")
    make_fleet_binaries = set(match.group(1).split())
    if make_fleet_binaries != validate_package_contents.FLEET_ONLY_BINARY_NAMES:
        raise AssertionError(
            "package validator Fleet-only binary set differs from Makefile: "
            f"{make_fleet_binaries ^ validate_package_contents.FLEET_ONLY_BINARY_NAMES}"
        )

    binary_manifest_sources = {
        repository / "docs/PACKAGE_README.md": "What Is Included",
        repository / "README.md": "Build And Package",
        repository / "docs/DEPLOYMENT.md": "Package Contents",
    }
    for path, heading in binary_manifest_sources.items():
        section = markdown_section(path.read_text(encoding="utf-8"), heading)
        for package_file in validate_package_contents.BINARY_PACKAGE_FILES:
            if package_file not in section:
                raise AssertionError(f"{path}: binary package manifest omits {package_file}")

    package_readme = (repository / "docs/PACKAGE_README.md").read_text(encoding="utf-8")
    docker_section = markdown_section(package_readme, "What Is Included")
    docker_start = docker_section.find("Docker Compose packages include:")
    if docker_start == -1:
        raise AssertionError("docs/PACKAGE_README.md: missing Docker Compose package manifest")
    docker_manifest = docker_section[docker_start:]
    for fleet_binary in (
        f"bin/{name}" for name in validate_package_contents.FLEET_ONLY_BINARY_NAMES
    ):
        if fleet_binary in docker_manifest:
            raise AssertionError(f"docs/PACKAGE_README.md: Docker package manifest must omit {fleet_binary}")
    for guidance in (
        "The standard Docker and Docker Compose images do not include",
        "binary package on a separate trusted administration host.",
    ):
        if guidance not in docker_manifest:
            raise AssertionError(f"docs/PACKAGE_README.md: missing Docker CLI guidance: {guidance}")

    dockerfile = (repository / "Dockerfile").read_text(encoding="utf-8")
    docker_build_steps = (
        "COPY docs-site/package.json docs-site/package-lock.json ./docs-site/",
        "RUN npm ci --prefix docs-site --no-audit --no-fund",
        "COPY . .",
        "RUN DOCS_ROUTER_VERSION=$VERSION DOCS_ROUTER_COMMIT=$COMMIT "
        "DOCS_ROUTER_BUILD_DATE=$BUILD_DATE npm run build --prefix docs-site",
        "ARG TARGETARCH",
    )
    positions = [dockerfile.find(step) for step in docker_build_steps]
    if -1 in positions or positions != sorted(positions):
        raise AssertionError(
            "Dockerfile: docs dependencies must be installed from the lockfile before "
            "the source copy, and the docs build must remain architecture-independent"
        )
    for runtime_binary in (
        "metrum-ai-router",
        "metrum-ai-router-token-gen",
        "metrum-ai-router-usage-report",
        "metrum-ai-router-migrate",
        "metrum-ai-routerctl",
    ):
        expected_copy = f"COPY --from=build /out/{runtime_binary} /app/bin/{runtime_binary}"
        if expected_copy not in dockerfile:
            raise AssertionError(f"Dockerfile: missing runtime binary copy: {runtime_binary}")
    for fleet_binary in validate_package_contents.FLEET_ONLY_BINARY_NAMES:
        if f"/out/{fleet_binary}" in dockerfile or f"/app/bin/{fleet_binary}" in dockerfile:
            raise AssertionError(f"Dockerfile: standard image must not include {fleet_binary}")


def write_allowlist(root: Path) -> Path:
    allowlist = root / "allowlist.txt"
    allowlist.write_text("".join(f"docs/{doc}\n" for doc in PACKAGE_DOCS), encoding="utf-8")
    return allowlist


def elf(machine: int) -> bytes:
    header = bytearray(64)
    header[:4] = b"\x7fELF"
    header[4] = 2
    header[5] = 1
    header[18:20] = machine.to_bytes(2, "little")
    return bytes(header)


def write_tar(path: Path, files: dict[str, str | bytes]) -> None:
    with tarfile.open(path, "w:gz") as package:
        for name, content in files.items():
            data = content if isinstance(content, bytes) else content.encode("utf-8")
            info = tarfile.TarInfo(name)
            info.size = len(data)
            package.addfile(info, io.BytesIO(data))


def expect_errors(archive: Path, allowlist: Path, want: list[str]) -> None:
    errors = validate_package_contents.validate_archives([archive], allowlist)
    for expected in want:
        if not any(expected in error for error in errors):
            raise AssertionError(f"{archive}: missing {expected!r} in errors {errors!r}")


def expect_ok(archive: Path, allowlist: Path) -> None:
    errors = validate_package_contents.validate_archives([archive], allowlist)
    if errors:
        raise AssertionError(f"{archive}: unexpected errors {errors!r}")


def binary_package_files(root: str = "metrum-ai-router-v1.0.0-linux-amd64") -> dict[str, str | bytes]:
    files: dict[str, str | bytes] = {
        f"{root}/bin/metrum-ai-router": elf(62),
        f"{root}/bin/metrum-ai-router-token-gen": elf(62),
        f"{root}/bin/metrum-ai-router-usage-report": elf(62),
        f"{root}/bin/metrum-ai-router-migrate": elf(62),
        f"{root}/bin/metrum-ai-routerctl": elf(62),
        f"{root}/bin/metrum-ai-router-fleetctl": elf(62),
        f"{root}/bin/metrum-ai-router-fleet-sign": elf(62),
        f"{root}/config/config.example.yaml": "server: {}\n",
        f"{root}/config/env.example.json": "{}\n",
        f"{root}/config/scripts/router.ts": "export function route() {}\n",
        f"{root}/caddy/Caddyfile": ":80\n",
    }
    files.update(
        {
            f"{root}/{name}": "license notice\n"
            for name in ("LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "MODEL_LICENSES.md")
        }
    )
    for doc in PACKAGE_DOCS:
        files[f"{root}/docs/{doc}"] = "package-safe docs\n"
    return files


def docker_image_tar(
    extra_layer_files: dict[str, str | bytes] | None = None,
    *,
    arch: str = "amd64",
) -> bytes:
    layer_data = io.BytesIO()
    with tarfile.open(fileobj=layer_data, mode="w") as layer:
        for name in [
            "app/bin/metrum-ai-router",
            "app/bin/metrum-ai-router-token-gen",
            "app/bin/metrum-ai-router-usage-report",
            "app/bin/metrum-ai-router-migrate",
            "app/bin/metrum-ai-routerctl",
        ]:
            data = elf(62)
            info = tarfile.TarInfo(name)
            info.size = len(data)
            layer.addfile(info, io.BytesIO(data))
        for name, content in (extra_layer_files or {}).items():
            data = content if isinstance(content, bytes) else content.encode("utf-8")
            info = tarfile.TarInfo(name)
            info.size = len(data)
            layer.addfile(info, io.BytesIO(data))
    layer_blob = layer_data.getvalue()

    image_data = io.BytesIO()
    with tarfile.open(fileobj=image_data, mode="w") as image:
        manifest = [{"Config": "config.json", "RepoTags": ["metrum-ai-router:v1.0.0-linux-amd64"], "Layers": ["layer.tar"]}]
        config = {"architecture": arch, "os": "linux"}
        for name, content in {
            "manifest.json": json.dumps(manifest).encode("utf-8"),
            "config.json": json.dumps(config).encode("utf-8"),
            "layer.tar": layer_blob,
        }.items():
            info = tarfile.TarInfo(name)
            info.size = len(content)
            image.addfile(info, io.BytesIO(content))
    return image_data.getvalue()


def docker_package_files(root: str = "metrum-ai-router-v1.0.0-docker-linux-amd64") -> dict[str, str | bytes]:
    files: dict[str, str | bytes] = {
        f"{root}/compose/docker-compose.yml": "services: {}\n",
        f"{root}/compose/docker-compose.postgres-localhost.yml": "services: {}\n",
        f"{root}/compose/Caddyfile.compose": ":80\n",
        f"{root}/compose/.env.example": "SMART_LLMROUTER_VERSION=v1.0.0-linux-amd64\n",
        f"{root}/compose/.env": "SMART_LLMROUTER_VERSION=v1.0.0-linux-amd64\n",
        f"{root}/config/config.example.yaml": "server: {}\n",
        f"{root}/config/env.example.json": "{}\n",
        f"{root}/config/scripts/router.ts": "export function route() {}\n",
        f"{root}/images/metrum-ai-router-v1.0.0-linux-amd64.tar": docker_image_tar(),
    }
    files.update(
        {
            f"{root}/{name}": "license notice\n"
            for name in ("LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "MODEL_LICENSES.md")
        }
    )
    for doc in PACKAGE_DOCS:
        files[f"{root}/docs/{doc}"] = "package-safe docs\n"
    return files


def main() -> int:
    assert_offline_package_documentation_contract()
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        allowlist = write_allowlist(root)

        good = root / "metrum-ai-router-v1.0.0-linux-amd64.tar.gz"
        write_tar(good, binary_package_files())
        expect_ok(good, allowlist)

        missing_cli = root / "missing-cli.tar.gz"
        missing_cli_files = binary_package_files()
        del missing_cli_files["metrum-ai-router-v1.0.0-linux-amd64/bin/metrum-ai-router-fleetctl"]
        write_tar(missing_cli, missing_cli_files)
        expect_errors(missing_cli, allowlist, ["required package file is missing: bin/metrum-ai-router-fleetctl"])

        missing_fleet_sign = root / "missing-fleet-sign.tar.gz"
        missing_fleet_sign_files = binary_package_files()
        del missing_fleet_sign_files["metrum-ai-router-v1.0.0-linux-amd64/bin/metrum-ai-router-fleet-sign"]
        write_tar(missing_fleet_sign, missing_fleet_sign_files)
        expect_errors(
            missing_fleet_sign,
            allowlist,
            ["required package file is missing: bin/metrum-ai-router-fleet-sign"],
        )

        good_docker = root / "metrum-ai-router-v1.0.0-docker-linux-amd64.tar.gz"
        write_tar(good_docker, docker_package_files())
        expect_ok(good_docker, allowlist)

        for fleet_binary in sorted(validate_package_contents.FLEET_ONLY_BINARY_NAMES):
            forbidden_image = root / f"forbidden-image-{fleet_binary}.tar.gz"
            forbidden_image_files = docker_package_files()
            image_path = (
                "metrum-ai-router-v1.0.0-docker-linux-amd64/"
                "images/metrum-ai-router-v1.0.0-linux-amd64.tar"
            )
            forbidden_image_files[image_path] = docker_image_tar(
                {f"app/bin/{fleet_binary}": elf(62)}
            )
            write_tar(forbidden_image, forbidden_image_files)
            expect_errors(
                forbidden_image,
                allowlist,
                [f"forbidden fleet lifecycle binary /app/bin/{fleet_binary}"],
            )

        relocated_fleet_binary = root / "relocated-fleet-binary.tar.gz"
        relocated_files = docker_package_files()
        relocated_image_path = (
            "metrum-ai-router-v1.0.0-docker-linux-amd64/"
            "images/metrum-ai-router-v1.0.0-linux-amd64.tar"
        )
        relocated_files[relocated_image_path] = docker_image_tar(
            {"usr/local/bin/metrum-ai-router-fleetctl": elf(62)}
        )
        write_tar(relocated_fleet_binary, relocated_files)
        expect_errors(
            relocated_fleet_binary,
            allowlist,
            [
                "forbidden fleet lifecycle binary "
                "/usr/local/bin/metrum-ai-router-fleetctl"
            ],
        )

        wrong_image_arch = root / "metrum-ai-router-v1.0.0-docker-linux-amd64.tar.gz"
        wrong_image_arch_files = docker_package_files()
        wrong_image_arch_files[
            "metrum-ai-router-v1.0.0-docker-linux-amd64/images/metrum-ai-router-v1.0.0-linux-amd64.tar"
        ] = docker_image_tar(arch="arm64")
        write_tar(wrong_image_arch, wrong_image_arch_files)
        expect_errors(wrong_image_arch, allowlist, ["image platform linux/arm64 does not match linux/amd64"])

        wrong_root = root / "metrum-ai-router-v1.0.0-linux-amd64.tar.gz"
        write_tar(wrong_root, binary_package_files("another-root"))
        expect_errors(wrong_root, allowlist, ["top-level directory must be"])

        extra_image = root / "extra-image.tar.gz"
        extra_image_files = docker_package_files()
        extra_image_files["metrum-ai-router-v1.0.0-docker-linux-amd64/images/extra.tar"] = docker_image_tar()
        write_tar(extra_image, extra_image_files)
        expect_errors(extra_image, allowlist, ["unexpected package file included"])

        image_source_path = root / "image-source-path.tar.gz"
        image_source_files = docker_package_files()
        image_source_files["metrum-ai-router-v1.0.0-docker-linux-amd64/images/metrum-ai-router-v1.0.0-linux-amd64.tar"] = (
            docker_image_tar({"app/docs/PRODUCTION_RUNBOOK.md": "private\n"})
        )
        write_tar(image_source_path, image_source_files)
        expect_errors(image_source_path, allowlist, ["forbidden runtime/source path"])

        image_source_dot_path = root / "image-source-dot-path.tar.gz"
        image_source_dot_files = docker_package_files()
        image_source_dot_files["metrum-ai-router-v1.0.0-docker-linux-amd64/images/metrum-ai-router-v1.0.0-linux-amd64.tar"] = (
            docker_image_tar({"./app/internal/router/secret.go": "private\n"})
        )
        write_tar(image_source_dot_path, image_source_dot_files)
        expect_errors(image_source_dot_path, allowlist, ["forbidden runtime/source path"])

        binary_source_path = root / "binary-source-path.tar.gz"
        binary_source_files = binary_package_files()
        binary_source_files["metrum-ai-router-v1.0.0-linux-amd64/cmd/metrum-fleetctl/main.go"] = "package main\n"
        write_tar(binary_source_path, binary_source_files)
        expect_errors(binary_source_path, allowlist, ["forbidden source path"])

        apple_double = root / "appledouble.tar.gz"
        apple_files = binary_package_files()
        apple_files["metrum-ai-router-v1.0.0-linux-amd64/docs/._PACKAGE_README.md"] = "mac metadata\n"
        write_tar(apple_double, apple_files)
        expect_errors(apple_double, allowlist, ["AppleDouble metadata entry"])

        private_runbook = root / "private-runbook.tar.gz"
        runbook_files = binary_package_files()
        runbook_files["metrum-ai-router-v1.0.0-linux-amd64/docs/PRODUCTION_RUNBOOK.md"] = "private\n"
        write_tar(private_runbook, runbook_files)
        expect_errors(private_runbook, allowlist, ["forbidden local secret/state file", "not in package docs allowlist"])

        private_marker = root / "private-marker.tar.gz"
        marker_files = binary_package_files()
        marker_files["metrum-ai-router-v1.0.0-linux-amd64/docs/PACKAGE_README.md"] = "Host: 100.30.225.66\n"
        write_tar(private_marker, marker_files)
        expect_errors(private_marker, allowlist, ["private production host marker"])

        raw_token = root / "raw-token.tar.gz"
        token_files = binary_package_files()
        token_files["metrum-ai-router-v1.0.0-linux-amd64/docs/PACKAGE_README.md"] = (
            "token rtr_metrum_user_project_prod_key_abcdefghijklmnopqrstuvwxyz\n"
        )
        write_tar(raw_token, token_files)
        expect_errors(raw_token, allowlist, ["raw router token"])

        forbidden_files = root / "forbidden-files.tar.gz"
        bad_files = binary_package_files()
        bad_files.update(
            {
                "metrum-ai-router-v1.0.0-linux-amd64/config/env.json": "{}\n",
                "metrum-ai-router-v1.0.0-linux-amd64/config/config.production.yaml": "server: {}\n",
                "metrum-ai-router-v1.0.0-linux-amd64/ROUTER_TOKEN.txt": "placeholder\n",
                "metrum-ai-router-v1.0.0-linux-amd64/config/license.json": "{}\n",
                "metrum-ai-router-v1.0.0-linux-amd64/state/usage.sqlite": "not actually sqlite\n",
            }
        )
        write_tar(forbidden_files, bad_files)
        expect_errors(forbidden_files, allowlist, ["forbidden local secret/state file"])

        wrong_arch = root / "metrum-ai-router-v1.0.0-linux-arm64.tar.gz"
        write_tar(wrong_arch, binary_package_files("metrum-ai-router-v1.0.0-linux-arm64"))
        expect_errors(wrong_arch, allowlist, ["expected 183 for linux-arm64"])

        unexpected = root / "unexpected.tar.gz"
        unexpected_files = binary_package_files()
        unexpected_files["metrum-ai-router-v1.0.0-linux-amd64/docs-site/source.md"] = "source\n"
        write_tar(unexpected, unexpected_files)
        expect_errors(unexpected, allowlist, ["unexpected package file included"])

        missing_doc = root / "missing-doc.tar.gz"
        missing_files = binary_package_files()
        del missing_files["metrum-ai-router-v1.0.0-linux-amd64/docs/PACKAGE_VALIDATION.md"]
        write_tar(missing_doc, missing_files)
        expect_errors(missing_doc, allowlist, ["from package docs allowlist is missing"])

    print("package content validation self-test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
