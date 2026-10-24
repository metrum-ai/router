#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Regression tests for Makefile build metadata handling."""

from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MALICIOUS_VERSION = "x; id >/tmp/smart-llmrouter-make-poc #"
MALICIOUS_EKS_INPUT = '"; id >/tmp/smart-llmrouter-eks-make-poc; echo "'


def run_make(
    *args: str, environment: dict[str, str] | None = None
) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env.pop("EKS_AWS_PROFILE", None)
    env.pop("EKS_DELIVERY_AWS_PROFILE", None)
    env.update(
        {
            "VERSION": "v1.2.3-4-gabcdef0-dirty",
            "COMMIT": "abcdef0",
            "BUILD_DATE": "2026-06-28T00:00:00Z",
            "GOOS": "linux",
            "GOARCH": "amd64",
            "PKG_NAME": "metrum-ai-router",
            "DIST_DIR": "dist",
            "IMAGE_NAME": "metrum-ai-router",
            "IMAGE_TAG": "v1.2.3-linux-amd64",
        }
    )
    if environment:
        env.update(environment)
    return subprocess.run(
        ["make", *args],
        cwd=ROOT,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def main() -> int:
    good = run_make("validate-build-metadata")
    require(good.returncode == 0, f"normal metadata rejected:\n{good.stdout}\n{good.stderr}")

    bad = run_make("validate-build-metadata", f"VERSION={MALICIOUS_VERSION}")
    require(bad.returncode != 0, "malicious VERSION was accepted")
    require("unsupported characters" in bad.stderr, f"missing validation error: {bad.stderr}")

    package_dry_run = run_make("-n", "package-one-no-docs", f"VERSION={MALICIOUS_VERSION}")
    docker_dry_run = run_make("-n", "package-docker-one-no-docs", f"VERSION={MALICIOUS_VERSION}")
    combined = package_dry_run.stdout + package_dry_run.stderr + docker_dry_run.stdout + docker_dry_run.stderr
    require("id >/tmp/smart-llmrouter-make-poc" not in combined, f"dry-run exposed executable payload:\n{combined}")
    require("$VERSION" in combined or "${VERSION}" in combined, "dry-run should defer metadata to shell environment expansion")

    host_goos = subprocess.check_output(["go", "env", "GOHOSTOS"], text=True).strip()
    host_goarch = subprocess.check_output(["go", "env", "GOHOSTARCH"], text=True).strip()
    cross_arch_smoke = run_make("-n", "capability-smoke-unit", "GOARCH=arm64")
    cross_arch_output = cross_arch_smoke.stdout + cross_arch_smoke.stderr
    require(
        f"GOOS={host_goos} GOARCH={host_goarch} go test" in cross_arch_output,
        "cross-architecture packaging must run capability Go tests for the build host",
    )

    eks_profile_dry_run = run_make(
        "-n", "eks-discover", f"EKS_AWS_PROFILE={MALICIOUS_EKS_INPUT}"
    )
    eks_combined = eks_profile_dry_run.stdout + eks_profile_dry_run.stderr
    require(
        "id >/tmp/smart-llmrouter-eks-make-poc" not in eks_combined,
        f"EKS dry-run exposed executable payload:\n{eks_combined}",
    )
    require(
        "$EKS_AWS_PROFILE" in eks_combined or "${EKS_AWS_PROFILE}" in eks_combined,
        "EKS discovery profile must defer expansion to the recipe shell",
    )

    discovery_dry_run = run_make("-n", "eks-session-bootstrap")
    discovery_combined = discovery_dry_run.stdout + discovery_dry_run.stderr
    require(
        "$EKS_AWS_PROFILE" in discovery_combined or "${EKS_AWS_PROFILE}" in discovery_combined,
        "legacy discovery/session bootstrap must retain the discovery profile variable",
    )

    # `make -pn` with no goal still walks the default `test` target. Recipes
    # containing recursive `$(MAKE)` commands execute even under `-n`, which
    # would invoke the API-compatibility bootstrap and require `uv`. Anchor
    # database inspection to the help target instead.
    no_uv_path = os.pathsep.join(
        directory
        for directory in os.environ["PATH"].split(os.pathsep)
        if not (Path(directory) / "uv").exists()
    )
    require(shutil.which("uv", path=no_uv_path) is None, "no-uv test path still resolves uv")
    make_database = run_make("-pn", "help", environment={"PATH": no_uv_path})
    require(make_database.returncode == 0, f"Make database inspection failed:\n{make_database.stderr}")
    require(
        "EKS_AWS_PROFILE =" in make_database.stdout
        and "EKS_AWS_PROFILE = metrum-ai-router-eks-discovery" not in make_database.stdout,
        "EKS_AWS_PROFILE must have an empty Make default (operator-supplied)",
    )

    # Keep the exact bare-Make inspection path independent of the bootstrap
    # toolchain. This would run `uv` before #728 because a recursive Make call
    # shared its recipe line with Python provisioning.
    default_dry_run = run_make("-n", environment={"PATH": no_uv_path})
    require(default_dry_run.returncode == 0, f"bare make dry-run failed without uv:\n{default_dry_run.stderr}")
    require("go test $(go list ./... | grep -v '/internal/router$') -timeout=15m" in default_dry_run.stdout
            and "go test ./internal/router -timeout=30m" in default_dry_run.stdout,
            "bare make must retain every Go package with router-only timeout headroom")

    # The offline child deliberately scrubs Make override transport, but it
    # must remain a visible recursive `$(MAKE)` call. GNU Make encodes short
    # flags compactly in MAKEFLAGS, so cover both combined orders plus the
    # equivalent separated and long spellings. Each inspection carries only
    # -n across the scrubbed boundary; with uv absent, any real offline work
    # would fail deterministically.
    for description, flags in (
        ("standalone -n", ("-n",)),
        ("combined -ns", ("-ns",)),
        ("combined -sn", ("-sn",)),
        ("separated short flags", ("-n", "-s")),
        ("reverse separated short flags", ("-s", "-n")),
        ("long flags", ("--dry-run", "--silent")),
    ):
        scrubbed_mirror_dry_run = run_make(
            *flags,
            "api-compat-mock",
            "API_COMPAT_BOOTSTRAP_GO_PROXY=https://mirror.invalid/go",
            "API_COMPAT_BOOTSTRAP_GO_SUMDB=sumdb.invalid",
            environment={"PATH": no_uv_path},
        )
        require(
            scrubbed_mirror_dry_run.returncode == 0,
            f"{description} executed the scrubbed offline child without uv:\n"
            f"{scrubbed_mirror_dry_run.stderr}",
        )
        require(
            "uv: not found" not in scrubbed_mirror_dry_run.stderr,
            f"{description} attempted to execute uv after mirror override scrubbing",
        )

    print("Makefile security self-test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
