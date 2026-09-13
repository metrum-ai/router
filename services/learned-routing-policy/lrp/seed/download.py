# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Pinned upstream download helpers. Operator data stays outside the repository."""

from __future__ import annotations

import hashlib
from collections.abc import Callable
from pathlib import Path
from typing import Any

import httpx

from ..collect import DataError, protected_path, utc_now
from . import SOURCE_DATASET, SOURCE_REVISION
from .license_audit import HF_RAW, sha256_bytes

HttpGet = Callable[[str], bytes]


def default_http_get(url: str, *, timeout_s: float = 60.0) -> bytes:
    with httpx.Client(trust_env=False, follow_redirects=True, timeout=timeout_s) as client:
        response = client.get(url)
        if response.status_code != 200:
            raise DataError("upstream_download_failed")
        return response.content


def expected_artifact_path(root: Path, relative: str) -> Path:
    root = protected_path(root)
    path = (root / relative).resolve()
    if not path.is_relative_to(root.resolve()):
        raise DataError("path_escape_forbidden")
    return path


def download_pinned_file(
    *,
    out_root: Path,
    relative_path: str,
    expected_sha256: str,
    dataset: str = SOURCE_DATASET,
    revision: str = SOURCE_REVISION,
    http_get: HttpGet | None = None,
) -> dict[str, Any]:
    """Download one hub path at a pinned revision and verify sha256.

    Writes under ``out_root`` (must be outside the repository). Returns a
    provenance record. Unit tests inject ``http_get`` and never hit the network.
    """
    if revision != SOURCE_REVISION and dataset == SOURCE_DATASET:
        raise DataError("unexpected_source_revision")
    if len(expected_sha256) != 64 or any(
        ch not in "0123456789abcdef" for ch in expected_sha256
    ):
        raise DataError("invalid_sha256")
    getter = http_get or default_http_get
    url = HF_RAW.format(dataset=dataset, revision=revision, path=relative_path)
    body = getter(url)
    digest = sha256_bytes(body)
    if digest != expected_sha256:
        raise DataError("sha256_mismatch")
    destination = expected_artifact_path(out_root, relative_path)
    destination.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
    destination.write_bytes(body)
    destination.chmod(0o600)
    return {
        "schema_version": "lrp.seed.download.v1",
        "dataset": dataset,
        "revision": revision,
        "path": relative_path,
        "url": url,
        "sha256": digest,
        "bytes": len(body),
        "fetched_at": utc_now(),
        "local_path": str(destination),
    }


def verify_local_sha256(path: Path, expected_sha256: str, *, fixture: bool = False) -> str:
    path = protected_path(path, fixture_read=fixture)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    if digest != expected_sha256:
        raise DataError("sha256_mismatch")
    return digest
