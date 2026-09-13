# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Record pinned source license and README provenance for seed construction."""

from __future__ import annotations

import hashlib
from typing import Any

from ..collect import DataError, canonical, utc_now
from . import AUDIT_DATE, SOURCE_DATASET, SOURCE_REVISION

HF_RAW = "https://huggingface.co/datasets/{dataset}/raw/{revision}/{path}"


def source_urls(
    *,
    dataset: str = SOURCE_DATASET,
    revision: str = SOURCE_REVISION,
) -> dict[str, str]:
    return {
        "readme_url": HF_RAW.format(dataset=dataset, revision=revision, path="README.md"),
        "license_url": HF_RAW.format(dataset=dataset, revision=revision, path="LICENSE"),
    }


def sha256_bytes(payload: bytes) -> str:
    return hashlib.sha256(payload).hexdigest()


def record_license_audit(
    *,
    readme_body: bytes,
    license_body: bytes,
    dataset: str = SOURCE_DATASET,
    revision: str = SOURCE_REVISION,
    audit_date: str = AUDIT_DATE,
    redistribution_status: str = "cc_by_4_0_derived_sharing_subject_to_attribution",
    fetched_at: str | None = None,
) -> dict[str, Any]:
    """Build an audit record from already-fetched README and LICENSE bytes.

    Callers supply bodies (fixture or operator download). This function does not
    perform network I/O.
    """
    if not readme_body or not license_body:
        raise DataError("license_audit_bodies_required")
    if revision != SOURCE_REVISION and dataset == SOURCE_DATASET:
        raise DataError("unexpected_source_revision")
    urls = source_urls(dataset=dataset, revision=revision)
    return {
        "schema_version": "lrp.seed.license_audit.v1",
        "dataset": dataset,
        "revision": revision,
        "audit_date": audit_date,
        "fetched_at": fetched_at or utc_now(),
        "readme_url": urls["readme_url"],
        "license_url": urls["license_url"],
        "readme_sha256": sha256_bytes(readme_body),
        "license_sha256": sha256_bytes(license_body),
        "redistribution_status": redistribution_status,
        "notes": (
            "CC BY 4.0 permits sharing and adapted material subject to attribution "
            "and underlying repository rights. The license does not grant rights "
            "the licensor does not hold."
        ),
        "identity": "",
    }


def seal_audit(record: dict[str, Any]) -> dict[str, Any]:
    sealed = dict(record)
    sealed.pop("identity", None)
    sealed["identity"] = hashlib.sha256(canonical(sealed).encode()).hexdigest()
    return sealed
