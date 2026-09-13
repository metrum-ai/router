# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import hashlib

import pytest
from lrp.collect import DataError
from lrp.seed import SOURCE_REVISION
from lrp.seed.download import download_pinned_file, verify_local_sha256
from lrp.seed.license_audit import record_license_audit, seal_audit, source_urls


def test_license_audit_records_pin_and_hashes():
    readme = b"# Dataset\nlicense: cc-by-4.0\n"
    license_body = b"Creative Commons Attribution 4.0\n"
    record = seal_audit(
        record_license_audit(readme_body=readme, license_body=license_body)
    )
    assert record["revision"] == SOURCE_REVISION
    assert record["readme_sha256"] == hashlib.sha256(readme).hexdigest()
    assert record["license_sha256"] == hashlib.sha256(license_body).hexdigest()
    assert "huggingface.co" in record["readme_url"]
    assert record["identity"]
    urls = source_urls()
    assert urls["license_url"].endswith("/LICENSE")


def test_download_pinned_file_verifies_sha_without_network(tmp_path):
    body = b'{"rows":[]}\n'
    digest = hashlib.sha256(body).hexdigest()
    seen: list[str] = []

    def fake_get(url: str) -> bytes:
        seen.append(url)
        return body

    meta = download_pinned_file(
        out_root=tmp_path / "upstream",
        relative_path="README.md",
        expected_sha256=digest,
        http_get=fake_get,
    )
    assert seen and "35455389ab51bf5e2306bfd436ef72d0f98bf882" in seen[0]
    assert meta["sha256"] == digest
    assert (tmp_path / "upstream" / "README.md").read_bytes() == body
    assert verify_local_sha256(tmp_path / "upstream" / "README.md", digest) == digest


def test_download_rejects_sha_mismatch(tmp_path):
    def fake_get(_: str) -> bytes:
        return b"wrong"

    with pytest.raises(DataError, match="sha256_mismatch"):
        download_pinned_file(
            out_root=tmp_path / "upstream",
            relative_path="LICENSE",
            expected_sha256="0" * 64,
            http_get=fake_get,
        )
